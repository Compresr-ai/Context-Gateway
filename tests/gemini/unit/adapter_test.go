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

func TestGemini_Name(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()
	assert.Equal(t, "gemini", adapter.Name())
}

func TestGemini_Provider(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()
	assert.Equal(t, adapters.ProviderGemini, adapter.Provider())
}

// =============================================================================
// TOOL OUTPUT - Extract
// =============================================================================

func TestGemini_ExtractToolOutput(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	body := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "Read the config file"}]},
			{"role": "model", "parts": [{"functionCall": {"name": "read_file", "args": {"path": "config.yaml"}}}]},
			{"role": "user", "parts": [{"functionResponse": {"name": "read_file", "response": {"content": "server:\n  port: 8080\n  host: localhost"}}}]}
		]
	}`)

	extracted, err := adapter.ExtractToolOutput(body)

	require.NoError(t, err)
	require.Len(t, extracted, 1)
	assert.Equal(t, "2_0", extracted[0].ID)
	assert.Equal(t, "server:\n  port: 8080\n  host: localhost", extracted[0].Content)
	assert.Equal(t, "tool_result", extracted[0].ContentType)
	assert.Equal(t, "read_file", extracted[0].ToolName)
	assert.Equal(t, 2, extracted[0].MessageIndex)
	assert.Equal(t, 0, extracted[0].BlockIndex)
}

func TestGemini_ExtractToolOutput_ObjectResponse(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	// When response has multiple fields, it should be serialized to JSON
	body := []byte(`{
		"contents": [
			{"role": "model", "parts": [{"functionCall": {"name": "get_weather", "args": {}}}]},
			{"role": "user", "parts": [{"functionResponse": {"name": "get_weather", "response": {"temperature": 72, "condition": "sunny"}}}]}
		]
	}`)

	extracted, err := adapter.ExtractToolOutput(body)

	require.NoError(t, err)
	require.Len(t, extracted, 1)
	assert.Equal(t, "get_weather", extracted[0].ToolName)
	// Multi-field response is serialized to JSON
	assert.Contains(t, extracted[0].Content, "temperature")
	assert.Contains(t, extracted[0].Content, "sunny")
}

func TestGemini_ExtractToolOutput_MultipleTools(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	body := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "Read both files"}]},
			{"role": "model", "parts": [
				{"functionCall": {"name": "read_file", "args": {"path": "a.txt"}}},
				{"functionCall": {"name": "read_file", "args": {"path": "b.txt"}}}
			]},
			{"role": "user", "parts": [
				{"functionResponse": {"name": "read_file", "response": {"content": "contents of file a"}}},
				{"functionResponse": {"name": "read_file", "response": {"content": "contents of file b"}}}
			]}
		]
	}`)

	extracted, err := adapter.ExtractToolOutput(body)

	require.NoError(t, err)
	require.Len(t, extracted, 2)
	assert.Equal(t, "2_0", extracted[0].ID)
	assert.Equal(t, "contents of file a", extracted[0].Content)
	assert.Equal(t, "2_1", extracted[1].ID)
	assert.Equal(t, "contents of file b", extracted[1].Content)
}

func TestGemini_ExtractToolOutput_Empty(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	body := []byte(`{"contents": []}`)

	extracted, err := adapter.ExtractToolOutput(body)

	require.NoError(t, err)
	assert.Empty(t, extracted)
}

func TestGemini_ExtractToolOutput_NoContents(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	body := []byte(`{}`)

	extracted, err := adapter.ExtractToolOutput(body)

	require.NoError(t, err)
	assert.Nil(t, extracted)
}

func TestGemini_ExtractToolOutput_InvalidJSON(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	_, err := adapter.ExtractToolOutput([]byte(`{invalid}`))

	require.Error(t, err)
}

// =============================================================================
// TOOL OUTPUT - Apply
// =============================================================================

func TestGemini_ApplyToolOutput(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	body := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "Read the config"}]},
			{"role": "model", "parts": [{"functionCall": {"name": "read_file", "args": {}}}]},
			{"role": "user", "parts": [{"functionResponse": {"name": "read_file", "response": {"content": "original long content"}}}]}
		]
	}`)

	results := []adapters.CompressedResult{
		{ID: "2_0", Compressed: "compressed: config with port 8080"},
	}

	modified, err := adapter.ApplyToolOutput(body, results)

	require.NoError(t, err)

	var req map[string]any
	require.NoError(t, json.Unmarshal(modified, &req))

	contents := req["contents"].([]any)
	userMsg := contents[2].(map[string]any)
	parts := userMsg["parts"].([]any)
	part := parts[0].(map[string]any)
	fnResp := part["functionResponse"].(map[string]any)
	resp := fnResp["response"].(map[string]any)
	assert.Equal(t, "compressed: config with port 8080", resp["result"])
}

func TestGemini_ApplyToolOutput_NoResults(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	body := []byte(`{"contents": []}`)

	modified, err := adapter.ApplyToolOutput(body, nil)

	require.NoError(t, err)
	assert.Equal(t, body, modified)
}

func TestGemini_ApplyToolOutput_InvalidJSON(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	_, err := adapter.ApplyToolOutput([]byte(`{invalid}`), []adapters.CompressedResult{{ID: "0_0", Compressed: "x"}})

	require.Error(t, err)
}

// =============================================================================
// TOOL DISCOVERY (Stub)
// =============================================================================

func TestGemini_ExtractToolDiscovery_Stub(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	extracted, err := adapter.ExtractToolDiscovery([]byte(`{}`), nil)

	require.NoError(t, err)
	assert.Empty(t, extracted)
}

func TestGemini_ApplyToolDiscovery_Stub(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	body := []byte(`{"contents": [], "tools": []}`)

	modified, err := adapter.ApplyToolDiscovery(body, nil)

	require.NoError(t, err)
	assert.Equal(t, body, modified)
}

// =============================================================================
// USER QUERY EXTRACTION
// =============================================================================

func TestGemini_ExtractUserQuery(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	body := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "What is the capital of France?"}]}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Equal(t, "What is the capital of France?", query)
}

func TestGemini_ExtractUserQuery_MultipleMessages(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	body := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "First question"}]},
			{"role": "model", "parts": [{"text": "First answer"}]},
			{"role": "user", "parts": [{"text": "Follow-up question"}]}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Equal(t, "Follow-up question", query, "Should return the last user message")
}

func TestGemini_ExtractUserQuery_SkipsFunctionResponseParts(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	// The last user message contains only functionResponse, no text
	body := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "Read the file and summarize it"}]},
			{"role": "model", "parts": [{"functionCall": {"name": "read_file", "args": {}}}]},
			{"role": "user", "parts": [{"functionResponse": {"name": "read_file", "response": {"content": "file data"}}}]}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Equal(t, "Read the file and summarize it", query, "Should skip user messages with only functionResponse parts")
}

func TestGemini_ExtractUserQuery_Empty(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	query := adapter.ExtractUserQuery([]byte(`{"contents": []}`))
	assert.Empty(t, query)
}

func TestGemini_ExtractUserQuery_InvalidJSON(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	query := adapter.ExtractUserQuery([]byte(`{invalid}`))
	assert.Empty(t, query)
}

func TestGemini_ExtractUserQuery_MultipleTextParts(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	body := []byte(`{
		"contents": [
			{"role": "user", "parts": [
				{"text": "Part one."},
				{"text": "Part two."}
			]}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Equal(t, "Part one.\nPart two.", query, "Should join multiple text parts")
}

// =============================================================================
// USAGE EXTRACTION
// =============================================================================

func TestGemini_ExtractUsage(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	responseBody := []byte(`{
		"candidates": [{"content": {"parts": [{"text": "Hello!"}]}}],
		"usageMetadata": {
			"promptTokenCount": 150,
			"candidatesTokenCount": 80,
			"totalTokenCount": 230
		}
	}`)

	usage := adapter.ExtractUsage(responseBody)

	assert.Equal(t, 150, usage.InputTokens)
	assert.Equal(t, 80, usage.OutputTokens)
	assert.Equal(t, 230, usage.TotalTokens)
}

func TestGemini_ExtractUsage_Empty(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	usage := adapter.ExtractUsage([]byte{})
	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
	assert.Equal(t, 0, usage.TotalTokens)
}

func TestGemini_ExtractUsage_NoUsageField(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	usage := adapter.ExtractUsage([]byte(`{"candidates": []}`))
	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
	assert.Equal(t, 0, usage.TotalTokens)
}

func TestGemini_ExtractUsage_InvalidJSON(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	usage := adapter.ExtractUsage([]byte(`{invalid}`))
	assert.Equal(t, 0, usage.InputTokens)
}

// =============================================================================
// MODEL EXTRACTION
// =============================================================================

func TestGemini_ExtractModel(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	body := []byte(`{"model": "gemini-3-flash", "contents": []}`)
	model := adapter.ExtractModel(body)
	assert.Equal(t, "gemini-3-flash", model)
}

func TestGemini_ExtractModel_WithModelsPrefix(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	body := []byte(`{"model": "models/gemini-2.5-pro", "contents": []}`)
	model := adapter.ExtractModel(body)
	assert.Equal(t, "gemini-2.5-pro", model, "Should strip models/ prefix")
}

func TestGemini_ExtractModel_Empty(t *testing.T) {
	adapter := adapters.NewGeminiAdapter()

	model := adapter.ExtractModel([]byte{})
	assert.Empty(t, model)

	model = adapter.ExtractModel([]byte(`{}`))
	assert.Empty(t, model)
}

// =============================================================================
// PROVIDER DETECTION
// =============================================================================

func TestGemini_ProviderDetection_XProviderHeader(t *testing.T) {
	registry := adapters.NewRegistry()

	headers := http.Header{}
	headers.Set("X-Provider", "gemini")

	provider, adapter := adapters.IdentifyAndGetAdapter(registry, "/some/path", headers)
	assert.Equal(t, adapters.ProviderGemini, provider)
	assert.NotNil(t, adapter)
	assert.Equal(t, "gemini", adapter.Name())
}

func TestGemini_ProviderDetection_GoogleAPIKey(t *testing.T) {
	registry := adapters.NewRegistry()

	headers := http.Header{}
	headers.Set("x-goog-api-key", "AIza-test-key")

	provider, adapter := adapters.IdentifyAndGetAdapter(registry, "/some/path", headers)
	assert.Equal(t, adapters.ProviderGemini, provider)
	assert.NotNil(t, adapter)
	assert.Equal(t, "gemini", adapter.Name())
}

func TestGemini_ProviderDetection_PathBased(t *testing.T) {
	registry := adapters.NewRegistry()

	headers := http.Header{}

	provider, adapter := adapters.IdentifyAndGetAdapter(registry, "/v1beta/generativelanguage.googleapis.com/models/gemini-3-flash:generateContent", headers)
	assert.Equal(t, adapters.ProviderGemini, provider)
	assert.NotNil(t, adapter)
	assert.Equal(t, "gemini", adapter.Name())
}

// =============================================================================
// INTERFACE COMPLIANCE
// =============================================================================

func TestGemini_ImplementsAdapter(t *testing.T) {
	var _ adapters.Adapter = adapters.NewGeminiAdapter()
}

// =============================================================================
// PROVIDER FROM STRING
// =============================================================================

func TestGemini_ProviderFromString(t *testing.T) {
	assert.Equal(t, adapters.ProviderGemini, adapters.ProviderFromString("gemini"))
}
