// Pipe Integration Tests - Setup
//
// These tests use httptest.NewServer mock LLM backends to test the full
// request cycle through the gateway's pipe system. No real LLM calls.
//
// Run with: go test ./tests/pipes/integration/... -v
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
	// Silence logs during tests
	zerolog.SetGlobalLevel(zerolog.Disabled)
	log.Logger = zerolog.New(io.Discard)
}

func TestMain(m *testing.M) {
	// Load .env from project root
	godotenv.Load("../../../.env")
	// Enable localhost for tests using httptest.NewServer
	gateway.EnableLocalHostsForTesting()
	os.Exit(m.Run())
}

// =============================================================================
// MOCK LLM SERVER
// =============================================================================

// mockLLM is a configurable mock LLM backend that captures forwarded requests
// and returns programmable responses.
type mockLLM struct {
	mu       sync.Mutex
	requests []capturedRequest
	handler  http.HandlerFunc
	server   *httptest.Server
	callNum  atomic.Int32
}

// capturedRequest stores a request received at the mock LLM.
type capturedRequest struct {
	Body    []byte
	Headers http.Header
}

// newMockLLM creates a mock LLM that returns a static response.
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

func (m *mockLLM) close() {
	m.server.Close()
}

func (m *mockLLM) url() string {
	return m.server.URL
}

func (m *mockLLM) getRequests() []capturedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]capturedRequest, len(m.requests))
	copy(cp, m.requests)
	return cp
}

func (m *mockLLM) requestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

// =============================================================================
// GATEWAY HELPERS
// =============================================================================

// createGateway creates a gateway with the given config and returns its httptest.Server.
func createGateway(cfg *config.Config) *httptest.Server {
	gw := gateway.New(cfg)
	return httptest.NewServer(gw.Handler())
}

// sendAnthropicRequest sends a request in Anthropic format through the gateway.
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

// sendOpenAIRequest sends a request in OpenAI format through the gateway.
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

// passthroughConfig returns a passthrough-only config (no pipes active).
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

// expandContextConfig returns a config with tool_output compression and expand_context enabled.
// Uses "simple" strategy (first N words) so no external API is needed.
func expandContextConfig() *config.Config {
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

// toolSearchConfig returns a config with tool_discovery in tool-search mode.
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

// bothPipesConfig returns a config with both tool_output (simple compression)
// and tool_discovery (tool-search) enabled.
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

// =============================================================================
// RESPONSE BUILDERS
// =============================================================================

// anthropicTextResponse creates an Anthropic text-only response.
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

// anthropicExpandCallResponse creates an Anthropic response with expand_context tool call.
func anthropicExpandCallResponse(toolUseID, shadowID string) []byte {
	resp := map[string]interface{}{
		"id":   "msg_test_expand",
		"type": "message",
		"role": "assistant",
		"content": []interface{}{
			map[string]interface{}{
				"type":  "tool_use",
				"id":    toolUseID,
				"name":  "expand_context",
				"input": map[string]interface{}{"id": shadowID},
			},
		},
		"stop_reason": "tool_use",
		"usage": map[string]interface{}{
			"input_tokens":  100,
			"output_tokens": 50,
		},
	}
	data, _ := json.Marshal(resp)
	return data
}

// openAITextResponse creates an OpenAI text-only response.
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

// openAIExpandCallResponse creates an OpenAI response with expand_context tool call.
func openAIExpandCallResponse(toolCallID, shadowID string) []byte {
	resp := map[string]interface{}{
		"id":      "chatcmpl-test-expand",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "gpt-4",
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []interface{}{
						map[string]interface{}{
							"id":   toolCallID,
							"type": "function",
							"function": map[string]interface{}{
								"name":      "expand_context",
								"arguments": `{"id":"` + shadowID + `"}`,
							},
						},
					},
				},
				"finish_reason": "tool_calls",
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

// =============================================================================
// REQUEST BUILDERS
// =============================================================================

// anthropicRequestWithToolResult creates an Anthropic request with a tool result.
func anthropicRequestWithToolResult(toolOutput string) map[string]interface{} {
	return map[string]interface{}{
		"model":      "claude-3-haiku-20240307",
		"max_tokens": 500,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What are the key points?"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_test_001",
						"name":  "read_file",
						"input": map[string]string{"path": "system.log"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_test_001",
						"content":     toolOutput,
					},
				},
			},
		},
	}
}

// openAIRequestWithToolResult creates an OpenAI request with a tool result.
func openAIRequestWithToolResult(toolOutput string) map[string]interface{} {
	return map[string]interface{}{
		"model": "gpt-4",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What are the key points?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_test_001",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "system.log"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_test_001", "content": toolOutput},
		},
		"max_completion_tokens": 500,
	}
}

// makeAnthropicToolDefs creates N Anthropic-format tool definitions.
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

// makeOpenAIToolDefs creates N OpenAI-format tool definitions.
func makeOpenAIToolDefs(n int) []map[string]interface{} {
	tools := make([]map[string]interface{}, n)
	for i := 0; i < n; i++ {
		tools[i] = map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        fmt.Sprintf("tool_%03d", i),
				"description": fmt.Sprintf("This is tool number %d for testing purposes", i),
				"parameters": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"input": map[string]interface{}{
							"type":        "string",
							"description": "The input value",
						},
					},
					"required": []string{"input"},
				},
			},
		}
	}
	return tools
}

// =============================================================================
// LARGE CONTENT GENERATORS
// =============================================================================

// largeToolOutput generates a large string suitable for triggering compression.
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

// extractTools extracts the tools array from a captured request body.
func extractTools(body []byte) []interface{} {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	tools, _ := req["tools"].([]interface{})
	return tools
}

// extractToolNames extracts tool names from captured request body.
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
		// Anthropic format: {name: "..."}
		if name, ok := tool["name"].(string); ok {
			names = append(names, name)
		}
		// OpenAI format: {function: {name: "..."}}
		if fn, ok := tool["function"].(map[string]interface{}); ok {
			if name, ok := fn["name"].(string); ok {
				names = append(names, name)
			}
		}
	}
	return names
}

// containsToolName checks if the tools in a request body contain a specific tool name.
func containsToolName(body []byte, name string) bool {
	for _, n := range extractToolNames(body) {
		if n == name {
			return true
		}
	}
	return false
}

// extractMessages extracts the messages array from a captured request body.
func extractMessages(body []byte) []interface{} {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	msgs, _ := req["messages"].([]interface{})
	return msgs
}
