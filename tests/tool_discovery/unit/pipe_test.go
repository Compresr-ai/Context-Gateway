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
	pipe := tooldiscovery.New(testConfig(config.StrategyRelevance, 25, nil))
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
			pipe := tooldiscovery.New(testConfig(tt.strategy, 25, nil))
			assert.Equal(t, tt.strategy, pipe.Strategy())
		})
	}
}

func TestPipe_Enabled(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		pipe := tooldiscovery.New(testConfig(config.StrategyRelevance, 25, nil))
		assert.True(t, pipe.Enabled())
	})

	t.Run("disabled", func(t *testing.T) {
		cfg := testConfig(config.StrategyRelevance, 25, nil)
		cfg.Pipes.ToolDiscovery.Enabled = false
		pipe := tooldiscovery.New(cfg)
		assert.False(t, pipe.Enabled())
	})
}

// =============================================================================
// PASSTHROUGH AND DISABLED MODES
// =============================================================================

func TestPipe_Process_Disabled(t *testing.T) {
	cfg := testConfig(config.StrategyRelevance, 25, nil)
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
	pipe := tooldiscovery.New(testConfig(config.StrategyPassthrough, 25, nil))

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

func TestPipe_Process_BelowTokenThreshold(t *testing.T) {
	// TokenThreshold=99999: 3 small tools are well below threshold → no filtering
	pipe := tooldiscovery.New(testConfig(config.StrategyRelevance, 1, nil, 99999))

	body := openAIRequestWithTools(3)
	ctx := newOpenAIPipeContext(body)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.Equal(t, body, result)
	assert.False(t, ctx.ToolsFiltered)
	assert.Equal(t, "below_token_threshold", ctx.ToolDiscoverySkipReason)
}

func TestPipe_Process_ExactlyMinTools(t *testing.T) {
	// TokenThreshold large enough to hold all 5 tools → all fit in budget → skip
	pipe := tooldiscovery.New(testConfig(config.StrategyRelevance, 25, nil))

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
	// TokenThreshold=3*25=75 → keep 3 of 10; deferred tools become stubs preserving array length
	pipe := tooldiscovery.New(testConfig(config.StrategyRelevance, 3, nil))

	body := openAIRequestWithToolsAndQuery(10, "read the file contents")
	ctx := newOpenAIPipeContext(body)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.True(t, ctx.ToolsFiltered)

	var req map[string]any
	require.NoError(t, json.Unmarshal(result, &req))

	tools := req["tools"].([]any)
	// Total array length is preserved (stubs keep the array stable for KV-cache).
	assert.Equal(t, 10, len(tools))
	// But only ≤3 tools have full definitions visible to the LLM.
	effective := countEffectiveTools(tools)
	assert.LessOrEqual(t, effective, 3)
	assert.Greater(t, effective, 0)
}

func TestPipe_Process_FiltersTools_Anthropic(t *testing.T) {
	// TokenThreshold=3*25=75 → keep 3 of 10; deferred tools become stubs preserving array length
	pipe := tooldiscovery.New(testConfig(config.StrategyRelevance, 3, nil))

	body := anthropicRequestWithToolsAndQuery(10, "search for code patterns")
	ctx := newAnthropicPipeContext(body)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.True(t, ctx.ToolsFiltered)

	var req map[string]any
	require.NoError(t, json.Unmarshal(result, &req))

	tools := req["tools"].([]any)
	assert.Equal(t, 10, len(tools))
	effective := countEffectiveTools(tools)
	assert.LessOrEqual(t, effective, 3)
	assert.Greater(t, effective, 0)
}

func TestPipe_Process_Relevance_DoesNotInjectSearchTool(t *testing.T) {
	cfg := testConfig(config.StrategyRelevance, 2, nil)
	cfg.Pipes.ToolDiscovery.EnableSearchFallback = true // Should be ignored for relevance
	pipe := tooldiscovery.New(cfg)

	body := openAIRequestWithToolsAndQuery(8, "search for code")
	ctx := newOpenAIPipeContext(body)

	result, err := pipe.Process(ctx)
	require.NoError(t, err)
	assert.True(t, ctx.ToolsFiltered)

	var req map[string]any
	require.NoError(t, json.Unmarshal(result, &req))
	tools := req["tools"].([]any)
	names := extractToolNames(tools)
	assert.NotContains(t, names, "gateway_search_tools")
}

func TestPipe_Process_CompresrStrategy_IsUnknown(t *testing.T) {
	// "compresr" is no longer a valid tool discovery strategy
	cfg := testConfig("compresr", 10, []string{"run_tests"})
	pipe := tooldiscovery.New(cfg)

	body := openAIRequestWithToolsAndQuery(6, "search for code")
	ctx := newOpenAIPipeContext(body)

	result, err := pipe.Process(ctx)
	require.NoError(t, err)

	// Unknown strategy returns original request unchanged
	var req map[string]any
	require.NoError(t, json.Unmarshal(result, &req))
	tools := req["tools"].([]any)
	assert.Len(t, tools, 6) // All tools returned (unknown strategy = passthrough)
}

func TestPipe_Process_ToolSearch_ReplacesWithSearchToolOnly(t *testing.T) {
	// tool_search: all original tools become stubs.
	// gateway_search_tools is injected unconditionally by phantom_tools.InjectAll in handler.go — NOT by the pipe.
	cfg := testConfig(config.StrategyToolSearch, 10, []string{"run_tests"})
	pipe := tooldiscovery.New(cfg)

	body := openAIRequestWithToolsAndQuery(6, "search for code")
	ctx := newOpenAIPipeContext(body)

	result, err := pipe.Process(ctx)
	require.NoError(t, err)
	assert.True(t, ctx.ToolsFiltered)
	assert.Len(t, ctx.DeferredTools, 6)

	var req map[string]any
	require.NoError(t, json.Unmarshal(result, &req))
	tools := req["tools"].([]any)
	// 6 stubs only — gateway_search_tools is NOT injected by the pipe
	require.Len(t, tools, 6)
	// All 6 tools are stubs: OpenAI stubs have parameters injected by buildDeferredStubChat
	for i := 0; i < 6; i++ {
		tool := tools[i].(map[string]any)
		fn, ok := tool["function"].(map[string]any)
		require.True(t, ok, "tool[%d] must be OpenAI format (has 'function' key)", i)
		_, hasParams := fn["parameters"]
		assert.True(t, hasParams, "tool[%d] should be a stub (must have 'parameters' injected by buildDeferredStubChat)", i)
	}
}

// =============================================================================
// RELEVANCE SCORING - RECENTLY USED TOOLS
// =============================================================================

func TestPipe_Process_RecentlyUsedToolsScoreHigher(t *testing.T) {
	// TokenThreshold=2*25=50 → keep 2 of 6 tools
	pipe := tooldiscovery.New(testConfig(config.StrategyRelevance, 2, nil))

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
	// Total array length preserved (stubs); only 2 have full definitions.
	assert.Equal(t, 6, len(tools))
	assert.Equal(t, 2, countEffectiveTools(tools))

	// read_file should be in the kept (non-deferred) tools (it was recently used)
	keptNames := effectiveToolNames(tools)
	assert.Contains(t, keptNames, "read_file")
}

// =============================================================================
// RELEVANCE SCORING - KEYWORD MATCHING
// =============================================================================

func TestPipe_Process_KeywordMatchScoring(t *testing.T) {
	// TokenThreshold=2*25=50 → keep 2 of 6
	pipe := tooldiscovery.New(testConfig(config.StrategyRelevance, 2, nil))

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
	keptNames := effectiveToolNames(tools)

	// search_code and read_file should score higher due to keyword overlap
	assert.Contains(t, keptNames, "search_code")
}

// =============================================================================
// ALWAYS KEEP LIST
// =============================================================================

func TestPipe_Process_AlwaysKeepList(t *testing.T) {
	// TokenThreshold=2*25=50 → keep 2, but always_keep includes "run_tests"
	alwaysKeep := []string{"run_tests"}
	pipe := tooldiscovery.New(testConfig(config.StrategyRelevance, 2, alwaysKeep))

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
	keptNames := effectiveToolNames(tools)

	// run_tests should be kept (non-deferred) because it's in always_keep
	assert.Contains(t, keptNames, "run_tests")
}

// =============================================================================
// KEEP COUNT CALCULATION
// =============================================================================

func TestPipe_Process_MaxToolsCapsCount(t *testing.T) {
	// 20 tools, TokenThreshold=5*25=125 → keeps 5
	pipe := tooldiscovery.New(testConfig(config.StrategyRelevance, 5, nil))

	body := openAIRequestWithToolsAndQuery(20, "test query")
	ctx := newOpenAIPipeContext(body)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.True(t, ctx.ToolsFiltered)

	var req map[string]any
	require.NoError(t, json.Unmarshal(result, &req))

	tools := req["tools"].([]any)
	// 20 total (stubs preserve array length), 5 effective
	assert.Equal(t, 20, len(tools))
	assert.Equal(t, 5, countEffectiveTools(tools))
}

func TestPipe_Process_MinToolsFloor(t *testing.T) {
	// TokenThreshold=5*25=125 → keeps 5 of 10
	pipe := tooldiscovery.New(testConfig(config.StrategyRelevance, 5, nil))

	body := openAIRequestWithToolsAndQuery(10, "test query")
	ctx := newOpenAIPipeContext(body)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.True(t, ctx.ToolsFiltered)

	var req map[string]any
	require.NoError(t, json.Unmarshal(result, &req))

	tools := req["tools"].([]any)
	// 10 total (stubs preserve array length), 5 effective
	assert.Equal(t, 10, len(tools))
	assert.Equal(t, 5, countEffectiveTools(tools))
}

// =============================================================================
// EDGE CASES
// =============================================================================

func TestPipe_Process_NoAdapter(t *testing.T) {
	pipe := tooldiscovery.New(testConfig(config.StrategyRelevance, 5, nil))

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
	pipe := tooldiscovery.New(testConfig(config.StrategyRelevance, 5, nil))

	ctx := newOpenAIPipeContext(nil)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestPipe_Process_NoQuery(t *testing.T) {
	// When no user query exists, should still work (using only recently-used and always-keep signals)
	pipe := tooldiscovery.New(testConfig(config.StrategyRelevance, 3, nil))

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
	// Total array length preserved; effective count is fewer than all 6 (filtering occurred)
	assert.Equal(t, 6, len(tools))
	assert.Less(t, countEffectiveTools(tools), 6)
}

func TestPipe_Process_KeepCountExceedsTotalSkips(t *testing.T) {
	// TokenThreshold=100*25=2500 > total tokens (10*25=250), so no filtering should happen
	pipe := tooldiscovery.New(testConfig(config.StrategyRelevance, 100, nil))

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
	cfg.Pipes.ToolDiscovery.Strategy = config.StrategyPassthrough
	err := cfg.Pipes.ToolDiscovery.Validate()
	assert.NoError(t, err)
}

func TestToolDiscoveryConfig_Validate_Relevance(t *testing.T) {
	cfg := &config.Config{}
	cfg.Pipes.ToolDiscovery.Enabled = true
	cfg.Pipes.ToolDiscovery.Strategy = config.StrategyRelevance
	err := cfg.Pipes.ToolDiscovery.Validate()
	assert.NoError(t, err)
}

func TestToolDiscoveryConfig_Validate_ToolSearchValid(t *testing.T) {
	cfg := &config.Config{}
	cfg.Pipes.ToolDiscovery.Enabled = true
	cfg.Pipes.ToolDiscovery.Strategy = config.StrategyToolSearch
	err := cfg.Pipes.ToolDiscovery.Validate()
	assert.NoError(t, err) // tool-search is valid even without API config (falls back to local regex)
}

func TestToolDiscoveryConfig_Validate_CompresrIsValid(t *testing.T) {
	cfg := &config.Config{}
	cfg.Pipes.ToolDiscovery.Enabled = true
	cfg.Pipes.ToolDiscovery.Strategy = "compresr" // Compresr API-backed filtering; falls back to local relevance if unavailable
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

// testConfig creates a test pipe config.
// tokenThreshold controls how many tools are kept via token budget.
// When keepCount > 0, tokenThreshold is computed as keepCount * testToolTokens.
// When tokenThreshold is provided directly as 3rd variadic arg, it overrides keepCount.
// Default tokenThreshold=1 (keeps exactly 1 tool) unless keepCount is specified.
//
// testToolTokens is an estimate of the token cost per tool in unit tests.
// Each test tool JSON is ~100 bytes → ~25 estimated tokens.
const testToolTokens = 25

func testConfig(strategy string, keepCount int, alwaysKeep []string, opts ...int) *config.Config {
	// opts[0] = explicit tokenThreshold (overrides keepCount-based calculation)
	threshold := 0
	if len(opts) > 0 && opts[0] > 0 {
		threshold = opts[0]
	} else if keepCount > 0 {
		// Budget = keepCount tools worth of tokens.
		// The loop admits tool i if budget >= toolTokens (for i > 0).
		// To admit exactly keepCount tools: budget after (keepCount-1) admits >= toolTokens.
		// Simplification: budget = keepCount * testToolTokens covers keepCount tools.
		threshold = keepCount * testToolTokens
	}
	if threshold <= 0 {
		threshold = 1 // always trigger, keep only 1 tool
	}
	return &config.Config{
		Pipes: config.PipesConfig{
			ToolDiscovery: config.ToolDiscoveryPipeConfig{
				Enabled:        true,
				Strategy:       strategy,
				AlwaysKeep:     alwaysKeep,
				TokenThreshold: threshold,
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

// isOpenAIStub returns true when fn is an OpenAI-format stub function object.
// buildDeferredStubChat always injects parameters:{type:object,properties:{}};
// test tools from openAIRequestWithToolsAndQuery never have a parameters field.
func isOpenAIStub(fn map[string]any) bool {
	_, hasParams := fn["parameters"]
	return hasParams
}

// countEffectiveTools returns the number of tools with full definitions (not stubs).
// OpenAI stubs are detected by the injected parameters field (buildDeferredStubChat).
// Anthropic stubs use description=adapters.DeferredStubDescription.
func countEffectiveTools(tools []any) int {
	count := 0
	for _, t := range tools {
		tool := t.(map[string]any)
		if fn, ok := tool["function"].(map[string]any); ok {
			// OpenAI format: stubs have parameters injected; original test tools don't
			if !isOpenAIStub(fn) {
				count++
			}
		} else {
			// Anthropic format: stubs use DeferredStubDescription
			desc, _ := tool["description"].(string)
			if desc != adapters.DeferredStubDescription {
				count++
			}
		}
	}
	return count
}

// effectiveToolNames returns names of non-deferred (fully-defined) tools.
func effectiveToolNames(tools []any) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		tool := t.(map[string]any)
		if fn, ok := tool["function"].(map[string]any); ok {
			// OpenAI format: stubs have parameters injected
			if !isOpenAIStub(fn) {
				name, _ := fn["name"].(string)
				names = append(names, name)
			}
		} else {
			// Anthropic format: stubs use DeferredStubDescription
			desc, _ := tool["description"].(string)
			name, _ := tool["name"].(string)
			if desc != adapters.DeferredStubDescription {
				names = append(names, name)
			}
		}
	}
	return names
}
