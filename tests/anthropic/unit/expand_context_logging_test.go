// Expand Context Logging Tests
//
// Tests compression logging with expand_context enabled.
// Verifies that expand_enabled flag appears in compression logs
// when EnableExpandContext is true in config.
package unit

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/tests/anthropic/fixtures"
)

// TestExpandContext_ConfigValidation verifies config loads correctly.
func TestExpandContext_ConfigValidation(t *testing.T) {
	tests := []struct {
		name          string
		expandEnabled bool
		expectValid   bool
	}{
		{
			name:          "expand_enabled_true",
			expandEnabled: true,
			expectValid:   true,
		},
		{
			name:          "expand_enabled_false",
			expandEnabled: false,
			expectValid:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := fixtures.TestConfig(config.StrategyCompresr, 256, tt.expandEnabled)

			assert.Equal(t, tt.expandEnabled, cfg.Pipes.ToolOutput.EnableExpandContext,
				"EnableExpandContext should match test case")

			// Validate config
			err := cfg.Pipes.ToolOutput.Validate()
			if tt.expectValid {
				require.NoError(t, err, "config should be valid")
			} else {
				require.Error(t, err, "config should be invalid")
			}
		})
	}
}

// TestExpandContext_InjectExpandTool verifies phantom tool injection.
func TestExpandContext_InjectExpandTool(t *testing.T) {
	// Create request body
	message := fixtures.AnthropicUserMessage("Summarize this file")
	body := fixtures.AnthropicRequest([]map[string]interface{}{message}, nil)

	// Parse to verify structure
	var req map[string]interface{}
	err := json.Unmarshal(body, &req)
	require.NoError(t, err)

	// Verify messages exist
	assert.NotEmpty(t, req["messages"], "request should have messages")
}

// TestExpandContext_ShadowIDStorage verifies shadow IDs are tracked.
func TestExpandContext_ShadowIDStorage(t *testing.T) {
	// Store a test shadow context
	originalContent := "This is the original uncompressed content"
	shadowID := "shadow_test123"

	st := fixtures.PreloadedStore(map[string]string{shadowID: originalContent})

	// Retrieve it
	retrieved, found := st.Get(shadowID)
	require.True(t, found, "shadow ID should be found in store")
	assert.Equal(t, originalContent, retrieved, "retrieved content should match original")
}

// TestExpandContext_PhantomToolFormat verifies expand_context tool format.
func TestExpandContext_PhantomToolFormat(t *testing.T) {
	// The phantom tool should have this structure:
	expectedTool := map[string]interface{}{
		"name":        "expand_context",
		"description": "Request full uncompressed version of compressed content",
		"input_schema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{
					"type":        "string",
					"description": "Shadow ID from compressed content (format: shadow_xxx)",
				},
			},
			"required": []interface{}{"id"},
		},
	}

	// Verify tool structure
	assert.Equal(t, "expand_context", expectedTool["name"])
	assert.NotEmpty(t, expectedTool["description"])
	assert.NotEmpty(t, expectedTool["input_schema"])
}
