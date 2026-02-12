// Stream buffer for phantom tool suppression (V2: E14/E15).
//
// DESIGN: When streaming responses, we must suppress expand_context tool calls
// from being sent to the client. The challenge is that SSE chunks may split
// the tool call across multiple chunks, so we need to buffer until we can
// determine if the chunk contains expand_context.
//
// FLOW:
//  1. Buffer incoming SSE chunks
//  2. Parse tool_use events as they arrive
//  3. If tool name is expand_context → suppress from client
//  4. Otherwise → flush to client
//
// This ensures the client never sees the phantom expand_context tool.
package tooloutput

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
)

// StreamBuffer buffers SSE chunks for phantom tool suppression (V2: E14/E15).
type StreamBuffer struct {
	mu              sync.Mutex
	buffer          bytes.Buffer
	suppressedCalls []ExpandContextCall
	inToolUse       bool
	currentToolName string
	currentToolID   string
}

// NewStreamBuffer creates a new stream buffer.
func NewStreamBuffer() *StreamBuffer {
	return &StreamBuffer{
		suppressedCalls: make([]ExpandContextCall, 0),
	}
}

// ProcessChunk processes an SSE chunk and returns filtered output.
// Returns nil if the chunk should be suppressed, otherwise returns the chunk to forward.
func (sb *StreamBuffer) ProcessChunk(chunk []byte) ([]byte, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	// Parse SSE data
	lines := bytes.Split(chunk, []byte("\n"))
	var output bytes.Buffer
	var suppress bool

	for _, line := range lines {
		// Skip non-data lines
		if !bytes.HasPrefix(line, []byte("data: ")) {
			output.Write(line)
			output.WriteByte('\n')
			continue
		}

		data := bytes.TrimPrefix(line, []byte("data: "))

		// Handle [DONE] marker
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			output.Write(line)
			output.WriteByte('\n')
			continue
		}

		// Parse JSON
		var event map[string]interface{}
		if err := json.Unmarshal(data, &event); err != nil {
			// Not valid JSON, pass through
			output.Write(line)
			output.WriteByte('\n')
			continue
		}

		// Check for tool_use in content_block_start (Anthropic streaming)
		if eventType, _ := event["type"].(string); eventType == "content_block_start" {
			if contentBlock, ok := event["content_block"].(map[string]interface{}); ok {
				if contentBlock["type"] == "tool_use" {
					name, _ := contentBlock["name"].(string)
					id, _ := contentBlock["id"].(string)

					if name == ExpandContextToolName {
						sb.inToolUse = true
						sb.currentToolName = name
						sb.currentToolID = id
						suppress = true
						log.Debug().
							Str("tool_id", id).
							Msg("stream_buffer: suppressing expand_context tool (E14)")
						continue
					}
				}
			}
		}

		// Check for content_block_stop (Anthropic streaming)
		if eventType, _ := event["type"].(string); eventType == "content_block_stop" {
			if sb.inToolUse && sb.currentToolName == ExpandContextToolName {
				// End of suppressed tool call
				sb.suppressedCalls = append(sb.suppressedCalls, ExpandContextCall{
					ToolUseID: sb.currentToolID,
					ShadowID:  "", // Will be filled from input
				})
				sb.inToolUse = false
				sb.currentToolName = ""
				sb.currentToolID = ""
				suppress = true
				continue
			}
		}

		// Check for tool_use delta with input (Anthropic streaming)
		if eventType, _ := event["type"].(string); eventType == "content_block_delta" {
			if sb.inToolUse && sb.currentToolName == ExpandContextToolName {
				// Extract shadow ID from input if present
				if delta, ok := event["delta"].(map[string]interface{}); ok {
					if partialJSON, ok := delta["partial_json"].(string); ok {
						sb.extractShadowID(partialJSON)
					}
				}
				suppress = true
				continue
			}
		}

		// Check for tool_calls in delta (OpenAI streaming)
		if choices, ok := event["choices"].([]interface{}); ok {
			filtered := sb.filterOpenAIToolCalls(choices)
			if filtered {
				suppress = true
				continue
			}
		}

		// Not suppressed - write to output
		if !suppress {
			output.Write(line)
			output.WriteByte('\n')
		}
	}

	if output.Len() == 0 || suppress {
		return nil, nil
	}

	return output.Bytes(), nil
}

// extractShadowID tries to extract the shadow ID from partial JSON input
func (sb *StreamBuffer) extractShadowID(partialJSON string) {
	sb.buffer.WriteString(partialJSON)

	// Try to parse accumulated JSON
	var input map[string]interface{}
	if err := json.Unmarshal(sb.buffer.Bytes(), &input); err == nil {
		if id, ok := input["id"].(string); ok {
			// Update the last suppressed call with the shadow ID
			if len(sb.suppressedCalls) > 0 {
				sb.suppressedCalls[len(sb.suppressedCalls)-1].ShadowID = id
			}
		}
	}
}

// filterOpenAIToolCalls filters expand_context from OpenAI streaming format
func (sb *StreamBuffer) filterOpenAIToolCalls(choices []interface{}) bool {
	for _, choice := range choices {
		c, ok := choice.(map[string]interface{})
		if !ok {
			continue
		}

		delta, ok := c["delta"].(map[string]interface{})
		if !ok {
			continue
		}

		toolCalls, ok := delta["tool_calls"].([]interface{})
		if !ok {
			continue
		}

		for _, tc := range toolCalls {
			call, ok := tc.(map[string]interface{})
			if !ok {
				continue
			}

			fn, ok := call["function"].(map[string]interface{})
			if !ok {
				continue
			}

			name, _ := fn["name"].(string)
			if name == ExpandContextToolName {
				id, _ := call["id"].(string)
				sb.suppressedCalls = append(sb.suppressedCalls, ExpandContextCall{
					ToolUseID: id,
					ShadowID:  "",
				})
				log.Debug().
					Str("tool_id", id).
					Msg("stream_buffer: suppressing expand_context tool (OpenAI)")
				return true
			}

			// Check if this is an arguments delta for expand_context
			if args, ok := fn["arguments"].(string); ok && strings.Contains(args, "shadow_") {
				// Try to extract shadow ID
				var input map[string]string
				if err := json.Unmarshal([]byte(args), &input); err == nil {
					if shadowID, ok := input["id"]; ok && len(sb.suppressedCalls) > 0 {
						sb.suppressedCalls[len(sb.suppressedCalls)-1].ShadowID = shadowID
					}
				}
			}
		}
	}

	return false
}

// GetSuppressedCalls returns the list of suppressed expand_context calls.
// These need to be handled by the gateway before returning to client.
func (sb *StreamBuffer) GetSuppressedCalls() []ExpandContextCall {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.suppressedCalls
}

// Reset clears the buffer state.
func (sb *StreamBuffer) Reset() {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.buffer.Reset()
	sb.suppressedCalls = sb.suppressedCalls[:0]
	sb.inToolUse = false
	sb.currentToolName = ""
	sb.currentToolID = ""
}

// HasSuppressedCalls returns true if any expand_context calls were suppressed.
func (sb *StreamBuffer) HasSuppressedCalls() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return len(sb.suppressedCalls) > 0
}
