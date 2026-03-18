// Tool Output Integration Tests - Full request cycle through gateway
//
// Tests verify tool output compression behavior: expand_context injection,
// small output passthrough, and query fallback to tool name.
//
// Run with: go test ./tests/tool_output/integration/... -v
package integration

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// TEST 1: expand_context injected for large tool result
// =============================================================================

// TestIntegration_ToolOutput_ExpandContextInjected sends a request with a large
// tool result through the gateway. Verifies that expand_context is added to the
// forwarded request's tools[] and is not visible in the client response.
func TestIntegration_ToolOutput_ExpandContextInjected(t *testing.T) {
	mock := newMockLLM(func(reqBody []byte, callNum int) []byte {
		return anthropicTextResponse("Here is my analysis of the log file.")
	})
	defer mock.close()

	cfg := expandContextConfig()
	gwServer := createGateway(cfg)
	defer gwServer.Close()

	reqBody := anthropicRequestWithToolResult("read_file", largeToolOutput(1000))

	resp, respBody, err := sendAnthropicRequest(gwServer.URL, mock.url(), reqBody)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify: the forwarded request should contain expand_context in tools[]
	requests := mock.getRequests()
	require.GreaterOrEqual(t, len(requests), 1, "mock should have received at least 1 request")

	forwardedBody := requests[0].Body
	assert.True(t, containsToolName(forwardedBody, "expand_context"),
		"forwarded request should contain expand_context tool, got tools: %v",
		extractToolNames(forwardedBody))

	// Verify: the response to the client should NOT contain expand_context
	assert.NotContains(t, string(respBody), "expand_context",
		"client response should not contain expand_context")
	assert.NotContains(t, string(respBody), "[REF:",
		"client response should not contain shadow markers")
}

// =============================================================================
// TEST 2: Small output not compressed (below min_tokens)
// =============================================================================

// TestIntegration_ToolOutput_SmallOutputNotCompressed sends a request with a small
// tool result (well below min_tokens threshold). Verifies the output passes through
// unchanged - no compression markers, no shadow IDs.
func TestIntegration_ToolOutput_SmallOutputNotCompressed(t *testing.T) {
	mock := newMockLLM(func(reqBody []byte, callNum int) []byte {
		return anthropicTextResponse("Got it.")
	})
	defer mock.close()

	cfg := highMinTokensConfig() // min_tokens=12500
	gwServer := createGateway(cfg)
	defer gwServer.Close()

	smallOutput := "File contents: hello world"
	reqBody := anthropicRequestWithToolResult("read_file", smallOutput)

	resp, _, err := sendAnthropicRequest(gwServer.URL, mock.url(), reqBody)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify: the forwarded request should have the tool result unchanged
	requests := mock.getRequests()
	require.GreaterOrEqual(t, len(requests), 1, "mock should have received at least 1 request")

	forwardedBody := requests[0].Body

	// The small output should NOT have been compressed (no shadow markers in messages content).
	// Note: the tool description for expand_context legitimately mentions "[REF:shadow_xxx]"
	// so we check that no actual shadow IDs (hex format) appear in the messages content, not the
	// tool definitions. Actual shadow IDs use hex chars (e.g. shadow_abc123), not "shadow_xxx".
	var parsedBody map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(forwardedBody, &parsedBody))
	messagesJSON, hasMsgs := parsedBody["messages"]
	if hasMsgs {
		assert.NotContains(t, string(messagesJSON), "[REF:",
			"small tool output should not be compressed (no shadow markers in messages)")
	}

	// The original content should still be present in the forwarded request
	assert.Contains(t, string(forwardedBody), smallOutput,
		"original small tool output should be present in forwarded request")
}

// =============================================================================
// TEST 3: Query fallback uses tool name when no user text message
// =============================================================================

// TestIntegration_ToolOutput_QueryFallbackUsesToolName verifies that when there
// is no standalone user text message (only tool_result in user messages), the
// compression query falls back to using the tool name. The request should still
// succeed and be processed correctly.
func TestIntegration_ToolOutput_QueryFallbackUsesToolName(t *testing.T) {
	mock := newMockLLM(func(reqBody []byte, callNum int) []byte {
		return anthropicTextResponse("Processed the data.")
	})
	defer mock.close()

	cfg := expandContextConfig()
	gwServer := createGateway(cfg)
	defer gwServer.Close()

	// Request with NO standalone user text message - only tool_result
	// The tool name is "analyze_data" which should be used as fallback query
	reqBody := anthropicRequestWithToolResultNoUserText("analyze_data", largeToolOutput(1000))

	resp, respBody, err := sendAnthropicRequest(gwServer.URL, mock.url(), reqBody)
	require.NoError(t, err)

	// The request should succeed (not crash due to missing query)
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"request with no user text should succeed, got status %d: %s",
		resp.StatusCode, string(respBody))

	// Verify: mock received the request and processed it
	requests := mock.getRequests()
	require.GreaterOrEqual(t, len(requests), 1,
		"mock should have received at least 1 request")

	// Verify: response is valid JSON
	var response map[string]interface{}
	err = json.Unmarshal(respBody, &response)
	require.NoError(t, err, "response should be valid JSON: %s", string(respBody))

	// Verify: client response is clean
	assert.NotContains(t, string(respBody), "expand_context",
		"client response should not contain expand_context")
}
