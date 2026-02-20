// Gateway Unit Tests - HTTP Server Testing with Mocks
//
// These tests spawn HTTP servers and make HTTP requests through the gateway
// using MOCK upstream LLM servers (not real API calls).
//
// Test flow:
//  1. Start mock upstream LLM server (mimics Anthropic/OpenAI API)
//  2. Start the actual Gateway HTTP server
//  3. Make HTTP requests to Gateway with X-Target-URL pointing to mock
//  4. Verify Gateway correctly proxies, compresses, expands
//
// For real E2E tests with actual LLM APIs, see integration/real_e2e_test.go
package unit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/compresr/context-gateway/internal/monitoring"
)

// =============================================================================
// TEST: Gateway HTTP Server - Passthrough Mode
// =============================================================================

// TestGateway_Passthrough_ForwardsRequestUnchanged tests that in passthrough mode,
// the gateway forwards requests to the upstream LLM without any modifications.
func TestGateway_Passthrough_ForwardsRequestUnchanged(t *testing.T) {
	// 1. Create mock upstream LLM (mimics Anthropic API)
	var receivedBody []byte
	var receivedHeaders http.Header
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		receivedHeaders = r.Header.Clone()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":    "msg_123",
			"type":  "message",
			"role":  "assistant",
			"model": "claude-3-sonnet",
			"content": []map[string]interface{}{
				{"type": "text", "text": "Hello! How can I help you today?"},
			},
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 10, "output_tokens": 15},
		})
	}))
	defer mockLLM.Close()

	// 2. Create and start the Gateway
	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// 3. Make request through Gateway
	requestBody := `{
		"model": "claude-3-sonnet",
		"max_tokens": 100,
		"messages": [
			{"role": "user", "content": "Hello!"}
		]
	}`

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", strings.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-api-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("X-Target-URL", mockLLM.URL+"/v1/messages")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// 4. Verify response
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	assert.Equal(t, "msg_123", response["id"])
	assert.Equal(t, "assistant", response["role"])

	// 5. Verify upstream received the request
	var upstreamReq map[string]interface{}
	err = json.Unmarshal(receivedBody, &upstreamReq)
	require.NoError(t, err)

	assert.Equal(t, "claude-3-sonnet", upstreamReq["model"])
	assert.Equal(t, float64(100), upstreamReq["max_tokens"])
	assert.NotEmpty(t, receivedHeaders.Get("X-Api-Key"))
}

func TestGateway_SubscriptionFallback_RetriesWithAPIKey(t *testing.T) {
	var callCount int32
	authByCall := make(map[int]string)
	xAPIByCall := make(map[int]string)

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := int(atomic.AddInt32(&callCount, 1))
		authByCall[call] = r.Header.Get("Authorization")
		xAPIByCall[call] = r.Header.Get("x-api-key")

		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"subscription rate limit exceeded"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_retry_ok","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}]}`))
	}))
	defer mockLLM.Close()

	cfg := passthroughConfig()
	cfg.Providers = config.ProvidersConfig{
		"anthropic": {
			APIKey: "sk-ant-api03-fallback-key",
			Model:  "claude-3-5-sonnet-latest",
		},
	}

	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := `{
		"model":"claude-3-5-sonnet-latest",
		"max_tokens":32,
		"messages":[{"role":"user","content":"hello"}]
	}`
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", strings.NewReader(requestBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-subscription-token")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("X-Target-URL", mockLLM.URL+"/v1/messages")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int32(2), atomic.LoadInt32(&callCount), "should retry once with api key")
	assert.NotEmpty(t, authByCall[1], "first call should use subscription bearer token")
	assert.Empty(t, xAPIByCall[1], "first call should not use x-api-key")
	assert.Empty(t, authByCall[2], "retry should drop Authorization header")
	assert.Equal(t, "sk-ant-api03-fallback-key", xAPIByCall[2], "retry should use configured fallback api key")
}

func TestGateway_SubscriptionFallback_StickyPerSession(t *testing.T) {
	var callCount int32
	authByCall := make(map[int]string)
	xAPIByCall := make(map[int]string)

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := int(atomic.AddInt32(&callCount, 1))
		authByCall[call] = r.Header.Get("Authorization")
		xAPIByCall[call] = r.Header.Get("x-api-key")

		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"subscription quota exceeded"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_ok","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}]}`))
	}))
	defer mockLLM.Close()

	cfg := passthroughConfig()
	cfg.Providers = config.ProvidersConfig{
		"anthropic": {
			APIKey: "sk-ant-api03-fallback-key",
			Model:  "claude-3-5-sonnet-latest",
		},
	}

	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := `{
		"model":"claude-3-5-sonnet-latest",
		"max_tokens":32,
		"messages":[{"role":"user","content":"same session message"}]
	}`

	send := func() *http.Response {
		req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", strings.NewReader(requestBody))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk-ant-oat01-subscription-token")
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("X-Target-URL", mockLLM.URL+"/v1/messages")
		resp, doErr := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		require.NoError(t, doErr)
		return resp
	}

	resp1 := send()
	assert.Equal(t, http.StatusOK, resp1.StatusCode)
	resp1.Body.Close()

	resp2 := send()
	assert.Equal(t, http.StatusOK, resp2.StatusCode)
	resp2.Body.Close()

	assert.Equal(t, int32(3), atomic.LoadInt32(&callCount), "first request should retry; second should directly use api key")
	assert.NotEmpty(t, authByCall[1])
	assert.Empty(t, xAPIByCall[1])
	assert.Empty(t, authByCall[2])
	assert.Equal(t, "sk-ant-api03-fallback-key", xAPIByCall[2])
	assert.Empty(t, authByCall[3], "session should remain in api key mode")
	assert.Equal(t, "sk-ant-api03-fallback-key", xAPIByCall[3])
}

func TestGateway_AuthFallback_TelemetryAndInitLogs(t *testing.T) {
	var callCount int32
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"subscription quota exceeded"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_ok","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}]}`))
	}))
	defer mockLLM.Close()

	tempDir := t.TempDir()
	telemetryPath := filepath.Join(tempDir, "telemetry.jsonl")

	cfg := passthroughConfig()
	cfg.Monitoring.TelemetryEnabled = true
	cfg.Monitoring.TelemetryPath = telemetryPath
	cfg.Providers = config.ProvidersConfig{
		"anthropic": {
			APIKey: "sk-ant-api03-fallback-key",
			Model:  "claude-3-5-sonnet-latest",
		},
	}
	cfg.AgentFlags = config.NewAgentFlags("claude_code", []string{"--dangerously-skip-permissions"})

	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := `{
		"model":"claude-3-5-sonnet-latest",
		"max_tokens":32,
		"messages":[{"role":"user","content":"telemetry auth test"}]
	}`
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", strings.NewReader(requestBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-subscription-token")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("X-Target-URL", mockLLM.URL+"/v1/messages")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	telemetryBytes, err := os.ReadFile(telemetryPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(telemetryBytes)), "\n")
	require.NotEmpty(t, lines)

	var reqEvent monitoring.RequestEvent
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &reqEvent))
	assert.Equal(t, "subscription", reqEvent.AuthModeInitial)
	assert.Equal(t, "api_key", reqEvent.AuthModeEffective)
	assert.True(t, reqEvent.AuthFallbackUsed)

	initPath := filepath.Join(tempDir, "init.jsonl")
	initBytes, err := os.ReadFile(initPath)
	require.NoError(t, err)
	initLines := strings.Split(strings.TrimSpace(string(initBytes)), "\n")
	require.NotEmpty(t, initLines)

	var initEvent monitoring.InitEvent
	require.NoError(t, json.Unmarshal([]byte(initLines[len(initLines)-1]), &initEvent))
	assert.Equal(t, "gateway_init", initEvent.Event)
	assert.Equal(t, "claude_code", initEvent.AgentName)
	assert.True(t, initEvent.AutoApproveMode)
}

// =============================================================================
// TEST: Gateway HTTP Server - Tool Output Compression
// =============================================================================

// TestGateway_Compression_CompressesLargeToolOutput tests that the gateway
// compresses large tool outputs before forwarding to the upstream LLM.
// Uses OpenAI Responses API format (input[] with function_call_output).
func TestGateway_Compression_CompressesLargeToolOutput(t *testing.T) {
	// 1. Create mock compression API
	mockCompressionAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"compressed_output": "Go file with User struct, UserService for CRUD ops, caching with sync.Map",
			},
		})
	}))
	defer mockCompressionAPI.Close()

	// 2. Create mock upstream LLM
	var receivedUpstreamBody []byte
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUpstreamBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":         "resp_123",
			"object":     "response",
			"created_at": time.Now().Unix(),
			"output": []map[string]interface{}{
				{
					"type":    "message",
					"role":    "assistant",
					"content": []map[string]interface{}{{"type": "output_text", "text": "This Go file defines a User service with caching."}},
				},
			},
		})
	}))
	defer mockLLM.Close()

	// 3. Create Gateway with compression enabled
	cfg := compressionConfig(mockCompressionAPI.URL)
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// 4. Create request with LARGE tool output (OpenAI Responses API format)
	largeToolOutput := generateLargeToolOutput(2000)

	// OpenAI Responses API format: input[] with function_call and function_call_output
	requestBody := fmt.Sprintf(`{
		"model": "gpt-4o",
		"input": [
			{"role": "user", "content": "Read main.go and explain it"},
			{"type": "function_call", "call_id": "call_001", "name": "read_file", "arguments": "{\"path\": \"main.go\"}"},
			{"type": "function_call_output", "call_id": "call_001", "output": %q}
		]
	}`, largeToolOutput)

	originalLen := len(requestBody)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/responses", strings.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-api-key")
	req.Header.Set("X-Target-URL", mockLLM.URL+"/v1/responses")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// 5. Verify response is successful
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// 6. Verify upstream received COMPRESSED content (smaller than original)
	// The compressed summary should be much smaller than the 2KB original
	assert.Less(t, len(receivedUpstreamBody), originalLen,
		"Upstream should receive compressed (smaller) request: got %d, want < %d", len(receivedUpstreamBody), originalLen)

	// Verify the compressed content contains shadow prefix
	// Note: JSON encoding may HTML-escape < > as \u003c \u003e
	upstreamStr := string(receivedUpstreamBody)
	hasShadow := strings.Contains(upstreamStr, "<<<SHADOW:") || strings.Contains(upstreamStr, "\\u003c\\u003c\\u003cSHADOW:")
	assert.True(t, hasShadow, "Compressed content should have shadow ID prefix, got: %s", upstreamStr[:min(200, len(upstreamStr))])
}

// =============================================================================
// TEST: Gateway HTTP Server - Small Content Not Compressed
// =============================================================================

// TestGateway_SmallContent_NotCompressed tests that small tool outputs
// pass through without compression.
func TestGateway_SmallContent_NotCompressed(t *testing.T) {
	compressionAPICalled := false
	mockCompressionAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		compressionAPICalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer mockCompressionAPI.Close()

	var receivedUpstreamBody []byte
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUpstreamBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":     "resp_789",
			"object": "response",
			"output": []map[string]interface{}{{"type": "message", "role": "assistant", "content": []map[string]interface{}{{"type": "output_text", "text": "OK"}}}},
		})
	}))
	defer mockLLM.Close()

	cfg := compressionConfig(mockCompressionAPI.URL)
	cfg.Pipes.ToolOutput.MinBytes = 500 // Set threshold to 500 bytes
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Small tool output (below threshold) - OpenAI Responses API format
	smallToolOutput := "package main\n\nfunc main() {}"

	requestBody := fmt.Sprintf(`{
		"model": "gpt-4o",
		"input": [
			{"role": "user", "content": "Check main.go"},
			{"type": "function_call", "call_id": "call_001", "name": "read_file", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "call_001", "output": %q}
		]
	}`, smallToolOutput)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/responses", strings.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-api-key")
	req.Header.Set("X-Target-URL", mockLLM.URL+"/v1/responses")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Compression API should NOT have been called
	assert.False(t, compressionAPICalled, "Compression API should not be called for small content")

	// Content should be unchanged (no shadow prefix)
	assert.NotContains(t, string(receivedUpstreamBody), "<<<SHADOW:",
		"Small content should not be compressed")
}

// =============================================================================
// TEST: Gateway HTTP Server - Cache Hit Reuses Compressed
// =============================================================================

// TestGateway_CacheHit_ReusesCompressed tests that when the same tool output
// is seen again, the cached compressed version is used.
func TestGateway_CacheHit_ReusesCompressed(t *testing.T) {
	compressionCallCount := 0
	mockCompressionAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		compressionCallCount++

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"compressed_output": "Compressed summary of the code",
			},
		})
	}))
	defer mockCompressionAPI.Close()

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":     "resp_cache",
			"object": "response",
			"output": []map[string]interface{}{{"type": "message", "role": "assistant", "content": []map[string]interface{}{{"type": "output_text", "text": "Got it"}}}},
		})
	}))
	defer mockLLM.Close()

	cfg := compressionConfig(mockCompressionAPI.URL)
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Same large tool output for both requests
	largeToolOutput := generateLargeToolOutput(1500)

	makeRequest := func() {
		requestBody := fmt.Sprintf(`{
			"model": "gpt-4o",
			"input": [
				{"role": "user", "content": "Analyze"},
				{"type": "function_call", "call_id": "call_001", "name": "read_file", "arguments": "{}"},
				{"type": "function_call_output", "call_id": "call_001", "output": %q}
			]
		}`, largeToolOutput)

		req, _ := http.NewRequest("POST", gwServer.URL+"/v1/responses", strings.NewReader(requestBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("X-Target-URL", mockLLM.URL+"/v1/responses")

		client := &http.Client{Timeout: 10 * time.Second}
		resp, _ := client.Do(req)
		resp.Body.Close()
	}

	// First request - should call compression API
	makeRequest()
	assert.Equal(t, 1, compressionCallCount, "First request should call compression API")

	// Second request with SAME content - should use cache
	makeRequest()
	assert.Equal(t, 1, compressionCallCount, "Second request should use cache, not call API again")

	// Third request
	makeRequest()
	assert.Equal(t, 1, compressionCallCount, "Third request should also use cache")
}

// =============================================================================
// TEST: Gateway HTTP Server - OpenAI Responses API Format Support
// =============================================================================

// TestGateway_OpenAI_ResponsesAPI_FormatSupported tests that the gateway handles OpenAI Responses API format.
func TestGateway_OpenAI_ResponsesAPI_FormatSupported(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":         "resp_123",
			"object":     "response",
			"created_at": time.Now().Unix(),
			"output": []map[string]interface{}{
				{
					"type": "message",
					"role": "assistant",
					"content": []map[string]interface{}{
						{"type": "output_text", "text": "Hello! I'm here to help."},
					},
				},
			},
		})
	}))
	defer mockLLM.Close()

	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := `{
		"model": "gpt-4o",
		"input": [
			{"role": "user", "content": "Hello!"}
		]
	}`

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/responses", strings.NewReader(requestBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test-key")
	req.Header.Set("X-Target-URL", mockLLM.URL+"/v1/responses")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	assert.Equal(t, "resp_123", response["id"])
	assert.Equal(t, "response", response["object"])
}

// =============================================================================
// TEST: Gateway HTTP Server - Health Endpoint
// =============================================================================

func TestGateway_Health_ReturnsOK(t *testing.T) {
	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	resp, err := http.Get(gwServer.URL + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var health map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&health)

	assert.Equal(t, "ok", health["status"])
}

// =============================================================================
// TEST: Gateway HTTP Server - Missing Target URL
// =============================================================================

func TestGateway_MissingTargetURL_ReturnsError(t *testing.T) {
	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Use a path that won't be auto-detected (not /v1/messages or /v1/chat/completions)
	requestBody := `{"model": "custom-model", "messages": [{"role": "user", "content": "Hi"}]}`

	req, _ := http.NewRequest("POST", gwServer.URL+"/custom/endpoint", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	// Intentionally NOT setting X-Target-URL and no provider-specific headers

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should fail because no target URL can be determined
	// Gateway returns 502 Bad Gateway when upstream request fails
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
}

// =============================================================================
// TEST: Gateway HTTP Server - Compression API Failure Fallback
// =============================================================================

// TestGateway_CompressionFailure_FallsBackToPassthrough tests fallback behavior.
func TestGateway_CompressionFailure_FallsBackToPassthrough(t *testing.T) {
	// Mock compression API that always fails
	mockCompressionAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "service unavailable"}`))
	}))
	defer mockCompressionAPI.Close()

	var receivedUpstreamBody []byte
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUpstreamBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      "msg_fallback",
			"type":    "message",
			"role":    "assistant",
			"content": []map[string]interface{}{{"type": "text", "text": "OK"}},
		})
	}))
	defer mockLLM.Close()

	cfg := compressionConfig(mockCompressionAPI.URL)
	cfg.Pipes.ToolOutput.FallbackStrategy = "passthrough"
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	largeToolOutput := generateLargeToolOutput(1500)

	requestBody := fmt.Sprintf(`{
		"model": "claude-3-sonnet",
		"max_tokens": 100,
		"messages": [
			{"role": "user", "content": "Analyze"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "tool_001", "name": "read_file", "input": {}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "tool_001", "content": %q}]}
		]
	}`, largeToolOutput)

	req, _ := http.NewRequest("POST", gwServer.URL+"/v1/messages", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("X-Target-URL", mockLLM.URL+"/v1/messages")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should still succeed (fallback to passthrough)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Content should NOT be compressed due to fallback
	assert.NotContains(t, string(receivedUpstreamBody), "<<<SHADOW:",
		"Fallback should pass through original content")
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

func passthroughConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         18080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled: false,
			},
			ToolDiscovery: config.ToolDiscoveryPipeConfig{
				Enabled: false,
			},
		},
		Store: config.StoreConfig{
			Type: "memory",
			TTL:  1 * time.Hour,
		},
		Monitoring: config.MonitoringConfig{
			LogLevel:  "error",
			LogFormat: "json",
			LogOutput: "stdout",
		},
	}
}

func compressionConfig(compressionAPIURL string) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         18080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		URLs: config.URLsConfig{
			Compresr: compressionAPIURL,
		},
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:             true,
				Strategy:            "api",
				FallbackStrategy:    "passthrough",
				MinBytes:            256,
				MaxBytes:            65536,
				TargetRatio:         0.5,
				IncludeExpandHint:   false,
				EnableExpandContext: false,
				API: config.APIConfig{
					Endpoint: "/compress",
					Timeout:  5 * time.Second,
				},
			},
			ToolDiscovery: config.ToolDiscoveryPipeConfig{
				Enabled: false,
			},
		},
		Store: config.StoreConfig{
			Type: "memory",
			TTL:  1 * time.Hour,
		},
		Monitoring: config.MonitoringConfig{
			LogLevel:  "error",
			LogFormat: "json",
			LogOutput: "stdout",
		},
	}
}

func generateLargeToolOutput(minSize int) string {
	var buf bytes.Buffer
	buf.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n)\n\n")

	i := 0
	for buf.Len() < minSize {
		buf.WriteString(fmt.Sprintf("// Function %d does important work\n", i))
		buf.WriteString(fmt.Sprintf("func handler%d(w http.ResponseWriter, r *http.Request) {\n", i))
		buf.WriteString(fmt.Sprintf("\tfmt.Fprintf(w, \"Handler %d responding\")\n", i))
		buf.WriteString("}\n\n")
		i++
	}

	buf.WriteString("func main() {\n")
	for j := 0; j < i; j++ {
		buf.WriteString(fmt.Sprintf("\thttp.HandleFunc(\"/h%d\", handler%d)\n", j, j))
	}
	buf.WriteString("\thttp.ListenAndServe(\":8080\", nil)\n}\n")

	return buf.String()
}
