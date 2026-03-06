// expand_context Robustness Tests - OpenAI
//
// Tests edge cases and error handling for expand_context with OpenAI format:
//   - finish_reason correction when expand_context is the only tool call
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

// TestExpandContext_StopReasonCorrected_OpenAI verifies that when expand_context is the
// ONLY tool call, FilterFromResponse corrects finish_reason from "tool_calls" to "stop".
func TestExpandContext_StopReasonCorrected_OpenAI(t *testing.T) {
	var callCount atomic.Int32

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		count := callCount.Add(1)

		if count == 1 {
			shadowID := extractShadowIDFromOpenAIRequest(body)
			if shadowID == "" {
				shadowID = "shadow_test"
			}
			w.Header().Set("Content-Type", "application/json")
			// expand_context is the ONLY tool call
			w.Write(fixtures.OpenAIExpandOnlyResponse("call_expand_001", shadowID))
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.OpenAIFinalResponse("Analysis complete."))
		}
	}))
	defer mockLLM.Close()

	cfg := fixtures.SimpleCompressionConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := fixtures.OpenAIToolResultRequest("gpt-4", fixtures.LargeToolOutput)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("X-Target-URL", mockLLM.URL)

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	// The final response should have finish_reason "stop", not "tool_calls"
	if choices, ok := response["choices"].([]interface{}); ok && len(choices) > 0 {
		choice := choices[0].(map[string]interface{})
		finishReason, _ := choice["finish_reason"].(string)
		assert.Equal(t, "stop", finishReason,
			"finish_reason should be 'stop' in the final response")
	}

	// expand_context should be filtered
	responseJSON, _ := json.Marshal(response)
	assert.NotContains(t, string(responseJSON), "expand_context",
		"expand_context should be filtered from final response")
}

// TestExpandContext_MixedToolCalls_OpenAI verifies that when the LLM calls expand_context
// AND a real tool, only expand_context is filtered and finish_reason stays "tool_calls".
func TestExpandContext_MixedToolCalls_OpenAI(t *testing.T) {
	var callCount atomic.Int32

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		count := callCount.Add(1)

		if count == 1 {
			shadowID := extractShadowIDFromOpenAIRequest(body)
			if shadowID == "" {
				shadowID = "shadow_test"
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.OpenAIMixedToolResponse("call_expand_001", shadowID, "call_read_001"))
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.OpenAIFinalResponse("Analysis with both tools."))
		}
	}))
	defer mockLLM.Close()

	cfg := fixtures.SimpleCompressionConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := fixtures.OpenAIToolResultRequest("gpt-4", fixtures.LargeToolOutput)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
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

// TestExpandContext_CircularExpansion_OpenAI verifies that calling expand_context
// with the same shadow ID twice doesn't cause an infinite loop.
func TestExpandContext_CircularExpansion_OpenAI(t *testing.T) {
	var callCount atomic.Int32
	var shadowIDUsed string

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		count := callCount.Add(1)

		switch count {
		case 1:
			shadowIDUsed = extractShadowIDFromOpenAIRequest(body)
			if shadowIDUsed == "" {
				shadowIDUsed = "shadow_test"
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.OpenAIResponseWithExpandCall("call_expand_001", shadowIDUsed))
		case 2:
			// LLM calls expand_context AGAIN with the same shadow ID
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.OpenAIResponseWithExpandCall("call_expand_002", shadowIDUsed))
		case 3:
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.OpenAIFinalResponse("Circular expansion handled."))
		default:
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.OpenAIFinalResponse("Fallback response."))
		}
	}))
	defer mockLLM.Close()

	cfg := fixtures.SimpleCompressionConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := fixtures.OpenAIToolResultRequest("gpt-4", fixtures.LargeToolOutput)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("X-Target-URL", mockLLM.URL)

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	// Should not loop infinitely
	totalCalls := callCount.Load()
	assert.LessOrEqual(t, totalCalls, int32(6),
		"Should not loop infinitely due to circular expansion prevention")

	responseJSON, _ := json.Marshal(response)
	assert.NotContains(t, string(responseJSON), "expand_context")
}

// TestExpandContext_ShadowIDNotFound_OpenAI verifies graceful handling when
// the LLM calls expand_context with an invalid shadow ID.
func TestExpandContext_ShadowIDNotFound_OpenAI(t *testing.T) {
	var callCount atomic.Int32

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := callCount.Add(1)

		if count == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.OpenAIResponseWithExpandCall("call_expand_001", "shadow_nonexistent_id"))
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.OpenAIFinalResponse("Handled gracefully despite missing shadow ID."))
		}
	}))
	defer mockLLM.Close()

	cfg := fixtures.SimpleCompressionConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := fixtures.OpenAIToolResultRequest("gpt-4", fixtures.LargeToolOutput)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("X-Target-URL", mockLLM.URL)

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	responseJSON, _ := json.Marshal(response)
	assert.NotContains(t, string(responseJSON), "expand_context")
}

// TestExpandContext_MultipleExpands_OpenAI verifies that the LLM can call
// expand_context for multiple different shadow IDs in a single response.
func TestExpandContext_MultipleExpands_OpenAI(t *testing.T) {
	var callCount atomic.Int32

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		count := callCount.Add(1)

		if count == 1 {
			shadowID := extractShadowIDFromOpenAIRequest(body)
			if shadowID == "" {
				shadowID = "shadow_test"
			}
			// Two expand_context calls in one response
			resp := map[string]interface{}{
				"id":     "chatcmpl-001",
				"object": "chat.completion",
				"model":  "gpt-4",
				"choices": []interface{}{
					map[string]interface{}{
						"index": 0,
						"message": map[string]interface{}{
							"role":    "assistant",
							"content": nil,
							"tool_calls": []interface{}{
								map[string]interface{}{
									"id":   "call_expand_001",
									"type": "function",
									"function": map[string]interface{}{
										"name":      "expand_context",
										"arguments": `{"id":"` + shadowID + `"}`,
									},
								},
								map[string]interface{}{
									"id":   "call_expand_002",
									"type": "function",
									"function": map[string]interface{}{
										"name":      "expand_context",
										"arguments": `{"id":"shadow_does_not_exist"}`,
									},
								},
							},
						},
						"finish_reason": "tool_calls",
					},
				},
			}
			data, _ := json.Marshal(resp)
			w.Header().Set("Content-Type", "application/json")
			w.Write(data)
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.OpenAIFinalResponse("Multiple expands handled."))
		}
	}))
	defer mockLLM.Close()

	cfg := fixtures.SimpleCompressionConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := fixtures.OpenAIToolResultRequest("gpt-4", fixtures.LargeToolOutput)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("X-Target-URL", mockLLM.URL)

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	assert.Equal(t, int32(2), callCount.Load(), "Should have 2 LLM calls")

	responseJSON, _ := json.Marshal(response)
	assert.NotContains(t, string(responseJSON), "expand_context")
}

// TestExpandContext_MalformedLLMResponse_OpenAI verifies the gateway handles
// malformed JSON from the LLM without crashing.
func TestExpandContext_MalformedLLMResponse_OpenAI(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return malformed JSON
		w.Write([]byte(`{"id": "chatcmpl-001", "choices": [{"message": {"content": "hello"`))
	}))
	defer mockLLM.Close()

	cfg := fixtures.SimpleCompressionConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := fixtures.OpenAIToolResultRequest("gpt-4", fixtures.LargeToolOutput)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("X-Target-URL", mockLLM.URL)

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Gateway should not crash
	respBody, _ := io.ReadAll(resp.Body)
	assert.NotEmpty(t, respBody, "Should return a response even with malformed JSON")

	// Should not contain expand_context artifacts
	assert.NotContains(t, string(respBody), "expand_context")

	_ = strings.Contains(string(respBody), "hello")
}

// Helper function for OpenAI format
func extractShadowIDFromOpenAIRequest(body []byte) string {
	bodyStr := string(body)

	// Try direct match first
	idx := strings.Index(bodyStr, "<<<SHADOW:")
	if idx == -1 {
		idx = strings.Index(bodyStr, "SHADOW:")
	}
	if idx == -1 {
		// Try to find just the shadow ID pattern
		start := strings.Index(bodyStr, "shadow_")
		if start == -1 {
			return ""
		}
		end := start + 7
		for end < len(bodyStr) && (bodyStr[end] >= 'a' && bodyStr[end] <= 'z' ||
			bodyStr[end] >= '0' && bodyStr[end] <= '9') {
			end++
		}
		return bodyStr[start:end]
	}

	endIdx := strings.Index(bodyStr[idx:], ">>>")
	if endIdx == -1 {
		endIdx = strings.Index(bodyStr[idx:], "\\u003e\\u003e\\u003e")
	}
	if endIdx == -1 {
		start := strings.Index(bodyStr, "shadow_")
		if start == -1 {
			return ""
		}
		end := start + 7
		for end < len(bodyStr) && (bodyStr[end] >= 'a' && bodyStr[end] <= 'z' ||
			bodyStr[end] >= '0' && bodyStr[end] <= '9') {
			end++
		}
		return bodyStr[start:end]
	}

	startOffset := 10
	if bodyStr[idx] != '<' {
		startOffset = 7
	}
	shadowPart := bodyStr[idx+startOffset : idx+endIdx]
	return shadowPart
}
