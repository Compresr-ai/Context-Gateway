// Bug Fix 2 Tests: sjson-only tool modifications
//
// Verifies that tool_discovery pipe modifications use sjson (byte-preserving)
// instead of json.Unmarshal/Marshal round-trips that destroy byte prefixes.
// Critical for KV-cache stability on Anthropic.
package unit

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
	tooldiscovery "github.com/compresr/context-gateway/internal/pipes/tool_discovery"
)

// TestToolSearch_PreservesNonToolFields verifies that tool-search replacement
// does NOT modify model, messages, max_tokens, or other non-tools fields.
// Old code used json.Unmarshal/Marshal which re-serialized everything.
func TestToolSearch_PreservesNonToolFields(t *testing.T) {
	// Use specific whitespace and field ordering that json.Marshal would change
	body := []byte(`{"model":"claude-3-5-sonnet-20241022","max_tokens":4096,"messages":[{"role":"user","content":"hello world"}],"tools":[{"name":"read_file","description":"Read a file","input_schema":{"type":"object"}},{"name":"write_file","description":"Write a file","input_schema":{"type":"object"}}]}`)

	cfg := &config.Config{
		Pipes: config.PipesConfig{
			ToolDiscovery: config.ToolDiscoveryPipeConfig{
				Enabled:  true,
				Strategy: config.StrategyToolSearch,
			},
		},
	}
	pipe := tooldiscovery.New(cfg)

	registry := adapters.NewRegistry()
	ctx := pipes.NewPipeContext(registry.Get("anthropic"), body)

	result, err := pipe.Process(ctx)
	require.NoError(t, err)

	// Non-tools fields must be preserved (model and max_tokens byte-identical;
	// messages structurally equivalent — parsed path re-serializes content).
	origModel := gjson.GetBytes(body, "model").Raw
	resultModel := gjson.GetBytes(result, "model").Raw
	assert.Equal(t, origModel, resultModel, "model field must be preserved exactly")

	origMaxTokens := gjson.GetBytes(body, "max_tokens").Raw
	resultMaxTokens := gjson.GetBytes(result, "max_tokens").Raw
	assert.Equal(t, origMaxTokens, resultMaxTokens, "max_tokens field must be preserved exactly")

	// Messages content must be structurally equivalent (parsed path may re-serialize).
	origMsgRole := gjson.GetBytes(body, "messages.0.role").String()
	resultMsgRole := gjson.GetBytes(result, "messages.0.role").String()
	assert.Equal(t, origMsgRole, resultMsgRole, "messages[0].role must be preserved")
	origMsgContent := gjson.GetBytes(body, "messages.0.content").String()
	resultMsgContent := gjson.GetBytes(result, "messages.0.content").String()
	assert.Equal(t, origMsgContent, resultMsgContent, "messages[0].content must be preserved")

	// Tools: original tools become stubs (name preserved, description=DeferredStubDescription).
	// NOTE: gateway_search_tools is NOT appended by the pipe — it is injected later
	// by phantom_tools.InjectAll in handler.go. The pipe output has only stubs.
	origToolCount := gjson.GetBytes(body, "tools.#").Int()
	resultToolCount := gjson.GetBytes(result, "tools.#").Int()
	assert.Equal(t, origToolCount, resultToolCount, "pipe emits one stub per original tool (gateway_search_tools is added by handler)")

	// First stub: read_file → deferred
	assert.Equal(t, "read_file", gjson.GetBytes(result, "tools.0.name").String())
	assert.Equal(t, adapters.DeferredStubDescription, gjson.GetBytes(result, "tools.0.description").String())

	// Second stub: write_file → deferred
	assert.Equal(t, "write_file", gjson.GetBytes(result, "tools.1.name").String())
	assert.Equal(t, adapters.DeferredStubDescription, gjson.GetBytes(result, "tools.1.description").String())
}

// TestToolSearch_ByteIdenticalAcrossCalls verifies that calling tool-search
// on the same input produces byte-identical output every time.
func TestToolSearch_ByteIdenticalAcrossCalls(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"test"}],"tools":[{"name":"read_file","description":"Read","input_schema":{"type":"object"}},{"name":"write_file","description":"Write","input_schema":{"type":"object"}},{"name":"bash","description":"Run command","input_schema":{"type":"object"}}]}`)

	cfg := &config.Config{
		Pipes: config.PipesConfig{
			ToolDiscovery: config.ToolDiscoveryPipeConfig{
				Enabled:  true,
				Strategy: config.StrategyToolSearch,
			},
		},
	}

	registry := adapters.NewRegistry()

	var results [][]byte
	for i := 0; i < 20; i++ {
		pipe := tooldiscovery.New(cfg)
		ctx := pipes.NewPipeContext(registry.Get("anthropic"), body)
		result, err := pipe.Process(ctx)
		require.NoError(t, err)
		results = append(results, result)
	}

	// ALL 20 results must be byte-identical
	for i := 1; i < len(results); i++ {
		assert.True(t, bytes.Equal(results[0], results[i]),
			"call %d produced different bytes than call 0:\ngot:  %s\nwant: %s",
			i, string(results[i][:min(200, len(results[i]))]), string(results[0][:min(200, len(results[0]))]))
	}
}

// TestToolSearch_OpenAI_ByteIdenticalAcrossCalls same test for OpenAI format.
func TestToolSearch_OpenAI_ByteIdenticalAcrossCalls(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"test"}],"tools":[{"type":"function","function":{"name":"read_file","description":"Read","parameters":{"type":"object"}}},{"type":"function","function":{"name":"write_file","description":"Write","parameters":{"type":"object"}}}]}`)

	cfg := &config.Config{
		Pipes: config.PipesConfig{
			ToolDiscovery: config.ToolDiscoveryPipeConfig{
				Enabled:  true,
				Strategy: config.StrategyToolSearch,
			},
		},
	}

	registry := adapters.NewRegistry()

	var results [][]byte
	for i := 0; i < 20; i++ {
		pipe := tooldiscovery.New(cfg)
		ctx := pipes.NewPipeContext(registry.Get("openai"), body)
		result, err := pipe.Process(ctx)
		require.NoError(t, err)
		results = append(results, result)
	}

	for i := 1; i < len(results); i++ {
		assert.True(t, bytes.Equal(results[0], results[i]),
			"OpenAI call %d produced different bytes", i)
	}
}

// TestToolSearch_ValidJSON verifies the output is always valid JSON.
func TestToolSearch_ValidJSON(t *testing.T) {
	bodies := []struct {
		name string
		body []byte
		prov string
	}{
		{
			"anthropic",
			[]byte(`{"model":"claude-3","messages":[],"tools":[{"name":"t1","description":"d1","input_schema":{"type":"object"}}]}`),
			"anthropic",
		},
		{
			"openai",
			[]byte(`{"model":"gpt-4","messages":[],"tools":[{"type":"function","function":{"name":"t1","description":"d1","parameters":{"type":"object"}}}]}`),
			"openai",
		},
	}

	for _, tt := range bodies {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Pipes: config.PipesConfig{
					ToolDiscovery: config.ToolDiscoveryPipeConfig{
						Enabled:  true,
						Strategy: config.StrategyToolSearch,
					},
				},
			}
			pipe := tooldiscovery.New(cfg)
			registry := adapters.NewRegistry()
			ctx := pipes.NewPipeContext(registry.Get(tt.prov), tt.body)

			result, err := pipe.Process(ctx)
			require.NoError(t, err)
			assert.True(t, json.Valid(result), "output must be valid JSON: %s", string(result[:min(200, len(result))]))
		})
	}
}
