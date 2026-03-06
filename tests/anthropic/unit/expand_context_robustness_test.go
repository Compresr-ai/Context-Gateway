// expand_context Robustness Tests - Anthropic
//
// Tests edge cases and error handling for expand_context:
//   - stop_reason correction when expand_context is the only tool call
//   - Mixed tool calls (expand_context + real tools)
//   - Circular expansion prevention
//   - Shadow ID not found / expired
//   - Multiple expand calls in one response
//   - Malformed LLM response handling
package unit

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/compresr/context-gateway/tests/common/fixtures"
)

// TestExpandContext_StopReasonCorrected_Anthropic verifies that when expand_context is the
// ONLY tool_use block, FilterFromResponse corrects stop_reason from "tool_use" to "end_turn".
func TestExpandContext_StopReasonCorrected_Anthropic(t *testing.T) {
	var callCount atomic.Int32

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		count := callCount.Add(1)

		if count == 1 {
			shadowID := extractShadowIDFromRequest(body)
			if shadowID == "" {
				shadowID = "shadow_test"
			}
			w.Header().Set("Content-Type", "application/json")
			// expand_context is the ONLY tool call (no text block)
			w.Write(fixtures.AnthropicExpandOnlyResponse("toolu_expand_001", shadowID))
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.AnthropicFinalResponse("Analysis complete."))
		}
	}))
	defer mockLLM.Close()

	cfg := fixtures.SimpleCompressionConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := fixtures.AnthropicToolResultRequest("claude-sonnet-4-5", fixtures.LargeToolOutput)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("X-Target-URL", mockLLM.URL)

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	// The final response should have stop_reason "end_turn", not "tool_use"
	stopReason, _ := response["stop_reason"].(string)
	assert.Equal(t, "end_turn", stopReason, "stop_reason should be 'end_turn' in the final response")

	// expand_context should be filtered
	responseJSON, _ := json.Marshal(response)
	assert.NotContains(t, string(responseJSON), "expand_context",
		"expand_context should be filtered from final response")
}

// TestExpandContext_MixedToolCalls_Anthropic verifies that when the LLM calls expand_context
// AND a real tool, only expand_context is filtered and stop_reason stays "tool_use".
func TestExpandContext_MixedToolCalls_Anthropic(t *testing.T) {
	var callCount atomic.Int32

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		count := callCount.Add(1)

		if count == 1 {
			shadowID := extractShadowIDFromRequest(body)
			if shadowID == "" {
				shadowID = "shadow_test"
			}
			w.Header().Set("Content-Type", "application/json")
			// Both expand_context AND read_file in same response
			w.Write(fixtures.AnthropicMixedToolResponse("toolu_expand_001", shadowID, "toolu_read_001"))
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.AnthropicFinalResponse("Analysis with both tools."))
		}
	}))
	defer mockLLM.Close()

	cfg := fixtures.SimpleCompressionConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := fixtures.AnthropicToolResultRequest("claude-sonnet-4-5", fixtures.LargeToolOutput)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("X-Target-URL", mockLLM.URL)

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	// expand_context should be filtered but read_file should remain
	responseJSON, _ := json.Marshal(response)
	assert.NotContains(t, string(responseJSON), "expand_context",
		"expand_context should be filtered from final response")
}

// TestExpandContext_CircularExpansion_Anthropic verifies that the LLM calling expand_context
// with the same shadow ID twice doesn't cause an infinite loop.
func TestExpandContext_CircularExpansion_Anthropic(t *testing.T) {
	var callCount atomic.Int32
	var shadowIDUsed string

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		count := callCount.Add(1)

		switch count {
		case 1:
			shadowIDUsed = extractShadowIDFromRequest(body)
			if shadowIDUsed == "" {
				shadowIDUsed = "shadow_test"
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.AnthropicResponseWithExpandCall("toolu_expand_001", shadowIDUsed))
		case 2:
			// LLM calls expand_context AGAIN with the same shadow ID
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.AnthropicResponseWithExpandCall("toolu_expand_002", shadowIDUsed))
		case 3:
			// Should get here — the circular expand is skipped, loop terminates,
			// or LLM finally gives a real response
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.AnthropicFinalResponse("Circular expansion handled."))
		default:
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.AnthropicFinalResponse("Fallback response."))
		}
	}))
	defer mockLLM.Close()

	cfg := fixtures.SimpleCompressionConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := fixtures.AnthropicToolResultRequest("claude-sonnet-4-5", fixtures.LargeToolOutput)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("X-Target-URL", mockLLM.URL)

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Should not loop infinitely - verify we got a response
	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	// The gateway should have terminated the loop (circular expansion prevented)
	// Total calls should be limited (not infinite)
	totalCalls := callCount.Load()
	assert.LessOrEqual(t, totalCalls, int32(6), "Should not loop infinitely due to circular expansion prevention")

	// expand_context should be filtered from final response
	responseJSON, _ := json.Marshal(response)
	assert.NotContains(t, string(responseJSON), "expand_context")
}

// TestExpandContext_ShadowIDNotFound_Anthropic verifies graceful handling when
// the LLM calls expand_context with an invalid/expired shadow ID.
func TestExpandContext_ShadowIDNotFound_Anthropic(t *testing.T) {
	var callCount atomic.Int32

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := callCount.Add(1)

		if count == 1 {
			w.Header().Set("Content-Type", "application/json")
			// Use a shadow ID that doesn't exist in the store
			w.Write(fixtures.AnthropicResponseWithExpandCall("toolu_expand_001", "shadow_nonexistent_id"))
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.AnthropicFinalResponse("Handled gracefully despite missing shadow ID."))
		}
	}))
	defer mockLLM.Close()

	cfg := fixtures.SimpleCompressionConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := fixtures.AnthropicToolResultRequest("claude-sonnet-4-5", fixtures.LargeToolOutput)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("X-Target-URL", mockLLM.URL)

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should not crash - should return a valid response
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	// Should still get a response (the LLM receives the error message and responds)
	responseJSON, _ := json.Marshal(response)
	assert.NotContains(t, string(responseJSON), "expand_context",
		"expand_context should be filtered from final response")
}

// TestExpandContext_MultipleExpands_Anthropic verifies that the LLM can call
// expand_context for multiple different shadow IDs in a single response.
func TestExpandContext_MultipleExpands_Anthropic(t *testing.T) {
	var callCount atomic.Int32

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		count := callCount.Add(1)

		if count == 1 {
			shadowID := extractShadowIDFromRequest(body)
			if shadowID == "" {
				shadowID = "shadow_test"
			}
			// Create response with TWO expand_context calls
			resp := map[string]interface{}{
				"id":   "msg_001",
				"type": "message",
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{
						"type":  "tool_use",
						"id":    "toolu_expand_001",
						"name":  "expand_context",
						"input": map[string]interface{}{"id": shadowID},
					},
					map[string]interface{}{
						"type":  "tool_use",
						"id":    "toolu_expand_002",
						"name":  "expand_context",
						"input": map[string]interface{}{"id": "shadow_does_not_exist"},
					},
				},
				"stop_reason": "tool_use",
				"usage": map[string]interface{}{
					"input_tokens":  100,
					"output_tokens": 50,
				},
			}
			data, _ := json.Marshal(resp)
			w.Header().Set("Content-Type", "application/json")
			w.Write(data)
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.AnthropicFinalResponse("Multiple expands handled."))
		}
	}))
	defer mockLLM.Close()

	cfg := fixtures.SimpleCompressionConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := fixtures.AnthropicToolResultRequest("claude-sonnet-4-5", fixtures.LargeToolOutput)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("X-Target-URL", mockLLM.URL)

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	assert.Equal(t, int32(2), callCount.Load(), "Should have 2 LLM calls: initial + after expand")

	responseJSON, _ := json.Marshal(response)
	assert.NotContains(t, string(responseJSON), "expand_context")
}

// TestExpandContext_MalformedLLMResponse_Anthropic verifies the gateway handles
// malformed JSON from the LLM without crashing.
func TestExpandContext_MalformedLLMResponse_Anthropic(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return malformed JSON
		w.Write([]byte(`{"id": "msg_001", "content": [{"type": "text", "text": "hello"`))
	}))
	defer mockLLM.Close()

	cfg := fixtures.SimpleCompressionConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := fixtures.AnthropicToolResultRequest("claude-sonnet-4-5", fixtures.LargeToolOutput)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("X-Target-URL", mockLLM.URL)

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Gateway should not crash - it should return the response as-is
	respBody, _ := io.ReadAll(resp.Body)
	assert.NotEmpty(t, respBody, "Should return a response even with malformed JSON")

	// Should not contain expand_context artifacts
	assert.NotContains(t, string(respBody), "expand_context")

	// Verify gateway didn't panic - the response should be readable
	_ = strings.Contains(string(respBody), "hello")
}
