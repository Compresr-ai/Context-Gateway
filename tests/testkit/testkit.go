// Package testkit provides shared mock infrastructure for integration tests.
// All integration test suites that need a mock LLM backend, gateway helper,
// or common request/response builders should import this package.
package testkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
)

// =============================================================================
// MOCK LLM SERVER
// =============================================================================

// MockLLM is a configurable mock LLM backend that captures forwarded requests
// and returns programmable responses.
type MockLLM struct {
	mu      sync.Mutex
	reqs    []CapturedRequest
	handler http.HandlerFunc
	server  *httptest.Server
	callNum atomic.Int32
}

// CapturedRequest stores a request received at the mock LLM.
type CapturedRequest struct {
	Body    []byte
	Headers http.Header
}

// NewMockLLM creates a mock LLM that delegates responses to responseFunc.
func NewMockLLM(responseFunc func(reqBody []byte, callNum int) []byte) *MockLLM {
	m := &MockLLM{}
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()

		m.mu.Lock()
		m.reqs = append(m.reqs, CapturedRequest{Body: body, Headers: r.Header.Clone()})
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

// NewMockLLMWithStatus creates a mock LLM that returns the given HTTP status code.
func NewMockLLMWithStatus(status int, responseFunc func(reqBody []byte, callNum int) []byte) *MockLLM {
	m := &MockLLM{}
	m.handler = func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()

		m.mu.Lock()
		m.reqs = append(m.reqs, CapturedRequest{Body: body, Headers: r.Header.Clone()})
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

func (m *MockLLM) Close()      { m.server.Close() }
func (m *MockLLM) URL() string { return m.server.URL }

func (m *MockLLM) GetRequests() []CapturedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]CapturedRequest, len(m.reqs))
	copy(cp, m.reqs)
	return cp
}

func (m *MockLLM) RequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.reqs)
}

// =============================================================================
// GATEWAY HELPERS
// =============================================================================

// CreateGateway creates a gateway with the given config and returns its test server.
func CreateGateway(cfg *config.Config) *httptest.Server {
	gw := gateway.New(cfg)
	return httptest.NewServer(gw.Handler())
}

// SendAnthropicRequest sends a request in Anthropic format through the gateway.
func SendAnthropicRequest(gwURL, targetURL string, body map[string]interface{}) (*http.Response, []byte, error) {
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

// SendOpenAIRequest sends a request in OpenAI format through the gateway.
func SendOpenAIRequest(gwURL, targetURL string, body map[string]interface{}) (*http.Response, []byte, error) {
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
// RESPONSE BUILDERS
// =============================================================================

// AnthropicTextResponse creates an Anthropic text-only response.
func AnthropicTextResponse(text string) []byte {
	resp := map[string]interface{}{
		"id":   "msg_test_001",
		"type": "message",
		"role": "assistant",
		"content": []interface{}{
			map[string]interface{}{"type": "text", "text": text},
		},
		"stop_reason": "end_turn",
		"usage":       map[string]interface{}{"input_tokens": 100, "output_tokens": 50},
	}
	data, _ := json.Marshal(resp)
	return data
}

// OpenAITextResponse creates an OpenAI text-only response.
func OpenAITextResponse(text string) []byte {
	resp := map[string]interface{}{
		"id":      "chatcmpl-test001",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "gpt-4",
		"choices": []interface{}{
			map[string]interface{}{
				"index":         0,
				"message":       map[string]interface{}{"role": "assistant", "content": text},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150,
		},
	}
	data, _ := json.Marshal(resp)
	return data
}

// =============================================================================
// REQUEST / TOOL BUILDERS
// =============================================================================

// MakeAnthropicToolDefs creates n Anthropic-format tool definitions.
func MakeAnthropicToolDefs(n int) []map[string]interface{} {
	tools := make([]map[string]interface{}, n)
	for i := range tools {
		tools[i] = map[string]interface{}{
			"name":        fmt.Sprintf("tool_%03d", i),
			"description": fmt.Sprintf("This is tool number %d for testing purposes", i),
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"input": map[string]interface{}{"type": "string", "description": "The input value"},
				},
				"required": []string{"input"},
			},
		}
	}
	return tools
}

// MakeOpenAIToolDefs creates n OpenAI-format tool definitions.
func MakeOpenAIToolDefs(n int) []map[string]interface{} {
	tools := make([]map[string]interface{}, n)
	for i := range tools {
		tools[i] = map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        fmt.Sprintf("tool_%03d", i),
				"description": fmt.Sprintf("This is tool number %d for testing purposes", i),
				"parameters": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"input": map[string]interface{}{"type": "string", "description": "The input value"},
					},
					"required": []string{"input"},
				},
			},
		}
	}
	return tools
}

// LargeToolOutput generates a large string suitable for triggering compression.
func LargeToolOutput(minSize int) string {
	var buf strings.Builder
	buf.WriteString("CRITICAL ERROR LOG - System Diagnostic Report\n")
	buf.WriteString("==============================================\n\n")
	services := []string{"auth", "db", "cache", "api", "worker"}
	codes := []int{500, 502, 503, 504, 429}
	for i := 0; buf.Len() < minSize; i++ {
		fmt.Fprintf(&buf, "Line %d: [2024-01-15T%02d:%02d:%02d.%03dZ] ERROR - Service %s failed with status code %d\n",
			i, i%24, i%60, i%60, i%1000, services[i%5], codes[i%5])
		fmt.Fprintf(&buf, "  Stack: module%d.handler -> module%d.process -> module%d.execute\n", i, i+1, i+2)
		fmt.Fprintf(&buf, "  Context: request_id=%d, user_id=%d, duration=%dms\n\n", i*100, i*10, 50+i*3)
	}
	return buf.String()
}

// =============================================================================
// JSON HELPERS
// =============================================================================

// ExtractTools extracts the tools array from a request body.
func ExtractTools(body []byte) []interface{} {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	tools, _ := req["tools"].([]interface{})
	return tools
}

// ExtractToolNames extracts tool names from a request body (handles both Anthropic and OpenAI formats).
func ExtractToolNames(body []byte) []string {
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

// ContainsToolName reports whether a request body contains the given tool name.
func ContainsToolName(body []byte, name string) bool {
	for _, n := range ExtractToolNames(body) {
		if n == name {
			return true
		}
	}
	return false
}

// CountEffectiveToolNames returns the number of non-stub tools.
// Stubs (description == adapters.DeferredStubDescription) preserve array length
// for KV-cache stability but are invisible to the model.
func CountEffectiveToolNames(body []byte) int {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return 0
	}
	tools, ok := req["tools"].([]interface{})
	if !ok {
		return 0
	}
	count := 0
	for _, t := range tools {
		tool, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		desc := ""
		if d, ok := tool["description"].(string); ok {
			desc = d
		} else if fn, ok := tool["function"].(map[string]interface{}); ok {
			if d, ok := fn["description"].(string); ok {
				desc = d
			}
		}
		if desc != adapters.DeferredStubDescription {
			count++
		}
	}
	return count
}

// ExtractMessages extracts the messages array from a request body.
func ExtractMessages(body []byte) []interface{} {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	msgs, _ := req["messages"].([]interface{})
	return msgs
}
