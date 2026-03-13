// Gateway Integration Tests - Setup
//
// These tests use httptest.NewServer mock LLM backends to test core gateway
// behavior: parallel pipes, provider auto-detection, graceful degradation.
//
// Run with: go test ./tests/gateway/integration/... -v
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

// newMockLLMWithStatus creates a mock LLM that returns a specific status code.
func newMockLLMWithStatus(status int, responseFunc func(reqBody []byte, callNum int) []byte) *mockLLM {
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
		w.WriteHeader(status)
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

func sendOpenAIRequest(gwURL string, targetURL string, body map[string]interface{}) (*http.Response, []byte, error) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}

	req, err := http.NewRequest("POST", gwURL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test-key")
	req.Header.Set("X-Target-URL", targetURL+"/v1/chat/completions")

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

func bothPipesConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         18080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:                true,
				Strategy:               "simple",
				FallbackStrategy:       "passthrough",
				MinBytes:               100,
				MaxBytes:               65536,
				TargetCompressionRatio: 0.1,
				IncludeExpandHint:      true,
				EnableExpandContext:    true,
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

func openAITextResponse(text string) []byte {
	resp := map[string]interface{}{
		"id":      "chatcmpl-test001",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "gpt-4",
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": text,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     100,
			"completion_tokens": 50,
			"total_tokens":      150,
		},
	}
	data, _ := json.Marshal(resp)
	return data
}

func anthropicErrorResponse() []byte {
	resp := map[string]interface{}{
		"type": "error",
		"error": map[string]interface{}{
			"type":    "overloaded_error",
			"message": "Overloaded",
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

func largeToolOutput(minSize int) string {
	var buf strings.Builder
	buf.WriteString("CRITICAL ERROR LOG - System Diagnostic Report\n")
	buf.WriteString("==============================================\n\n")

	i := 0
	for buf.Len() < minSize {
		buf.WriteString(fmt.Sprintf("Line %d: [2024-01-15T%02d:%02d:%02d.%03dZ] ERROR - Service %s failed with status code %d\n",
			i, i%24, i%60, i%60, i%1000,
			[]string{"auth", "db", "cache", "api", "worker"}[i%5],
			[]int{500, 502, 503, 504, 429}[i%5]))
		buf.WriteString(fmt.Sprintf("  Stack: module%d.handler -> module%d.process -> module%d.execute\n", i, i+1, i+2))
		buf.WriteString(fmt.Sprintf("  Context: request_id=%d, user_id=%d, duration=%dms\n\n", i*100, i*10, 50+i*3))
		i++
	}

	return buf.String()
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

func extractMessages(body []byte) []interface{} {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	msgs, _ := req["messages"].([]interface{})
	return msgs
}
