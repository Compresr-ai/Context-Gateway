package unit

import (
	"encoding/json"
	"testing"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// BEDROCK ADAPTER BASIC TESTS
// =============================================================================

func TestBedrock_Name(t *testing.T) {
	adapter := adapters.NewBedrockAdapter()
	assert.Equal(t, "bedrock", adapter.Name())
}

func TestBedrock_Provider(t *testing.T) {
	adapter := adapters.NewBedrockAdapter()
	assert.Equal(t, adapters.ProviderBedrock, adapter.Provider())
}

// =============================================================================
// BEDROCK TOOL OUTPUT TESTS
// Bedrock with Anthropic models uses the same Messages API format.
// =============================================================================

func TestBedrock_ExtractToolOutput(t *testing.T) {
	adapter := adapters.NewBedrockAdapter()

	// Bedrock request body — same structure as Anthropic Messages API
	// but with anthropic_version field instead of header
	body := []byte(`{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens": 4096,
		"messages": [
			{"role": "user", "content": "Read the config file"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_001", "name": "read_file", "input": {"path": "config.yaml"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_001", "content": "server:\n  port: 8080\n  host: localhost"}]}
		]
	}`)

	extracted, err := adapter.ExtractToolOutput(body)

	require.NoError(t, err)
	require.Len(t, extracted, 1)
	assert.Equal(t, "toolu_001", extracted[0].ID)
	assert.Equal(t, "server:\n  port: 8080\n  host: localhost", extracted[0].Content)
	assert.Equal(t, "tool_result", extracted[0].ContentType)
	assert.Equal(t, "read_file", extracted[0].ToolName)
}

func TestBedrock_ApplyToolOutput(t *testing.T) {
	adapter := adapters.NewBedrockAdapter()

	body := []byte(`{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens": 4096,
		"messages": [
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_001", "content": "original long content that needs compression"}]}
		]
	}`)

	results := []adapters.CompressedResult{
		{ID: "toolu_001", Compressed: "compressed summary"},
	}

	modified, err := adapter.ApplyToolOutput(body, results)

	require.NoError(t, err)

	var req map[string]interface{}
	require.NoError(t, json.Unmarshal(modified, &req))

	// Verify anthropic_version is preserved
	assert.Equal(t, "bedrock-2023-05-31", req["anthropic_version"])

	messages := req["messages"].([]interface{})
	userMsg := messages[0].(map[string]interface{})
	content := userMsg["content"].([]interface{})
	block := content[0].(map[string]interface{})
	assert.Equal(t, "compressed summary", block["content"])
}

func TestBedrock_ExtractToolOutput_MultipleTools(t *testing.T) {
	adapter := adapters.NewBedrockAdapter()

	body := []byte(`{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens": 4096,
		"messages": [
			{"role": "user", "content": "Find and read files"},
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "toolu_001", "name": "search_files", "input": {}},
				{"type": "tool_use", "id": "toolu_002", "name": "read_file", "input": {}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_001", "content": "Found: main.go, utils.go, config.yaml"},
				{"type": "tool_result", "tool_use_id": "toolu_002", "content": "package main\nfunc main() {}"}
			]}
		]
	}`)

	extracted, err := adapter.ExtractToolOutput(body)

	require.NoError(t, err)
	require.Len(t, extracted, 2)
	assert.Equal(t, "toolu_001", extracted[0].ID)
	assert.Equal(t, "search_files", extracted[0].ToolName)
	assert.Equal(t, "toolu_002", extracted[1].ID)
	assert.Equal(t, "read_file", extracted[1].ToolName)
}

// =============================================================================
// BEDROCK USAGE EXTRACTION
// =============================================================================

func TestBedrock_ExtractUsage(t *testing.T) {
	adapter := adapters.NewBedrockAdapter()

	// Bedrock response format (same as Anthropic)
	responseBody := []byte(`{
		"id": "msg_01XAbc",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "text", "text": "Hello!"}],
		"model": "anthropic.claude-3-5-sonnet-20241022-v2:0",
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 150,
			"output_tokens": 25
		}
	}`)

	usage := adapter.ExtractUsage(responseBody)

	assert.Equal(t, 150, usage.InputTokens)
	assert.Equal(t, 25, usage.OutputTokens)
	assert.Equal(t, 175, usage.TotalTokens)
}

func TestBedrock_ExtractUsage_Empty(t *testing.T) {
	adapter := adapters.NewBedrockAdapter()

	usage := adapter.ExtractUsage([]byte{})
	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
}

// =============================================================================
// BEDROCK MODEL EXTRACTION
// =============================================================================

func TestBedrock_ExtractModel_FromBody(t *testing.T) {
	adapter := adapters.NewBedrockAdapter()

	body := []byte(`{
		"model": "anthropic.claude-3-5-sonnet-20241022-v2:0",
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens": 4096,
		"messages": [{"role": "user", "content": "Hello"}]
	}`)

	model := adapter.ExtractModel(body)
	assert.Equal(t, "anthropic.claude-3-5-sonnet-20241022-v2:0", model)
}

func TestBedrock_ExtractModel_NoModelField(t *testing.T) {
	adapter := adapters.NewBedrockAdapter()

	// Bedrock requests typically don't include model in body (it's in the URL)
	body := []byte(`{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens": 4096,
		"messages": [{"role": "user", "content": "Hello"}]
	}`)

	model := adapter.ExtractModel(body)
	assert.Equal(t, "", model)
}

func TestBedrock_ExtractModel_EmptyBody(t *testing.T) {
	adapter := adapters.NewBedrockAdapter()
	model := adapter.ExtractModel([]byte{})
	assert.Equal(t, "", model)
}

// =============================================================================
// BEDROCK MODEL FROM PATH
// =============================================================================

func TestBedrock_ExtractModelFromPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "invoke endpoint",
			path:     "/model/anthropic.claude-3-5-sonnet-20241022-v2:0/invoke",
			expected: "anthropic.claude-3-5-sonnet-20241022-v2:0",
		},
		{
			name:     "invoke-with-response-stream",
			path:     "/model/anthropic.claude-3-5-sonnet-20241022-v2:0/invoke-with-response-stream",
			expected: "anthropic.claude-3-5-sonnet-20241022-v2:0",
		},
		{
			name:     "converse endpoint",
			path:     "/model/anthropic.claude-3-5-sonnet-20241022-v2:0/converse",
			expected: "anthropic.claude-3-5-sonnet-20241022-v2:0",
		},
		{
			name:     "converse-stream",
			path:     "/model/anthropic.claude-3-5-sonnet-20241022-v2:0/converse-stream",
			expected: "anthropic.claude-3-5-sonnet-20241022-v2:0",
		},
		{
			name:     "meta model",
			path:     "/model/meta.llama3-70b-instruct-v1:0/invoke",
			expected: "meta.llama3-70b-instruct-v1:0",
		},
		{
			name:     "no model prefix",
			path:     "/v1/messages",
			expected: "",
		},
		{
			name:     "empty path",
			path:     "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := adapters.ExtractModelFromPath(tt.path)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// =============================================================================
// BEDROCK USER QUERY EXTRACTION
// =============================================================================

func TestBedrock_ExtractUserQuery(t *testing.T) {
	adapter := adapters.NewBedrockAdapter()

	body := []byte(`{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens": 4096,
		"messages": [
			{"role": "user", "content": "What is the weather?"},
			{"role": "assistant", "content": "Let me check."},
			{"role": "user", "content": "Check for San Francisco"}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Equal(t, "Check for San Francisco", query)
}

func TestBedrock_ExtractUserQuery_ContentBlocks(t *testing.T) {
	adapter := adapters.NewBedrockAdapter()

	body := []byte(`{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens": 4096,
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "Analyze this code"}
			]}
		]
	}`)

	query := adapter.ExtractUserQuery(body)
	assert.Equal(t, "Analyze this code", query)
}

// =============================================================================
// PROVIDER IDENTIFICATION TESTS
// =============================================================================

func TestBedrock_ProviderDetection_PathBased(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected adapters.Provider
	}{
		{
			name:     "invoke endpoint",
			path:     "/model/anthropic.claude-3-5-sonnet-20241022-v2:0/invoke",
			expected: adapters.ProviderBedrock,
		},
		{
			name:     "invoke-with-response-stream",
			path:     "/model/anthropic.claude-3-5-sonnet-20241022-v2:0/invoke-with-response-stream",
			expected: adapters.ProviderBedrock,
		},
		{
			name:     "converse endpoint",
			path:     "/model/anthropic.claude-3-5-sonnet-20241022-v2:0/converse",
			expected: adapters.ProviderBedrock,
		},
		{
			name:     "converse-stream",
			path:     "/model/anthropic.claude-3-5-sonnet-20241022-v2:0/converse-stream",
			expected: adapters.ProviderBedrock,
		},
	}

	registry := adapters.NewRegistry()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(map[string][]string)
			provider, adapter := adapters.IdentifyAndGetAdapter(registry, tt.path, headers)
			assert.Equal(t, tt.expected, provider)
			assert.NotNil(t, adapter)
			assert.Equal(t, "bedrock", adapter.Name())
		})
	}
}

func TestBedrock_ProviderDetection_XAmzDate(t *testing.T) {
	registry := adapters.NewRegistry()
	headers := make(map[string][]string)
	headers["X-Amz-Date"] = []string{"20240101T000000Z"}

	provider, adapter := adapters.IdentifyAndGetAdapter(registry, "/some/path", headers)
	assert.Equal(t, adapters.ProviderBedrock, provider)
	assert.NotNil(t, adapter)
	assert.Equal(t, "bedrock", adapter.Name())
}

func TestBedrock_ProviderDetection_XProviderHeader(t *testing.T) {
	registry := adapters.NewRegistry()
	headers := make(map[string][]string)
	headers["X-Provider"] = []string{"bedrock"}

	provider, adapter := adapters.IdentifyAndGetAdapter(registry, "/v1/messages", headers)
	assert.Equal(t, adapters.ProviderBedrock, provider)
	assert.NotNil(t, adapter)
	assert.Equal(t, "bedrock", adapter.Name())
}

// =============================================================================
// PROVIDER TYPE TESTS
// =============================================================================

func TestBedrock_ProviderFromString(t *testing.T) {
	assert.Equal(t, adapters.ProviderBedrock, adapters.ProviderFromString("bedrock"))
	assert.Equal(t, adapters.Provider("bedrock"), adapters.ProviderBedrock)
}

// =============================================================================
// BEDROCK ADAPTER IMPLEMENTS INTERFACE
// =============================================================================

func TestBedrock_ImplementsAdapter(t *testing.T) {
	// Compile-time check is in bedrock.go, but verify at runtime too
	var _ adapters.Adapter = adapters.NewBedrockAdapter()
}
