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
	// OpenAI streaming state: track suppress across chunks for the same tool call
	openAIInToolUse bool
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
		var event map[string]any
		if err := json.Unmarshal(data, &event); err != nil {
			// Not valid JSON, pass through
			output.Write(line)
			output.WriteByte('\n')
			continue
		}

		// Reset OpenAI tool tracking on finish
		if choices, ok := event["choices"].([]any); ok {
			for _, choice := range choices {
				if c, ok := choice.(map[string]any); ok {
					if fr, ok := c["finish_reason"].(string); ok && fr != "" {
						sb.openAIInToolUse = false
					}
				}
			}
		}

		// Check for tool_use in content_block_start (Anthropic streaming)
		if eventType, _ := event["type"].(string); eventType == "content_block_start" {
			if contentBlock, ok := event["content_block"].(map[string]any); ok {
				if contentBlock["type"] == "tool_use" {
					name, _ := contentBlock["name"].(string)
					id, _ := contentBlock["id"].(string)

					if name == ExpandContextToolName {
						sb.inToolUse = true
						sb.currentToolName = name
						sb.currentToolID = id
						// Reset buffer for this new tool call's input accumulation
						sb.buffer.Reset()
						// Append now so content_block_delta can fill in the shadow ID
						sb.suppressedCalls = append(sb.suppressedCalls, ExpandContextCall{
							ToolUseID: id,
							ShadowID:  "", // Filled by extractShadowID during content_block_delta
						})
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
				// End of suppressed tool call (already appended at content_block_start)
				sb.inToolUse = false
				sb.currentToolName = ""
				sb.currentToolID = ""
				continue
			}
		}

		// Check for tool_use delta with input (Anthropic streaming)
		if eventType, _ := event["type"].(string); eventType == "content_block_delta" {
			if sb.inToolUse && sb.currentToolName == ExpandContextToolName {
				// Extract shadow ID from input if present
				if delta, ok := event["delta"].(map[string]any); ok {
					if partialJSON, ok := delta["partial_json"].(string); ok {
						sb.extractShadowID(partialJSON)
					}
				}
				continue
			}
		}

		// Check for tool_calls in delta (OpenAI Chat Completions streaming)
		if choices, ok := event["choices"].([]any); ok {
			if sb.filterOpenAIToolCalls(choices) {
				continue
			}
		}

		// Check for Responses API function call events
		if eventType, _ := event["type"].(string); eventType == "response.output_item.added" {
			if item, ok := event["item"].(map[string]any); ok {
				if itemType, _ := item["type"].(string); itemType == "function_call" {
					name, _ := item["name"].(string)
					if name == ExpandContextToolName {
						callID, _ := item["call_id"].(string)
						sb.inToolUse = true
						sb.currentToolName = name
						sb.currentToolID = callID
						sb.buffer.Reset()
						sb.suppressedCalls = append(sb.suppressedCalls, ExpandContextCall{
							ToolUseID: callID,
							ShadowID:  "",
						})
						log.Debug().
							Str("tool_id", callID).
							Msg("stream_buffer: suppressing expand_context tool (Responses API)")
						continue
					}
				}
			}
		}

		if eventType, _ := event["type"].(string); eventType == "response.function_call_arguments.delta" {
			if sb.inToolUse && sb.currentToolName == ExpandContextToolName {
				if delta, ok := event["delta"].(string); ok {
					sb.extractShadowID(delta)
				}
				continue
			}
		}

		if eventType, _ := event["type"].(string); eventType == "response.output_item.done" {
			if sb.inToolUse && sb.currentToolName == ExpandContextToolName {
				sb.inToolUse = false
				sb.currentToolName = ""
				sb.currentToolID = ""
				continue
			}
		}

		// Not suppressed - write to output
		output.Write(line)
		output.WriteByte('\n')
	}

	if output.Len() == 0 {
		return nil, nil
	}

	return output.Bytes(), nil
}

// extractShadowID tries to extract the shadow ID from partial JSON input.
func (sb *StreamBuffer) extractShadowID(partialJSON string) {
	sb.buffer.WriteString(partialJSON)

	// Try to parse accumulated JSON
	var input map[string]any
	if err := json.Unmarshal(sb.buffer.Bytes(), &input); err == nil {
		if id, ok := input["id"].(string); ok {
			// Update the last suppressed call with the shadow ID
			if len(sb.suppressedCalls) > 0 {
				sb.suppressedCalls[len(sb.suppressedCalls)-1].ShadowID = id
			}
		}
	}
}

// filterOpenAIToolCalls filters expand_context from OpenAI streaming format.
// OpenAI streams tool calls across multiple chunks:
//   - First chunk: has function.name and call id
//   - Subsequent chunks: have function.arguments deltas (no name)
//
// We track state with openAIInToolUse to suppress all chunks for expand_context.
func (sb *StreamBuffer) filterOpenAIToolCalls(choices []any) bool {
	for _, choice := range choices {
		c, ok := choice.(map[string]any)
		if !ok {
			continue
		}

		delta, ok := c["delta"].(map[string]any)
		if !ok {
			continue
		}

		toolCalls, ok := delta["tool_calls"].([]any)
		if !ok {
			continue
		}

		for _, tc := range toolCalls {
			call, ok := tc.(map[string]any)
			if !ok {
				continue
			}

			fn, _ := call["function"].(map[string]any)
			if fn == nil {
				// No function field — check if we're in a suppressed tool call
				if sb.openAIInToolUse {
					return true // Suppress argument delta chunks
				}
				continue
			}

			name, _ := fn["name"].(string)
			if name == ExpandContextToolName {
				id, _ := call["id"].(string)
				sb.openAIInToolUse = true
				sb.buffer.Reset()
				sb.suppressedCalls = append(sb.suppressedCalls, ExpandContextCall{
					ToolUseID: id,
					ShadowID:  "",
				})
				log.Debug().
					Str("tool_id", id).
					Msg("stream_buffer: suppressing expand_context tool (OpenAI)")
				return true
			}

			// Not expand_context — if we were suppressing, stop
			if name != "" {
				sb.openAIInToolUse = false
			}

			// Check if this is an arguments delta for a suppressed expand_context
			if sb.openAIInToolUse {
				if args, ok := fn["arguments"].(string); ok && strings.Contains(args, "shadow_") {
					var input map[string]string
					if err := json.Unmarshal([]byte(args), &input); err == nil {
						if shadowID, ok := input["id"]; ok && len(sb.suppressedCalls) > 0 {
							sb.suppressedCalls[len(sb.suppressedCalls)-1].ShadowID = shadowID
						}
					}
				}
				return true // Suppress this chunk
			}
		}
	}

	return false
}

// GetSuppressedCalls returns a copy of the suppressed expand_context calls.
// These need to be handled by the gateway before returning to client.
func (sb *StreamBuffer) GetSuppressedCalls() []ExpandContextCall {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	result := make([]ExpandContextCall, len(sb.suppressedCalls))
	copy(result, sb.suppressedCalls)
	return result
}

// Reset clears the buffer state.
func (sb *StreamBuffer) Reset() {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.buffer.Reset()
	sb.suppressedCalls = sb.suppressedCalls[:0]
	sb.inToolUse = false
	sb.openAIInToolUse = false
	sb.currentToolName = ""
	sb.currentToolID = ""
}

// HasSuppressedCalls returns true if any expand_context calls were suppressed.
func (sb *StreamBuffer) HasSuppressedCalls() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return len(sb.suppressedCalls) > 0
}
