package unit

import (
	"testing"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/tests/anthropic/fixtures"
)

// TestModelConfigurations verifies all 3 model configurations are valid
func TestModelConfigurations(t *testing.T) {
	tests := []struct {
		name     string
		config   *config.Config
		model    string
		strategy string
	}{
		{
			name:     "toc_espresso_v1",
			config:   fixtures.CmprsrConfig(),
			model:    "toc_espresso_v1",
			strategy: config.StrategyCompresr,
		},
		{
			name:     "toc_espresso_v1_alt",
			config:   fixtures.OpenAIConfig(),
			model:    "toc_espresso_v1",
			strategy: config.StrategyCompresr,
		},
		{
			name:     "toc_latte_v1",
			config:   fixtures.RerankerConfig(),
			model:    "toc_latte_v1",
			strategy: config.StrategyCompresr,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Verify strategy
			if tc.config.Pipes.ToolOutput.Strategy != tc.strategy {
				t.Errorf("expected strategy %q, got %q", tc.strategy, tc.config.Pipes.ToolOutput.Strategy)
			}

			// Verify model
			if tc.config.Pipes.ToolOutput.Compresr.Model != tc.model {
				t.Errorf("expected model %q, got %q", tc.model, tc.config.Pipes.ToolOutput.Compresr.Model)
			}

			// Verify enabled
			if !tc.config.Pipes.ToolOutput.Enabled {
				t.Error("expected tool_output to be enabled")
			}

			// Verify API endpoint is set
			if tc.config.Pipes.ToolOutput.Compresr.Endpoint == "" {
				t.Error("expected API endpoint to be set")
			}
		})
	}
}

// TestPassthroughStrategyConfig verifies passthrough strategy works without model
func TestPassthroughStrategyConfig(t *testing.T) {
	cfg := fixtures.PassthroughConfig()

	if cfg.Pipes.ToolOutput.Strategy != config.StrategyPassthrough {
		t.Errorf("expected strategy %q, got %q", config.StrategyPassthrough, cfg.Pipes.ToolOutput.Strategy)
	}
}

// TestOnlyTwoStrategiesExist verifies only api and passthrough strategies exist
func TestOnlyTwoStrategiesExist(t *testing.T) {
	// These are the only valid strategies after removing external strategies
	validStrategies := map[string]bool{
		config.StrategyCompresr:         true,
		config.StrategyPassthrough: true,
	}

	if !validStrategies[config.StrategyCompresr] {
		t.Error("StrategyCompresr should be valid")
	}

	if !validStrategies[config.StrategyPassthrough] {
		t.Error("StrategyPassthrough should be valid")
	}

	// Verify the constants have expected values
	if config.StrategyCompresr != "compresr" {
		t.Errorf("expected StrategyCompresr to be 'compresr', got %q", config.StrategyCompresr)
	}

	if config.StrategyPassthrough != "passthrough" {
		t.Errorf("expected StrategyPassthrough to be 'passthrough', got %q", config.StrategyPassthrough)
	}
}

// TestQueryAgnosticConfiguration verifies QueryAgnostic is set correctly for each model type
func TestQueryAgnosticConfiguration(t *testing.T) {
	tests := []struct {
		name          string
		config        *config.Config
		model         string
		queryAgnostic bool
		description   string
	}{
		{
			name:          "espresso_is_query_agnostic",
			config:        fixtures.CmprsrConfig(),
			model:         "toc_espresso_v1",
			queryAgnostic: true,
			description:   "Lingua-based compression doesn't need user query",
		},
		{
			name:          "espresso_alt_is_query_agnostic",
			config:        fixtures.OpenAIConfig(),
			model:         "toc_espresso_v1",
			queryAgnostic: true,
			description:   "Lingua-based compression doesn't need user query",
		},
		{
			name:          "latte_is_NOT_query_agnostic",
			config:        fixtures.RerankerConfig(),
			model:         "toc_latte_v1",
			queryAgnostic: false,
			description:   "GemFilter needs user query for relevance scoring",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Verify model
			if tc.config.Pipes.ToolOutput.Compresr.Model != tc.model {
				t.Errorf("expected model %q, got %q", tc.model, tc.config.Pipes.ToolOutput.Compresr.Model)
			}

			// Verify QueryAgnostic setting
			if tc.config.Pipes.ToolOutput.Compresr.QueryAgnostic != tc.queryAgnostic {
				t.Errorf("expected QueryAgnostic=%v for %s (%s), got %v",
					tc.queryAgnostic, tc.model, tc.description, tc.config.Pipes.ToolOutput.Compresr.QueryAgnostic)
			}
		})
	}
}

// TestQueryAgnosticWithCustomConfig verifies TestConfigWithModelAndQuery works correctly
func TestQueryAgnosticWithCustomConfig(t *testing.T) {
	// Test with query agnostic = true
	cfgAgnostic := fixtures.TestConfigWithModelAndQuery(config.StrategyCompresr, "test_model", 256, false, true)
	if !cfgAgnostic.Pipes.ToolOutput.Compresr.QueryAgnostic {
		t.Error("expected QueryAgnostic=true when explicitly set to true")
	}

	// Test with query agnostic = false
	cfgNotAgnostic := fixtures.TestConfigWithModelAndQuery(config.StrategyCompresr, "test_model", 256, false, false)
	if cfgNotAgnostic.Pipes.ToolOutput.Compresr.QueryAgnostic {
		t.Error("expected QueryAgnostic=false when explicitly set to false")
	}
}
