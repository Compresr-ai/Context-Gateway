// Tool Discovery Integration Tests - Setup
//
// These tests use httptest.NewServer mock LLM backends to test tool discovery
// pipe behavior through the full gateway request cycle. No real LLM calls.
//
// Run with: go test ./tests/tool_discovery/integration/... -v
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func init() {
	zerolog.SetGlobalLevel(zerolog.Disabled)
	log.Logger = zerolog.New(io.Discard)
}

func TestMain(m *testing.M) {
	godotenv.Load("../../../.env")
	gateway.EnableLocalHostsForTesting()
	os.Exit(m.Run())
}

// =============================================================================
// MOCK LLM SERVER
// =============================================================================

type mockLLM struct {
	mu       sync.Mutex
	requests []capturedRequest
	handler  http.HandlerFunc
	server   *httptest.Server
	callNum  atomic.Int32
}

type capturedRequest struct {
	Body    []byte
	Headers http.Header
}

func newMockLLM(responseFunc func(reqBody []byte, callNum int) []byte) *mockLLM {
	m := &mockLLM{}
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()

		m.mu.Lock()
		m.requests = append(m.requests, capturedRequest{
			Body:    body,
			Headers: r.Header.Clone(),
		})
		m.mu.Unlock()

		n := int(m.callNum.Add(1))
		resp := responseFunc(body, n)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(resp)
	}
	m.server = httptest.NewServer(m.handler)
	return m
}

func (m *mockLLM) close()      { m.server.Close() }
func (m *mockLLM) url() string { return m.server.URL }

func (m *mockLLM) getRequests() []capturedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]capturedRequest, len(m.requests))
	copy(cp, m.requests)
	return cp
}

// =============================================================================
// GATEWAY HELPERS
// =============================================================================

func createGateway(cfg *config.Config) *httptest.Server {
	gw := gateway.New(cfg)
	return httptest.NewServer(gw.Handler())
}

func sendAnthropicRequest(gwURL string, targetURL string, body map[string]interface{}) (*http.Response, []byte, error) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}

	req, err := http.NewRequest("POST", gwURL+"/v1/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "sk-ant-test-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("X-Target-URL", targetURL+"/v1/messages")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}

	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, respBody, err
}

// =============================================================================
// CONFIG BUILDERS
// =============================================================================

func relevanceConfig(minTools, maxTools int) *config.Config {
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
				Enabled:          true,
				Strategy:         "relevance",
				FallbackStrategy: "passthrough",
				MinTools:         minTools,
				MaxTools:         maxTools,
				TargetRatio:      0.3,
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

func toolSearchConfig() *config.Config {
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
				Enabled:              true,
				Strategy:             "tool-search",
				FallbackStrategy:     "passthrough",
				MinTools:             5,
				MaxTools:             25,
				TargetRatio:          0.3,
				EnableSearchFallback: true,
				MaxSearchResults:     5,
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

// =============================================================================
// RESPONSE BUILDERS
// =============================================================================

func anthropicTextResponse(text string) []byte {
	resp := map[string]interface{}{
		"id":   "msg_test_001",
		"type": "message",
		"role": "assistant",
		"content": []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": text,
			},
		},
		"stop_reason": "end_turn",
		"usage": map[string]interface{}{
			"input_tokens":  100,
			"output_tokens": 50,
		},
	}
	data, _ := json.Marshal(resp)
	return data
}

// =============================================================================
// REQUEST BUILDERS
// =============================================================================

func makeAnthropicToolDefs(n int) []map[string]interface{} {
	tools := make([]map[string]interface{}, n)
	for i := 0; i < n; i++ {
		tools[i] = map[string]interface{}{
			"name":        fmt.Sprintf("tool_%03d", i),
			"description": fmt.Sprintf("This is tool number %d for testing purposes", i),
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"input": map[string]interface{}{
						"type":        "string",
						"description": "The input value",
					},
				},
				"required": []string{"input"},
			},
		}
	}
	return tools
}

// =============================================================================
// JSON HELPERS
// =============================================================================

func extractToolNames(body []byte) []string {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}

	tools, ok := req["tools"].([]interface{})
	if !ok {
		return nil
	}

	names := make([]string, 0, len(tools))
	for _, t := range tools {
		tool, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		if name, ok := tool["name"].(string); ok {
			names = append(names, name)
		}
		if fn, ok := tool["function"].(map[string]interface{}); ok {
			if name, ok := fn["name"].(string); ok {
				names = append(names, name)
			}
		}
	}
	return names
}

func containsToolName(body []byte, name string) bool {
	for _, n := range extractToolNames(body) {
		if n == name {
			return true
		}
	}
	return false
}
