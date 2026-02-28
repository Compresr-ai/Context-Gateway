package fixtures

import (
	"io"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
	"github.com/compresr/context-gateway/internal/store"
)

func init() {
	// Silence logs during tests
	zerolog.SetGlobalLevel(zerolog.Disabled)
	log.Logger = zerolog.New(io.Discard)
}

// TestRegistry creates a registry with all adapters for testing.
func TestRegistry() *adapters.Registry {
	return adapters.NewRegistry()
}

// TestPipeContext creates a PipeContext for testing with OpenAI adapter.
func TestPipeContext(requestBody []byte) *pipes.PipeContext {
	registry := TestRegistry()
	adapter := registry.Get("openai")
	return pipes.NewPipeContext(adapter, requestBody)
}

// TestPipeContextAnthropic creates a PipeContext for testing with Anthropic adapter.
func TestPipeContextAnthropic(requestBody []byte) *pipes.PipeContext {
	registry := TestRegistry()
	adapter := registry.Get("anthropic")
	return pipes.NewPipeContext(adapter, requestBody)
}

// TestConfig creates a config for testing
func TestConfig(strategy string, minBytes int, enableExpand bool) *config.Config {
	return &config.Config{
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:             true,
				Strategy:            strategy,
				FallbackStrategy:    config.StrategyPassthrough,
				MinBytes:            minBytes,
				TargetRatio:         0.5,
				IncludeExpandHint:   enableExpand,
				EnableExpandContext: enableExpand,
				Compresr: config.CompresrConfig{
					Endpoint: "/api/compress",
					Timeout:  5 * time.Second,
				},
			},
		},
		URLs: config.URLsConfig{
			Compresr: "http://localhost:18080",
		},
	}
}

// PassthroughConfig creates a passthrough-only config
func PassthroughConfig() *config.Config {
	return TestConfig(config.StrategyPassthrough, 256, false)
}

// CompresrCompressionConfig creates a Compresr API compression config
func CompresrCompressionConfig() *config.Config {
	return TestConfig(config.StrategyCompresr, 256, true)
}

// DisabledConfig creates a config with tool_output disabled
func DisabledConfig() *config.Config {
	cfg := TestConfig(config.StrategyPassthrough, 256, false)
	cfg.Pipes.ToolOutput.Enabled = false
	return cfg
}

// TestConfigWithModel creates a config for testing with a specific model
func TestConfigWithModel(strategy string, model string, minBytes int, enableExpand bool) *config.Config {
	return TestConfigWithModelAndQuery(strategy, model, minBytes, enableExpand, true) // Default: query agnostic
}

// TestConfigWithModelAndQuery creates a config for testing with a specific model and query agnostic setting
func TestConfigWithModelAndQuery(strategy string, model string, minBytes int, enableExpand bool, queryAgnostic bool) *config.Config {
	return &config.Config{
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:             true,
				Strategy:            strategy,
				FallbackStrategy:    config.StrategyPassthrough,
				MinBytes:            minBytes,
				TargetRatio:         0.5,
				IncludeExpandHint:   enableExpand,
				EnableExpandContext: enableExpand,
				Compresr: config.CompresrConfig{
					Endpoint:      "/api/compress/tool-output",
					Model:         model,
					Timeout:       5 * time.Second,
					QueryAgnostic: queryAgnostic,
				},
			},
		},
		URLs: config.URLsConfig{
			Compresr: "http://localhost:18080",
		},
	}
}

// CmprsrConfig creates a config using toc_espresso_v1 model (query agnostic)
func CmprsrConfig() *config.Config {
	return TestConfigWithModelAndQuery(config.StrategyCompresr, "toc_espresso_v1", 256, true, true)
}

// OpenAIConfig creates a config using toc_espresso_v1 model (query agnostic)
func OpenAIConfig() *config.Config {
	return TestConfigWithModelAndQuery(config.StrategyCompresr, "toc_espresso_v1", 256, true, true)
}

// RerankerConfig creates a config using toc_latte_v1 model (query specific - needs user query)
func RerankerConfig() *config.Config {
	return TestConfigWithModelAndQuery(config.StrategyCompresr, "toc_latte_v1", 256, true, false)
}

// TestStore creates a memory store for testing
func TestStore() store.Store {
	return store.NewMemoryStore(5 * time.Minute)
}

func PreloadedStore(entries map[string]string) store.Store {
	st := store.NewMemoryStore(5 * time.Minute)
	for k, v := range entries {
		_ = st.Set(k, v)
	}
	return st
}

func PreloadedStoreWithCompressed(original, compressed map[string]string) store.Store {
	st := store.NewMemoryStore(5 * time.Minute)
	for k, v := range original {
		_ = st.Set(k, v)
	}
	for k, v := range compressed {
		_ = st.SetCompressed(k, v)
	}
	return st
}
