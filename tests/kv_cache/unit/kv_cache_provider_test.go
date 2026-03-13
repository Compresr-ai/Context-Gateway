// KV Cache Preservation & Cross-Provider Compatibility Tests
//
// Verifies that phantom tool injection (expand_context, gateway_search_tools)
// produces byte-identical tools[] across multiple turns and concurrent access,
// and that each provider receives the correct tool format.
package unit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
	tooldiscovery "github.com/compresr/context-gateway/internal/pipes/tool_discovery"
	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
)

// =============================================================================
// KV CACHE STABILITY TESTS
// =============================================================================

// TestKVCache_ExpandContext_10Turns_Anthropic simulates 10 conversation turns
// with growing messages in Anthropic format. Verifies that InjectExpandContextTool
// produces byte-identical tools[] across all turns.
func TestKVCache_ExpandContext_10Turns_Anthropic(t *testing.T) {
	baseTools := `[{"name":"read_file","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]`

	var toolsRawPerTurn []string

	for turn := 1; turn <= 10; turn++ {
		// Build growing messages array
		var msgs []string
		for i := 0; i < turn; i++ {
			if i%2 == 0 {
				msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":"message %d"}`, i))
			} else {
				msgs = append(msgs, fmt.Sprintf(`{"role":"assistant","content":"response %d"}`, i))
			}
		}
		messagesJSON := "[" + joinStrings(msgs, ",") + "]"
		body := []byte(fmt.Sprintf(`{"model":"claude-3-5-sonnet-20241022","max_tokens":4096,"messages":%s,"tools":%s}`, messagesJSON, baseTools))

		result, err := tooloutput.InjectExpandContextTool(body, nil, "anthropic")
		require.NoError(t, err, "turn %d", turn)

		toolsRaw := gjson.GetBytes(result, "tools").Raw
		require.NotEmpty(t, toolsRaw, "turn %d: tools must exist", turn)
		toolsRawPerTurn = append(toolsRawPerTurn, toolsRaw)
	}

	// All 10 turns must have byte-identical tools[]
	for i := 1; i < len(toolsRawPerTurn); i++ {
		assert.Equal(t, toolsRawPerTurn[0], toolsRawPerTurn[i],
			"turn %d tools[] differs from turn 1", i+1)
	}
}

// TestKVCache_ExpandContext_10Turns_OpenAI simulates 10 conversation turns
// with growing messages in OpenAI Chat Completions format.
func TestKVCache_ExpandContext_10Turns_OpenAI(t *testing.T) {
	baseTools := `[{"type":"function","function":{"name":"read_file","description":"Read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}]`

	var toolsRawPerTurn []string

	for turn := 1; turn <= 10; turn++ {
		var msgs []string
		for i := 0; i < turn; i++ {
			if i%2 == 0 {
				msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":"message %d"}`, i))
			} else {
				msgs = append(msgs, fmt.Sprintf(`{"role":"assistant","content":"response %d"}`, i))
			}
		}
		messagesJSON := "[" + joinStrings(msgs, ",") + "]"
		body := []byte(fmt.Sprintf(`{"model":"gpt-4o","messages":%s,"tools":%s}`, messagesJSON, baseTools))

		result, err := tooloutput.InjectExpandContextTool(body, nil, "openai")
		require.NoError(t, err, "turn %d", turn)

		toolsRaw := gjson.GetBytes(result, "tools").Raw
		require.NotEmpty(t, toolsRaw, "turn %d: tools must exist", turn)
		toolsRawPerTurn = append(toolsRawPerTurn, toolsRaw)
	}

	for i := 1; i < len(toolsRawPerTurn); i++ {
		assert.Equal(t, toolsRawPerTurn[0], toolsRawPerTurn[i],
			"OpenAI turn %d tools[] differs from turn 1", i+1)
	}
}

// TestKVCache_SearchTool_10Turns applies tool-search Process() 10 times on
// bodies with the same tools but different messages. Verifies tools[] identical.
func TestKVCache_SearchTool_10Turns(t *testing.T) {
	baseTools := `[{"name":"read_file","description":"Read a file","input_schema":{"type":"object"}},{"name":"write_file","description":"Write a file","input_schema":{"type":"object"}},{"name":"bash","description":"Run command","input_schema":{"type":"object"}}]`

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

	var toolsRawPerTurn []string

	for turn := 1; turn <= 10; turn++ {
		var msgs []string
		for i := 0; i < turn; i++ {
			msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":"turn %d msg %d"}`, turn, i))
		}
		messagesJSON := "[" + joinStrings(msgs, ",") + "]"
		body := []byte(fmt.Sprintf(`{"model":"claude-3","messages":%s,"tools":%s}`, messagesJSON, baseTools))

		pipe := tooldiscovery.New(cfg)
		ctx := pipes.NewPipeContext(registry.Get("anthropic"), body)
		result, err := pipe.Process(ctx)
		require.NoError(t, err, "turn %d", turn)

		toolsRaw := gjson.GetBytes(result, "tools").Raw
		require.NotEmpty(t, toolsRaw, "turn %d: tools must exist", turn)
		toolsRawPerTurn = append(toolsRawPerTurn, toolsRaw)
	}

	for i := 1; i < len(toolsRawPerTurn); i++ {
		assert.Equal(t, toolsRawPerTurn[0], toolsRawPerTurn[i],
			"search tool turn %d tools[] differs from turn 1", i+1)
	}
}

// TestKVCache_BothPhantomTools_Coexist verifies interaction between expand_context
// and gateway_search_tools phantom tool injection.
func TestKVCache_BothPhantomTools_Coexist(t *testing.T) {
	baseTools := `[{"name":"read_file","description":"Read a file","input_schema":{"type":"object"}},{"name":"write_file","description":"Write a file","input_schema":{"type":"object"}}]`

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

	t.Run("expand_then_search_replaces_all", func(t *testing.T) {
		// First inject expand_context
		body := []byte(fmt.Sprintf(`{"model":"claude-3","messages":[{"role":"user","content":"test"}],"tools":%s}`, baseTools))
		withExpand, err := tooloutput.InjectExpandContextTool(body, nil, "anthropic")
		require.NoError(t, err)

		// Verify expand_context was added
		expandTools := gjson.GetBytes(withExpand, "tools")
		hasExpand := false
		expandTools.ForEach(func(_, value gjson.Result) bool {
			if value.Get("name").String() == "expand_context" {
				hasExpand = true
				return false
			}
			return true
		})
		assert.True(t, hasExpand, "expand_context should be present after injection")

		// Now apply tool-search which replaces ALL tools
		pipe := tooldiscovery.New(cfg)
		ctx := pipes.NewPipeContext(registry.Get("anthropic"), withExpand)
		result, err := pipe.Process(ctx)
		require.NoError(t, err)

		// tool-search replaces everything with just gateway_search_tools
		resultTools := gjson.GetBytes(result, "tools")
		assert.Equal(t, int64(1), resultTools.Get("#").Int(),
			"tool-search should replace all tools with just search tool")
		assert.Equal(t, "gateway_search_tools", resultTools.Get("0.name").String())
	})

	t.Run("search_then_expand_coexist", func(t *testing.T) {
		// First apply tool-search
		body := []byte(fmt.Sprintf(`{"model":"claude-3","messages":[{"role":"user","content":"test"}],"tools":%s}`, baseTools))
		pipe := tooldiscovery.New(cfg)
		ctx := pipes.NewPipeContext(registry.Get("anthropic"), body)
		withSearch, err := pipe.Process(ctx)
		require.NoError(t, err)

		// Verify only search tool
		searchTools := gjson.GetBytes(withSearch, "tools")
		assert.Equal(t, int64(1), searchTools.Get("#").Int())
		assert.Equal(t, "gateway_search_tools", searchTools.Get("0.name").String())

		// Now inject expand_context on top
		result, err := tooloutput.InjectExpandContextTool(withSearch, nil, "anthropic")
		require.NoError(t, err)

		// Both tools should coexist: [gateway_search_tools, expand_context]
		resultTools := gjson.GetBytes(result, "tools")
		assert.Equal(t, int64(2), resultTools.Get("#").Int(),
			"should have both phantom tools")

		toolNames := make(map[string]bool)
		resultTools.ForEach(func(_, value gjson.Result) bool {
			toolNames[value.Get("name").String()] = true
			return true
		})
		assert.True(t, toolNames["gateway_search_tools"], "must have gateway_search_tools")
		assert.True(t, toolNames["expand_context"], "must have expand_context")
	})
}

// TestKVCache_ToolsPrefixPreserved_WhenMessagesGrow verifies that the tools[]
// bytes remain identical even as the messages array grows across turns.
func TestKVCache_ToolsPrefixPreserved_WhenMessagesGrow(t *testing.T) {
	baseTools := `[{"name":"read_file","description":"Read a file","input_schema":{"type":"object"}},{"name":"write_file","description":"Write a file","input_schema":{"type":"object"}},{"name":"bash","description":"Execute","input_schema":{"type":"object"}}]`

	messageCounts := []int{1, 3, 5}
	var toolsByteSlices [][]byte

	for _, msgCount := range messageCounts {
		var msgs []string
		for i := 0; i < msgCount; i++ {
			msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":"message %d with some content"}`, i))
		}
		messagesJSON := "[" + joinStrings(msgs, ",") + "]"
		body := []byte(fmt.Sprintf(`{"model":"claude-3","messages":%s,"tools":%s}`, messagesJSON, baseTools))

		result, err := tooloutput.InjectExpandContextTool(body, nil, "anthropic")
		require.NoError(t, err)

		// Extract tools bytes using bytes.Index for raw position comparison
		toolsMarker := []byte(`"tools":`)
		idx := bytes.Index(result, toolsMarker)
		require.Greater(t, idx, 0, "tools field must exist in result")

		// Extract from "tools": to end of the tools array
		toolsRaw := gjson.GetBytes(result, "tools").Raw
		require.NotEmpty(t, toolsRaw)
		toolsByteSlices = append(toolsByteSlices, []byte(toolsRaw))
	}

	// Tools bytes must be identical regardless of message count
	for i := 1; i < len(toolsByteSlices); i++ {
		assert.True(t, bytes.Equal(toolsByteSlices[0], toolsByteSlices[i]),
			"tools bytes with %d messages differ from %d messages",
			messageCounts[i], messageCounts[0])
	}
}

// =============================================================================
// CROSS-PROVIDER FORMAT TESTS
// =============================================================================

// TestProvider_Anthropic_ExpandToolFormat verifies expand_context tool uses
// Anthropic format: {name, description, input_schema}. No "type":"function" wrapper.
func TestProvider_Anthropic_ExpandToolFormat(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[],"tools":[{"name":"read_file","description":"Read","input_schema":{"type":"object"}}]}`)

	result, err := tooloutput.InjectExpandContextTool(body, nil, "anthropic")
	require.NoError(t, err)

	// Find the expand_context tool
	tools := gjson.GetBytes(result, "tools")
	var expandTool gjson.Result
	tools.ForEach(func(_, value gjson.Result) bool {
		if value.Get("name").String() == "expand_context" {
			expandTool = value
			return false
		}
		return true
	})
	require.True(t, expandTool.Exists(), "expand_context tool must be present")

	raw := expandTool.Raw

	// Must have Anthropic fields
	assert.True(t, gjson.Get(raw, "name").Exists(), "must have name")
	assert.Equal(t, "expand_context", gjson.Get(raw, "name").String())
	assert.True(t, gjson.Get(raw, "description").Exists(), "must have description")
	assert.True(t, gjson.Get(raw, "input_schema").Exists(), "must have input_schema")
	assert.Equal(t, "object", gjson.Get(raw, "input_schema.type").String())
	assert.True(t, gjson.Get(raw, "input_schema.properties.id").Exists(), "must have id property")

	// Must NOT have OpenAI fields
	assert.False(t, gjson.Get(raw, "type").Exists(), "must NOT have type field")
	assert.False(t, gjson.Get(raw, "function").Exists(), "must NOT have function wrapper")
	assert.False(t, gjson.Get(raw, "parameters").Exists(), "must NOT have parameters key")
}

// TestProvider_OpenAI_Chat_ExpandToolFormat verifies expand_context tool uses
// OpenAI Chat Completions format: {type:"function", function:{name, description, parameters}}.
func TestProvider_OpenAI_Chat_ExpandToolFormat(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[],"tools":[{"type":"function","function":{"name":"read_file","description":"Read","parameters":{"type":"object"}}}]}`)

	result, err := tooloutput.InjectExpandContextTool(body, nil, "openai")
	require.NoError(t, err)

	tools := gjson.GetBytes(result, "tools")
	var expandTool gjson.Result
	tools.ForEach(func(_, value gjson.Result) bool {
		if value.Get("function.name").String() == "expand_context" {
			expandTool = value
			return false
		}
		return true
	})
	require.True(t, expandTool.Exists(), "expand_context tool must be present")

	raw := expandTool.Raw

	// Must have OpenAI Chat Completions fields
	assert.Equal(t, "function", gjson.Get(raw, "type").String())
	assert.Equal(t, "expand_context", gjson.Get(raw, "function.name").String())
	assert.True(t, gjson.Get(raw, "function.description").Exists(), "must have function.description")
	assert.True(t, gjson.Get(raw, "function.parameters").Exists(), "must have function.parameters")
	assert.Equal(t, "object", gjson.Get(raw, "function.parameters.type").String())

	// Must NOT have flat Anthropic-style fields
	assert.False(t, gjson.Get(raw, "input_schema").Exists(), "must NOT have input_schema")
	// "name" at root should not exist (only inside "function")
	assert.False(t, gjson.Get(raw, "name").Exists(), "must NOT have flat name")
}

// TestProvider_OpenAI_Responses_ExpandToolFormat verifies expand_context tool uses
// OpenAI Responses API format: {type:"function", name, description, parameters} (flat).
func TestProvider_OpenAI_Responses_ExpandToolFormat(t *testing.T) {
	// Responses API uses "input" instead of "messages"
	body := []byte(`{"model":"gpt-4o","input":"What is the weather?","tools":[{"type":"function","name":"read_file","description":"Read","parameters":{"type":"object"}}]}`)

	result, err := tooloutput.InjectExpandContextTool(body, nil, "openai")
	require.NoError(t, err)

	tools := gjson.GetBytes(result, "tools")
	var expandTool gjson.Result
	tools.ForEach(func(_, value gjson.Result) bool {
		if value.Get("name").String() == "expand_context" {
			expandTool = value
			return false
		}
		return true
	})
	require.True(t, expandTool.Exists(), "expand_context tool must be present")

	raw := expandTool.Raw

	// Must have Responses API fields (flat)
	assert.Equal(t, "function", gjson.Get(raw, "type").String())
	assert.Equal(t, "expand_context", gjson.Get(raw, "name").String())
	assert.True(t, gjson.Get(raw, "description").Exists(), "must have description")
	assert.True(t, gjson.Get(raw, "parameters").Exists(), "must have parameters")
	assert.Equal(t, "object", gjson.Get(raw, "parameters.type").String())

	// Must NOT have "function" wrapper (that's Chat Completions format)
	assert.False(t, gjson.Get(raw, "function").Exists(), "must NOT have function wrapper")
	// Must NOT have Anthropic's input_schema
	assert.False(t, gjson.Get(raw, "input_schema").Exists(), "must NOT have input_schema")
}

// TestProvider_SearchTool_Anthropic_Format verifies gateway_search_tools uses
// Anthropic format: {name, description, input_schema}.
func TestProvider_SearchTool_Anthropic_Format(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"test"}],"tools":[{"name":"read_file","description":"Read","input_schema":{"type":"object"}},{"name":"write_file","description":"Write","input_schema":{"type":"object"}}]}`)

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

	tools := gjson.GetBytes(result, "tools")
	require.Equal(t, int64(1), tools.Get("#").Int())

	searchTool := tools.Get("0").Raw

	// Must have Anthropic fields
	assert.Equal(t, "gateway_search_tools", gjson.Get(searchTool, "name").String())
	assert.True(t, gjson.Get(searchTool, "description").Exists(), "must have description")
	assert.True(t, gjson.Get(searchTool, "input_schema").Exists(), "must have input_schema")
	assert.Equal(t, "object", gjson.Get(searchTool, "input_schema.type").String())

	// Must NOT have OpenAI wrapper
	assert.False(t, gjson.Get(searchTool, "type").Exists(), "must NOT have type field")
	assert.False(t, gjson.Get(searchTool, "function").Exists(), "must NOT have function wrapper")
}

// TestProvider_SearchTool_OpenAI_Format verifies gateway_search_tools uses
// OpenAI Chat Completions format: {type:"function", function:{name, description, parameters}}.
func TestProvider_SearchTool_OpenAI_Format(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"test"}],"tools":[{"type":"function","function":{"name":"read_file","description":"Read","parameters":{"type":"object"}}},{"type":"function","function":{"name":"write_file","description":"Write","parameters":{"type":"object"}}}]}`)

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
	ctx := pipes.NewPipeContext(registry.Get("openai"), body)

	result, err := pipe.Process(ctx)
	require.NoError(t, err)

	tools := gjson.GetBytes(result, "tools")
	require.Equal(t, int64(1), tools.Get("#").Int())

	searchTool := tools.Get("0").Raw

	// Must have OpenAI Chat Completions fields
	assert.Equal(t, "function", gjson.Get(searchTool, "type").String())
	assert.Equal(t, "gateway_search_tools", gjson.Get(searchTool, "function.name").String())
	assert.True(t, gjson.Get(searchTool, "function.description").Exists(), "must have function.description")
	assert.True(t, gjson.Get(searchTool, "function.parameters").Exists(), "must have function.parameters")

	// Must NOT have flat Anthropic-style fields
	assert.False(t, gjson.Get(searchTool, "input_schema").Exists(), "must NOT have input_schema")
}

// =============================================================================
// STRESS TESTS
// =============================================================================

// TestStress_InjectExpandContext_1000Times calls InjectExpandContextTool 1000 times
// on the same body. All results must be byte-identical.
func TestStress_InjectExpandContext_1000Times(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"hello"}],"tools":[{"name":"read_file","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]}`)

	var results [][]byte
	for i := 0; i < 1000; i++ {
		result, err := tooloutput.InjectExpandContextTool(body, nil, "anthropic")
		require.NoError(t, err, "iteration %d", i)
		results = append(results, result)
	}

	// All 1000 must be byte-identical
	for i := 1; i < len(results); i++ {
		if !bytes.Equal(results[0], results[i]) {
			t.Fatalf("iteration %d produced different bytes than iteration 0:\ngot:  %s\nwant: %s",
				i, string(results[i][:min(300, len(results[i]))]), string(results[0][:min(300, len(results[0]))]))
		}
	}
}

// TestStress_ToolSearch_50Tools creates a body with 50 tools, applies tool-search,
// and verifies exactly 1 tool in output, all 50 in DeferredTools, and valid JSON.
func TestStress_ToolSearch_50Tools(t *testing.T) {
	// Build 50 tools
	var tools []string
	for i := 0; i < 50; i++ {
		tools = append(tools, fmt.Sprintf(`{"name":"tool_%d","description":"Tool number %d does things","input_schema":{"type":"object","properties":{"arg":{"type":"string"}}}}`, i, i))
	}
	toolsJSON := "[" + joinStrings(tools, ",") + "]"
	body := []byte(fmt.Sprintf(`{"model":"claude-3","messages":[{"role":"user","content":"test"}],"tools":%s}`, toolsJSON))

	// Verify input is valid JSON
	require.True(t, json.Valid(body), "input body must be valid JSON")

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

	// Output must be valid JSON
	assert.True(t, json.Valid(result), "output must be valid JSON")

	// Exactly 1 tool in output (gateway_search_tools)
	resultTools := gjson.GetBytes(result, "tools")
	assert.Equal(t, int64(1), resultTools.Get("#").Int(),
		"should have exactly 1 tool (gateway_search_tools)")
	assert.Equal(t, "gateway_search_tools", resultTools.Get("0.name").String())

	// All 50 original tools stored as deferred
	assert.Equal(t, 50, len(ctx.DeferredTools),
		"all 50 tools should be deferred")
}

// TestStress_Concurrent_Inject runs 100 goroutines each injecting expand_context
// into the same body concurrently. All results must be byte-identical.
// Run with -race to verify no data races on pre-computed bytes.
func TestStress_Concurrent_Inject(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"concurrent test"}],"tools":[{"name":"read_file","description":"Read","input_schema":{"type":"object"}},{"name":"write_file","description":"Write","input_schema":{"type":"object"}}]}`)

	const goroutines = 100
	results := make([][]byte, goroutines)
	errs := make([]error, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			// Each goroutine gets its own copy of body to avoid input mutation
			bodyCopy := make([]byte, len(body))
			copy(bodyCopy, body)
			results[idx], errs[idx] = tooloutput.InjectExpandContextTool(bodyCopy, nil, "anthropic")
		}(i)
	}

	wg.Wait()

	// Check all succeeded
	for i := 0; i < goroutines; i++ {
		require.NoError(t, errs[i], "goroutine %d failed", i)
		require.NotNil(t, results[i], "goroutine %d returned nil", i)
	}

	// All must be byte-identical
	for i := 1; i < goroutines; i++ {
		assert.True(t, bytes.Equal(results[0], results[i]),
			"goroutine %d produced different bytes than goroutine 0:\ngot:  %s\nwant: %s",
			i, string(results[i][:min(200, len(results[i]))]), string(results[0][:min(200, len(results[0]))]))
	}

	// Verify the result is valid JSON
	assert.True(t, json.Valid(results[0]), "concurrent result must be valid JSON")

	// Verify expand_context was injected
	tools := gjson.GetBytes(results[0], "tools")
	hasExpand := false
	tools.ForEach(func(_, value gjson.Result) bool {
		if value.Get("name").String() == "expand_context" {
			hasExpand = true
			return false
		}
		return true
	})
	assert.True(t, hasExpand, "expand_context must be present in concurrent result")
}

// =============================================================================
// HELPERS
// =============================================================================

// joinStrings joins string slices with a separator (avoids strings import for minimal deps).
func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}
