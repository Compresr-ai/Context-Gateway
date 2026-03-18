// Pipe Integration Tests - Setup
//
// These tests use httptest.NewServer mock LLM backends to test the full
// request cycle through the gateway's pipe system. No real LLM calls.
//
// Run with: go test ./tests/pipes/integration/... -v
package integration

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/compresr/context-gateway/tests/testkit"
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
// MOCK LLM — thin wrapper preserving lowercase API used by test files
// =============================================================================

type capturedRequest = testkit.CapturedRequest

type mockLLM struct{ *testkit.MockLLM }

func (m *mockLLM) close()                         { m.MockLLM.Close() }
func (m *mockLLM) url() string                    { return m.MockLLM.URL() }
func (m *mockLLM) getRequests() []capturedRequest { return m.MockLLM.GetRequests() }
func (m *mockLLM) requestCount() int              { return m.MockLLM.RequestCount() }

func newMockLLM(fn func([]byte, int) []byte) *mockLLM {
	return &mockLLM{testkit.NewMockLLM(fn)}
}

// =============================================================================
// GATEWAY / REQUEST HELPERS (delegated to testkit)
// =============================================================================

func createGateway(cfg *config.Config) *httptest.Server { return testkit.CreateGateway(cfg) }

var (
	sendAnthropicRequest    = testkit.SendAnthropicRequest
	sendOpenAIRequest       = testkit.SendOpenAIRequest
	anthropicTextResponse   = testkit.AnthropicTextResponse
	openAITextResponse      = testkit.OpenAITextResponse
	largeToolOutput         = testkit.LargeToolOutput
	extractTools            = testkit.ExtractTools
	extractToolNames        = testkit.ExtractToolNames
	containsToolName        = testkit.ContainsToolName
	countEffectiveToolNames = testkit.CountEffectiveToolNames
	extractMessages         = testkit.ExtractMessages
)

func makeAnthropicToolDefs(n int) []map[string]interface{} { return testkit.MakeAnthropicToolDefs(n) }
func makeOpenAIToolDefs(n int) []map[string]interface{}    { return testkit.MakeOpenAIToolDefs(n) }

// =============================================================================
// CONFIG BUILDERS
// =============================================================================

func passthroughConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         18080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		Pipes: config.PipesConfig{
			ToolOutput:    config.ToolOutputPipeConfig{Enabled: false, Strategy: "passthrough", FallbackStrategy: "passthrough"},
			ToolDiscovery: config.ToolDiscoveryPipeConfig{Enabled: false},
		},
		Store:      config.StoreConfig{Type: "memory", TTL: 1 * time.Hour},
		Monitoring: config.MonitoringConfig{LogLevel: "disabled", LogFormat: "json", LogOutput: "discard"},
	}
}

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
				MinTokens:              25,
				MaxTokens:              16384,
				TargetCompressionRatio: 0.1,
				IncludeExpandHint:      true,
				EnableExpandContext:    true,
			},
			ToolDiscovery: config.ToolDiscoveryPipeConfig{Enabled: false},
		},
		Store:      config.StoreConfig{Type: "memory", TTL: 1 * time.Hour},
		Monitoring: config.MonitoringConfig{LogLevel: "disabled", LogFormat: "json", LogOutput: "discard"},
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
				EnableSearchFallback: true,
				MaxSearchResults:     5,
			},
		},
		Store:      config.StoreConfig{Type: "memory", TTL: 1 * time.Hour},
		Monitoring: config.MonitoringConfig{LogLevel: "disabled", LogFormat: "json", LogOutput: "discard"},
	}
}

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
				MinTokens:              25,
				MaxTokens:              16384,
				TargetCompressionRatio: 0.1,
				IncludeExpandHint:      true,
				EnableExpandContext:    true,
			},
			ToolDiscovery: config.ToolDiscoveryPipeConfig{
				Enabled:              true,
				Strategy:             "tool-search",
				FallbackStrategy:     "passthrough",
				EnableSearchFallback: true,
				MaxSearchResults:     5,
			},
		},
		Store:      config.StoreConfig{Type: "memory", TTL: 1 * time.Hour},
		Monitoring: config.MonitoringConfig{LogLevel: "disabled", LogFormat: "json", LogOutput: "discard"},
	}
}

// =============================================================================
// RESPONSE BUILDERS (phantom-tools specific)
// =============================================================================

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
		"usage":       map[string]interface{}{"input_tokens": 100, "output_tokens": 50},
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
			"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150,
		},
	}
	data, _ := json.Marshal(resp)
	return data
}

// =============================================================================
// REQUEST BUILDERS (phantom-tools specific)
// =============================================================================

func anthropicRequestWithToolResult(toolOutput string) map[string]interface{} {
	return map[string]interface{}{
		"model":      "claude-3-haiku-20240307",
		"max_tokens": 500,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What are the key points?"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "tool_use", "id": "toolu_test_001", "name": "read_file", "input": map[string]string{"path": "system.log"}},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "tool_result", "tool_use_id": "toolu_test_001", "content": toolOutput},
				},
			},
		},
	}
}

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
