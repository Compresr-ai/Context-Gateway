// LiteLLM E2E Integration Tests - Real LiteLLM Proxy
//
// These tests make REAL calls to a LiteLLM proxy server through the gateway.
// LiteLLM routes to Ollama (local) or OpenAI (cloud) depending on model alias.
//
// Run with Docker:
//   make docker-test-up
//   go test ./tests/litellm/integration/... -v
//
// Or with local LiteLLM:
//   litellm --config docker/providers/litellm/litellm_config.yaml --port 4000
//   go test ./tests/litellm/integration/... -v

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
	defaultLiteLLMURL   = "http://localhost:4000"
	defaultLiteLLMModel = "local-model"
	defaultLiteLLMKey   = "sk-test-litellm-key"
	litellmTimeout      = 60 * time.Second
)

func getLiteLLMURL() string {
	if url := os.Getenv("LITELLM_URL"); url != "" {
		return url
	}
	return defaultLiteLLMURL
}

func getLiteLLMModel() string {
	if model := os.Getenv("LITELLM_MODEL"); model != "" {
		return model
	}
	return defaultLiteLLMModel
}

func getLiteLLMKey() string {
	if key := os.Getenv("LITELLM_API_KEY"); key != "" {
		return key
	}
	return defaultLiteLLMKey
}

func skipIfLiteLLMUnavailable(t *testing.T) {
	t.Helper()
	url := getLiteLLMURL()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url + "/health/liveliness")
	if err != nil {
		t.Skipf("LiteLLM not available at %s: %v", url, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("LiteLLM not healthy at %s: status %d", url, resp.StatusCode)
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
// TEST 1: Simple Chat through LiteLLM
// =============================================================================

func TestE2E_LiteLLM_SimpleChat(t *testing.T) {
	skipIfLiteLLMUnavailable(t)

	gwServer := newGatewayServer(passthroughConfig())
	defer gwServer.Close()

	body := map[string]interface{}{
		"model":      getLiteLLMModel(),
		"max_tokens": 50,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Say hello in one word."},
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+getLiteLLMKey())
	req.Header.Set("X-Provider", "litellm")
	req.Header.Set("X-Target-URL", getLiteLLMURL()+"/v1/chat/completions")

	client := &http.Client{Timeout: litellmTimeout}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&response))

	content := extractOpenAIContent(response)
	assert.NotEmpty(t, content)
	t.Logf("LiteLLM response: %s", content)
}

// =============================================================================
// TEST 2: Tool Result through LiteLLM
// =============================================================================

func TestE2E_LiteLLM_ToolResult(t *testing.T) {
	skipIfLiteLLMUnavailable(t)

	gwServer := newGatewayServer(passthroughConfig())
	defer gwServer.Close()

	body := map[string]interface{}{
		"model":      getLiteLLMModel(),
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What does this config contain?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_ll01",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "config.yaml"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_ll01", "content": "server:\n  port: 8080\n  host: localhost"},
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+getLiteLLMKey())
	req.Header.Set("X-Provider", "litellm")
	req.Header.Set("X-Target-URL", getLiteLLMURL()+"/v1/chat/completions")

	client := &http.Client{Timeout: litellmTimeout}
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
// TEST 3: Large Tool Output with Compression
// =============================================================================

func TestE2E_LiteLLM_ToolOutputCompression(t *testing.T) {
	skipIfLiteLLMUnavailable(t)

	gwServer := newGatewayServer(compressionConfig())
	defer gwServer.Close()

	largeOutput := generateLargeOutput(2000)
	t.Logf("Large output size: %d bytes", len(largeOutput))

	body := map[string]interface{}{
		"model":      getLiteLLMModel(),
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Summarize this log output."},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_ll_lg",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "app.log"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_ll_lg", "content": largeOutput},
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+getLiteLLMKey())
	req.Header.Set("X-Provider", "litellm")
	req.Header.Set("X-Target-URL", getLiteLLMURL()+"/v1/chat/completions")

	client := &http.Client{Timeout: litellmTimeout}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	respBytes, _ := io.ReadAll(resp.Body)
	var response map[string]interface{}
	require.NoError(t, json.Unmarshal(respBytes, &response))

	assert.NotNil(t, response["choices"], "response should have choices")
}

// =============================================================================
// TEST 4: Model Alias Resolution
// =============================================================================

func TestE2E_LiteLLM_ModelAlias(t *testing.T) {
	skipIfLiteLLMUnavailable(t)

	gwServer := newGatewayServer(passthroughConfig())
	defer gwServer.Close()

	body := map[string]interface{}{
		"model":      getLiteLLMModel(),
		"max_tokens": 30,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Reply with just OK."},
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+getLiteLLMKey())
	req.Header.Set("X-Provider", "litellm")
	req.Header.Set("X-Target-URL", getLiteLLMURL()+"/v1/chat/completions")

	client := &http.Client{Timeout: litellmTimeout}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&response))

	// LiteLLM returns the actual model used (e.g., ollama/qwen2:0.5b)
	model, _ := response["model"].(string)
	assert.NotEmpty(t, model, "response should contain model field")
	t.Logf("LiteLLM resolved model: %s", model)
}

// =============================================================================
// TEST 5: Full Chain - Gateway -> LiteLLM -> Ollama
// =============================================================================

func TestE2E_LiteLLM_ToOllama(t *testing.T) {
	skipIfLiteLLMUnavailable(t)

	gwServer := newGatewayServer(passthroughConfig())
	defer gwServer.Close()

	body := map[string]interface{}{
		"model":      "local-model", // Explicitly use the Ollama-backed alias
		"max_tokens": 50,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What is 2+2? Answer with just the number."},
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+getLiteLLMKey())
	req.Header.Set("X-Provider", "litellm")
	req.Header.Set("X-Target-URL", getLiteLLMURL()+"/v1/chat/completions")

	client := &http.Client{Timeout: litellmTimeout}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&response))

	content := extractOpenAIContent(response)
	assert.NotEmpty(t, content)
	t.Logf("Full chain (Gateway->LiteLLM->Ollama) response: %s", content)

	// Verify usage is present
	usage, ok := response["usage"].(map[string]interface{})
	if ok {
		promptTokens, _ := usage["prompt_tokens"].(float64)
		completionTokens, _ := usage["completion_tokens"].(float64)
		t.Logf("Usage - Prompt: %.0f, Completion: %.0f", promptTokens, completionTokens)
	}
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
		buf.WriteString(fmt.Sprintf("Line %d: CRITICAL ERROR - Database connection failed with timeout.\n", i))
		buf.WriteString(fmt.Sprintf("Line %d: WARNING - Memory usage at 95%%.\n", i))
		buf.WriteString(fmt.Sprintf("Line %d: INFO - Retry attempt %d of 3.\n", i, i%3+1))
		i++
	}
	return buf.String()
}
