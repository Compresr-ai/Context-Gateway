package unit

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// BASIC ADAPTER PROPERTIES
// =============================================================================

func TestOllama_Name(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()
	assert.Equal(t, "ollama", adapter.Name())
}

func TestOllama_Provider(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()
	assert.Equal(t, adapters.ProviderOllama, adapter.Provider())
}

// =============================================================================
// TOOL OUTPUT - Extract (Chat Completions format, same as OpenAI)
// =============================================================================

func TestOllama_ExtractToolOutput(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	body := []byte(`{
		"model": "llama3.1",
		"messages": [
			{"role": "user", "content": "Read the config file"},
			{"role": "assistant", "content": "", "tool_calls": [
				{"id": "call_001", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\": \"config.yaml\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_001", "content": "server:\n  port: 8080\n  host: localhost"}
		]
	}`)

	extracted, err := adapter.ExtractToolOutput(body)

	require.NoError(t, err)
	require.Len(t, extracted, 1)
	assert.Equal(t, "call_001", extracted[0].ID)
	assert.Equal(t, "server:\n  port: 8080\n  host: localhost", extracted[0].Content)
	assert.Equal(t, "tool_result", extracted[0].ContentType)
	assert.Equal(t, "read_file", extracted[0].ToolName)
}

// =============================================================================
// TOOL OUTPUT - Apply (Chat Completions format, same as OpenAI)
// =============================================================================

func TestOllama_ApplyToolOutput(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	body := []byte(`{
		"model": "llama3.1",
		"messages": [
			{"role": "user", "content": "Read the config file"},
			{"role": "assistant", "content": "", "tool_calls": [
				{"id": "call_001", "type": "function", "function": {"name": "read_file", "arguments": "{}"}}
			]},
			{"role": "tool", "tool_call_id": "call_001", "content": "original long config content here"}
		]
	}`)

	results := []adapters.CompressedResult{
		{ID: "call_001", Compressed: "compressed: server config with port 8080", MessageIndex: 2},
	}

	modified, err := adapter.ApplyToolOutput(body, results)

	require.NoError(t, err)

	var req map[string]any
	require.NoError(t, json.Unmarshal(modified, &req))

	messages := req["messages"].([]any)
	toolMsg := messages[2].(map[string]any)
	assert.Equal(t, "compressed: server config with port 8080", toolMsg["content"])
}

// =============================================================================
// TOOL OUTPUT - Multiple tools
// =============================================================================

func TestOllama_ExtractToolOutput_MultipleTools(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	body := []byte(`{
		"model": "llama3.1",
		"messages": [
			{"role": "user", "content": "Read both files"},
			{"role": "assistant", "content": "", "tool_calls": [
				{"id": "call_001", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\": \"a.txt\"}"}},
				{"id": "call_002", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\": \"b.txt\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_001", "content": "contents of file a"},
			{"role": "tool", "tool_call_id": "call_002", "content": "contents of file b"}
		]
	}`)

	extracted, err := adapter.ExtractToolOutput(body)

	require.NoError(t, err)
	require.Len(t, extracted, 2)
	assert.Equal(t, "call_001", extracted[0].ID)
	assert.Equal(t, "read_file", extracted[0].ToolName)
	assert.Equal(t, "contents of file a", extracted[0].Content)
	assert.Equal(t, "call_002", extracted[1].ID)
	assert.Equal(t, "read_file", extracted[1].ToolName)
	assert.Equal(t, "contents of file b", extracted[1].Content)
}

// =============================================================================
// USAGE EXTRACTION - Ollama-specific format
// =============================================================================

func TestOllama_ExtractUsage(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	responseBody := []byte(`{
		"model": "llama3.1",
		"created_at": "2024-01-01T00:00:00Z",
		"message": {"role": "assistant", "content": "Hello!"},
		"done": true,
		"prompt_eval_count": 100,
		"eval_count": 50
	}`)

	usage := adapter.ExtractUsage(responseBody)

	assert.Equal(t, 100, usage.InputTokens)
	assert.Equal(t, 50, usage.OutputTokens)
	assert.Equal(t, 150, usage.TotalTokens)
}

func TestOllama_ExtractUsage_Empty(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	// Empty response
	usage := adapter.ExtractUsage([]byte{})
	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
	assert.Equal(t, 0, usage.TotalTokens)

	// Missing usage fields
	usage = adapter.ExtractUsage([]byte(`{"model": "llama3.1", "done": true}`))
	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
	assert.Equal(t, 0, usage.TotalTokens)
}

func TestOllama_ExtractUsage_OpenAIFormat(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	// Some Ollama versions (especially with /v1/chat/completions endpoint) return OpenAI format
	responseBody := []byte(`{
		"id": "chatcmpl-123",
		"object": "chat.completion",
		"model": "llama3.1",
		"choices": [{"message": {"role": "assistant", "content": "Hello!"}}],
		"usage": {
			"prompt_tokens": 200,
			"completion_tokens": 80,
			"total_tokens": 280
		}
	}`)

	usage := adapter.ExtractUsage(responseBody)

	assert.Equal(t, 200, usage.InputTokens)
	assert.Equal(t, 80, usage.OutputTokens)
	assert.Equal(t, 280, usage.TotalTokens)
}

// =============================================================================
// MODEL EXTRACTION
// =============================================================================

func TestOllama_ExtractModel(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	body := []byte(`{"model": "llama3.1:70b", "messages": []}`)
	model := adapter.ExtractModel(body)
	assert.Equal(t, "llama3.1:70b", model)
}

func TestOllama_ExtractModel_Empty(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	model := adapter.ExtractModel([]byte{})
	assert.Empty(t, model)

	model = adapter.ExtractModel([]byte(`{}`))
	assert.Empty(t, model)
}

// =============================================================================
// USER QUERY EXTRACTION
// =============================================================================

func TestOllama_ExtractUserQuery(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	body := []byte(`{
		"model": "llama3.1",
		"messages": [
			{"role": "user", "content": "What is the capital of France?"}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Equal(t, "What is the capital of France?", query)
}

func TestOllama_ExtractUserQuery_ContentBlocks(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	body := []byte(`{
		"model": "llama3.1",
		"messages": [
			{"role": "user", "content": "First question"},
			{"role": "assistant", "content": "Answer"},
			{"role": "user", "content": "Follow-up question"}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Equal(t, "Follow-up question", query, "Should return the last user message")
}

// =============================================================================
// PROVIDER DETECTION
// =============================================================================

func TestOllama_ProviderDetection_PathBased(t *testing.T) {
	registry := adapters.NewRegistry()

	tests := []struct {
		path     string
		wantProv adapters.Provider
	}{
		{"/api/chat", adapters.ProviderOllama},
		{"/api/generate", adapters.ProviderOllama},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			headers := http.Header{}
			provider, adapter := adapters.IdentifyAndGetAdapter(registry, tt.path, headers)
			assert.Equal(t, tt.wantProv, provider)
			assert.NotNil(t, adapter)
			assert.Equal(t, "ollama", adapter.Name())
		})
	}
}

func TestOllama_ProviderDetection_XProviderHeader(t *testing.T) {
	registry := adapters.NewRegistry()

	headers := http.Header{}
	headers.Set("X-Provider", "ollama")

	provider, adapter := adapters.IdentifyAndGetAdapter(registry, "/v1/chat/completions", headers)
	assert.Equal(t, adapters.ProviderOllama, provider)
	assert.NotNil(t, adapter)
	assert.Equal(t, "ollama", adapter.Name())
}

// =============================================================================
// INTERFACE COMPLIANCE
// =============================================================================

func TestOllama_ImplementsAdapter(t *testing.T) {
	var _ adapters.Adapter = adapters.NewOllamaAdapter()
}

// =============================================================================
// PROVIDER FROM STRING
// =============================================================================

func TestOllama_ProviderFromString(t *testing.T) {
	assert.Equal(t, adapters.ProviderOllama, adapters.ProviderFromString("ollama"))
	assert.Equal(t, adapters.ProviderUnknown, adapters.ProviderFromString("invalid"))
}

// =============================================================================
// TOOL DISCOVERY - Extract/Apply
// =============================================================================

func TestOllama_ExtractToolDiscovery(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	body := []byte(`{
		"model": "llama3.1",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [
			{"type": "function", "function": {"name": "read_file", "description": "Read a file from disk"}},
			{"type": "function", "function": {"name": "write_file", "description": "Write content to a file"}}
		]
	}`)

	extracted, err := adapter.ExtractToolDiscovery(body, nil)

	require.NoError(t, err)
	require.Len(t, extracted, 2)
	assert.Equal(t, "read_file", extracted[0].ToolName)
	assert.Equal(t, "write_file", extracted[1].ToolName)
}

func TestOllama_ApplyToolDiscovery(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	body := []byte(`{
		"model": "llama3.1",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [
			{"type": "function", "function": {"name": "read_file", "description": "Read a file"}},
			{"type": "function", "function": {"name": "write_file", "description": "Write a file"}},
			{"type": "function", "function": {"name": "delete_file", "description": "Delete a file"}}
		]
	}`)

	results := []adapters.CompressedResult{
		{ID: "read_file", Keep: true},
		{ID: "write_file", Keep: false},
		{ID: "delete_file", Keep: true},
	}

	modified, err := adapter.ApplyToolDiscovery(body, results)

	require.NoError(t, err)

	var req map[string]any
	require.NoError(t, json.Unmarshal(modified, &req))

	tools := req["tools"].([]any)
	// With stub behavior, deferred tools remain as stubs with DeferredStubDescription.
	// Total count stays at 3 (write_file becomes a stub, not removed).
	assert.Len(t, tools, 3, "Deferred tools remain as stubs, total count unchanged")
	// Verify write_file is now a stub with DeferredStubDescription
	writeFileTool := tools[1].(map[string]any)["function"].(map[string]any)
	assert.Equal(t, adapters.DeferredStubDescription, writeFileTool["description"], "write_file should be stubbed with DeferredStubDescription")
}

func TestOllama_ExtractToolDiscovery_Empty(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	body := []byte(`{
		"model": "llama3.1",
		"messages": [{"role": "user", "content": "hello"}]
	}`)

	extracted, err := adapter.ExtractToolDiscovery(body, nil)

	require.NoError(t, err)
	assert.Empty(t, extracted)
}

// =============================================================================
// EDGE CASES
// =============================================================================

func TestOllama_ApplyToolOutput_NoMatchingResults(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	body := []byte(`{
		"model": "llama3.1",
		"messages": [
			{"role": "user", "content": "Read the config file"},
			{"role": "assistant", "content": "", "tool_calls": [
				{"id": "call_001", "type": "function", "function": {"name": "read_file", "arguments": "{}"}}
			]},
			{"role": "tool", "tool_call_id": "call_001", "content": "original content"}
		]
	}`)

	// Non-matching ID — body should remain unchanged
	results := []adapters.CompressedResult{
		{ID: "call_999", Compressed: "this should not appear"},
	}

	modified, err := adapter.ApplyToolOutput(body, results)

	require.NoError(t, err)

	var req map[string]any
	require.NoError(t, json.Unmarshal(modified, &req))

	messages := req["messages"].([]any)
	toolMsg := messages[2].(map[string]any)
	assert.Equal(t, "original content", toolMsg["content"])
}

func TestOllama_ExtractUsage_BothFormats(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	// Response with both Ollama-native and OpenAI fields — native should win
	responseBody := []byte(`{
		"model": "llama3.1",
		"prompt_eval_count": 50,
		"eval_count": 25,
		"usage": {
			"prompt_tokens": 200,
			"completion_tokens": 80,
			"total_tokens": 280
		}
	}`)

	usage := adapter.ExtractUsage(responseBody)

	// Ollama native format should take priority
	assert.Equal(t, 50, usage.InputTokens)
	assert.Equal(t, 25, usage.OutputTokens)
	assert.Equal(t, 75, usage.TotalTokens)
}

// =============================================================================
// PHANTOM TOOL OPERATIONS - Ollama-native overrides
// =============================================================================

func TestOllama_ExtractToolCallsFromResponse_NativeFormat(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	// Ollama native response: top-level "message.tool_calls[]" with object arguments
	responseBody := []byte(`{
		"model": "llama3.2",
		"message": {
			"role": "assistant",
			"content": "",
			"tool_calls": [
				{"function": {"name": "get_weather", "arguments": {"location": "Paris", "unit": "celsius"}}},
				{"function": {"name": "search_web", "arguments": {"query": "news today"}}}
			]
		},
		"done": true
	}`)

	calls, err := adapter.ExtractToolCallsFromResponse(responseBody)
	require.NoError(t, err)
	require.Len(t, calls, 2)

	assert.Equal(t, "get_weather", calls[0].ToolName)
	assert.Equal(t, "ollama_call_0", calls[0].ToolUseID)
	assert.Equal(t, "Paris", calls[0].Input["location"])
	assert.Equal(t, "celsius", calls[0].Input["unit"])

	assert.Equal(t, "search_web", calls[1].ToolName)
	assert.Equal(t, "ollama_call_1", calls[1].ToolUseID)
	assert.Equal(t, "news today", calls[1].Input["query"])
}

func TestOllama_ExtractToolCallsFromResponse_NoToolCalls(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	// Response with no tool calls
	responseBody := []byte(`{
		"model": "llama3.2",
		"message": {"role": "assistant", "content": "Hello there!"},
		"done": true
	}`)

	calls, err := adapter.ExtractToolCallsFromResponse(responseBody)
	require.NoError(t, err)
	assert.Empty(t, calls)
}

func TestOllama_ExtractToolCallsFromResponse_StringArgsFallback(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	// Some Ollama builds may encode arguments as a JSON string — should handle gracefully
	responseBody := []byte(`{
		"message": {
			"role": "assistant",
			"tool_calls": [
				{"function": {"name": "my_tool", "arguments": "{\"key\": \"value\"}"}}
			]
		}
	}`)

	calls, err := adapter.ExtractToolCallsFromResponse(responseBody)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "my_tool", calls[0].ToolName)
	assert.Equal(t, "value", calls[0].Input["key"])
}

func TestOllama_FilterToolCallFromResponse_NativeFormat(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	responseBody := []byte(`{
		"message": {
			"role": "assistant",
			"tool_calls": [
				{"function": {"name": "expand_context", "arguments": {}}},
				{"function": {"name": "read_file", "arguments": {"path": "/foo"}}}
			]
		}
	}`)

	result, modified := adapter.FilterToolCallFromResponse(responseBody, "expand_context")
	require.True(t, modified)

	// "expand_context" removed, "read_file" remains
	calls, err := adapter.ExtractToolCallsFromResponse(result)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "read_file", calls[0].ToolName)
}

func TestOllama_FilterToolCallFromResponse_ToolNotPresent(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	responseBody := []byte(`{
		"message": {
			"role": "assistant",
			"tool_calls": [
				{"function": {"name": "read_file", "arguments": {}}}
			]
		}
	}`)

	result, modified := adapter.FilterToolCallFromResponse(responseBody, "nonexistent_tool")
	assert.False(t, modified)
	assert.Equal(t, responseBody, result)
}

func TestOllama_BuildToolResultMessages(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	calls := []adapters.ToolCall{
		{ToolUseID: "ollama_call_0", ToolName: "get_weather"},
		{ToolUseID: "ollama_call_1", ToolName: "search_web"},
	}
	content := []string{"Sunny, 22°C", "No results found"}

	results := adapter.BuildToolResultMessages(calls, content, nil)
	require.Len(t, results, 2)

	assert.Equal(t, "tool", results[0]["role"])
	assert.Equal(t, "get_weather", results[0]["name"])
	assert.Equal(t, "Sunny, 22°C", results[0]["content"])

	assert.Equal(t, "tool", results[1]["role"])
	assert.Equal(t, "search_web", results[1]["name"])
	assert.Equal(t, "No results found", results[1]["content"])

	// Ollama does NOT include tool_call_id
	assert.NotContains(t, results[0], "tool_call_id")
}

func TestOllama_AppendMessages_NativeAssistantResponse(t *testing.T) {
	adapter := adapters.NewOllamaAdapter()

	requestBody := []byte(`{"model":"llama3.2","messages":[{"role":"user","content":"What's the weather?"}]}`)
	assistantResponse := []byte(`{
		"message": {
			"role": "assistant",
			"content": "",
			"tool_calls": [{"function": {"name": "get_weather", "arguments": {"location": "Paris"}}}]
		},
		"done": true
	}`)
	toolResults := []map[string]any{
		{"role": "tool", "name": "get_weather", "content": "Sunny, 22°C"},
	}

	result, err := adapter.AppendMessages(requestBody, assistantResponse, toolResults)
	require.NoError(t, err)

	var req map[string]any
	require.NoError(t, json.Unmarshal(result, &req))

	messages, ok := req["messages"].([]any)
	require.True(t, ok)
	// Original user message + appended assistant + tool result = 3
	assert.Len(t, messages, 3)

	assistantMsg := messages[1].(map[string]any)
	assert.Equal(t, "assistant", assistantMsg["role"])

	toolMsg := messages[2].(map[string]any)
	assert.Equal(t, "tool", toolMsg["role"])
	assert.Equal(t, "Sunny, 22°C", toolMsg["content"])
}
