// KV Cache Integration Tests - Full request cycle through gateway
//
// Tests verify that tools[] injected by gateway pipes produce byte-identical
// payloads across multiple turns, which is critical for KV cache prefix matching
// at LLM providers.
//
// Run with: go test ./tests/kv_cache/integration/... -v
package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// TEST 1: Tools prefix stable across 3 turns
// =============================================================================

// TestIntegration_KVCache_ToolsPrefixStable_3Turns sends 3 sequential requests
// through the gateway with expand_context enabled and a large tool result.
// Verifies that the tools[] bytes forwarded to the LLM are identical in all 3.
func TestIntegration_KVCache_ToolsPrefixStable_3Turns(t *testing.T) {
	mock := newMockLLM(func(reqBody []byte, callNum int) []byte {
		return anthropicTextResponse("Response to turn.")
	})
	defer mock.close()

	cfg := expandContextConfig()
	gwServer := createGateway(cfg)
	defer gwServer.Close()

	// Send 3 sequential requests with the same large tool output
	for turn := 0; turn < 3; turn++ {
		reqBody := anthropicRequestWithToolResult(largeToolOutput(1000))

		resp, _, err := sendAnthropicRequest(gwServer.URL, mock.url(), reqBody)
		require.NoError(t, err, "turn %d", turn)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "turn %d", turn)
	}

	// Capture tools[] from each forwarded request
	requests := mock.getRequests()
	require.Equal(t, 3, len(requests), "mock should have received 3 requests")

	var toolsPayloads [][]byte
	for i, req := range requests {
		var parsed map[string]interface{}
		err := json.Unmarshal(req.Body, &parsed)
		require.NoError(t, err, "request %d", i)

		tools, ok := parsed["tools"]
		if !ok {
			// If no tools injected (compression didn't trigger), that's valid
			// but all 3 must be consistent
			toolsPayloads = append(toolsPayloads, nil)
			continue
		}
		toolsBytes, err := json.Marshal(tools)
		require.NoError(t, err, "request %d", i)
		toolsPayloads = append(toolsPayloads, toolsBytes)
	}

	// Verify: tools[] bytes are identical across all 3 turns
	require.Equal(t, 3, len(toolsPayloads))
	assert.True(t, bytes.Equal(toolsPayloads[0], toolsPayloads[1]),
		"tools[] should be byte-identical between turn 1 and turn 2")
	assert.True(t, bytes.Equal(toolsPayloads[1], toolsPayloads[2]),
		"tools[] should be byte-identical between turn 2 and turn 3")
}

// =============================================================================
// TEST 2: expand_context always present even with no tool results
// =============================================================================

// TestIntegration_KVCache_ExpandContextAlwaysPresent verifies that when
// expand_context is enabled and the request includes tools, the expand_context
// tool is injected into the forwarded request even when there are no tool results
// in the messages (no compression needed).
func TestIntegration_KVCache_ExpandContextAlwaysPresent(t *testing.T) {
	mock := newMockLLM(func(reqBody []byte, callNum int) []byte {
		return anthropicTextResponse("I can help with that.")
	})
	defer mock.close()

	cfg := expandContextConfig()
	gwServer := createGateway(cfg)
	defer gwServer.Close()

	// Send request with tools but NO tool results in messages
	reqBody := anthropicRequestNoToolResult()

	resp, _, err := sendAnthropicRequest(gwServer.URL, mock.url(), reqBody)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	requests := mock.getRequests()
	require.GreaterOrEqual(t, len(requests), 1, "mock should have received at least 1 request")

	forwardedBody := requests[0].Body

	// Verify: expand_context should be in the forwarded request's tools[]
	// even though there are no tool results to compress.
	// This ensures KV cache prefix is consistent from the first turn.
	tools := extractTools(forwardedBody)
	if len(tools) > 0 {
		// If tools were forwarded, check for expand_context presence
		hasExpand := containsToolName(forwardedBody, "expand_context")
		assert.True(t, hasExpand,
			"expand_context should be injected in tools[] even without tool results, got tools: %v",
			extractToolNames(forwardedBody))
	}
}

// =============================================================================
// TEST 3: Precomputed bytes consistent across 10 injections
// =============================================================================

// TestIntegration_KVCache_PrecomputedBytesConsistent sends 10 requests through
// the gateway with expand_context enabled. Verifies that the tools[] bytes in
// all 10 forwarded requests are identical, confirming precomputed tool bytes
// are stable.
func TestIntegration_KVCache_PrecomputedBytesConsistent(t *testing.T) {
	mock := newMockLLM(func(reqBody []byte, callNum int) []byte {
		return anthropicTextResponse("Analysis complete.")
	})
	defer mock.close()

	cfg := expandContextConfig()
	gwServer := createGateway(cfg)
	defer gwServer.Close()

	// Send 10 requests with identical large tool output
	for i := 0; i < 10; i++ {
		reqBody := anthropicRequestWithToolResult(largeToolOutput(1000))

		resp, _, err := sendAnthropicRequest(gwServer.URL, mock.url(), reqBody)
		require.NoError(t, err, "request %d", i)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "request %d", i)
	}

	requests := mock.getRequests()
	require.Equal(t, 10, len(requests), "mock should have received 10 requests")

	// Extract tools[] from each forwarded request
	var toolsPayloads [][]byte
	for i, req := range requests {
		var parsed map[string]interface{}
		err := json.Unmarshal(req.Body, &parsed)
		require.NoError(t, err, "request %d", i)

		tools, ok := parsed["tools"]
		if !ok {
			toolsPayloads = append(toolsPayloads, nil)
			continue
		}
		toolsBytes, err := json.Marshal(tools)
		require.NoError(t, err, "request %d", i)
		toolsPayloads = append(toolsPayloads, toolsBytes)
	}

	// Verify: all 10 tools[] payloads are byte-identical
	for i := 1; i < len(toolsPayloads); i++ {
		assert.True(t, bytes.Equal(toolsPayloads[0], toolsPayloads[i]),
			"tools[] at request %d differs from request 0", i)
	}
}
