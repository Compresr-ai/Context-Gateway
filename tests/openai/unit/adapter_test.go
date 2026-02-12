package unit

import (
	"encoding/json"
	"testing"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// OPENAI RESPONSES API - TOOL OUTPUT TESTS
// =============================================================================

func TestOpenAI_ExtractToolOutput(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-5",
		"input": [
			{"type": "function_call", "call_id": "call_001", "name": "read_file", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "call_001", "output": "package main\n\nfunc main() {}"}
		]
	}`)

	extracted, err := adapter.ExtractToolOutput(body)

	require.NoError(t, err)
	require.Len(t, extracted, 1)
	assert.Equal(t, "call_001", extracted[0].ID)
	assert.Equal(t, "package main\n\nfunc main() {}", extracted[0].Content)
	assert.Equal(t, "tool_result", extracted[0].ContentType)
	assert.Equal(t, "read_file", extracted[0].ToolName)
	assert.Equal(t, 1, extracted[0].MessageIndex)
}

func TestOpenAI_ApplyToolOutput(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-5",
		"input": [
			{"type": "function_call", "call_id": "call_001", "name": "read_file", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "call_001", "output": "original content"}
		]
	}`)

	results := []adapters.CompressedResult{
		{ID: "call_001", Compressed: "compressed summary"},
	}

	modified, err := adapter.ApplyToolOutput(body, results)

	require.NoError(t, err)

	var req map[string]any
	require.NoError(t, json.Unmarshal(modified, &req))

	items := req["input"].([]any)
	outputItem := items[1].(map[string]any)
	assert.Equal(t, "compressed summary", outputItem["output"])
}

func TestOpenAI_ExtractToolOutput_Multiple(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-5",
		"input": [
			{"type": "function_call", "call_id": "call_001", "name": "read_file", "arguments": "{}"},
			{"type": "function_call", "call_id": "call_002", "name": "write_file", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "call_001", "output": "file1 content"},
			{"type": "function_call_output", "call_id": "call_002", "output": "file2 content"}
		]
	}`)

	extracted, err := adapter.ExtractToolOutput(body)

	require.NoError(t, err)
	assert.Len(t, extracted, 2)
	assert.Equal(t, "call_001", extracted[0].ID)
	assert.Equal(t, "read_file", extracted[0].ToolName)
	assert.Equal(t, "call_002", extracted[1].ID)
	assert.Equal(t, "write_file", extracted[1].ToolName)
}

func TestOpenAI_ApplyToolOutput_Multiple(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-5",
		"input": [
			{"type": "function_call", "call_id": "call_001", "name": "read_file", "arguments": "{}"},
			{"type": "function_call", "call_id": "call_002", "name": "write_file", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "call_001", "output": "original1"},
			{"type": "function_call_output", "call_id": "call_002", "output": "original2"}
		]
	}`)

	results := []adapters.CompressedResult{
		{ID: "call_001", Compressed: "compressed1"},
		{ID: "call_002", Compressed: "compressed2"},
	}

	modified, err := adapter.ApplyToolOutput(body, results)

	require.NoError(t, err)

	var req map[string]any
	require.NoError(t, json.Unmarshal(modified, &req))

	items := req["input"].([]any)
	assert.Equal(t, "compressed1", items[2].(map[string]any)["output"])
	assert.Equal(t, "compressed2", items[3].(map[string]any)["output"])
}

// =============================================================================
// OPENAI TOOL DISCOVERY TESTS (Stub)
// =============================================================================

func TestOpenAI_ExtractToolDiscovery_Stub(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-5",
		"input": [{"type": "message", "role": "user", "content": "Help"}],
		"tools": [{"type": "function", "name": "read_file"}]
	}`)

	extracted, err := adapter.ExtractToolDiscovery(body, nil)

	require.NoError(t, err)
	assert.Empty(t, extracted)
}

func TestOpenAI_ApplyToolDiscovery_Stub(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{"model": "gpt-5", "tools": []}`)

	modified, err := adapter.ApplyToolDiscovery(body, nil)

	require.NoError(t, err)
	assert.Equal(t, body, modified)
}

// =============================================================================
// EDGE CASES
// =============================================================================

func TestOpenAI_ExtractToolOutput_EmptyInput(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{"model": "gpt-5", "input": []}`)

	extracted, err := adapter.ExtractToolOutput(body)

	require.NoError(t, err)
	assert.Empty(t, extracted)
}

func TestOpenAI_ExtractToolOutput_NoInput(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{"model": "gpt-5"}`)

	extracted, err := adapter.ExtractToolOutput(body)

	require.NoError(t, err)
	assert.Nil(t, extracted)
}

func TestOpenAI_ExtractToolOutput_StringInput(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{"model": "gpt-5", "input": "just a string prompt"}`)

	extracted, err := adapter.ExtractToolOutput(body)

	require.NoError(t, err)
	assert.Nil(t, extracted)
}

func TestOpenAI_ApplyToolOutput_NoResults(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{"model": "gpt-5", "input": []}`)

	modified, err := adapter.ApplyToolOutput(body, nil)

	require.NoError(t, err)
	assert.Equal(t, body, modified)
}

func TestOpenAI_ExtractToolOutput_InvalidJSON(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{invalid json}`)

	_, err := adapter.ExtractToolOutput(body)

	require.Error(t, err)
}

func TestOpenAI_ApplyToolOutput_InvalidJSON(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{invalid json}`)

	_, err := adapter.ApplyToolOutput(body, []adapters.CompressedResult{{ID: "x", Compressed: "y"}})

	require.Error(t, err)
}

// =============================================================================
// EXTRACT USER QUERY TESTS (OpenAI Responses API)
// =============================================================================

func TestOpenAI_ExtractUserQuery_SimpleMessage(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-4",
		"input": [
			{"type": "message", "role": "user", "content": "What is the capital of France?"}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Equal(t, "What is the capital of France?", query)
}

func TestOpenAI_ExtractUserQuery_MultipleUserMessages(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-4",
		"input": [
			{"type": "message", "role": "user", "content": "First question"},
			{"type": "message", "role": "assistant", "content": "First answer"},
			{"type": "message", "role": "user", "content": "Second question"},
			{"type": "message", "role": "assistant", "content": "Second answer"},
			{"type": "message", "role": "user", "content": "Third and final question"}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Equal(t, "Third and final question", query, "Should return the last user message")
}

func TestOpenAI_ExtractUserQuery_WithFunctionCalls(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-4",
		"input": [
			{"type": "message", "role": "user", "content": "Read the config file"},
			{"type": "function_call", "call_id": "call_001", "name": "read_file", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "call_001", "output": "config data..."}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Equal(t, "Read the config file", query, "Should find user message before function calls")
}

func TestOpenAI_ExtractUserQuery_NoUserMessages(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-4",
		"input": [
			{"type": "message", "role": "assistant", "content": "I'm an assistant"}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Empty(t, query, "Should return empty when no user messages")
}

func TestOpenAI_ExtractUserQuery_EmptyInput(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-4",
		"input": []
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Empty(t, query)
}

func TestOpenAI_ExtractUserQuery_StringInput(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	// Some OpenAI calls use a simple string as input
	body := []byte(`{
		"model": "gpt-4",
		"input": "Just a simple prompt string"
	}`)

	query := adapter.ExtractUserQuery(body)
	// String inputs might not be parsed as user message, depends on implementation
	// The test validates the behavior doesn't crash
	t.Logf("Query from string input: %q", query)
}

func TestOpenAI_ExtractUserQuery_InvalidJSON(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{invalid json}`)

	query := adapter.ExtractUserQuery(body)
	assert.Empty(t, query, "Should return empty on invalid JSON")
}

func TestOpenAI_ExtractUserQuery_MixedItemTypes(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-4",
		"input": [
			{"type": "message", "role": "user", "content": "Initial request"},
			{"type": "function_call", "call_id": "call_001", "name": "read_file", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "call_001", "output": "file contents"},
			{"type": "message", "role": "assistant", "content": "I found the file"},
			{"type": "message", "role": "user", "content": "Now what is in line 5?"}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Equal(t, "Now what is in line 5?", query, "Should return the last user message in mixed input")
}

// =============================================================================
// ADAPTER INTERFACE COMPLIANCE
// =============================================================================

func TestOpenAIAdapter_ImplementsInterface(t *testing.T) {
	var _ adapters.Adapter = adapters.NewOpenAIAdapter()
}
