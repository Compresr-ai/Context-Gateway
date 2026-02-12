package unit

import (
	"encoding/json"
	"testing"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// ANTHROPIC TOOL OUTPUT TESTS
// =============================================================================

func TestAnthropic_ExtractToolOutput(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{
		"model": "claude-3",
		"messages": [
			{"role": "user", "content": "Read the file"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_001", "name": "read_file", "input": {}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_001", "content": "package main\n\nfunc main() {}"}]}
		]
	}`)

	extracted, err := adapter.ExtractToolOutput(body)

	require.NoError(t, err)
	require.Len(t, extracted, 1)
	assert.Equal(t, "toolu_001", extracted[0].ID)
	assert.Equal(t, "package main\n\nfunc main() {}", extracted[0].Content)
	assert.Equal(t, "tool_result", extracted[0].ContentType)
	assert.Equal(t, "read_file", extracted[0].ToolName)
}

func TestAnthropic_ApplyToolOutput(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{
		"model": "claude-3",
		"messages": [
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_001", "content": "original content"}]}
		]
	}`)

	results := []adapters.CompressedResult{
		{ID: "toolu_001", Compressed: "compressed summary"},
	}

	modified, err := adapter.ApplyToolOutput(body, results)

	require.NoError(t, err)

	var req map[string]interface{}
	require.NoError(t, json.Unmarshal(modified, &req))

	messages := req["messages"].([]interface{})
	userMsg := messages[0].(map[string]interface{})
	content := userMsg["content"].([]interface{})
	block := content[0].(map[string]interface{})
	assert.Equal(t, "compressed summary", block["content"])
}

func TestAnthropic_ExtractToolOutput_ArrayContent(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{
		"model": "claude-3",
		"messages": [
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_001", "content": [
					{"type": "text", "text": "first part"},
					{"type": "text", "text": " second part"}
				]}
			]}
		]
	}`)

	extracted, err := adapter.ExtractToolOutput(body)

	require.NoError(t, err)
	require.Len(t, extracted, 1)
	assert.Equal(t, "first part second part", extracted[0].Content)
}

// =============================================================================
// ANTHROPIC TOOL DISCOVERY TESTS (Stub - Not Yet Implemented)
// =============================================================================

func TestAnthropic_ExtractToolDiscovery_Stub(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{
		"model": "claude-3",
		"messages": [{"role": "user", "content": "Help"}],
		"tools": [
			{"name": "read_file", "description": "Read a file", "input_schema": {}}
		]
	}`)

	extracted, err := adapter.ExtractToolDiscovery(body, nil)

	require.NoError(t, err)
	assert.Empty(t, extracted) // Stub: Not yet implemented
}

func TestAnthropic_ApplyToolDiscovery_Stub(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{
		"model": "claude-3",
		"messages": [{"role": "user", "content": "Help"}],
		"tools": [
			{"name": "read_file", "description": "Read"}
		]
	}`)

	results := []adapters.CompressedResult{
		{ID: "read_file", Keep: true},
	}

	modified, err := adapter.ApplyToolDiscovery(body, results)

	require.NoError(t, err)
	assert.Equal(t, body, modified) // Stub: returns unchanged
}

// =============================================================================
// REGISTRY TESTS
// =============================================================================

func TestRegistry_GetAdapters(t *testing.T) {
	registry := adapters.NewRegistry()

	openai := registry.Get("openai")
	assert.NotNil(t, openai)
	assert.Equal(t, "openai", openai.Name())

	anthropic := registry.Get("anthropic")
	assert.NotNil(t, anthropic)
	assert.Equal(t, "anthropic", anthropic.Name())

	unknown := registry.Get("unknown")
	assert.Nil(t, unknown)
}

// =============================================================================
// EDGE CASES
// =============================================================================

func TestAnthropic_ExtractToolOutput_NoToolResults(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{
		"model": "claude-3",
		"messages": [
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi there"}
		]
	}`)

	extracted, err := adapter.ExtractToolOutput(body)

	require.NoError(t, err)
	assert.Empty(t, extracted)
}

func TestAnthropic_ExtractToolOutput_InvalidJSON(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{invalid json}`)

	_, err := adapter.ExtractToolOutput(body)

	require.Error(t, err)
}

// =============================================================================
// EXTRACT USER QUERY TESTS
// =============================================================================

func TestAnthropic_ExtractUserQuery_SimpleTextMessage(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{
		"model": "claude-3",
		"messages": [
			{"role": "user", "content": "What is the capital of France?"}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Equal(t, "What is the capital of France?", query)
}

func TestAnthropic_ExtractUserQuery_MultipleUserMessages(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{
		"model": "claude-3",
		"messages": [
			{"role": "user", "content": "First question"},
			{"role": "assistant", "content": "First answer"},
			{"role": "user", "content": "Second question"},
			{"role": "assistant", "content": "Second answer"},
			{"role": "user", "content": "Third and final question"}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Equal(t, "Third and final question", query, "Should return the last user message")
}

func TestAnthropic_ExtractUserQuery_ContentBlocks(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{
		"model": "claude-3",
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "Please analyze this:"},
				{"type": "text", "text": "More context here"}
			]}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	// Implementation concatenates all text blocks
	assert.Equal(t, "Please analyze this:More context here", query, "Should extract and concatenate all text blocks")
}

func TestAnthropic_ExtractUserQuery_WithToolResult(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	// When last user message contains tool_result, it should still find the last actual user query
	body := []byte(`{
		"model": "claude-3",
		"messages": [
			{"role": "user", "content": "Read the config file"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_001", "name": "read_file", "input": {}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_001", "content": "config data..."}]}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	// The last user message contains tool_result, so it should look for text content
	// If no text content in tool_result message, may return empty or previous message
	assert.NotEmpty(t, query, "Should handle tool_result messages gracefully")
}

func TestAnthropic_ExtractUserQuery_NoUserMessages(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{
		"model": "claude-3",
		"messages": [
			{"role": "assistant", "content": "I'm an assistant"}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Empty(t, query, "Should return empty when no user messages")
}

func TestAnthropic_ExtractUserQuery_EmptyMessages(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{
		"model": "claude-3",
		"messages": []
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Empty(t, query)
}

func TestAnthropic_ExtractUserQuery_InvalidJSON(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{invalid json}`)

	query := adapter.ExtractUserQuery(body)
	assert.Empty(t, query, "Should return empty on invalid JSON")
}

// =============================================================================
// ADAPTER INTERFACE COMPLIANCE
// =============================================================================

func TestAnthropicAdapter_ImplementsInterface(t *testing.T) {
	var _ adapters.Adapter = adapters.NewAnthropicAdapter()
}
