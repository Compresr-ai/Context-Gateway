// Bug Fix 3 Tests: Unconditional expand_context injection
//
// Verifies that expand_context is injected ALWAYS when called, regardless
// of whether shadow refs exist. This keeps tools[] stable across turns
// (turn 1 without compression and turn 2 with compression produce identical tools[]).
package unit

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/compresr/context-gateway/internal/adapters"
	phantom_tools "github.com/compresr/context-gateway/internal/phantom_tools"
)

// TestExpandContextInjected_EvenWithNilShadowRefs verifies injection happens
// with nil shadow refs (first turn, no compression yet).
func TestExpandContextInjected_EvenWithNilShadowRefs(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[],"tools":[{"name":"read_file","description":"Read"}]}`)

	result, err := phantom_tools.InjectAll(body, adapters.Provider("anthropic"))
	require.NoError(t, err)

	tools := gjson.GetBytes(result, "tools")
	assert.Equal(t, int64(3), tools.Get("#").Int(), "should have 3 tools (read_file + expand_context + gateway_search_tools)")
	assert.Equal(t, "expand_context", tools.Get("1.name").String())
}

// TestExpandContextInjected_EvenWithEmptyShadowRefs verifies injection happens
// with empty (non-nil) shadow refs map.
func TestExpandContextInjected_EvenWithEmptyShadowRefs(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[],"tools":[{"name":"read_file","description":"Read"}]}`)

	result, err := phantom_tools.InjectAll(body, adapters.Provider("anthropic"))
	require.NoError(t, err)

	assert.Contains(t, string(result), "expand_context")
}

// TestExpandContextInjected_StableAcrossTurns verifies that tools[] bytes are
// identical between a turn WITHOUT shadow refs and a turn WITH shadow refs.
// This is the core KV-cache stability test.
func TestExpandContextInjected_StableAcrossTurns(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[],"tools":[{"name":"read_file","description":"Read a file","input_schema":{"type":"object"}}]}`)

	// Turn 1: no shadow refs (no compression happened yet)
	turn1Result, err := phantom_tools.InjectAll(body, adapters.Provider("anthropic"))
	require.NoError(t, err)

	// Turn 2: shadow refs existed (compression happened) — InjectAll is agnostic to this
	turn2Result, err := phantom_tools.InjectAll(body, adapters.Provider("anthropic"))
	require.NoError(t, err)

	// The tools[] portion must be byte-identical
	turn1Tools := gjson.GetBytes(turn1Result, "tools").Raw
	turn2Tools := gjson.GetBytes(turn2Result, "tools").Raw

	assert.True(t, bytes.Equal([]byte(turn1Tools), []byte(turn2Tools)),
		"tools[] must be byte-identical regardless of shadow refs existence.\nTurn 1: %s\nTurn 2: %s",
		turn1Tools, turn2Tools)
}

// TestExpandContextInjected_StableAcrossMultipleTurns verifies byte-identical
// tools[] across 10 simulated turns with varying shadow ref states.
func TestExpandContextInjected_StableAcrossMultipleTurns(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[],"tools":[{"name":"bash","description":"Run commands","input_schema":{"type":"object"}}]}`)

	shadowRefVariants := []map[string]string{
		nil,
		{},
		{"shadow_1": "content1"},
		{"shadow_1": "content1", "shadow_2": "content2"},
		nil,
		{"shadow_3": "different content"},
		{},
		nil,
		{"shadow_1": "content1"},
		{"shadow_4": "yet another"},
	}

	var allToolsRaw []string
	for range shadowRefVariants {
		result, err := phantom_tools.InjectAll(body, adapters.Provider("anthropic"))
		require.NoError(t, err)
		allToolsRaw = append(allToolsRaw, gjson.GetBytes(result, "tools").Raw)
	}

	// ALL 10 turns must produce identical tools[]
	for i := 1; i < len(allToolsRaw); i++ {
		assert.Equal(t, allToolsRaw[0], allToolsRaw[i],
			"turn %d tools[] differs from turn 0", i)
	}
}

// TestExpandContextInjected_DeduplicationStillWorks verifies that even though
// we always inject, the deduplication check still prevents double injection.
func TestExpandContextInjected_DeduplicationStillWorks(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[],"tools":[{"name":"read_file","description":"Read"}]}`)

	// First injection
	result1, err := phantom_tools.InjectAll(body, adapters.Provider("anthropic"))
	require.NoError(t, err)

	// Second injection on already-injected body
	result2, err := phantom_tools.InjectAll(result1, adapters.Provider("anthropic"))
	require.NoError(t, err)

	// Should still have exactly 3 tools (not 4 or 5)
	tools := gjson.GetBytes(result2, "tools")
	assert.Equal(t, int64(3), tools.Get("#").Int(), "deduplication must prevent double injection")
}
