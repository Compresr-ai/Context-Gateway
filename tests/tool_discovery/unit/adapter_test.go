package unit

import (
	"encoding/json"
	"testing"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// OPENAI - EXTRACT TOOL DISCOVERY
// =============================================================================

func TestOpenAI_ExtractToolDiscovery(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "read_file",
					"description": "Read the contents of a file",
					"parameters": {"type": "object", "properties": {"path": {"type": "string"}}}
				}
			},
			{
				"type": "function",
				"function": {
					"name": "write_file",
					"description": "Write content to a file",
					"parameters": {"type": "object", "properties": {"path": {"type": "string"}, "content": {"type": "string"}}}
				}
			}
		]
	}`)

	extracted, err := adapter.ExtractToolDiscovery(body, nil)

	require.NoError(t, err)
	require.Len(t, extracted, 2)

	assert.Equal(t, "read_file", extracted[0].ID)
	assert.Equal(t, "Read the contents of a file", extracted[0].Content)
	assert.Equal(t, "tool_def", extracted[0].ContentType)
	assert.Equal(t, "read_file", extracted[0].ToolName)
	assert.Equal(t, 0, extracted[0].MessageIndex)

	assert.Equal(t, "write_file", extracted[1].ID)
	assert.Equal(t, "Write content to a file", extracted[1].Content)
	assert.Equal(t, 1, extracted[1].MessageIndex)
}

func TestOpenAI_ExtractToolDiscovery_NoTools(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hello"}]
	}`)

	extracted, err := adapter.ExtractToolDiscovery(body, nil)

	require.NoError(t, err)
	assert.Empty(t, extracted)
}

func TestOpenAI_ExtractToolDiscovery_EmptyTools(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": []
	}`)

	extracted, err := adapter.ExtractToolDiscovery(body, nil)

	require.NoError(t, err)
	assert.Empty(t, extracted)
}

func TestOpenAI_ExtractToolDiscovery_NoDescription(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-4o",
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "my_tool",
					"parameters": {"type": "object"}
				}
			}
		]
	}`)

	extracted, err := adapter.ExtractToolDiscovery(body, nil)

	require.NoError(t, err)
	require.Len(t, extracted, 1)
	assert.Equal(t, "my_tool", extracted[0].ToolName)
	assert.Equal(t, "", extracted[0].Content) // No description
}

// =============================================================================
// OPENAI - APPLY TOOL DISCOVERY
// =============================================================================

func TestOpenAI_ApplyToolDiscovery(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [
			{"type": "function", "function": {"name": "read_file", "description": "Read file"}},
			{"type": "function", "function": {"name": "write_file", "description": "Write file"}},
			{"type": "function", "function": {"name": "delete_file", "description": "Delete file"}}
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
	require.Len(t, tools, 2)

	// Verify the right tools were kept
	tool0 := tools[0].(map[string]any)["function"].(map[string]any)
	tool1 := tools[1].(map[string]any)["function"].(map[string]any)
	assert.Equal(t, "read_file", tool0["name"])
	assert.Equal(t, "delete_file", tool1["name"])
}

func TestOpenAI_ApplyToolDiscovery_EmptyResults(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-4o",
		"tools": [
			{"type": "function", "function": {"name": "read_file", "description": "Read file"}}
		]
	}`)

	modified, err := adapter.ApplyToolDiscovery(body, nil)

	require.NoError(t, err)
	assert.Equal(t, body, modified) // Unchanged
}

func TestOpenAI_ApplyToolDiscovery_KeepAll(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	body := []byte(`{
		"model": "gpt-4o",
		"tools": [
			{"type": "function", "function": {"name": "read_file", "description": "Read file"}},
			{"type": "function", "function": {"name": "write_file", "description": "Write file"}}
		]
	}`)

	results := []adapters.CompressedResult{
		{ID: "read_file", Keep: true},
		{ID: "write_file", Keep: true},
	}

	modified, err := adapter.ApplyToolDiscovery(body, results)

	require.NoError(t, err)

	var req map[string]any
	require.NoError(t, json.Unmarshal(modified, &req))

	tools := req["tools"].([]any)
	assert.Len(t, tools, 2)
}

// =============================================================================
// ANTHROPIC - EXTRACT TOOL DISCOVERY
// =============================================================================

func TestAnthropic_ExtractToolDiscovery(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [
			{
				"name": "read_file",
				"description": "Read the contents of a file from disk",
				"input_schema": {"type": "object", "properties": {"path": {"type": "string"}}}
			},
			{
				"name": "execute_command",
				"description": "Execute a shell command",
				"input_schema": {"type": "object", "properties": {"command": {"type": "string"}}}
			}
		]
	}`)

	extracted, err := adapter.ExtractToolDiscovery(body, nil)

	require.NoError(t, err)
	require.Len(t, extracted, 2)

	assert.Equal(t, "read_file", extracted[0].ID)
	assert.Equal(t, "Read the contents of a file from disk", extracted[0].Content)
	assert.Equal(t, "tool_def", extracted[0].ContentType)
	assert.Equal(t, "read_file", extracted[0].ToolName)
	assert.Equal(t, 0, extracted[0].MessageIndex)

	assert.Equal(t, "execute_command", extracted[1].ID)
	assert.Equal(t, "Execute a shell command", extracted[1].Content)
	assert.Equal(t, 1, extracted[1].MessageIndex)
}

func TestAnthropic_ExtractToolDiscovery_NoTools(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [{"role": "user", "content": "hello"}]
	}`)

	extracted, err := adapter.ExtractToolDiscovery(body, nil)

	require.NoError(t, err)
	assert.Empty(t, extracted)
}

func TestAnthropic_ExtractToolDiscovery_NoDescription(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"tools": [
			{
				"name": "custom_tool",
				"input_schema": {"type": "object"}
			}
		]
	}`)

	extracted, err := adapter.ExtractToolDiscovery(body, nil)

	require.NoError(t, err)
	require.Len(t, extracted, 1)
	assert.Equal(t, "custom_tool", extracted[0].ToolName)
	assert.Equal(t, "", extracted[0].Content)
}

// =============================================================================
// ANTHROPIC - APPLY TOOL DISCOVERY
// =============================================================================

func TestAnthropic_ApplyToolDiscovery(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [
			{"name": "read_file", "description": "Read file"},
			{"name": "write_file", "description": "Write file"},
			{"name": "search_code", "description": "Search code"}
		]
	}`)

	results := []adapters.CompressedResult{
		{ID: "read_file", Keep: true},
		{ID: "write_file", Keep: false},
		{ID: "search_code", Keep: true},
	}

	modified, err := adapter.ApplyToolDiscovery(body, results)

	require.NoError(t, err)

	var req map[string]any
	require.NoError(t, json.Unmarshal(modified, &req))

	tools := req["tools"].([]any)
	require.Len(t, tools, 2)

	tool0 := tools[0].(map[string]any)
	tool1 := tools[1].(map[string]any)
	assert.Equal(t, "read_file", tool0["name"])
	assert.Equal(t, "search_code", tool1["name"])
}

func TestAnthropic_ApplyToolDiscovery_RemoveAll(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	body := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"tools": [
			{"name": "read_file", "description": "Read file"},
			{"name": "write_file", "description": "Write file"}
		]
	}`)

	results := []adapters.CompressedResult{
		{ID: "read_file", Keep: false},
		{ID: "write_file", Keep: false},
	}

	modified, err := adapter.ApplyToolDiscovery(body, results)

	require.NoError(t, err)

	var req map[string]any
	require.NoError(t, json.Unmarshal(modified, &req))

	tools := req["tools"].([]any)
	assert.Len(t, tools, 0)
}

// =============================================================================
// BEDROCK - DELEGATES TO ANTHROPIC
// =============================================================================

func TestBedrock_ExtractToolDiscovery(t *testing.T) {
	adapter := adapters.NewBedrockAdapter()

	body := []byte(`{
		"anthropic_version": "bedrock-2023-05-31",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [
			{"name": "read_file", "description": "Read a file"},
			{"name": "list_dir", "description": "List directory contents"}
		]
	}`)

	extracted, err := adapter.ExtractToolDiscovery(body, nil)

	require.NoError(t, err)
	require.Len(t, extracted, 2)
	assert.Equal(t, "read_file", extracted[0].ToolName)
	assert.Equal(t, "list_dir", extracted[1].ToolName)
}

func TestBedrock_ApplyToolDiscovery(t *testing.T) {
	adapter := adapters.NewBedrockAdapter()

	body := []byte(`{
		"anthropic_version": "bedrock-2023-05-31",
		"tools": [
			{"name": "read_file", "description": "Read a file"},
			{"name": "list_dir", "description": "List directory contents"}
		]
	}`)

	results := []adapters.CompressedResult{
		{ID: "read_file", Keep: true},
		{ID: "list_dir", Keep: false},
	}

	modified, err := adapter.ApplyToolDiscovery(body, results)

	require.NoError(t, err)

	var req map[string]any
	require.NoError(t, json.Unmarshal(modified, &req))

	tools := req["tools"].([]any)
	require.Len(t, tools, 1)
	assert.Equal(t, "read_file", tools[0].(map[string]any)["name"])
}

// =============================================================================
// INVALID JSON
// =============================================================================

func TestOpenAI_ExtractToolDiscovery_InvalidJSON(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	_, err := adapter.ExtractToolDiscovery([]byte(`not json`), nil)

	assert.Error(t, err)
}

func TestAnthropic_ExtractToolDiscovery_InvalidJSON(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	_, err := adapter.ExtractToolDiscovery([]byte(`not json`), nil)

	assert.Error(t, err)
}

func TestOpenAI_ApplyToolDiscovery_InvalidJSON(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	results := []adapters.CompressedResult{{ID: "test", Keep: true}}
	_, err := adapter.ApplyToolDiscovery([]byte(`not json`), results)

	assert.Error(t, err)
}

func TestAnthropic_ApplyToolDiscovery_InvalidJSON(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	results := []adapters.CompressedResult{{ID: "test", Keep: true}}
	_, err := adapter.ApplyToolDiscovery([]byte(`not json`), results)

	assert.Error(t, err)
}
