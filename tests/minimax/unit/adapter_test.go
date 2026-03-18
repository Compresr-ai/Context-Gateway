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

func TestMiniMax_Name(t *testing.T) {
	adapter := adapters.NewMiniMaxAdapter()
	assert.Equal(t, "minimax", adapter.Name())
}

func TestMiniMax_Provider(t *testing.T) {
	adapter := adapters.NewMiniMaxAdapter()
	assert.Equal(t, adapters.ProviderMiniMax, adapter.Provider())
}

// =============================================================================
// TOOL OUTPUT - Extract (Chat Completions format with MiniMax models)
// =============================================================================

func TestMiniMax_ExtractToolOutput(t *testing.T) {
	adapter := adapters.NewMiniMaxAdapter()

	body := []byte(`{
		"model": "MiniMax-M2.5",
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
// TOOL OUTPUT - Apply
// =============================================================================

func TestMiniMax_ApplyToolOutput(t *testing.T) {
	adapter := adapters.NewMiniMaxAdapter()

	body := []byte(`{
		"model": "MiniMax-M2.5",
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

func TestMiniMax_ExtractToolOutput_MultipleTools(t *testing.T) {
	adapter := adapters.NewMiniMaxAdapter()

	body := []byte(`{
		"model": "MiniMax-M2.5",
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
// USAGE EXTRACTION - Standard OpenAI format (MiniMax returns this)
// =============================================================================

func TestMiniMax_ExtractUsage(t *testing.T) {
	adapter := adapters.NewMiniMaxAdapter()

	responseBody := []byte(`{
		"id": "chatcmpl-abc123",
		"object": "chat.completion",
		"model": "MiniMax-M2.5",
		"choices": [{"message": {"role": "assistant", "content": "Hello!"}}],
		"usage": {
			"prompt_tokens": 150,
			"completion_tokens": 60,
			"total_tokens": 210
		}
	}`)

	usage := adapter.ExtractUsage(responseBody)

	assert.Equal(t, 150, usage.InputTokens)
	assert.Equal(t, 60, usage.OutputTokens)
	assert.Equal(t, 210, usage.TotalTokens)
}

func TestMiniMax_ExtractUsage_Empty(t *testing.T) {
	adapter := adapters.NewMiniMaxAdapter()

	// Empty response
	usage := adapter.ExtractUsage([]byte{})
	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
	assert.Equal(t, 0, usage.TotalTokens)

	// Missing usage fields
	usage = adapter.ExtractUsage([]byte(`{"model": "MiniMax-M2.5", "choices": []}`))
	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
	assert.Equal(t, 0, usage.TotalTokens)
}

// =============================================================================
// MODEL EXTRACTION
// =============================================================================

func TestMiniMax_ExtractModel(t *testing.T) {
	adapter := adapters.NewMiniMaxAdapter()

	body := []byte(`{"model": "MiniMax-M2.5", "messages": []}`)
	model := adapter.ExtractModel(body)
	assert.Equal(t, "MiniMax-M2.5", model)
}

func TestMiniMax_ExtractModel_Highspeed(t *testing.T) {
	adapter := adapters.NewMiniMaxAdapter()

	body := []byte(`{"model": "MiniMax-M2.5-highspeed", "messages": []}`)
	model := adapter.ExtractModel(body)
	assert.Equal(t, "MiniMax-M2.5-highspeed", model)
}

func TestMiniMax_ExtractModel_Empty(t *testing.T) {
	adapter := adapters.NewMiniMaxAdapter()

	model := adapter.ExtractModel([]byte{})
	assert.Empty(t, model)

	model = adapter.ExtractModel([]byte(`{}`))
	assert.Empty(t, model)
}

// =============================================================================
// USER QUERY EXTRACTION
// =============================================================================

func TestMiniMax_ExtractUserQuery(t *testing.T) {
	adapter := adapters.NewMiniMaxAdapter()

	body := []byte(`{
		"model": "MiniMax-M2.5",
		"messages": [
			{"role": "user", "content": "What is the capital of France?"}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Equal(t, "What is the capital of France?", query)
}

func TestMiniMax_ExtractUserQuery_MultiTurn(t *testing.T) {
	adapter := adapters.NewMiniMaxAdapter()

	body := []byte(`{
		"model": "MiniMax-M2.5",
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
// TOOL DISCOVERY - Extract/Apply
// =============================================================================

func TestMiniMax_ExtractToolDiscovery(t *testing.T) {
	adapter := adapters.NewMiniMaxAdapter()

	body := []byte(`{
		"model": "MiniMax-M2.5",
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

func TestMiniMax_ApplyToolDiscovery(t *testing.T) {
	adapter := adapters.NewMiniMaxAdapter()

	body := []byte(`{
		"model": "MiniMax-M2.5",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [
			{"type": "function", "function": {"name": "read_file", "description": "Read a file from disk"}},
			{"type": "function", "function": {"name": "write_file", "description": "Write content to a file"}},
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
	require.Len(t, tools, 3, "Deferred tools remain as stubs, total count unchanged")
	// write_file (deferred) becomes a stub with DeferredStubDescription
	writeFn := tools[1].(map[string]any)["function"].(map[string]any)
	assert.Equal(t, adapters.DeferredStubDescription, writeFn["description"], "write_file should be stubbed with DeferredStubDescription")
}

// =============================================================================
// PROVIDER DETECTION
// =============================================================================

func TestMiniMax_ProviderDetection_XProviderHeader(t *testing.T) {
	registry := adapters.NewRegistry()

	headers := http.Header{}
	headers.Set("X-Provider", "minimax")

	provider, adapter := adapters.IdentifyAndGetAdapter(registry, "/v1/chat/completions", headers)
	assert.Equal(t, adapters.ProviderMiniMax, provider)
	assert.NotNil(t, adapter)
	assert.Equal(t, "minimax", adapter.Name())
}

func TestMiniMax_ProviderDetection_FallsBackToOpenAI(t *testing.T) {
	registry := adapters.NewRegistry()

	// Without X-Provider header, /chat/completions routes to OpenAI (expected)
	headers := http.Header{}
	provider, adapter := adapters.IdentifyAndGetAdapter(registry, "/v1/chat/completions", headers)
	assert.Equal(t, adapters.ProviderOpenAI, provider)
	assert.NotNil(t, adapter)
	assert.Equal(t, "openai", adapter.Name())
}

// =============================================================================
// INTERFACE COMPLIANCE
// =============================================================================

func TestMiniMax_ImplementsAdapter(t *testing.T) {
	var _ adapters.Adapter = adapters.NewMiniMaxAdapter()
}

// =============================================================================
// PROVIDER FROM STRING
// =============================================================================

func TestMiniMax_ProviderFromString(t *testing.T) {
	assert.Equal(t, adapters.ProviderMiniMax, adapters.ProviderFromString("minimax"))
	assert.Equal(t, adapters.ProviderUnknown, adapters.ProviderFromString("invalid"))
}
