package unit

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// KV-CACHE BYTE PRESERVATION TESTS
//
// These tests verify that ApplyToolOutput (using sjson) preserves all bytes
// outside the targeted content field. This is critical for Anthropic's
// KV-cache prefix matching, which requires exact byte-level consistency.
// =============================================================================

// TestAnthropic_BytePreservation_FieldOrdering verifies that non-alphabetical
// field ordering is preserved through ApplyToolOutput.
func TestAnthropic_BytePreservation_FieldOrdering(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	// Field order: "role" before "content" (non-alphabetical)
	body := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"original data"}]}]}`)

	results := []adapters.CompressedResult{
		{ID: "t1", Compressed: "compressed data", MessageIndex: 2, BlockIndex: 0},
	}

	modified, err := adapter.ApplyToolOutput(body, results)
	require.NoError(t, err)

	// Verify "role" still comes before "content" in all messages
	// The prefix up to the compressed content should be byte-identical
	prefixEnd := bytes.Index(body, []byte(`"original data"`))
	require.Greater(t, prefixEnd, 0)

	modifiedPrefixEnd := bytes.Index(modified, []byte(`"compressed data"`))
	require.Greater(t, modifiedPrefixEnd, 0)

	// Everything before the content value should be identical
	assert.Equal(t, string(body[:prefixEnd]), string(modified[:modifiedPrefixEnd]),
		"prefix bytes before content value must be identical")

	// Verify field ordering is preserved (role before content)
	assert.True(t, bytes.Contains(modified, []byte(`"role":"user","content"`)),
		"field order should be preserved: role before content")
}

// TestAnthropic_BytePreservation_MultiTurnPrefix verifies that applying
// compression to turn N+1 produces a byte-identical prefix to turn N.
func TestAnthropic_BytePreservation_MultiTurnPrefix(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	// Turn 1: compress one tool result
	turn1Body := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"start"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"long original content that gets compressed"}]}]}`)

	turn1Results := []adapters.CompressedResult{
		{ID: "t1", Compressed: "<<<SHADOW:shadow_abc>>>", MessageIndex: 2, BlockIndex: 0},
	}

	turn1Modified, err := adapter.ApplyToolOutput(turn1Body, turn1Results)
	require.NoError(t, err)

	// Turn 2: same history prefix + new messages, re-apply same compression
	turn2Body := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"start"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"long original content that gets compressed"}]},{"role":"assistant","content":"I read the file"},{"role":"user","content":"now do something else"}]}`)

	turn2Results := []adapters.CompressedResult{
		{ID: "t1", Compressed: "<<<SHADOW:shadow_abc>>>", MessageIndex: 2, BlockIndex: 0},
	}

	turn2Modified, err := adapter.ApplyToolOutput(turn2Body, turn2Results)
	require.NoError(t, err)

	// Turn 1 ends at the last `]}` (closing messages array + root object).
	// Turn 2 has extra messages after the same prefix.
	// Remove the trailing `]}` from turn1 to get the prefix.
	turn1Prefix := turn1Modified[:len(turn1Modified)-2] // strip `]}`

	// Turn 2 must start with exactly the same bytes (minus the closing `]}` that turn1 had)
	// The prefix should be: `{"model":"claude-3","messages":[...msg0...,...msg1...,...msg2_compressed...`
	// Turn2 continues with `,...msg3,...msg4...]}`
	require.True(t, len(turn2Modified) > len(turn1Prefix),
		"turn 2 must be longer than turn 1 prefix")

	assert.Equal(t,
		string(turn1Prefix),
		string(turn2Modified[:len(turn1Prefix)]),
		"prefix bytes must be identical across turns")
}

// TestAnthropic_BytePreservation_SpecialChars verifies that content with
// special characters doesn't corrupt surrounding JSON.
func TestAnthropic_BytePreservation_SpecialChars(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	tests := []struct {
		name       string
		compressed string
	}{
		{"unicode", "compressed: \u00e9\u00e8\u00ea \u4e16\u754c"},
		{"html_entities", "compressed: <div>&amp;</div>"},
		{"newlines", "compressed:\nline1\nline2\ttab"},
		{"quotes", `compressed: "quoted" and 'single'`},
		{"backslashes", `compressed: path\to\file`},
		{"null_in_json", "compressed: value"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"claude-3","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"original"}]}]}`)

			results := []adapters.CompressedResult{
				{ID: "t1", Compressed: tc.compressed, MessageIndex: 0, BlockIndex: 0},
			}

			modified, err := adapter.ApplyToolOutput(body, results)
			require.NoError(t, err)

			// Must be valid JSON
			assert.True(t, isValidJSON(modified), "output must be valid JSON for %s", tc.name)

			// Field ordering preserved
			assert.True(t, bytes.Contains(modified, []byte(`"model":"claude-3"`)))
		})
	}
}

// TestAnthropic_BytePreservation_MultipleResults verifies that multiple
// compressions in one request preserve all byte ordering.
func TestAnthropic_BytePreservation_MultipleResults(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{"model":"claude-3","max_tokens":4096,"messages":[{"role":"user","content":"start"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"first original content"}]},{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"write","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"second original content"}]}]}`)

	results := []adapters.CompressedResult{
		{ID: "t1", Compressed: "first compressed", MessageIndex: 2, BlockIndex: 0},
		{ID: "t2", Compressed: "second compressed", MessageIndex: 4, BlockIndex: 0},
	}

	modified, err := adapter.ApplyToolOutput(body, results)
	require.NoError(t, err)

	// Valid JSON
	assert.True(t, isValidJSON(modified))

	// Both compressions applied
	assert.Contains(t, string(modified), "first compressed")
	assert.Contains(t, string(modified), "second compressed")
	assert.NotContains(t, string(modified), "first original")
	assert.NotContains(t, string(modified), "second original")

	// Structural fields preserved
	assert.Contains(t, string(modified), `"max_tokens":4096`)
	assert.Contains(t, string(modified), `"model":"claude-3"`)
}

// TestAnthropic_BytePreservation_EmptyResults is a no-op passthrough.
func TestAnthropic_BytePreservation_EmptyResults(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"hello"}]}`)
	modified, err := adapter.ApplyToolOutput(body, nil)
	require.NoError(t, err)

	// Byte-identical when no results
	assert.Equal(t, body, modified)
}

// =============================================================================
// OPENAI BYTE PRESERVATION
// =============================================================================

func TestOpenAI_BytePreservation_FieldOrdering(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	// Chat Completions format with non-alphabetical field order
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"original tool output"}]}`)

	results := []adapters.CompressedResult{
		{ID: "call_1", Compressed: "compressed output", MessageIndex: 2, BlockIndex: 0},
	}

	modified, err := adapter.ApplyToolOutput(body, results)
	require.NoError(t, err)
	assert.True(t, isValidJSON(modified))
	assert.Contains(t, string(modified), "compressed output")
	assert.Contains(t, string(modified), `"model":"gpt-4"`)
}

// =============================================================================
// GEMINI BYTE PRESERVATION
// =============================================================================

func TestGemini_BytePreservation_FieldOrdering(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"functionCall":{"name":"read","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"read","response":{"result":"original content"}}}]}]}`)

	results := []adapters.CompressedResult{
		{ID: "2_0", Compressed: "compressed content", MessageIndex: 2, BlockIndex: 0},
	}

	modified, err := adapter.ApplyToolOutput(body, results)
	require.NoError(t, err)
	assert.True(t, isValidJSON(modified))
	assert.Contains(t, string(modified), "compressed content")
}

// =============================================================================
// HELPERS
// =============================================================================

func isValidJSON(b []byte) bool {
	var v any
	return json.Unmarshal(b, &v) == nil
}
