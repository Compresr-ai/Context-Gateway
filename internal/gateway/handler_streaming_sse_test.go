package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseSSEEvents parses raw SSE bytes into a slice of (event, data) pairs.
func parseSSEEvents(raw []byte) []sseEvent {
	var events []sseEvent
	lines := strings.Split(string(raw), "\n")
	var currentEvent string
	for _, line := range lines {
		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			events = append(events, sseEvent{Event: currentEvent, Data: data})
			currentEvent = ""
		}
	}
	return events
}

type sseEvent struct {
	Event string
	Data  string
}

func (e sseEvent) JSON() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(e.Data), &m)
	return m
}

func TestJsonToAnthropicSSE_ThinkingBlock(t *testing.T) {
	response := map[string]any{
		"id":    "msg_123",
		"type":  "message",
		"role":  "assistant",
		"model": "claude-opus-4-6",
		"content": []any{
			map[string]any{
				"type":      "thinking",
				"thinking":  "Let me analyze the code...",
				"signature": "abc123sig",
			},
			map[string]any{
				"type": "text",
				"text": "Here is my analysis.",
			},
		},
		"stop_reason": "end_turn",
		"usage":       map[string]any{"input_tokens": float64(100), "output_tokens": float64(50)},
	}
	body, err := json.Marshal(response)
	require.NoError(t, err)

	result := jsonToAnthropicSSE(body)
	events := parseSSEEvents(result)

	// Find thinking-related events
	var thinkingStart, thinkingDelta, signatureDelta sseEvent
	var foundThinkingStart, foundThinkingDelta, foundSignatureDelta, foundThinkingStop bool

	for _, e := range events {
		data := e.JSON()
		switch e.Event {
		case "content_block_start":
			if cb, ok := data["content_block"].(map[string]any); ok {
				if cb["type"] == "thinking" {
					thinkingStart = e
					foundThinkingStart = true
				}
			}
		case "content_block_delta":
			if delta, ok := data["delta"].(map[string]any); ok {
				if delta["type"] == "thinking_delta" {
					thinkingDelta = e
					foundThinkingDelta = true
				}
				if delta["type"] == "signature_delta" {
					signatureDelta = e
					foundSignatureDelta = true
				}
			}
		case "content_block_stop":
			if idx, ok := data["index"].(float64); ok && idx == 0 {
				_ = e
				foundThinkingStop = true
			}
		}
	}

	// Verify all thinking events are present
	assert.True(t, foundThinkingStart, "missing content_block_start for thinking")
	assert.True(t, foundThinkingDelta, "missing content_block_delta with thinking_delta")
	assert.True(t, foundSignatureDelta, "missing content_block_delta with signature_delta")
	assert.True(t, foundThinkingStop, "missing content_block_stop for thinking")

	// Verify content_block_start has EMPTY thinking (content comes via delta)
	startData := thinkingStart.JSON()
	cb := startData["content_block"].(map[string]any)
	assert.Equal(t, "thinking", cb["type"])
	assert.Equal(t, "", cb["thinking"], "content_block_start must have empty thinking field")

	// Verify thinking_delta carries the actual content
	deltaData := thinkingDelta.JSON()
	delta := deltaData["delta"].(map[string]any)
	assert.Equal(t, "thinking_delta", delta["type"])
	assert.Equal(t, "Let me analyze the code...", delta["thinking"])

	// Verify signature_delta
	sigData := signatureDelta.JSON()
	sigDelta := sigData["delta"].(map[string]any)
	assert.Equal(t, "signature_delta", sigDelta["type"])
	assert.Equal(t, "abc123sig", sigDelta["signature"])
}

func TestJsonToAnthropicSSE_ThinkingBlockWithoutSignature(t *testing.T) {
	response := map[string]any{
		"id":    "msg_456",
		"type":  "message",
		"role":  "assistant",
		"model": "claude-opus-4-6",
		"content": []any{
			map[string]any{
				"type":     "thinking",
				"thinking": "Quick thought",
			},
		},
		"stop_reason": "end_turn",
		"usage":       map[string]any{"input_tokens": float64(10), "output_tokens": float64(5)},
	}
	body, err := json.Marshal(response)
	require.NoError(t, err)

	result := jsonToAnthropicSSE(body)
	events := parseSSEEvents(result)

	var hasThinkingDelta, hasSignatureDelta bool
	for _, e := range events {
		data := e.JSON()
		if e.Event == "content_block_delta" {
			if delta, ok := data["delta"].(map[string]any); ok {
				if delta["type"] == "thinking_delta" {
					hasThinkingDelta = true
					assert.Equal(t, "Quick thought", delta["thinking"])
				}
				if delta["type"] == "signature_delta" {
					hasSignatureDelta = true
				}
			}
		}
	}

	assert.True(t, hasThinkingDelta, "thinking_delta must be present")
	assert.False(t, hasSignatureDelta, "signature_delta must NOT be present when no signature")
}

func TestJsonToAnthropicSSE_RedactedThinking(t *testing.T) {
	response := map[string]any{
		"id":    "msg_789",
		"type":  "message",
		"role":  "assistant",
		"model": "claude-opus-4-6",
		"content": []any{
			map[string]any{
				"type": "redacted_thinking",
				"data": "opaque-encrypted-data-here",
			},
		},
		"stop_reason": "end_turn",
		"usage":       map[string]any{"input_tokens": float64(10), "output_tokens": float64(5)},
	}
	body, err := json.Marshal(response)
	require.NoError(t, err)

	result := jsonToAnthropicSSE(body)
	events := parseSSEEvents(result)

	// Redacted thinking should be emitted as-is in content_block_start (no deltas)
	var foundStart bool
	for _, e := range events {
		if e.Event == "content_block_start" {
			data := e.JSON()
			if cb, ok := data["content_block"].(map[string]any); ok {
				if cb["type"] == "redacted_thinking" {
					foundStart = true
					assert.Equal(t, "opaque-encrypted-data-here", cb["data"])
				}
			}
		}
	}
	assert.True(t, foundStart, "redacted_thinking must appear in content_block_start")
}

func TestJsonToAnthropicSSE_MixedContent(t *testing.T) {
	// Simulate a real response: thinking + text + tool_use
	response := map[string]any{
		"id":    "msg_mixed",
		"type":  "message",
		"role":  "assistant",
		"model": "claude-opus-4-6",
		"content": []any{
			map[string]any{
				"type":      "thinking",
				"thinking":  "I should read the file first.",
				"signature": "sig001",
			},
			map[string]any{
				"type": "text",
				"text": "Let me read that file.",
			},
			map[string]any{
				"type":  "tool_use",
				"id":    "toolu_01",
				"name":  "Read",
				"input": map[string]any{"file_path": "/tmp/test.go"},
			},
		},
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": float64(200), "output_tokens": float64(100)},
	}
	body, err := json.Marshal(response)
	require.NoError(t, err)

	result := jsonToAnthropicSSE(body)
	events := parseSSEEvents(result)

	// Count event types
	var startEvents, deltaEvents, stopEvents int
	blockTypes := map[string]bool{}
	deltaTypes := map[string]bool{}

	for _, e := range events {
		data := e.JSON()
		switch e.Event {
		case "content_block_start":
			startEvents++
			if cb, ok := data["content_block"].(map[string]any); ok {
				if t, ok := cb["type"].(string); ok {
					blockTypes[t] = true
				}
			}
		case "content_block_delta":
			deltaEvents++
			if d, ok := data["delta"].(map[string]any); ok {
				if t, ok := d["type"].(string); ok {
					deltaTypes[t] = true
				}
			}
		case "content_block_stop":
			stopEvents++
		}
	}

	// 3 blocks → 3 starts, 3 stops
	assert.Equal(t, 3, startEvents, "expected 3 content_block_start events")
	assert.Equal(t, 3, stopEvents, "expected 3 content_block_stop events")

	// 4 deltas: thinking_delta + signature_delta + text_delta + input_json_delta
	assert.Equal(t, 4, deltaEvents, "expected 4 content_block_delta events")

	// All block types present
	assert.True(t, blockTypes["thinking"])
	assert.True(t, blockTypes["text"])
	assert.True(t, blockTypes["tool_use"])

	// All delta types present
	assert.True(t, deltaTypes["thinking_delta"])
	assert.True(t, deltaTypes["signature_delta"])
	assert.True(t, deltaTypes["text_delta"])
	assert.True(t, deltaTypes["input_json_delta"])
}

func TestJsonToAnthropicSSE_TextBlock_Unchanged(t *testing.T) {
	// Regression: ensure existing text block handling is not broken
	response := map[string]any{
		"id":    "msg_text",
		"type":  "message",
		"role":  "assistant",
		"model": "claude-opus-4-6",
		"content": []any{
			map[string]any{"type": "text", "text": "Hello world"},
		},
		"stop_reason": "end_turn",
		"usage":       map[string]any{"input_tokens": float64(5), "output_tokens": float64(2)},
	}
	body, err := json.Marshal(response)
	require.NoError(t, err)

	result := jsonToAnthropicSSE(body)
	events := parseSSEEvents(result)

	var textDeltaContent string
	for _, e := range events {
		if e.Event == "content_block_delta" {
			data := e.JSON()
			if d, ok := data["delta"].(map[string]any); ok {
				if d["type"] == "text_delta" {
					textDeltaContent, _ = d["text"].(string)
				}
			}
		}
	}
	assert.Equal(t, "Hello world", textDeltaContent)
}
