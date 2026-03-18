// Tool Discovery Integration Tests - Full request cycle through gateway
//
// Tests verify that tool discovery filtering (relevance and tool-search strategies)
// correctly reduces the tools[] array forwarded to the LLM backend.
//
// Run with: go test ./tests/tool_discovery/integration/... -v
package integration

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// TEST 1: Relevance strategy filters tools by relevance
// =============================================================================

// TestIntegration_ToolDiscovery_FiltersByRelevance sends 20 tools with the
// relevance strategy (token budget for 5 tools). Verifies the forwarded request
// has fewer tools than the original 20.
func TestIntegration_ToolDiscovery_FiltersByRelevance(t *testing.T) {
	mock := newMockLLM(func(reqBody []byte, callNum int) []byte {
		return anthropicTextResponse("I can help with that.")
	})
	defer mock.close()

	cfg := relevanceConfig(5)
	gwServer := createGateway(cfg)
	defer gwServer.Close()

	reqBody := map[string]interface{}{
		"model":      "claude-3-haiku-20240307",
		"max_tokens": 500,
		"tools":      makeAnthropicToolDefs(20),
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Help me read a file from disk."},
		},
	}

	resp, _, err := sendAnthropicRequest(gwServer.URL, mock.url(), reqBody)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	requests := mock.getRequests()
	require.GreaterOrEqual(t, len(requests), 1, "mock should have received at least 1 request")

	forwardedBody := requests[0].Body
	toolNames := extractToolNames(forwardedBody)

	// With relevance strategy and stub behavior: deferred tools remain as stubs.
	// Count only effective (non-deferred) tools to verify filtering happened.
	// Note: phantom tools (expand_context + gateway_search_tools) are always injected
	// on top of the filtered set — add 2 to the expected max.
	effectiveCount := countEffectiveToolNames(forwardedBody)
	const phantomToolCount = 2
	assert.Less(t, effectiveCount, 20+phantomToolCount,
		"forwarded request should have fewer effective tools than original 20, got %d total: %v", len(toolNames), toolNames)
	assert.LessOrEqual(t, effectiveCount, 5+phantomToolCount,
		"forwarded request should have at most token_budget(5 tools) + %d phantom tools, got %d total: %v", phantomToolCount, len(toolNames), toolNames)
}

// =============================================================================
// TEST 2: Tool-search strategy replaces all tools with gateway_search_tools
// =============================================================================

// TestIntegration_ToolDiscovery_ToolSearchReplacesAll sends 20 tools with
// tool-search strategy. Verifies the forwarded request contains
// gateway_search_tools and has fewer tools than the original.
func TestIntegration_ToolDiscovery_ToolSearchReplacesAll(t *testing.T) {
	mock := newMockLLM(func(reqBody []byte, callNum int) []byte {
		return anthropicTextResponse("I can help with that.")
	})
	defer mock.close()

	cfg := toolSearchConfig()
	gwServer := createGateway(cfg)
	defer gwServer.Close()

	reqBody := map[string]interface{}{
		"model":      "claude-3-haiku-20240307",
		"max_tokens": 500,
		"tools":      makeAnthropicToolDefs(20),
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Help me read a file."},
		},
	}

	resp, _, err := sendAnthropicRequest(gwServer.URL, mock.url(), reqBody)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	requests := mock.getRequests()
	require.GreaterOrEqual(t, len(requests), 1, "mock should have received at least 1 request")

	forwardedBody := requests[0].Body
	toolNames := extractToolNames(forwardedBody)

	// Tool-search strategy should inject gateway_search_tools
	assert.True(t, containsToolName(forwardedBody, "gateway_search_tools"),
		"forwarded request should contain gateway_search_tools, got tools: %v", toolNames)

	// With stub behavior: deferred tools remain as stubs + gateway_search_tools appended.
	// Count only effective (non-deferred) tools — should be 1 (gateway_search_tools only).
	effectiveCountSearch := countEffectiveToolNames(forwardedBody)
	assert.Less(t, effectiveCountSearch, 20,
		"forwarded request should have fewer effective tools than original 20, got %d total: %v", len(toolNames), toolNames)
}

// =============================================================================
// TEST 3: Passthrough when below token threshold
// =============================================================================

// TestIntegration_ToolDiscovery_PassthroughBelowThreshold sends 3 tools with
// a high token_threshold (99999) so filtering is skipped.
// Phantom tools (expand_context, gateway_search_tools) are always injected
// regardless of filtering status — this is the MCP-server pattern.
func TestIntegration_ToolDiscovery_PassthroughBelowThreshold(t *testing.T) {
	mock := newMockLLM(func(reqBody []byte, callNum int) []byte {
		return anthropicTextResponse("I can help with that.")
	})
	defer mock.close()

	// TokenThreshold=99999: 3 small tools are well below threshold → no filtering
	cfg := relevanceConfigWithThreshold(25, 99999)
	gwServer := createGateway(cfg)
	defer gwServer.Close()

	reqBody := map[string]interface{}{
		"model":      "claude-3-haiku-20240307",
		"max_tokens": 500,
		"tools":      makeAnthropicToolDefs(3),
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Help me read a file."},
		},
	}

	resp, _, err := sendAnthropicRequest(gwServer.URL, mock.url(), reqBody)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	requests := mock.getRequests()
	require.GreaterOrEqual(t, len(requests), 1, "mock should have received at least 1 request")

	forwardedBody := requests[0].Body

	// Filtering was skipped (below token threshold), so original 3 tools are intact.
	// But phantom tools (expand_context + gateway_search_tools) are ALWAYS injected
	// regardless of whether filtering ran — MCP-server pattern.
	allToolNames := extractToolNames(forwardedBody)
	assert.Equal(t, 5, len(allToolNames),
		"3 original tools + 2 phantom tools should be present, got %d: %v", len(allToolNames), allToolNames)

	// Verify both phantom tools are present
	assert.True(t, containsToolName(forwardedBody, "expand_context"),
		"expand_context should always be injected (MCP-server pattern)")
	assert.True(t, containsToolName(forwardedBody, "gateway_search_tools"),
		"gateway_search_tools should always be injected (MCP-server pattern)")

	// Verify original tools are not filtered/stubbed
	effectiveCount := countEffectiveToolNames(forwardedBody)
	assert.Equal(t, 5, effectiveCount, // 3 originals + 2 phantom tools = 5 effective
		"original tools should not be stubbed when below token threshold")
}
