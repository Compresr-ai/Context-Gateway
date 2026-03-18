// Tool Discovery Integration Tests - Setup
//
// These tests use httptest.NewServer mock LLM backends to test tool discovery
// pipe behavior through the full gateway request cycle. No real LLM calls.
//
// Run with: go test ./tests/tool_discovery/integration/... -v
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
	sendAnthropicRequest      = testkit.SendAnthropicRequest
	anthropicTextResponse     = testkit.AnthropicTextResponse
	extractToolNames          = testkit.ExtractToolNames
	containsToolName          = testkit.ContainsToolName
	countEffectiveToolNames   = testkit.CountEffectiveToolNames
)

func makeAnthropicToolDefs(n int) []map[string]interface{} { return testkit.MakeAnthropicToolDefs(n) }

// =============================================================================
// CONFIG BUILDERS
// =============================================================================

func relevanceConfig(keepCount int) *config.Config {
	return relevanceConfigWithThreshold(keepCount, 1) // TokenThreshold=1: always trigger
}

func relevanceConfigWithThreshold(keepCount, tokenThreshold int) *config.Config {
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
				TokenThreshold:   tokenThreshold,
			},
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
				TokenThreshold:       1, // always trigger in tests
			},
		},
		Store:      config.StoreConfig{Type: "memory", TTL: 1 * time.Hour},
		Monitoring: config.MonitoringConfig{LogLevel: "disabled", LogFormat: "json", LogOutput: "discard"},
	}
}
