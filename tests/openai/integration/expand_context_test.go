// expand_context Integration Tests - OpenAI Real API Calls
//
// These tests make REAL calls to OpenAI API through the gateway.
// They verify the complete expand_context flow with actual LLM behavior.
//
// Requirements:
//   - OPENAI_API_KEY environment variable set in .env
//   - Network connectivity to OpenAI API
//
// Run with: go test ./tests/openai/integration/... -v -run TestExpandContext
package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/compresr/context-gateway/tests/common/fixtures"
)

const (
	expandCtxOpenaiBaseURL = "https://api.openai.com"
	expandCtxOpenaiModel   = "gpt-4o" // gpt-4o ($2.5/MTok) triggers compression; gpt-4o-mini skipped as budget model
	expandCtxTestTimeout   = 60 * time.Second
)

// TestExpandContext_OpenAI_RealAPI tests expand_context with real OpenAI API.
func TestExpandContext_OpenAI_RealAPI(t *testing.T) {
	apiKey := fixtures.GetOpenAIKey()
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration test")
	}

	cfg := fixtures.SimpleCompressionConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := fixtures.OpenAIToolResultRequest(expandCtxOpenaiModel, fixtures.LargeToolOutput)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", expandCtxOpenaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: expandCtxTestTimeout}
	resp, err := retryableRequest(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	t.Logf("Response status: %d", resp.StatusCode)
	t.Logf("Response body: %s", string(bodyBytes))

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	err = json.Unmarshal(bodyBytes, &response)
	require.NoError(t, err)

	assert.NotContains(t, string(bodyBytes), "expand_context")
	assert.NotContains(t, string(bodyBytes), "<<<SHADOW:")

	content := extractOpenAIContent(response)
	t.Logf("GPT response: %s", content)
	assert.NotEmpty(t, content)
}

// TestExpandContext_OpenAI_SmallContent tests no compression for small content.
func TestExpandContext_OpenAI_SmallContent(t *testing.T) {
	apiKey := fixtures.GetOpenAIKey()
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration test")
	}

	cfg := fixtures.SimpleCompressionConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := fixtures.OpenAIToolResultRequest(expandCtxOpenaiModel, fixtures.SmallToolOutput)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", expandCtxOpenaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: expandCtxTestTimeout}
	resp, err := retryableRequest(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractOpenAIContent(response)
	t.Logf("GPT response: %s", content)
	assert.NotEmpty(t, content)
}

// TestExpandContext_Trajectory_OpenAI verifies full trajectory for OpenAI.
func TestExpandContext_Trajectory_OpenAI(t *testing.T) {
	apiKey := fixtures.GetOpenAIKey()
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration test")
	}

	t.Log("=== EXPAND_CONTEXT TRAJECTORY TEST (OPENAI) ===")

	cfg := fixtures.SimpleCompressionConfig()
	t.Logf("Step 1: Gateway configured with simple compression (min_bytes=100, expand_context=enabled)")

	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	originalSize := len(fixtures.LargeToolOutput)
	t.Logf("Step 2: Client prepares request with large tool output (%d bytes)", originalSize)

	requestBody := fixtures.OpenAIToolResultRequest(expandCtxOpenaiModel, fixtures.LargeToolOutput)
	t.Log("Step 3: Client sends request to gateway")

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", expandCtxOpenaiBaseURL+"/v1/chat/completions")

	t.Log("Step 4: Gateway intercepts and compresses tool output")
	t.Log("Step 5: Gateway forwards compressed request to LLM")
	t.Log("Step 6: If LLM needs more detail, it calls expand_context(shadow_id)")
	t.Log("Step 7: Gateway captures expand_context, retrieves full content from store")
	t.Log("Step 8: Gateway sends FULL content (history + original) to LLM")
	t.Log("Step 9: LLM generates final response with full context")
	t.Log("Step 10: Gateway filters expand_context from response")
	t.Log("Step 11: Client receives clean final response")

	client := &http.Client{Timeout: expandCtxTestTimeout}
	resp, err := retryableRequest(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	t.Log("=== TRAJECTORY COMPLETE ===")

	assert.NotContains(t, string(bodyBytes), "expand_context")
	assert.NotContains(t, string(bodyBytes), "<<<SHADOW:")

	var response map[string]interface{}
	json.Unmarshal(bodyBytes, &response)
	content := extractOpenAIContent(response)
	t.Logf("Final response content: %s", content)
	assert.NotEmpty(t, content)
}

// Helper functions

func retryableRequest(client *http.Client, req *http.Request, t *testing.T) (*http.Response, error) {
	maxRetries := 3
	retryDelay := 2 * time.Second

	var resp *http.Response
	var err error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		var bodyBytes []byte
		if req.Body != nil {
			bodyBytes, _ = io.ReadAll(req.Body)
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		resp, err = client.Do(req)

		if err == nil && resp.StatusCode < 500 {
			return resp, nil
		}

		if err != nil {
			t.Logf("Attempt %d/%d failed with error: %v", attempt, maxRetries, err)
		} else {
			t.Logf("Attempt %d/%d failed with status %d", attempt, maxRetries, resp.StatusCode)
			if resp != nil {
				resp.Body.Close()
			}
		}

		if attempt < maxRetries {
			time.Sleep(retryDelay * time.Duration(attempt))
			if bodyBytes != nil {
				req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}
		}
	}

	return resp, err
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
