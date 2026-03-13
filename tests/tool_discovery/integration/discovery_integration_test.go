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
// relevance strategy (min_tools=2, max_tools=5). Verifies the forwarded request
// has fewer tools than the original 20.
func TestIntegration_ToolDiscovery_FiltersByRelevance(t *testing.T) {
	mock := newMockLLM(func(reqBody []byte, callNum int) []byte {
		return anthropicTextResponse("I can help with that.")
	})
	defer mock.close()

	cfg := relevanceConfig(2, 5)
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

	// With relevance strategy, max_tools=5, the forwarded request should have
	// at most 5 tools (fewer than the original 20)
	assert.Less(t, len(toolNames), 20,
		"forwarded request should have fewer tools than original 20, got %d: %v", len(toolNames), toolNames)
	assert.LessOrEqual(t, len(toolNames), 5,
		"forwarded request should have at most max_tools=5 tools, got %d: %v", len(toolNames), toolNames)
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

	// Should have fewer tools than the original 20
	assert.Less(t, len(toolNames), 20,
		"forwarded request should have fewer tools than original 20, got %d: %v", len(toolNames), toolNames)
}

// =============================================================================
// TEST 3: Passthrough when below min_tools threshold
// =============================================================================

// TestIntegration_ToolDiscovery_PassthroughBelowThreshold sends 3 tools with
// min_tools=5. Since 3 < 5, all tools should pass through unchanged.
func TestIntegration_ToolDiscovery_PassthroughBelowThreshold(t *testing.T) {
	mock := newMockLLM(func(reqBody []byte, callNum int) []byte {
		return anthropicTextResponse("I can help with that.")
	})
	defer mock.close()

	cfg := relevanceConfig(5, 25)
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
	toolNames := extractToolNames(forwardedBody)

	// 3 tools < min_tools=5, so all should pass through unchanged
	// (no gateway_search_tools injected, all original tools preserved)
	assert.Equal(t, 3, len(toolNames),
		"all 3 tools should pass through unchanged when below min_tools threshold, got %d: %v", len(toolNames), toolNames)

	// Verify no gateway phantom tools were added
	assert.False(t, containsToolName(forwardedBody, "gateway_search_tools"),
		"gateway_search_tools should NOT be injected when below min_tools threshold")
}
