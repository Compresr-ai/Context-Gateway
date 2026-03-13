// Inject/Filter Tests - Text-Based Expand Context
//
// Tests the text-based expand_context pattern extraction:
// - ParseExpandPatternsFromText: Extract shadow IDs from <<<EXPAND:...>>> patterns
// These enable transparent context expansion without modifying tools[] (preserves KV-cache).
package unit

import (
	"testing"

	"github.com/stretchr/testify/assert"

	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
)

// TestParseExpandPatternsFromText_Anthropic verifies pattern extraction from Anthropic response.
func TestParseExpandPatternsFromText_Anthropic(t *testing.T) {
	response := []byte(`{
		"content": [
			{"type": "text", "text": "I need <<<EXPAND:shadow_abc>>> and <<<EXPAND:shadow_def>>>"}
		]
	}`)

	ids := tooloutput.ParseExpandPatternsFromText(response)
	assert.Len(t, ids, 2)
	assert.Contains(t, ids, "shadow_abc")
	assert.Contains(t, ids, "shadow_def")
}

// TestParseExpandPatternsFromText_OpenAI verifies pattern extraction from OpenAI response.
func TestParseExpandPatternsFromText_OpenAI(t *testing.T) {
	response := []byte(`{
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "Let me get <<<EXPAND:shadow_xyz>>>"
			}
		}]
	}`)

	ids := tooloutput.ParseExpandPatternsFromText(response)
	assert.Len(t, ids, 1)
	assert.Equal(t, "shadow_xyz", ids[0])
}

// TestParseExpandPatternsFromText_NoPatterns verifies empty result when no patterns.
func TestParseExpandPatternsFromText_NoPatterns(t *testing.T) {
	response := []byte(`{
		"content": [
			{"type": "text", "text": "Just a regular response"}
		]
	}`)

	ids := tooloutput.ParseExpandPatternsFromText(response)
	assert.Empty(t, ids)
}

// TestParseExpandPatternsFromText_Dedup verifies deduplication of shadow IDs.
func TestParseExpandPatternsFromText_Dedup(t *testing.T) {
	response := []byte(`{
		"content": [
			{"type": "text", "text": "<<<EXPAND:shadow_abc>>> and again <<<EXPAND:shadow_abc>>>"}
		]
	}`)

	ids := tooloutput.ParseExpandPatternsFromText(response)
	assert.Len(t, ids, 1, "should deduplicate shadow IDs")
	assert.Equal(t, "shadow_abc", ids[0])
}
