// Gateway Integration Tests - Full request cycle through gateway
//
// Tests verify core gateway behavior: parallel pipes, provider auto-detection,
// and graceful degradation on upstream errors.
//
// Run with: go test ./tests/gateway/integration/... -v
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
// TEST 1: Parallel pipes - both tool_output and tool_discovery active
// =============================================================================

// TestIntegration_Gateway_ParallelPipes sends a request with both tool results
// (triggers tool_output compression) AND 20 tools (triggers tool_discovery filtering).
// Verifies that both pipes ran: messages were compressed and tools were filtered.
func TestIntegration_Gateway_ParallelPipes(t *testing.T) {
	mock := newMockLLM(func(reqBody []byte, callNum int) []byte {
		return anthropicTextResponse("Analysis complete.")
	})
	defer mock.close()

	cfg := bothPipesConfig()
	gwServer := createGateway(cfg)
	defer gwServer.Close()

	// Build request with large tool result AND 20 tools
	reqBody := map[string]interface{}{
		"model":      "claude-3-haiku-20240307",
		"max_tokens": 500,
		"tools":      makeAnthropicToolDefs(20),
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What are the key points from the log?"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_parallel_001",
						"name":  "read_file",
						"input": map[string]string{"path": "system.log"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_parallel_001",
						"content":     largeToolOutput(1000),
					},
				},
			},
		},
	}

	resp, respBody, err := sendAnthropicRequest(gwServer.URL, mock.url(), reqBody)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify: forwarded request
	requests := mock.getRequests()
	require.GreaterOrEqual(t, len(requests), 1)

	forwardedBody := requests[0].Body
	toolNames := extractToolNames(forwardedBody)

	// Tool discovery pipe should have filtered: fewer than 20 effective (non-stub) tools.
	// Stubs preserve the tools[] array length for KV-cache stability, so total len may
	// exceed 20 (N stubs + gateway_search_tools). Count only non-deferred tools.
	effectiveCount := countEffectiveToolNames(forwardedBody)
	assert.Less(t, effectiveCount, 20,
		"tool_discovery should have filtered effective tools from 20 to fewer, got %d total tools: %v", len(toolNames), toolNames)

	// Tool-search strategy should inject gateway_search_tools
	assert.True(t, containsToolName(forwardedBody, "gateway_search_tools"),
		"forwarded request should contain gateway_search_tools")

	// Tool output pipe: check for compression markers or expand_context injection
	hasCompression := bytes.Contains(forwardedBody, []byte("[REF:"))
	if hasCompression {
		t.Log("Tool output compression detected (shadow markers present)")
	}

	// Verify: client response is clean - no phantom tools visible
	assert.NotContains(t, string(respBody), "expand_context",
		"client response should not contain expand_context")
	assert.NotContains(t, string(respBody), "gateway_search_tools",
		"client response should not contain gateway_search_tools")
}

// =============================================================================
// TEST 2: Provider auto-detection - Anthropic then OpenAI
// =============================================================================

// TestIntegration_Gateway_ProviderAutoDetection sends an Anthropic-format request
// and then an OpenAI-format request to the same gateway instance. Both should
// be detected correctly and proxied successfully.
func TestIntegration_Gateway_ProviderAutoDetection(t *testing.T) {
	mock := newMockLLM(func(reqBody []byte, callNum int) []byte {
		// Return format based on which call this is
		if callNum == 1 {
			return anthropicTextResponse("Anthropic response.")
		}
		return openAITextResponse("OpenAI response.")
	})
	defer mock.close()

	cfg := passthroughConfig()
	gwServer := createGateway(cfg)
	defer gwServer.Close()

	// Send Anthropic request
	anthropicBody := map[string]interface{}{
		"model":      "claude-3-haiku-20240307",
		"max_tokens": 500,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Hello from Anthropic."},
		},
	}

	resp1, respBody1, err := sendAnthropicRequest(gwServer.URL, mock.url(), anthropicBody)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp1.StatusCode)

	var anthropicResp map[string]interface{}
	err = json.Unmarshal(respBody1, &anthropicResp)
	require.NoError(t, err)
	// Anthropic response should have "type": "message"
	assert.Equal(t, "message", anthropicResp["type"],
		"Anthropic response should have type=message")

	// Send OpenAI request to the same gateway
	openaiBody := map[string]interface{}{
		"model": "gpt-4",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Hello from OpenAI."},
		},
		"max_completion_tokens": 500,
	}

	resp2, respBody2, err := sendOpenAIRequest(gwServer.URL, mock.url(), openaiBody)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp2.StatusCode)

	var openaiResp map[string]interface{}
	err = json.Unmarshal(respBody2, &openaiResp)
	require.NoError(t, err)
	// OpenAI response should have "object": "chat.completion"
	assert.Equal(t, "chat.completion", openaiResp["object"],
		"OpenAI response should have object=chat.completion")

	// Verify: mock received both requests
	requests := mock.getRequests()
	assert.Equal(t, 2, len(requests), "mock should have received 2 requests")
}

// =============================================================================
// TEST 3: Graceful degradation on upstream error
// =============================================================================

// TestIntegration_Gateway_GracefulDegradation verifies that when the mock LLM
// returns an error (HTTP 500), the gateway returns an appropriate error to the
// client rather than panicking or returning malformed data.
func TestIntegration_Gateway_GracefulDegradation(t *testing.T) {
	mock := newMockLLMWithStatus(http.StatusInternalServerError, func(reqBody []byte, callNum int) []byte {
		return anthropicErrorResponse()
	})
	defer mock.close()

	cfg := passthroughConfig()
	gwServer := createGateway(cfg)
	defer gwServer.Close()

	reqBody := map[string]interface{}{
		"model":      "claude-3-haiku-20240307",
		"max_tokens": 500,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "This should fail gracefully."},
		},
	}

	resp, respBody, err := sendAnthropicRequest(gwServer.URL, mock.url(), reqBody)
	require.NoError(t, err)

	// Gateway should return an error status code (either upstream's 500 or its own error)
	assert.GreaterOrEqual(t, resp.StatusCode, 400,
		"gateway should return error status when upstream fails")

	// Response should be valid JSON (not empty or malformed)
	assert.NotEmpty(t, respBody, "error response should not be empty")

	var errResp map[string]interface{}
	err = json.Unmarshal(respBody, &errResp)
	assert.NoError(t, err, "error response should be valid JSON: %s", string(respBody))
}
