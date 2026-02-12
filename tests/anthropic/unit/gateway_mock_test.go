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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
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
