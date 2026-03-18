// Tool Output Integration Tests - Setup
//
// These tests use httptest.NewServer mock LLM backends to test tool output
// compression behavior through the full gateway request cycle. No real LLM calls.
//
// Run with: go test ./tests/tool_output/integration/... -v
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
	makeAnthropicToolDefs = testkit.MakeAnthropicToolDefs
	largeToolOutput       = testkit.LargeToolOutput
	extractToolNames      = testkit.ExtractToolNames
	containsToolName      = testkit.ContainsToolName
	extractMessages       = testkit.ExtractMessages
)

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

func highMinTokensConfig() *config.Config {
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
				MinTokens:              12500, // Very high threshold — small outputs won't be compressed
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

func anthropicRequestWithToolResult(toolName, toolOutput string) map[string]interface{} {
	return map[string]interface{}{
		"model":      "claude-3-haiku-20240307",
		"max_tokens": 500,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What are the key points?"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "tool_use", "id": "toolu_test_001", "name": toolName, "input": map[string]string{"path": "system.log"}},
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

// anthropicRequestWithToolResultNoUserText creates a request where the only
// user message is embedded in the tool result flow (no standalone text message).
// This tests the query fallback behavior.
func anthropicRequestWithToolResultNoUserText(toolName, toolOutput string) map[string]interface{} {
	return map[string]interface{}{
		"model":      "claude-3-haiku-20240307",
		"max_tokens": 500,
		"messages": []map[string]interface{}{
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "tool_use", "id": "toolu_test_001", "name": toolName, "input": map[string]string{"path": "data.json"}},
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
