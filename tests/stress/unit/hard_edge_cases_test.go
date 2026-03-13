// Hard Edge Case Tests
//
// Stress tests for KV-cache stability, phantom tool coexistence, large tool sets,
// parallel merge with real-world payloads, and pre-computed byte correctness.
package unit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
	tooldiscovery "github.com/compresr/context-gateway/internal/pipes/tool_discovery"
	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
)

// =============================================================================
// TEST 1: KV-Cache — expand_context tools[] byte-identical across 10 turns
// =============================================================================

// TestKVCache_ToolsPrefix_ByteIdentical_10Turns simulates 10 conversation turns
// where the base body grows with additional messages each turn. After injecting
// expand_context on each, the tools[] raw bytes must be identical across all 10.
func TestKVCache_ToolsPrefix_ByteIdentical_10Turns(t *testing.T) {
	baseTool := `{"name":"read_file","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}`

	var toolsRaws []string
	for turn := 0; turn < 10; turn++ {
		// Build growing messages array simulating a real conversation
		var msgs []string
		for i := 0; i <= turn; i++ {
			if i%2 == 0 {
				msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":"Turn %d user message with some context about the task"}`, i))
			} else {
				msgs = append(msgs, fmt.Sprintf(`{"role":"assistant","content":"Turn %d assistant response with analysis"}`, i))
			}
		}
		body := []byte(fmt.Sprintf(`{"model":"claude-3-5-sonnet-20241022","max_tokens":4096,"messages":[%s],"tools":[%s]}`,
			strings.Join(msgs, ","), baseTool))

		result, err := tooloutput.InjectExpandContextTool(body, nil, "anthropic")
		require.NoError(t, err, "turn %d", turn)
		require.True(t, json.Valid(result), "turn %d: invalid JSON", turn)

		toolsRaw := gjson.GetBytes(result, "tools").Raw
		require.NotEmpty(t, toolsRaw, "turn %d: tools should exist", turn)
		toolsRaws = append(toolsRaws, toolsRaw)
	}

	// All 10 turns must produce byte-identical tools[]
	for i := 1; i < len(toolsRaws); i++ {
		assert.Equal(t, toolsRaws[0], toolsRaws[i],
			"turn %d tools[] differs from turn 0", i)
	}
}

// =============================================================================
// TEST 2: KV-Cache — search tool tools[] byte-identical across 10 turns
// =============================================================================

// TestKVCache_SearchTool_ByteIdentical_10Turns simulates 10 turns with the
// tool-search strategy replacing all tools. tools[] must be byte-identical.
func TestKVCache_SearchTool_ByteIdentical_10Turns(t *testing.T) {
	baseTools := make([]string, 5)
	for i := 0; i < 5; i++ {
		baseTools[i] = fmt.Sprintf(`{"name":"tool_%d","description":"Tool %d description","input_schema":{"type":"object"}}`, i, i)
	}
	toolsJSON := "[" + strings.Join(baseTools, ",") + "]"

	cfg := &config.Config{
		Pipes: config.PipesConfig{
			ToolDiscovery: config.ToolDiscoveryPipeConfig{
				Enabled:  true,
				Strategy: config.StrategyToolSearch,
				MinTools: 1,
			},
		},
	}

	registry := adapters.NewRegistry()

	var toolsRaws []string
	for turn := 0; turn < 10; turn++ {
		var msgs []string
		for i := 0; i <= turn; i++ {
			msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":"message %d"}`, i))
		}
		body := []byte(fmt.Sprintf(`{"model":"claude-3-5-sonnet-20241022","messages":[%s],"tools":%s}`,
			strings.Join(msgs, ","), toolsJSON))

		pipe := tooldiscovery.New(cfg)
		ctx := pipes.NewPipeContext(registry.Get("anthropic"), body)
		result, err := pipe.Process(ctx)
		require.NoError(t, err, "turn %d", turn)

		toolsRaw := gjson.GetBytes(result, "tools").Raw
		require.NotEmpty(t, toolsRaw, "turn %d", turn)
		toolsRaws = append(toolsRaws, toolsRaw)
	}

	for i := 1; i < len(toolsRaws); i++ {
		assert.Equal(t, toolsRaws[0], toolsRaws[i],
			"turn %d search-tool tools[] differs from turn 0", i)
	}
}

// =============================================================================
// TEST 3: Both phantom tools coexist stably
// =============================================================================

// TestBothPhantomTools_CoexistStably injects both expand_context AND
// gateway_search_tools into the same body. Verifies coexistence, no duplication,
// and byte-identical output on repeated calls.
func TestBothPhantomTools_CoexistStably(t *testing.T) {
	body := []byte(`{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"hello"}],"tools":[{"name":"read_file","description":"Read","input_schema":{"type":"object"}}]}`)

	// First: inject expand_context
	withExpand, err := tooloutput.InjectExpandContextTool(body, nil, "anthropic")
	require.NoError(t, err)

	// Second: inject search tool via tool_discovery injectSearchTool
	// We simulate this by using sjson to append the search tool bytes
	// (since injectSearchTool is unexported, we use the pipe's Process with a strategy
	// that adds the search tool as a fallback — instead, we'll do it at the tools level)
	// Actually, let's use the pipe which replaces tools. Instead, for coexistence,
	// we manually append the search tool using the pipe that adds (not replaces).
	// The real scenario: tool_output injects expand_context, tool_discovery injects search.
	// In practice, mergeParallelResults takes tools from tool_discovery output.
	// For this test, we verify they can coexist in the same tools array.

	// Simulate: tool_discovery keeps existing tools and appends search tool
	cfg := &config.Config{
		Pipes: config.PipesConfig{
			ToolDiscovery: config.ToolDiscoveryPipeConfig{
				Enabled:              true,
				Strategy:             config.StrategyRelevance,
				MinTools:             1,
				MaxTools:             25,
				TargetRatio:          1.0, // Keep all tools
				EnableSearchFallback: false,
			},
		},
	}
	registry := adapters.NewRegistry()

	// Run tool_discovery on the body that already has expand_context
	pipe := tooldiscovery.New(cfg)
	ctx := pipes.NewPipeContext(registry.Get("anthropic"), withExpand)
	tdResult, err := pipe.Process(ctx)
	require.NoError(t, err)

	// The result should have expand_context from the inject
	assert.True(t, json.Valid(tdResult), "result must be valid JSON")
	tools := gjson.GetBytes(tdResult, "tools")
	assert.True(t, tools.Exists())

	// Now manually add search tool to simulate coexistence
	searchToolJSON := []byte(`{"name":"gateway_search_tools","description":"Search for tools","input_schema":{"type":"object","properties":{"query":{"type":"string"}}}}`)
	coexistBody, err := sjson.SetRawBytes(tdResult, "tools.-1", searchToolJSON)
	require.NoError(t, err)
	require.True(t, json.Valid(coexistBody))

	// Verify both phantom tools exist
	coexistTools := gjson.GetBytes(coexistBody, "tools")
	hasExpand := false
	hasSearch := false
	coexistTools.ForEach(func(_, value gjson.Result) bool {
		name := value.Get("name").String()
		if name == "expand_context" {
			hasExpand = true
		}
		if name == "gateway_search_tools" {
			hasSearch = true
		}
		return true
	})
	assert.True(t, hasExpand, "expand_context must be present")
	assert.True(t, hasSearch, "gateway_search_tools must be present")

	// Verify no duplication: re-inject expand_context on the coexist body
	reinjected, err := tooloutput.InjectExpandContextTool(coexistBody, nil, "anthropic")
	require.NoError(t, err)

	// Count expand_context occurrences
	expandCount := int64(0)
	gjson.GetBytes(reinjected, "tools").ForEach(func(_, value gjson.Result) bool {
		if value.Get("name").String() == "expand_context" {
			expandCount++
		}
		return true
	})
	assert.Equal(t, int64(1), expandCount, "expand_context must not be duplicated")

	// Verify byte-identical on repeated calls
	var repeatedResults [][]byte
	for i := 0; i < 5; i++ {
		r, err := tooloutput.InjectExpandContextTool(body, nil, "anthropic")
		require.NoError(t, err)
		repeatedResults = append(repeatedResults, r)
	}
	for i := 1; i < len(repeatedResults); i++ {
		assert.True(t, bytes.Equal(repeatedResults[0], repeatedResults[i]),
			"repeated call %d produced different bytes", i)
	}
}

// =============================================================================
// TEST 4: Large tool set (40 tools) — inject expand_context
// =============================================================================

// TestInject_LargeToolSet_40Tools builds a body with 40 tools, injects
// expand_context, and verifies: valid JSON, 41 tools, original 40 preserved
// exactly, expand_context appended at end.
func TestInject_LargeToolSet_40Tools(t *testing.T) {
	// Build 40 Anthropic-format tools with deterministic JSON
	var tools []string
	for i := 0; i < 40; i++ {
		tools = append(tools, fmt.Sprintf(
			`{"name":"tool_%03d","description":"Tool %d does something useful with parameters","input_schema":{"type":"object","properties":{"arg":{"type":"string"}},"required":["arg"]}}`,
			i, i))
	}
	toolsJSON := "[" + strings.Join(tools, ",") + "]"
	body := []byte(fmt.Sprintf(`{"model":"claude-3","messages":[{"role":"user","content":"test"}],"tools":%s}`, toolsJSON))

	result, err := tooloutput.InjectExpandContextTool(body, nil, "anthropic")
	require.NoError(t, err)
	require.True(t, json.Valid(result), "result must be valid JSON")

	resultTools := gjson.GetBytes(result, "tools")
	assert.Equal(t, int64(41), resultTools.Get("#").Int(), "should have 41 tools (40 + expand_context)")

	// Verify original 40 preserved exactly
	for i := 0; i < 40; i++ {
		name := resultTools.Get(fmt.Sprintf("%d.name", i)).String()
		assert.Equal(t, fmt.Sprintf("tool_%03d", i), name, "tool %d name mismatch", i)
	}

	// Verify expand_context is at the end (index 40)
	assert.Equal(t, "expand_context", resultTools.Get("40.name").String(),
		"expand_context must be appended at the end")

	// Verify original tools bytes are preserved (not re-serialized)
	origToolsRaw := gjson.GetBytes(body, "tools").Raw
	resultToolsArr := resultTools.Array()
	// Reconstruct the first 40 tools from result and compare
	var resultFirst40 []string
	for i := 0; i < 40; i++ {
		resultFirst40 = append(resultFirst40, resultToolsArr[i].Raw)
	}
	origToolsArr := gjson.Parse(origToolsRaw).Array()
	for i := 0; i < 40; i++ {
		assert.Equal(t, origToolsArr[i].Raw, resultFirst40[i],
			"tool %d bytes should be preserved exactly", i)
	}
}

// =============================================================================
// TEST 5: Large tool set (40 tools) — tool-search replaces all
// =============================================================================

// TestInject_ToolSearch_LargeToolSet_40Tools builds a body with 40 tools,
// applies tool-search strategy. Verifies: only 1 tool (gateway_search_tools),
// deferred tools count = 40.
func TestInject_ToolSearch_LargeToolSet_40Tools(t *testing.T) {
	var tools []string
	for i := 0; i < 40; i++ {
		tools = append(tools, fmt.Sprintf(
			`{"name":"tool_%03d","description":"Tool %d description","input_schema":{"type":"object"}}`,
			i, i))
	}
	toolsJSON := "[" + strings.Join(tools, ",") + "]"
	body := []byte(fmt.Sprintf(
		`{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"do something"}],"tools":%s}`,
		toolsJSON))

	cfg := &config.Config{
		Pipes: config.PipesConfig{
			ToolDiscovery: config.ToolDiscoveryPipeConfig{
				Enabled:  true,
				Strategy: config.StrategyToolSearch,
				MinTools: 1,
			},
		},
	}
	pipe := tooldiscovery.New(cfg)
	registry := adapters.NewRegistry()
	ctx := pipes.NewPipeContext(registry.Get("anthropic"), body)

	result, err := pipe.Process(ctx)
	require.NoError(t, err)
	require.True(t, json.Valid(result), "result must be valid JSON")

	// Should have exactly 1 tool: gateway_search_tools
	resultTools := gjson.GetBytes(result, "tools")
	assert.Equal(t, int64(1), resultTools.Get("#").Int(), "should have exactly 1 tool")
	assert.Equal(t, "gateway_search_tools", resultTools.Get("0.name").String())

	// Deferred tools should contain all 40 original tools
	assert.Len(t, ctx.DeferredTools, 40, "deferred tools should have 40 entries")
	assert.True(t, ctx.ToolsFiltered, "ToolsFiltered must be true")
}

// =============================================================================
// TEST 6: Merge large body with compression results
// =============================================================================

// TestMerge_LargeBody_WithCompression creates a 50KB+ body simulating a real
// conversation. Both pipes produce results. Verifies merge produces valid JSON
// and preserves all content.
func TestMerge_LargeBody_WithCompression(t *testing.T) {
	// Build a large messages array (50KB+)
	var msgs []string
	for i := 0; i < 50; i++ {
		// Each message ~1KB
		content := strings.Repeat(fmt.Sprintf("Message %d content with detailed information. ", i), 20)
		if i%3 == 0 {
			msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":"%s"}`, content))
		} else if i%3 == 1 {
			msgs = append(msgs, fmt.Sprintf(`{"role":"assistant","content":"%s"}`, content))
		} else {
			msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t%d","content":"%s"}]}`, i, content))
		}
	}
	msgsJSON := "[" + strings.Join(msgs, ",") + "]"

	var tools []string
	for i := 0; i < 10; i++ {
		tools = append(tools, fmt.Sprintf(`{"name":"tool_%d","description":"Description for tool %d"}`, i, i))
	}
	toolsJSON := "[" + strings.Join(tools, ",") + "]"

	original := []byte(fmt.Sprintf(`{"model":"claude-3","max_tokens":8192,"stream":true,"messages":%s,"tools":%s}`, msgsJSON, toolsJSON))
	require.Greater(t, len(original), 40000, "body should be > 40KB")

	// Simulate tool_output: compressed some messages
	toBody, err := sjson.SetBytes(original, "messages.0.content", "compressed first message")
	require.NoError(t, err)

	// Simulate tool_discovery: filtered tools
	tdBody, err := sjson.SetRawBytes(original, "tools", []byte(`[{"name":"tool_0","description":"Description for tool 0"},{"name":"gateway_search_tools","description":"Search"}]`))
	require.NoError(t, err)

	result := mergeParallelResults(original, toBody, nil, tdBody, nil)

	require.True(t, json.Valid(result), "merged result must be valid JSON")

	// Messages from tool_output (compressed)
	assert.Equal(t, "compressed first message", gjson.GetBytes(result, "messages.0.content").String())

	// Tools from tool_discovery (filtered)
	assert.Equal(t, int64(2), gjson.GetBytes(result, "tools.#").Int())
	assert.Equal(t, "gateway_search_tools", gjson.GetBytes(result, "tools.1.name").String())

	// Other fields preserved
	assert.Equal(t, "claude-3", gjson.GetBytes(result, "model").String())
	assert.Equal(t, int64(8192), gjson.GetBytes(result, "max_tokens").Int())
	assert.Equal(t, true, gjson.GetBytes(result, "stream").Bool())

	// All 50 messages present
	assert.Equal(t, int64(50), gjson.GetBytes(result, "messages.#").Int())
}

// =============================================================================
// TEST 7: OpenAI Responses API format (flat, no function wrapper)
// =============================================================================

// TestInject_OpenAI_ResponsesAPI_Format verifies that when provider is "openai"
// and the body uses "input" instead of "messages" (Responses API), the injected
// tool uses flat format: {type, name, description, parameters} — NOT wrapped in function.
func TestInject_OpenAI_ResponsesAPI_Format(t *testing.T) {
	// Responses API body: has "input" field, no "messages" field
	body := []byte(`{"model":"gpt-4o","input":[{"role":"user","content":"hello"}],"tools":[{"type":"function","name":"read_file","description":"Read","parameters":{"type":"object"}}]}`)

	result, err := tooloutput.InjectExpandContextTool(body, nil, "openai")
	require.NoError(t, err)
	require.True(t, json.Valid(result), "result must be valid JSON")

	tools := gjson.GetBytes(result, "tools")
	assert.Equal(t, int64(2), tools.Get("#").Int())

	// The injected tool (last one) should be flat format
	injected := tools.Get("1")
	assert.Equal(t, "function", injected.Get("type").String())
	assert.Equal(t, "expand_context", injected.Get("name").String())
	assert.True(t, injected.Get("description").Exists(), "must have description at top level")
	assert.True(t, injected.Get("parameters").Exists(), "must have parameters at top level")

	// Must NOT have a "function" wrapper
	assert.False(t, injected.Get("function").Exists(),
		"Responses API format must NOT wrap in 'function' — should be flat {type, name, description, parameters}")
}

// =============================================================================
// TEST 8: Empty tools array
// =============================================================================

// TestInject_EmptyToolsArray verifies that injecting expand_context into
// a body with "tools":[] creates tools with exactly 1 element.
func TestInject_EmptyToolsArray(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"test"}],"tools":[]}`)

	result, err := tooloutput.InjectExpandContextTool(body, nil, "anthropic")
	require.NoError(t, err)
	require.True(t, json.Valid(result))

	tools := gjson.GetBytes(result, "tools")
	assert.Equal(t, int64(1), tools.Get("#").Int(), "should have exactly 1 tool")
	assert.Equal(t, "expand_context", tools.Get("0.name").String())
}

// =============================================================================
// TEST 9: No tools field at all
// =============================================================================

// TestInject_NoToolsField verifies that injecting expand_context into a body
// without any "tools" field creates "tools":[{expand_context}].
func TestInject_NoToolsField(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"test"}]}`)

	result, err := tooloutput.InjectExpandContextTool(body, nil, "anthropic")
	require.NoError(t, err)
	require.True(t, json.Valid(result))

	tools := gjson.GetBytes(result, "tools")
	assert.True(t, tools.Exists(), "tools field must be created")
	assert.Equal(t, int64(1), tools.Get("#").Int(), "should have exactly 1 tool")
	assert.Equal(t, "expand_context", tools.Get("0.name").String())
}

// =============================================================================
// TEST 10: Tool-search stores deferred tools with correct names
// =============================================================================

// TestToolSearch_DeferredToolsStored verifies that after tool-search replaces
// tools, ctx.DeferredTools contains all original tools with correct names.
func TestToolSearch_DeferredToolsStored(t *testing.T) {
	expectedNames := []string{"read_file", "write_file", "search_code", "list_dir", "execute_command", "git_commit"}

	var tools []string
	for _, name := range expectedNames {
		tools = append(tools, fmt.Sprintf(
			`{"name":"%s","description":"Description for %s","input_schema":{"type":"object"}}`,
			name, name))
	}
	toolsJSON := "[" + strings.Join(tools, ",") + "]"
	body := []byte(fmt.Sprintf(
		`{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"help me code"}],"tools":%s}`,
		toolsJSON))

	cfg := &config.Config{
		Pipes: config.PipesConfig{
			ToolDiscovery: config.ToolDiscoveryPipeConfig{
				Enabled:  true,
				Strategy: config.StrategyToolSearch,
				MinTools: 1,
			},
		},
	}
	pipe := tooldiscovery.New(cfg)
	registry := adapters.NewRegistry()
	ctx := pipes.NewPipeContext(registry.Get("anthropic"), body)

	result, err := pipe.Process(ctx)
	require.NoError(t, err)
	require.True(t, json.Valid(result))

	// Verify deferred tools count
	require.Len(t, ctx.DeferredTools, len(expectedNames), "should defer all original tools")

	// Verify each deferred tool has the correct name
	deferredNames := make(map[string]bool)
	for _, dt := range ctx.DeferredTools {
		deferredNames[dt.ToolName] = true
	}
	for _, name := range expectedNames {
		assert.True(t, deferredNames[name], "deferred tools should contain %s", name)
	}

	// Verify the result only has the search tool
	assert.Equal(t, int64(1), gjson.GetBytes(result, "tools.#").Int())
	assert.Equal(t, "gateway_search_tools", gjson.GetBytes(result, "tools.0.name").String())
}

// =============================================================================
// TEST 11: Real-world Anthropic request — both pipes + merge
// =============================================================================

// TestMerge_RealWorldAnthropicRequest builds a realistic Anthropic request with
// system prompt, 5 messages, 10 tools, tool_use blocks. Applies both pipes
// (simulated). Merges. Verifies valid JSON with correct structure.
func TestMerge_RealWorldAnthropicRequest(t *testing.T) {
	// Realistic Anthropic request
	body := []byte(`{
		"model":"claude-3-5-sonnet-20241022",
		"max_tokens":4096,
		"system":"You are a helpful coding assistant. Use tools when needed.",
		"messages":[
			{"role":"user","content":"Read the main.go file and tell me what it does"},
			{"role":"assistant","content":[
				{"type":"text","text":"I'll read that file for you."},
				{"type":"tool_use","id":"toolu_01","name":"read_file","input":{"path":"main.go"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_01","content":"package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}"}
			]},
			{"role":"assistant","content":"This is a simple Go program that prints hello."},
			{"role":"user","content":"Now search for all test files"}
		],
		"tools":[
			{"name":"read_file","description":"Read a file from disk","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}},
			{"name":"write_file","description":"Write content to a file","input_schema":{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}},
			{"name":"search_code","description":"Search for code patterns","input_schema":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}},
			{"name":"list_dir","description":"List directory contents","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}},
			{"name":"execute_command","description":"Execute a shell command","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},
			{"name":"git_status","description":"Get git status","input_schema":{"type":"object"}},
			{"name":"git_diff","description":"Get git diff","input_schema":{"type":"object","properties":{"ref":{"type":"string"}}}},
			{"name":"create_branch","description":"Create a git branch","input_schema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}},
			{"name":"run_tests","description":"Run test suite","input_schema":{"type":"object","properties":{"pattern":{"type":"string"}}}},
			{"name":"deploy","description":"Deploy to production","input_schema":{"type":"object","properties":{"env":{"type":"string"}},"required":["env"]}}
		]
	}`)

	// Compact the body (remove whitespace) to simulate real request
	var compacted bytes.Buffer
	require.NoError(t, json.Compact(&compacted, body))
	original := compacted.Bytes()
	require.True(t, json.Valid(original))

	// Simulate tool_output pipe: compress the tool result
	toBody, err := sjson.SetBytes(original, "messages.2.content.0.content", "<<<SHADOW:shadow_abc>>> Compressed: Go main function prints hello")
	require.NoError(t, err)

	// Inject expand_context tool into tool_output result
	toBody, err = tooloutput.InjectExpandContextTool(toBody, map[string]string{"shadow_abc": "original"}, "anthropic")
	require.NoError(t, err)

	// Simulate tool_discovery pipe: filter to 3 most relevant tools + search tool
	relevantTools := `[{"name":"read_file","description":"Read a file from disk","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}},{"name":"search_code","description":"Search for code patterns","input_schema":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}},{"name":"list_dir","description":"List directory contents","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]`
	tdBody, err := sjson.SetRawBytes(original, "tools", []byte(relevantTools))
	require.NoError(t, err)

	// Merge
	result := mergeParallelResults(original, toBody, nil, tdBody, nil)

	require.True(t, json.Valid(result), "merged result must be valid JSON")

	// Verify structure
	assert.Equal(t, "claude-3-5-sonnet-20241022", gjson.GetBytes(result, "model").String())
	assert.Equal(t, int64(4096), gjson.GetBytes(result, "max_tokens").Int())
	assert.True(t, gjson.GetBytes(result, "system").Exists(), "system prompt must survive merge")

	// Messages from tool_output (compressed content)
	assert.Equal(t, int64(5), gjson.GetBytes(result, "messages.#").Int(), "should have 5 messages")
	compressedContent := gjson.GetBytes(result, "messages.2.content.0.content").String()
	assert.Contains(t, compressedContent, "SHADOW", "compressed content should have shadow marker")

	// Tools from tool_discovery (filtered)
	assert.Equal(t, int64(3), gjson.GetBytes(result, "tools.#").Int(), "should have 3 tools from discovery")
	assert.Equal(t, "read_file", gjson.GetBytes(result, "tools.0.name").String())
}

// =============================================================================
// TEST 12: Pre-computed bytes have no HTML escaping
// =============================================================================

// TestPrecomputedBytes_NoHTMLEscaping verifies that pre-computed phantom tool
// bytes don't contain HTML-escaped characters. Go's json.Marshal escapes
// < > & as \u003c \u003e \u0026 by default. The description contains
// <<<SHADOW:>>> markers which must not be escaped, since LLMs need to read them.
func TestPrecomputedBytes_NoHTMLEscaping(t *testing.T) {
	providers := []struct {
		name     string
		provider string
		body     []byte
	}{
		{
			"Anthropic",
			"anthropic",
			[]byte(`{"model":"claude-3","messages":[],"tools":[]}`),
		},
		{
			"OpenAI_Chat",
			"openai",
			[]byte(`{"model":"gpt-4","messages":[],"tools":[]}`),
		},
		{
			"OpenAI_Responses",
			"openai",
			[]byte(`{"model":"gpt-4","input":[],"tools":[]}`),
		},
	}

	for _, tt := range providers {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tooloutput.InjectExpandContextTool(tt.body, nil, tt.provider)
			require.NoError(t, err)

			resultStr := string(result)

			// Check for HTML-escaped angle brackets that json.Marshal would produce
			if strings.Contains(resultStr, `\u003c`) {
				t.Errorf("found \\u003c (HTML-escaped '<') in pre-computed bytes — "+
					"this will confuse LLMs reading <<<SHADOW:>>> markers.\n"+
					"Snippet: ...%s...",
					extractAround(resultStr, `\u003c`, 40))
			}
			if strings.Contains(resultStr, `\u003e`) {
				t.Errorf("found \\u003e (HTML-escaped '>') in pre-computed bytes — "+
					"this will confuse LLMs reading <<<SHADOW:>>> markers.\n"+
					"Snippet: ...%s...",
					extractAround(resultStr, `\u003e`, 40))
			}

			// Verify the description mentions SHADOW markers
			// Try both Anthropic (flat) and OpenAI (nested) paths
			desc := gjson.GetBytes(result, "tools.0.description").String()
			if desc == "" {
				desc = gjson.GetBytes(result, "tools.0.function.description").String()
			}
			assert.Contains(t, desc, "SHADOW", "description must mention SHADOW markers")

			// Verify valid JSON despite any special characters
			assert.True(t, json.Valid(result), "must be valid JSON")
		})
	}
}

// =============================================================================
// HELPERS
// =============================================================================

// extractAround returns a substring centered around the first occurrence of needle.
func extractAround(s, needle string, radius int) string {
	idx := strings.Index(s, needle)
	if idx < 0 {
		return ""
	}
	start := idx - radius
	if start < 0 {
		start = 0
	}
	end := idx + len(needle) + radius
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}

// mergeParallelResults is the same logic as in router.go — duplicated here
// for unit testing without importing the gateway package (which has many deps).
// Also defined in tests/gateway/unit/parallel_merge_test.go for that package.
func mergeParallelResults(original, toBody []byte, toErr error, tdBody []byte, tdErr error) []byte {
	if toErr != nil && tdErr != nil {
		return original
	}
	if toErr != nil {
		return tdBody
	}
	if tdErr != nil {
		return toBody
	}
	toolsValue := gjson.GetBytes(tdBody, "tools")
	if !toolsValue.Exists() {
		result, err := sjson.DeleteBytes(toBody, "tools")
		if err != nil {
			return toBody
		}
		return result
	}
	result, err := sjson.SetRawBytes(toBody, "tools", []byte(toolsValue.Raw))
	if err != nil {
		return toBody
	}
	return result
}
