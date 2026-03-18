// expand_context Unit Tests - OpenAI
//
// Tests the complete expand_context flow for OpenAI without real API calls.
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

// TestExpandContext_OpenAI_FullFlow tests the complete expand_context flow for OpenAI.
func TestExpandContext_OpenAI_FullFlow(t *testing.T) {
	var callCount atomic.Int32
	var capturedRequests [][]byte

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedRequests = append(capturedRequests, body)
		count := callCount.Add(1)

		if count == 1 {
			shadowID := extractShadowIDFromRequest(body)
			if shadowID == "" {
				shadowID = "shadow_mock456"
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.OpenAIResponseWithExpandCall("call_expand_001", shadowID))
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(fixtures.OpenAIFinalResponse("Log analysis complete. Found: database failures, memory warnings, recovery after 45 seconds."))
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

	assert.Equal(t, int32(2), callCount.Load(), "Should have 2 LLM calls for expand flow")

	responseJSON, _ := json.Marshal(response)
	assert.NotContains(t, string(responseJSON), "expand_context")

	content := extractOpenAIContent(response)
	assert.NotEmpty(t, content)
}

// TestExpandContext_OpenAI_NoExpand tests when OpenAI doesn't request expand.
func TestExpandContext_OpenAI_NoExpand(t *testing.T) {
	var callCount atomic.Int32

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixtures.OpenAIFinalResponse("Quick summary: database and memory issues detected."))
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
	assert.Equal(t, int32(1), callCount.Load(), "Should have only 1 LLM call")
}

// Helper functions

func extractShadowIDFromRequest(body []byte) string {
	bodyStr := string(body)

	// Try direct match first
	idx := strings.Index(bodyStr, "[REF:")
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

	// Extract shadow_xxx from [REF:shadow_xxx] or SHADOW:shadow_xxx>>>
	startOffset := 10 // "[REF:" length
	if bodyStr[idx] != '<' {
		startOffset = 7 // "SHADOW:" length
	}
	shadowPart := bodyStr[idx+startOffset : idx+endIdx]
	return shadowPart
}

func extractOpenAIContent(response map[string]interface{}) string {
	choices, ok := response["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return ""
	}

	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return ""
	}

	message, ok := choice["message"].(map[string]interface{})
	if !ok {
		return ""
	}

	content, ok := message["content"].(string)
	if !ok {
		return ""
	}
	return content
}
