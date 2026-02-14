package unit

import (
	"encoding/json"
	"testing"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
	tooldiscovery "github.com/compresr/context-gateway/internal/pipes/tool_discovery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// PIPE METADATA
// =============================================================================

func TestPipe_Name(t *testing.T) {
	pipe := tooldiscovery.New(testConfig("relevance", 5, 25, 0.8, nil))
	assert.Equal(t, "tool_discovery", pipe.Name())
}

func TestPipe_Strategy(t *testing.T) {
	tests := []struct {
		name     string
		strategy string
	}{
		{"passthrough", "passthrough"},
		{"relevance", "relevance"},
		{"api", "api"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pipe := tooldiscovery.New(testConfig(tt.strategy, 5, 25, 0.8, nil))
			assert.Equal(t, tt.strategy, pipe.Strategy())
		})
	}
}

func TestPipe_Enabled(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		pipe := tooldiscovery.New(testConfig("relevance", 5, 25, 0.8, nil))
		assert.True(t, pipe.Enabled())
	})

	t.Run("disabled", func(t *testing.T) {
		cfg := testConfig("relevance", 5, 25, 0.8, nil)
		cfg.Pipes.ToolDiscovery.Enabled = false
		pipe := tooldiscovery.New(cfg)
		assert.False(t, pipe.Enabled())
	})
}

// =============================================================================
// PASSTHROUGH AND DISABLED MODES
// =============================================================================

func TestPipe_Process_Disabled(t *testing.T) {
	cfg := testConfig("relevance", 5, 25, 0.8, nil)
	cfg.Pipes.ToolDiscovery.Enabled = false
	pipe := tooldiscovery.New(cfg)

	body := openAIRequestWithTools(10)
	ctx := newOpenAIPipeContext(body)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.Equal(t, body, result)
	assert.False(t, ctx.ToolsFiltered)
}

func TestPipe_Process_Passthrough(t *testing.T) {
	pipe := tooldiscovery.New(testConfig("passthrough", 5, 25, 0.8, nil))

	body := openAIRequestWithTools(10)
	ctx := newOpenAIPipeContext(body)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.Equal(t, body, result)
	assert.False(t, ctx.ToolsFiltered)
}

// =============================================================================
// BELOW MIN THRESHOLD - NO FILTERING
// =============================================================================

func TestPipe_Process_BelowMinTools(t *testing.T) {
	// MinTools=5, so 3 tools should not be filtered
	pipe := tooldiscovery.New(testConfig("relevance", 5, 25, 0.8, nil))

	body := openAIRequestWithTools(3)
	ctx := newOpenAIPipeContext(body)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.Equal(t, body, result)
	assert.False(t, ctx.ToolsFiltered)
}

func TestPipe_Process_ExactlyMinTools(t *testing.T) {
	// MinTools=5, so exactly 5 tools should not be filtered (<=)
	pipe := tooldiscovery.New(testConfig("relevance", 5, 25, 0.8, nil))

	body := openAIRequestWithTools(5)
	ctx := newOpenAIPipeContext(body)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.Equal(t, body, result)
	assert.False(t, ctx.ToolsFiltered)
}

// =============================================================================
// BASIC FILTERING
// =============================================================================

func TestPipe_Process_FiltersTools_OpenAI(t *testing.T) {
	// MaxTools=3, MinTools=2, TargetRatio=0.5 → keep 50% of 10 = 5, capped at 3
	pipe := tooldiscovery.New(testConfig("relevance", 2, 3, 0.5, nil))

	body := openAIRequestWithToolsAndQuery(10, "read the file contents")
	ctx := newOpenAIPipeContext(body)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.True(t, ctx.ToolsFiltered)

	var req map[string]any
	require.NoError(t, json.Unmarshal(result, &req))

	tools := req["tools"].([]any)
	assert.LessOrEqual(t, len(tools), 3)
	assert.Greater(t, len(tools), 0)
}

func TestPipe_Process_FiltersTools_Anthropic(t *testing.T) {
	// MaxTools=3, MinTools=2, TargetRatio=0.5
	pipe := tooldiscovery.New(testConfig("relevance", 2, 3, 0.5, nil))

	body := anthropicRequestWithToolsAndQuery(10, "search for code patterns")
	ctx := newAnthropicPipeContext(body)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.True(t, ctx.ToolsFiltered)

	var req map[string]any
	require.NoError(t, json.Unmarshal(result, &req))

	tools := req["tools"].([]any)
	assert.LessOrEqual(t, len(tools), 3)
	assert.Greater(t, len(tools), 0)
}

// =============================================================================
// RELEVANCE SCORING - RECENTLY USED TOOLS
// =============================================================================

func TestPipe_Process_RecentlyUsedToolsScoreHigher(t *testing.T) {
	// MaxTools=2, keep only 2 of 6 tools (int(6*0.4)=2)
	pipe := tooldiscovery.New(testConfig("relevance", 1, 2, 0.4, nil))

	// Request with tool results for "read_file" in conversation history
	body := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": "help me with code"},
			{"role": "assistant", "content": null, "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "read_file", "arguments": "{}"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "file contents here"},
			{"role": "user", "content": "now do something else"}
		],
		"tools": [
			{"type": "function", "function": {"name": "read_file", "description": "Read a file"}},
			{"type": "function", "function": {"name": "write_file", "description": "Write a file"}},
			{"type": "function", "function": {"name": "delete_file", "description": "Delete a file"}},
			{"type": "function", "function": {"name": "search_code", "description": "Search code"}},
			{"type": "function", "function": {"name": "list_dir", "description": "List directory"}},
			{"type": "function", "function": {"name": "run_tests", "description": "Run tests"}}
		]
	}`)

	ctx := newOpenAIPipeContext(body)
	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.True(t, ctx.ToolsFiltered)

	var req map[string]any
	require.NoError(t, json.Unmarshal(result, &req))

	tools := req["tools"].([]any)
	require.Len(t, tools, 2)

	// read_file should be in the kept tools (it was recently used)
	toolNames := extractToolNames(tools)
	assert.Contains(t, toolNames, "read_file")
}

// =============================================================================
// RELEVANCE SCORING - KEYWORD MATCHING
// =============================================================================

func TestPipe_Process_KeywordMatchScoring(t *testing.T) {
	// MaxTools=2, keep only 2 of 6
	pipe := tooldiscovery.New(testConfig("relevance", 1, 2, 0.3, nil))

	body := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": "search for code patterns in the file"}
		],
		"tools": [
			{"type": "function", "function": {"name": "search_code", "description": "Search for code patterns"}},
			{"type": "function", "function": {"name": "read_file", "description": "Read file contents"}},
			{"type": "function", "function": {"name": "deploy_app", "description": "Deploy application to production"}},
			{"type": "function", "function": {"name": "send_email", "description": "Send an email notification"}},
			{"type": "function", "function": {"name": "create_db", "description": "Create database table"}},
			{"type": "function", "function": {"name": "run_tests", "description": "Run test suite"}}
		]
	}`)

	ctx := newOpenAIPipeContext(body)
	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.True(t, ctx.ToolsFiltered)

	var req map[string]any
	require.NoError(t, json.Unmarshal(result, &req))

	tools := req["tools"].([]any)
	toolNames := extractToolNames(tools)

	// search_code and read_file should score higher due to keyword overlap
	assert.Contains(t, toolNames, "search_code")
}

// =============================================================================
// ALWAYS KEEP LIST
// =============================================================================

func TestPipe_Process_AlwaysKeepList(t *testing.T) {
	// MaxTools=2, but always_keep includes "run_tests"
	alwaysKeep := []string{"run_tests"}
	pipe := tooldiscovery.New(testConfig("relevance", 1, 2, 0.3, alwaysKeep))

	body := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": "search for code patterns"}
		],
		"tools": [
			{"type": "function", "function": {"name": "search_code", "description": "Search for code patterns"}},
			{"type": "function", "function": {"name": "read_file", "description": "Read file contents"}},
			{"type": "function", "function": {"name": "deploy_app", "description": "Deploy application"}},
			{"type": "function", "function": {"name": "send_email", "description": "Send email"}},
			{"type": "function", "function": {"name": "create_db", "description": "Create database"}},
			{"type": "function", "function": {"name": "run_tests", "description": "Run tests"}}
		]
	}`)

	ctx := newOpenAIPipeContext(body)
	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.True(t, ctx.ToolsFiltered)

	var req map[string]any
	require.NoError(t, json.Unmarshal(result, &req))

	tools := req["tools"].([]any)
	toolNames := extractToolNames(tools)

	// run_tests should be kept because it's in always_keep
	assert.Contains(t, toolNames, "run_tests")
}

// =============================================================================
// KEEP COUNT CALCULATION
// =============================================================================

func TestPipe_Process_TargetRatioDeterminesCount(t *testing.T) {
	// 10 tools, target_ratio=0.6 → keep 6, capped at MaxTools=8
	pipe := tooldiscovery.New(testConfig("relevance", 2, 8, 0.6, nil))

	body := openAIRequestWithToolsAndQuery(10, "test query")
	ctx := newOpenAIPipeContext(body)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.True(t, ctx.ToolsFiltered)

	var req map[string]any
	require.NoError(t, json.Unmarshal(result, &req))

	tools := req["tools"].([]any)
	assert.Equal(t, 6, len(tools))
}

func TestPipe_Process_MaxToolsCapsCount(t *testing.T) {
	// 20 tools, target_ratio=0.8 → keep 16, but MaxTools=5 caps it
	pipe := tooldiscovery.New(testConfig("relevance", 2, 5, 0.8, nil))

	body := openAIRequestWithToolsAndQuery(20, "test query")
	ctx := newOpenAIPipeContext(body)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.True(t, ctx.ToolsFiltered)

	var req map[string]any
	require.NoError(t, json.Unmarshal(result, &req))

	tools := req["tools"].([]any)
	assert.Equal(t, 5, len(tools))
}

func TestPipe_Process_MinToolsFloor(t *testing.T) {
	// 10 tools, target_ratio=0.1 → keep 1, but MinTools=3 floors it
	pipe := tooldiscovery.New(testConfig("relevance", 3, 25, 0.1, nil))

	body := openAIRequestWithToolsAndQuery(10, "test query")
	ctx := newOpenAIPipeContext(body)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.True(t, ctx.ToolsFiltered)

	var req map[string]any
	require.NoError(t, json.Unmarshal(result, &req))

	tools := req["tools"].([]any)
	assert.Equal(t, 3, len(tools))
}

// =============================================================================
// EDGE CASES
// =============================================================================

func TestPipe_Process_NoAdapter(t *testing.T) {
	pipe := tooldiscovery.New(testConfig("relevance", 2, 5, 0.5, nil))

	body := openAIRequestWithTools(10)
	ctx := &pipes.PipeContext{
		Adapter:         nil,
		OriginalRequest: body,
	}

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.Equal(t, body, result)
}

func TestPipe_Process_EmptyBody(t *testing.T) {
	pipe := tooldiscovery.New(testConfig("relevance", 2, 5, 0.5, nil))

	ctx := newOpenAIPipeContext(nil)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestPipe_Process_NoQuery(t *testing.T) {
	// When no user query exists, should still work (using only recently-used and always-keep signals)
	pipe := tooldiscovery.New(testConfig("relevance", 1, 3, 0.3, nil))

	body := []byte(`{
		"model": "gpt-4o",
		"tools": [
			{"type": "function", "function": {"name": "tool_1", "description": "First tool"}},
			{"type": "function", "function": {"name": "tool_2", "description": "Second tool"}},
			{"type": "function", "function": {"name": "tool_3", "description": "Third tool"}},
			{"type": "function", "function": {"name": "tool_4", "description": "Fourth tool"}},
			{"type": "function", "function": {"name": "tool_5", "description": "Fifth tool"}},
			{"type": "function", "function": {"name": "tool_6", "description": "Sixth tool"}}
		]
	}`)

	ctx := newOpenAIPipeContext(body)
	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	// Should still filter (tools > minTools) even without a query
	assert.True(t, ctx.ToolsFiltered)

	var req map[string]any
	require.NoError(t, json.Unmarshal(result, &req))
	tools := req["tools"].([]any)
	assert.LessOrEqual(t, len(tools), 3)
}

func TestPipe_Process_KeepCountExceedsTotalSkips(t *testing.T) {
	// target_ratio=1.0 would keep all tools, so no filtering should happen
	pipe := tooldiscovery.New(testConfig("relevance", 2, 100, 1.0, nil))

	body := openAIRequestWithToolsAndQuery(10, "test query")
	ctx := newOpenAIPipeContext(body)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.False(t, ctx.ToolsFiltered)
	assert.Equal(t, body, result)
}

// =============================================================================
// CONFIG VALIDATION
// =============================================================================

func TestToolDiscoveryConfig_Validate_Disabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Pipes.ToolDiscovery.Enabled = false
	err := cfg.Pipes.ToolDiscovery.Validate()
	assert.NoError(t, err)
}

func TestToolDiscoveryConfig_Validate_Passthrough(t *testing.T) {
	cfg := &config.Config{}
	cfg.Pipes.ToolDiscovery.Enabled = true
	cfg.Pipes.ToolDiscovery.Strategy = "passthrough"
	err := cfg.Pipes.ToolDiscovery.Validate()
	assert.NoError(t, err)
}

func TestToolDiscoveryConfig_Validate_Relevance(t *testing.T) {
	cfg := &config.Config{}
	cfg.Pipes.ToolDiscovery.Enabled = true
	cfg.Pipes.ToolDiscovery.Strategy = "relevance"
	err := cfg.Pipes.ToolDiscovery.Validate()
	assert.NoError(t, err)
}

func TestToolDiscoveryConfig_Validate_APIMissingEndpoint(t *testing.T) {
	cfg := &config.Config{}
	cfg.Pipes.ToolDiscovery.Enabled = true
	cfg.Pipes.ToolDiscovery.Strategy = "api"
	err := cfg.Pipes.ToolDiscovery.Validate()
	assert.Error(t, err)
}

func TestToolDiscoveryConfig_Validate_APIWithProvider(t *testing.T) {
	cfg := &config.Config{}
	cfg.Pipes.ToolDiscovery.Enabled = true
	cfg.Pipes.ToolDiscovery.Strategy = "api"
	cfg.Pipes.ToolDiscovery.Provider = "some_provider"
	err := cfg.Pipes.ToolDiscovery.Validate()
	assert.NoError(t, err)
}

func TestToolDiscoveryConfig_Validate_UnknownStrategy(t *testing.T) {
	cfg := &config.Config{}
	cfg.Pipes.ToolDiscovery.Enabled = true
	cfg.Pipes.ToolDiscovery.Strategy = "unknown_strategy"
	err := cfg.Pipes.ToolDiscovery.Validate()
	assert.Error(t, err)
}

// =============================================================================
// HELPERS
// =============================================================================

func testConfig(strategy string, minTools, maxTools int, targetRatio float64, alwaysKeep []string) *config.Config {
	return &config.Config{
		Pipes: config.PipesConfig{
			ToolDiscovery: config.ToolDiscoveryPipeConfig{
				Enabled:     true,
				Strategy:    strategy,
				MinTools:    minTools,
				MaxTools:    maxTools,
				TargetRatio: targetRatio,
				AlwaysKeep:  alwaysKeep,
			},
		},
	}
}

func newOpenAIPipeContext(body []byte) *pipes.PipeContext {
	registry := adapters.NewRegistry()
	adapter := registry.Get("openai")
	return pipes.NewPipeContext(adapter, body)
}

func newAnthropicPipeContext(body []byte) *pipes.PipeContext {
	registry := adapters.NewRegistry()
	adapter := registry.Get("anthropic")
	return pipes.NewPipeContext(adapter, body)
}

func openAIRequestWithTools(n int) []byte {
	return openAIRequestWithToolsAndQuery(n, "")
}

func openAIRequestWithToolsAndQuery(n int, query string) []byte {
	tools := make([]map[string]any, n)
	for i := 0; i < n; i++ {
		tools[i] = map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        toolName(i),
				"description": toolDescription(i),
			},
		}
	}

	req := map[string]any{
		"model": "gpt-4o",
		"tools": tools,
	}

	if query != "" {
		req["messages"] = []map[string]any{
			{"role": "user", "content": query},
		}
	}

	body, _ := json.Marshal(req)
	return body
}

func anthropicRequestWithToolsAndQuery(n int, query string) []byte {
	tools := make([]map[string]any, n)
	for i := 0; i < n; i++ {
		tools[i] = map[string]any{
			"name":         toolName(i),
			"description":  toolDescription(i),
			"input_schema": map[string]any{"type": "object"},
		}
	}

	req := map[string]any{
		"model": "claude-3-5-sonnet-20241022",
		"tools": tools,
	}

	if query != "" {
		req["messages"] = []map[string]any{
			{"role": "user", "content": query},
		}
	}

	body, _ := json.Marshal(req)
	return body
}

func toolName(i int) string {
	names := []string{
		"read_file", "write_file", "search_code", "list_dir", "execute_command",
		"create_file", "delete_file", "git_commit", "run_tests", "deploy_app",
		"send_email", "fetch_url", "parse_json", "compress_data", "encrypt_data",
		"decrypt_data", "validate_schema", "generate_report", "upload_file", "download_file",
	}
	return names[i%len(names)]
}

func toolDescription(i int) string {
	descriptions := []string{
		"Read the contents of a file from disk",
		"Write content to a file on disk",
		"Search for code patterns across files",
		"List contents of a directory",
		"Execute a shell command",
		"Create a new file with content",
		"Delete a file from disk",
		"Create a git commit with message",
		"Run the test suite",
		"Deploy the application to production",
		"Send an email notification",
		"Fetch content from a URL",
		"Parse a JSON string into structured data",
		"Compress data using gzip",
		"Encrypt data with AES-256",
		"Decrypt AES-256 encrypted data",
		"Validate data against a JSON schema",
		"Generate a formatted report",
		"Upload a file to cloud storage",
		"Download a file from cloud storage",
	}
	return descriptions[i%len(descriptions)]
}

func extractToolNames(tools []any) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		tool := t.(map[string]any)
		// Try OpenAI format
		if fn, ok := tool["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok {
				names = append(names, name)
			}
		}
		// Try Anthropic format
		if name, ok := tool["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names
}
