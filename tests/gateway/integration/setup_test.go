// Gateway Integration Tests - Setup
//
// These tests use httptest.NewServer mock LLM backends to test core gateway
// behavior: parallel pipes, provider auto-detection, graceful degradation.
//
// Run with: go test ./tests/gateway/integration/... -v
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

func newMockLLM(fn func([]byte, int) []byte) *mockLLM {
	return &mockLLM{testkit.NewMockLLM(fn)}
}

// newMockLLMWithStatus creates a mock LLM that returns a specific HTTP status code.
func newMockLLMWithStatus(status int, fn func([]byte, int) []byte) *mockLLM {
	return &mockLLM{testkit.NewMockLLMWithStatus(status, fn)}
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
	extractToolNames        = testkit.ExtractToolNames
	containsToolName        = testkit.ContainsToolName
	countEffectiveToolNames = testkit.CountEffectiveToolNames
	extractMessages         = testkit.ExtractMessages
)

func makeAnthropicToolDefs(n int) []map[string]interface{} { return testkit.MakeAnthropicToolDefs(n) }

// =============================================================================
// RESPONSE BUILDERS (gateway-specific)
// =============================================================================

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
