// Agent Pattern Integration Tests
//
// Tests real agent request patterns through the gateway.
// Each test simulates a specific agent's request format and validates
// the gateway handles it correctly.
//
// Requires a running backend (Ollama or LiteLLM) and gateway.
//
// Run with Docker:
//   make docker-test-up
//   go test ./tests/agents/integration/... -v

package integration

import (
	"bytes"
	"encoding/json"
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

const agentTimeout = 60 * time.Second

func getBackendURL() string {
	if url := os.Getenv("OLLAMA_URL"); url != "" {
		return url
	}
	return "http://localhost:11434"
}

func getBackendModel() string {
	if model := os.Getenv("OLLAMA_MODEL"); model != "" {
		return model
	}
	return "qwen2:0.5b"
}

func skipIfBackendUnavailable(t *testing.T) {
	t.Helper()
	url := getBackendURL()
	model := getBackendModel()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url + "/api/tags")
	if err != nil {
		t.Skipf("Backend not available at %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("Backend not healthy at %s: status %d", url, resp.StatusCode)
	}

	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		t.Skipf("Cannot parse backend tags: %v", err)
	}
	found := false
	for _, m := range tags.Models {
		if strings.Contains(m.Name, strings.Split(model, ":")[0]) {
			found = true
			break
		}
	}
	if !found {
		t.Skipf("Backend model %s not available (have: %d models)", model, len(tags.Models))
	}
}

func newGW() *httptest.Server {
	cfg := &config.Config{
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
			LogLevel:  "error",
			LogFormat: "json",
			LogOutput: "stdout",
		},
	}
	gw := gateway.New(cfg)
	return httptest.NewServer(gw.Handler())
}

type agentTestCase struct {
	name    string
	method  string
	path    string
	headers map[string]string
	body    map[string]any
}

// =============================================================================
// TEST: Agent Request Patterns (Table-Driven)
// =============================================================================

func TestE2E_AgentPatterns(t *testing.T) {
	skipIfBackendUnavailable(t)

	gwServer := newGW()
	defer gwServer.Close()

	backendURL := getBackendURL()
	model := getBackendModel()

	tests := []agentTestCase{
		{
			name:   "Cursor - OpenAI Chat Completions",
			method: "POST",
			path:   "/v1/chat/completions",
			headers: map[string]string{
				"Content-Type":  "application/json",
				"Authorization": "Bearer sk-test",
				"X-Provider":    "ollama",
				"X-Target-URL":  backendURL + "/v1/chat/completions",
			},
			body: map[string]any{
				"model": model,
				"messages": []map[string]any{
					{"role": "user", "content": "Say hello."},
				},
			},
		},
		{
			name:   "Cursor - Tool result",
			method: "POST",
			path:   "/v1/chat/completions",
			headers: map[string]string{
				"Content-Type":  "application/json",
				"Authorization": "Bearer sk-test",
				"X-Provider":    "ollama",
				"X-Target-URL":  backendURL + "/v1/chat/completions",
			},
			body: map[string]any{
				"model": model,
				"messages": []map[string]any{
					{"role": "user", "content": "Read the file"},
					{
						"role":    "assistant",
						"content": nil,
						"tool_calls": []map[string]any{
							{
								"id":   "call_c01",
								"type": "function",
								"function": map[string]any{
									"name":      "read_file",
									"arguments": `{"path": "test.go"}`,
								},
							},
						},
					},
					{"role": "tool", "tool_call_id": "call_c01", "content": "package main\nfunc main() {}"},
				},
			},
		},
		{
			name:   "OpenCode - Multiple tool results",
			method: "POST",
			path:   "/v1/chat/completions",
			headers: map[string]string{
				"Content-Type":  "application/json",
				"Authorization": "Bearer sk-test",
				"X-Provider":    "ollama",
				"X-Target-URL":  backendURL + "/v1/chat/completions",
			},
			body: map[string]any{
				"model": model,
				"messages": []map[string]any{
					{"role": "user", "content": "Compare files"},
					{
						"role":    "assistant",
						"content": nil,
						"tool_calls": []map[string]any{
							{"id": "call_m1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path": "a.go"}`}},
							{"id": "call_m2", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path": "b.go"}`}},
						},
					},
					{"role": "tool", "tool_call_id": "call_m1", "content": "package a"},
					{"role": "tool", "tool_call_id": "call_m2", "content": "package b"},
				},
			},
		},
		{
			name:   "OpenCode - With tool definitions",
			method: "POST",
			path:   "/v1/chat/completions",
			headers: map[string]string{
				"Content-Type":  "application/json",
				"Authorization": "Bearer sk-test",
				"X-Provider":    "ollama",
				"X-Target-URL":  backendURL + "/v1/chat/completions",
			},
			body: map[string]any{
				"model": model,
				"messages": []map[string]any{
					{"role": "system", "content": "You are a coding assistant."},
					{"role": "user", "content": "Hello"},
				},
				"tools": []map[string]any{
					{"type": "function", "function": map[string]any{"name": "read_file", "description": "Read a file", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}},
				},
			},
		},
		{
			name:   "Ollama Direct - Native /api/chat",
			method: "POST",
			path:   "/api/chat",
			headers: map[string]string{
				"Content-Type": "application/json",
				"X-Target-URL": backendURL + "/api/chat",
			},
			body: map[string]any{
				"model":  model,
				"stream": false,
				"messages": []map[string]any{
					{"role": "user", "content": "Hi"},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bodyBytes, err := json.Marshal(tc.body)
			require.NoError(t, err)

			req, err := http.NewRequest(tc.method, gwServer.URL+tc.path, bytes.NewReader(bodyBytes))
			require.NoError(t, err)

			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}

			client := &http.Client{Timeout: agentTimeout}
			resp, err := client.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode, "expected 200 OK for %s", tc.name)

			var response map[string]any
			err = json.NewDecoder(resp.Body).Decode(&response)
			require.NoError(t, err, "response should be valid JSON for %s", tc.name)

			t.Logf("%s: status=%d response_keys=%v", tc.name, resp.StatusCode, mapKeys(response))
		})
	}
}

// =============================================================================
// TEST: Anthropic-format agents (Claude Code, OpenClaw)
// These need special handling since Ollama doesn't support /v1/messages.
// We test that the gateway correctly routes Anthropic-format requests.
// When using real Anthropic API, set ANTHROPIC_API_KEY.
// =============================================================================

func TestE2E_AgentPatterns_AnthropicFormat(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping Anthropic-format agent tests")
	}

	gwServer := newGW()
	defer gwServer.Close()

	tests := []agentTestCase{
		{
			name:   "Claude Code - Simple tool result",
			method: "POST",
			path:   "/v1/messages",
			headers: map[string]string{
				"Content-Type":      "application/json",
				"anthropic-version": "2023-06-01",
				"x-api-key":         apiKey,
				"X-Target-URL":      "https://api.anthropic.com/v1/messages",
			},
			body: map[string]any{
				"model":      "claude-sonnet-4-20250514",
				"max_tokens": 100,
				"messages": []map[string]any{
					{"role": "user", "content": "What does this file say?"},
					{"role": "assistant", "content": []map[string]any{
						{"type": "tool_use", "id": "toolu_cc01", "name": "read_file", "input": map[string]any{"path": "test.txt"}},
					}},
					{"role": "user", "content": []map[string]any{
						{"type": "tool_result", "tool_use_id": "toolu_cc01", "content": "Hello from the test file."},
					}},
				},
			},
		},
		{
			name:   "OpenClaw - Multiple tool results",
			method: "POST",
			path:   "/v1/messages",
			headers: map[string]string{
				"Content-Type":      "application/json",
				"anthropic-version": "2023-06-01",
				"x-api-key":         apiKey,
				"X-Target-URL":      "https://api.anthropic.com/v1/messages",
			},
			body: map[string]any{
				"model":      "claude-sonnet-4-20250514",
				"max_tokens": 100,
				"messages": []map[string]any{
					{"role": "user", "content": "Read both files"},
					{"role": "assistant", "content": []map[string]any{
						{"type": "tool_use", "id": "toolu_oc01", "name": "read_file", "input": map[string]any{"path": "a.go"}},
						{"type": "tool_use", "id": "toolu_oc02", "name": "read_file", "input": map[string]any{"path": "b.go"}},
					}},
					{"role": "user", "content": []map[string]any{
						{"type": "tool_result", "tool_use_id": "toolu_oc01", "content": "package a"},
						{"type": "tool_result", "tool_use_id": "toolu_oc02", "content": "package b"},
					}},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bodyBytes, err := json.Marshal(tc.body)
			require.NoError(t, err)

			req, err := http.NewRequest(tc.method, gwServer.URL+tc.path, bytes.NewReader(bodyBytes))
			require.NoError(t, err)

			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}

			client := &http.Client{Timeout: agentTimeout}
			resp, err := client.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode, "expected 200 OK for %s", tc.name)

			var response map[string]any
			err = json.NewDecoder(resp.Body).Decode(&response)
			require.NoError(t, err, "response should be valid JSON for %s", tc.name)

			t.Logf("%s: status=%d", tc.name, resp.StatusCode)
		})
	}
}

// =============================================================================
// HELPERS
// =============================================================================

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
