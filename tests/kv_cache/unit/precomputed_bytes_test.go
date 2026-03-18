// Bug Fix 1 Tests: Pre-computed phantom tool bytes
//
// Verifies that phantom tool JSON is computed once at init time and produces
// byte-identical output on every access. This prevents KV-cache invalidation
// caused by Go's non-deterministic map iteration in json.Marshal.
package unit

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/compresr/context-gateway/internal/adapters"
	phantom_tools "github.com/compresr/context-gateway/internal/phantom_tools"
)

// TestPrecomputedExpandBytes_Deterministic verifies expand_context tool JSON
// is byte-identical across 100 accesses (no map randomization).
func TestPrecomputedExpandBytes_Deterministic(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[],"tools":[{"name":"read_file","description":"Read"}]}`)

	// Inject 100 times on identical input
	var results [][]byte
	for i := 0; i < 100; i++ {
		result, err := phantom_tools.InjectAll(body, adapters.Provider("anthropic"))
		require.NoError(t, err)
		results = append(results, result)
	}

	// All must be byte-identical
	for i := 1; i < len(results); i++ {
		assert.True(t, bytes.Equal(results[0], results[i]),
			"injection %d produced different bytes than injection 0", i)
	}
}

// TestPrecomputedExpandBytes_ValidJSON verifies pre-computed bytes are valid JSON
// with correct structure per provider.
func TestPrecomputedExpandBytes_ValidJSON(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		checkFn  func(t *testing.T, toolJSON string)
	}{
		{
			name:     "Anthropic",
			provider: "anthropic",
			checkFn: func(t *testing.T, toolJSON string) {
				assert.True(t, gjson.Get(toolJSON, "name").Exists(), "must have name")
				assert.Equal(t, "expand_context", gjson.Get(toolJSON, "name").String())
				assert.True(t, gjson.Get(toolJSON, "description").Exists(), "must have description")
				assert.True(t, gjson.Get(toolJSON, "input_schema").Exists(), "must have input_schema")
				assert.Equal(t, "object", gjson.Get(toolJSON, "input_schema.type").String())
				assert.True(t, gjson.Get(toolJSON, "input_schema.properties.id").Exists(), "must have id property")
			},
		},
		{
			name:     "OpenAI_Chat",
			provider: "openai",
			checkFn: func(t *testing.T, toolJSON string) {
				assert.Equal(t, "function", gjson.Get(toolJSON, "type").String())
				assert.Equal(t, "expand_context", gjson.Get(toolJSON, "function.name").String())
				assert.True(t, gjson.Get(toolJSON, "function.parameters").Exists(), "must have parameters")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build body with appropriate format marker
			var body []byte
			if tt.provider == "openai" {
				body = []byte(`{"model":"gpt-4","messages":[],"tools":[]}`)
			} else {
				body = []byte(`{"model":"claude-3","messages":[],"tools":[]}`)
			}

			result, err := phantom_tools.InjectAll(body, adapters.Provider(tt.provider))
			require.NoError(t, err)
			assert.True(t, json.Valid(result), "result must be valid JSON")

			// Extract the injected tool (expand_context is at index 0, gateway_search_tools at index 1)
			tools := gjson.GetBytes(result, "tools")
			assert.Equal(t, int64(2), tools.Get("#").Int())
			tt.checkFn(t, tools.Get("0").Raw)
		})
	}
}

// TestPrecomputedExpandBytes_DescriptionImproved verifies the new description
// mentions [REF:id] markers and when to expand.
func TestPrecomputedExpandBytes_DescriptionImproved(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[],"tools":[]}`)

	result, err := phantom_tools.InjectAll(body, adapters.Provider("anthropic"))
	require.NoError(t, err)

	desc := gjson.GetBytes(result, "tools.0.description").String()
	assert.Contains(t, desc, "REF", "description should mention REF markers")
	// New concise description: "Expand a [REF:id] reference to retrieve the full uncompressed content."
	assert.Contains(t, desc, "Expand", "description should mention expanding")
	// Verify no HTML escaping of angle brackets
	assert.NotContains(t, desc, `\u003c`, "description must not have HTML-escaped < characters")
}
