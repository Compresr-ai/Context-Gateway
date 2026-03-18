// KV Cache Integration Tests - Setup
//
// These tests use httptest.NewServer mock LLM backends to test KV cache
// stability through the full gateway request cycle. No real LLM calls.
//
// Run with: go test ./tests/kv_cache/integration/... -v
package integration

import (
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

func newMockLLM(fn func([]byte, int) []byte) *mockLLM {
	return &mockLLM{testkit.NewMockLLM(fn)}
}

// =============================================================================
// GATEWAY / REQUEST HELPERS (delegated to testkit)
// =============================================================================

func createGateway(cfg *config.Config) *httptest.Server { return testkit.CreateGateway(cfg) }

var (
	sendAnthropicRequest  = testkit.SendAnthropicRequest
	anthropicTextResponse = testkit.AnthropicTextResponse
	largeToolOutput       = testkit.LargeToolOutput
	extractToolNames      = testkit.ExtractToolNames
	extractTools          = testkit.ExtractTools
	containsToolName      = testkit.ContainsToolName
)

func makeAnthropicToolDefs(n int) []map[string]interface{} { return testkit.MakeAnthropicToolDefs(n) }

// =============================================================================
// CONFIG BUILDERS
// =============================================================================

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

// =============================================================================
// REQUEST BUILDERS
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

func anthropicRequestNoToolResult() map[string]interface{} {
	return map[string]interface{}{
		"model":      "claude-3-haiku-20240307",
		"max_tokens": 500,
		"tools": []map[string]interface{}{
			{
				"name":        "read_file",
				"description": "Read a file from disk",
				"input_schema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"path": map[string]interface{}{"type": "string", "description": "The file path"},
					},
					"required": []string{"path"},
				},
			},
		},
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Help me read a file."},
		},
	}
}
