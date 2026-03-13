// Expander Tests - Context Expansion via Phantom Tool
//
// Tests the expand_context mechanism for restoring compressed content:
// - ParseExpandContextCalls: Extract shadow IDs from LLM responses
// - Filter functions: Remove phantom tool from client-facing responses
// Supports both Anthropic and OpenAI response formats.
package unit

import (
	"testing"

	"github.com/stretchr/testify/assert"

	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
	"github.com/compresr/context-gateway/tests/anthropic/fixtures"
)

// TestExpander_ParseExpandContextCalls_Anthropic verifies Anthropic format parsing.
// Anthropic uses content blocks with type:"tool_use" and name:"expand_context".
// Input contains {"id": "shadow_xxx"} with the shadow ID to expand.
func TestExpander_ParseExpandContextCalls_Anthropic(t *testing.T) {
	st := fixtures.TestStore()
	expander := tooloutput.NewExpander(st, nil)

	response := fixtures.AnthropicResponseWithExpandCall("toolu_001", "shadow_abc123")

	calls := expander.ParseExpandContextCalls(response)

	assert.Len(t, calls, 1)
	assert.Equal(t, "toolu_001", calls[0].ToolUseID)
	assert.Equal(t, "shadow_abc123", calls[0].ShadowID)
}

func TestExpander_ParseExpandContextCalls_OpenAI(t *testing.T) {
	st := fixtures.TestStore()
	expander := tooloutput.NewExpander(st, nil)

	response := fixtures.OpenAIResponseWithExpandCall("call_001", "shadow_xyz789")

	calls := expander.ParseExpandContextCalls(response)

	assert.Len(t, calls, 1)
	assert.Equal(t, "call_001", calls[0].ToolUseID)
	assert.Equal(t, "shadow_xyz789", calls[0].ShadowID)
}

func TestExpander_ParseExpandContextCalls_NoExpandCalls(t *testing.T) {
	st := fixtures.TestStore()
	expander := tooloutput.NewExpander(st, nil)

	response := fixtures.AnthropicResponseNoExpand("Here is my response.")

	calls := expander.ParseExpandContextCalls(response)

	assert.Empty(t, calls)
}

func TestExpander_ParseExpandContextCalls_OtherToolCall(t *testing.T) {
	st := fixtures.TestStore()
	expander := tooloutput.NewExpander(st, nil)

	response := fixtures.AnthropicResponseWithOtherToolCall("toolu_002", "read_file")

	calls := expander.ParseExpandContextCalls(response)

	assert.Empty(t, calls, "should not parse non-expand_context tool calls")
}

func TestExpander_ParseExpandContextCalls_EmptyResponse(t *testing.T) {
	st := fixtures.TestStore()
	expander := tooloutput.NewExpander(st, nil)

	calls := expander.ParseExpandContextCalls([]byte{})

	assert.Empty(t, calls)
}

func TestExpander_ParseExpandContextCalls_InvalidJSON(t *testing.T) {
	st := fixtures.TestStore()
	expander := tooloutput.NewExpander(st, nil)

	calls := expander.ParseExpandContextCalls([]byte("not valid json"))

	assert.Empty(t, calls)
}

func TestNewExpander(t *testing.T) {
	st := fixtures.TestStore()

	expander := tooloutput.NewExpander(st, nil)

	assert.NotNil(t, expander)
}
