// expand_context Unit Tests - Anthropic
//
// Tests the complete expand_context flow for Anthropic without real API calls.
// Uses mock HTTP servers to simulate LLM responses.
//
// Flow being tested:
//  1. Client sends request with tool_result
//  2. Proxy compresses tool output (simple strategy: first N words)
//  3. Forward to LLM
//  4. LLM detects truncation, calls expand_context(shadow_id)
//  5. Proxy intercepts, retrieves full content from store
//  6. Proxy sends full content (original + history) to LLM
//  7. LLM responds with final answer
//  8. Proxy filters expand_context from response
//  9. Client receives clean final response
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

// TestExpandContext_Anthropic_FullFlow tests the complete expand_context flow for Anthropic.
func TestExpandContext_Anthropic_FullFlow(t *testing.T) {
	var callCount atomic.Int32
	var capturedRequests [][]byte

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedRequests = append(capturedRequests, body)
		count := callCount.Add(1)

		if count == 1 {
			// Extract the REAL shadow ID from the compressed request
			shadowID := extractShadowIDFromRequest(body)
			t.Logf("Extracted shadow ID: %s", shadowID)
			if shadowID == "" {
				// Should not happen if compression worked
				t.Log("Warning: No shadow ID found in request")
				shadowID = "shadow_fallback"
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.AnthropicResponseWithExpandCall("toolu_expand_001", shadowID))
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.AnthropicFinalResponse("Based on the complete log analysis: database issues, memory warnings, recovery successful."))
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

	// First request should have compressed (truncated) content with shadow reference
	if len(capturedRequests) >= 1 {
		firstReq := string(capturedRequests[0])
		// Note: JSON encoding escapes < as \u003c, so check for both
		hasShadow := strings.Contains(firstReq, "<<<SHADOW:") || strings.Contains(firstReq, "SHADOW:")
		assert.True(t, hasShadow, "First request should have shadow reference from compression")
		assert.NotContains(t, firstReq, "Rate limit exceeded", "First request should have truncated content")
	}

	// Second request should have the expanded content
	if len(capturedRequests) >= 2 {
		secondReq := string(capturedRequests[1])
		// The second request contains the full content in the tool_result for expand_context
		assert.Contains(t, secondReq, "toolu_expand_001", "Second request should reference the expand tool call")
	}

	responseJSON, _ := json.Marshal(response)
	assert.NotContains(t, string(responseJSON), "expand_context", "expand_context should be filtered from response")

	content := extractAnthropicContent(response)
	assert.NotEmpty(t, content, "Should have response content")
}

// TestExpandContext_Anthropic_NoExpand tests when LLM doesn't need full content.
func TestExpandContext_Anthropic_NoExpand(t *testing.T) {
	var callCount atomic.Int32

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixtures.AnthropicFinalResponse("The log shows database and memory issues."))
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
	assert.Equal(t, int32(1), callCount.Load(), "Should have only 1 LLM call when no expand needed")
}

// TestExpandContext_Disabled_Anthropic verifies expand_context is skipped when disabled.
func TestExpandContext_Disabled_Anthropic(t *testing.T) {
	var callCount atomic.Int32

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := callCount.Add(1)
		if count == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.AnthropicResponseWithExpandCall("toolu_expand_001", "shadow_abc"))
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.AnthropicFinalResponse("This should not happen"))
		}
	}))
	defer mockLLM.Close()

	cfg := fixtures.SimpleCompressionConfigNoExpand()
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
	assert.Equal(t, int32(1), callCount.Load(), "Should have only 1 LLM call when expand disabled")
}

// TestExpandContext_SmallContent_NoCompression verifies small content is not compressed.
func TestExpandContext_SmallContent_NoCompression(t *testing.T) {
	var capturedRequest []byte

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedRequest, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixtures.AnthropicFinalResponse("The status is OK with count 42."))
	}))
	defer mockLLM.Close()

	cfg := fixtures.SimpleCompressionConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := fixtures.AnthropicToolResultRequest("claude-sonnet-4-5", fixtures.SmallToolOutput)

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

	// Content should NOT have shadow reference (not compressed)
	assert.NotContains(t, string(capturedRequest), "<<<SHADOW:",
		"Small content should NOT be compressed (no shadow reference)")
	// Content should have the actual data (JSON-escaped is fine)
	assert.Contains(t, string(capturedRequest), "status",
		"Small content should be passed through without compression")
	assert.Contains(t, string(capturedRequest), "ok",
		"Small content should be passed through")
}

// TestExpandContext_TrajectoryCorrect verifies the full trajectory is correct.
func TestExpandContext_TrajectoryCorrect(t *testing.T) {
	var step atomic.Int32
	var trajectory []string

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		currentStep := step.Add(1)

		switch currentStep {
		case 1:
			trajectory = append(trajectory, "LLM receives compressed content")
			if len(body) > 0 && !strings.Contains(string(body), "Rate limit exceeded") {
				trajectory = append(trajectory, "Content is truncated (first N words only)")
			}
			shadowID := extractShadowIDFromRequest(body)
			if shadowID == "" {
				shadowID = "shadow_test"
			}
			trajectory = append(trajectory, "LLM calls expand_context")
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.AnthropicResponseWithExpandCall("toolu_001", shadowID))

		case 2:
			trajectory = append(trajectory, "Proxy sends full content for expansion")
			if strings.Contains(string(body), "Rate limit exceeded") {
				trajectory = append(trajectory, "Full content received by LLM")
			}
			trajectory = append(trajectory, "LLM returns final answer")
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.AnthropicFinalResponse("Complete analysis: database issues, memory warnings, successful recovery."))
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

	trajectory = append(trajectory, "Client sends request")

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	trajectory = append(trajectory, "Client receives response")

	t.Logf("Trajectory: %v", trajectory)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, trajectory, "Client sends request")
	assert.Contains(t, trajectory, "LLM receives compressed content")
	assert.Contains(t, trajectory, "LLM calls expand_context")
	assert.Contains(t, trajectory, "Proxy sends full content for expansion")
	assert.Contains(t, trajectory, "LLM returns final answer")
	assert.Contains(t, trajectory, "Client receives response")

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)
	responseJSON, _ := json.Marshal(response)
	assert.NotContains(t, string(responseJSON), "expand_context")
	assert.NotContains(t, string(responseJSON), "shadow_")
}

// Helper functions

func extractShadowIDFromRequest(body []byte) string {
	bodyStr := string(body)

	// Try direct match first
	idx := strings.Index(bodyStr, "<<<SHADOW:")
	if idx == -1 {
		// Try JSON-escaped unicode version (\u003c = <)
		idx = strings.Index(bodyStr, "SHADOW:")
	}
	if idx == -1 {
		return ""
	}

	// Find the end (>>> or \u003e\u003e\u003e)
	endIdx := strings.Index(bodyStr[idx:], ">>>")
	if endIdx == -1 {
		endIdx = strings.Index(bodyStr[idx:], "\\u003e\\u003e\\u003e")
	}
	if endIdx == -1 {
		// Try to find just the shadow ID pattern
		start := strings.Index(bodyStr, "shadow_")
		if start == -1 {
			return ""
		}
		// Find end of shadow ID (alphanumeric)
		end := start + 7
		for end < len(bodyStr) && (bodyStr[end] >= 'a' && bodyStr[end] <= 'z' ||
			bodyStr[end] >= '0' && bodyStr[end] <= '9') {
			end++
		}
		return bodyStr[start:end]
	}

	// Extract shadow_xxx from <<<SHADOW:shadow_xxx>>> or SHADOW:shadow_xxx>>>
	startOffset := 10 // "<<<SHADOW:" length
	if bodyStr[idx] != '<' {
		startOffset = 7 // "SHADOW:" length
	}
	shadowPart := bodyStr[idx+startOffset : idx+endIdx]
	return shadowPart
}

func extractAnthropicContent(response map[string]interface{}) string {
	content, ok := response["content"].([]interface{})
	if !ok || len(content) == 0 {
		return ""
	}

	var result strings.Builder
	for _, block := range content {
		if blockMap, ok := block.(map[string]interface{}); ok {
			if blockMap["type"] == "text" {
				if text, ok := blockMap["text"].(string); ok {
					result.WriteString(text)
				}
			}
		}
	}
	return result.String()
}
