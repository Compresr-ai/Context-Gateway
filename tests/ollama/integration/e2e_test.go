// Ollama E2E Integration Tests - Real Ollama Server
//
// These tests make REAL calls to an Ollama server through the gateway proxy.
// Requires a running Ollama instance with qwen2:0.5b model pulled.
//
// Run with Docker:
//   make docker-test-up
//   go test ./tests/ollama/integration/... -v
//
// Or with local Ollama:
//   ollama pull qwen2:0.5b
//   go test ./tests/ollama/integration/... -v

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	defaultOllamaURL   = "http://localhost:11434"
	defaultOllamaModel = "qwen2:0.5b"
	ollamaTimeout      = 60 * time.Second
)

func getOllamaURL() string {
	if url := os.Getenv("OLLAMA_URL"); url != "" {
		return url
	}
	return defaultOllamaURL
}

func getOllamaModel() string {
	if model := os.Getenv("OLLAMA_MODEL"); model != "" {
		return model
	}
	return defaultOllamaModel
}

func skipIfOllamaUnavailable(t *testing.T) {
	t.Helper()
	url := getOllamaURL()
	model := getOllamaModel()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url + "/api/tags")
	if err != nil {
		t.Skipf("Ollama not available at %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("Ollama not healthy at %s: status %d", url, resp.StatusCode)
	}

	// Verify the required model is pulled
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		t.Skipf("Cannot parse Ollama tags: %v", err)
	}
	found := false
	for _, m := range tags.Models {
		if strings.Contains(m.Name, strings.Split(model, ":")[0]) {
			found = true
			break
		}
	}
	if !found {
		t.Skipf("Ollama model %s not available (have: %d models)", model, len(tags.Models))
	}
}

func newGatewayServer(cfg *config.Config) *httptest.Server {
	gw := gateway.New(cfg)
	return httptest.NewServer(gw.Handler())
}

func passthroughConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         18080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:          false,
				Strategy:         "passthrough",
				FallbackStrategy: "passthrough",
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
			LogLevel:  "disabled",
			LogFormat: "json",
			LogOutput:  "discard",
		},
	}
}

func compressionConfig() *config.Config {
	cfg := passthroughConfig()
	cfg.Pipes.ToolOutput.Enabled = true
	cfg.Pipes.ToolOutput.Strategy = "simple"
	cfg.Pipes.ToolOutput.MinTokens = 25
	cfg.Pipes.ToolOutput.MaxTokens = 16384
	cfg.Pipes.ToolOutput.TargetCompressionRatio = 0.3
	cfg.Pipes.ToolOutput.IncludeExpandHint = true
	cfg.Pipes.ToolOutput.EnableExpandContext = true
	return cfg
}

// =============================================================================
// TEST 1: Simple Chat via Native /api/chat
// =============================================================================

func TestE2E_Ollama_NativeChat(t *testing.T) {
	skipIfOllamaUnavailable(t)

	gwServer := newGatewayServer(passthroughConfig())
	defer gwServer.Close()

	body := map[string]interface{}{
		"model":  getOllamaModel(),
		"stream": false,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Say hello in one word."},
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", gwServer.URL+"/api/chat", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Target-URL", getOllamaURL()+"/api/chat")

	client := &http.Client{Timeout: ollamaTimeout}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&response))

	msg, ok := response["message"].(map[string]interface{})
	require.True(t, ok, "response should have message field")
	content, _ := msg["content"].(string)
	assert.NotEmpty(t, content, "response content should not be empty")
	t.Logf("Ollama response: %s", content)
}

// =============================================================================
// TEST 2: OpenAI-Compatible Endpoint
// =============================================================================

func TestE2E_Ollama_OpenAICompat(t *testing.T) {
	skipIfOllamaUnavailable(t)

	gwServer := newGatewayServer(passthroughConfig())
	defer gwServer.Close()

	body := map[string]interface{}{
		"model": getOllamaModel(),
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Say hi."},
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Provider", "ollama")
	req.Header.Set("X-Target-URL", getOllamaURL()+"/v1/chat/completions")

	client := &http.Client{Timeout: ollamaTimeout}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&response))

	content := extractOpenAIContent(response)
	assert.NotEmpty(t, content)
	t.Logf("Ollama OpenAI-compat response: %s", content)
}

// =============================================================================
// TEST 3: Tool Result Passthrough
// =============================================================================

func TestE2E_Ollama_ToolResultPassthrough(t *testing.T) {
	skipIfOllamaUnavailable(t)

	gwServer := newGatewayServer(passthroughConfig())
	defer gwServer.Close()

	body := map[string]interface{}{
		"model": getOllamaModel(),
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What does this config contain?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_ol01",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "config.yaml"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_ol01", "content": "server:\n  port: 8080\n  host: localhost"},
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Provider", "ollama")
	req.Header.Set("X-Target-URL", getOllamaURL()+"/v1/chat/completions")

	client := &http.Client{Timeout: ollamaTimeout}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&response))
	content := extractOpenAIContent(response)
	assert.NotEmpty(t, content)
}

// =============================================================================
// TEST 4: Large Tool Output with Compression
// =============================================================================

func TestE2E_Ollama_ToolOutputCompression(t *testing.T) {
	skipIfOllamaUnavailable(t)

	gwServer := newGatewayServer(compressionConfig())
	defer gwServer.Close()

	largeOutput := generateLargeOutput(2000)
	t.Logf("Large output size: %d bytes", len(largeOutput))

	body := map[string]interface{}{
		"model": getOllamaModel(),
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Summarize this output."},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_ol_lg",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "app.log"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_ol_lg", "content": largeOutput},
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Provider", "ollama")
	req.Header.Set("X-Target-URL", getOllamaURL()+"/v1/chat/completions")

	client := &http.Client{Timeout: ollamaTimeout}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	respBytes, _ := io.ReadAll(resp.Body)
	var response map[string]interface{}
	require.NoError(t, json.Unmarshal(respBytes, &response))

	// Verify we got a valid response (compressed content was sent to Ollama)
	assert.NotNil(t, response["choices"], "response should have choices")
}

// =============================================================================
// TEST 5: No Auth Required
// =============================================================================

func TestE2E_Ollama_NoAuthRequired(t *testing.T) {
	skipIfOllamaUnavailable(t)

	gwServer := newGatewayServer(passthroughConfig())
	defer gwServer.Close()

	body := map[string]interface{}{
		"model":  getOllamaModel(),
		"stream": false,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Hi"},
		},
	}
	bodyBytes, _ := json.Marshal(body)

	// No auth headers at all
	req, err := http.NewRequest("POST", gwServer.URL+"/api/chat", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Target-URL", getOllamaURL()+"/api/chat")

	client := &http.Client{Timeout: ollamaTimeout}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// =============================================================================
// HELPERS
// =============================================================================

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
	content, _ := message["content"].(string)
	return content
}

func generateLargeOutput(minSize int) string {
	var buf strings.Builder
	i := 0
	for buf.Len() < minSize {
		buf.WriteString(fmt.Sprintf("Line %d: CRITICAL ERROR - Database connection failed with timeout after 30 seconds. Stack trace shows connection pool exhausted.\n", i))
		buf.WriteString(fmt.Sprintf("Line %d: WARNING - Memory usage at 95%%, triggering garbage collection.\n", i))
		buf.WriteString(fmt.Sprintf("Line %d: INFO - Retry attempt %d of 3 for database connection.\n", i, i%3+1))
		i++
	}
	return buf.String()
}
