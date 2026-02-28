package unit

// Pipe Unit Tests
//
// Tests basic Pipe struct functionality: Name(), Strategy(), Enabled(), Close().
// These are simple getter/setter tests that verify the pipe initializes correctly
// from config and returns the expected values. Also tests cleanup via Close().

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/compresr/context-gateway/internal/config"
	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
	"github.com/compresr/context-gateway/tests/anthropic/fixtures"
)

// TestPipeName verifies Name() returns "tool_output".
func TestPipeName(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyCompresr, 100, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())
	assert.Equal(t, "tool_output", pipe.Name())
}

// TestPipeStrategy verifies Strategy() returns configured strategy.
func TestPipeStrategy(t *testing.T) {
	testCases := []struct {
		name     string
		strategy string
	}{
		{"passthrough", config.StrategyPassthrough},
		{"api", config.StrategyCompresr},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fixtures.TestConfig(tc.strategy, 100, true)
			pipe := tooloutput.New(cfg, fixtures.TestStore())
			assert.Equal(t, tc.strategy, pipe.Strategy())
		})
	}
}

// TestPipeEnabled verifies Enabled() returns config.Enabled value.
func TestPipeEnabled(t *testing.T) {
	cfgEnabled := fixtures.TestConfig(config.StrategyCompresr, 100, true)
	pipeEnabled := tooloutput.New(cfgEnabled, fixtures.TestStore())
	assert.True(t, pipeEnabled.Enabled())

	cfgDisabled := fixtures.DisabledConfig()
	pipeDisabled := tooloutput.New(cfgDisabled, fixtures.TestStore())
	assert.False(t, pipeDisabled.Enabled())
}

// TestPipeClose verifies Close() cleans up resources.
func TestPipeClose(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyCompresr, 100, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())
	// Close should not panic
	pipe.Close()
}

// TestPipeMetrics verifies metrics are tracked.
func TestPipeMetrics(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyCompresr, 100, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())
	metrics := pipe.GetMetrics()
	assert.NotNil(t, metrics)
	assert.GreaterOrEqual(t, metrics.CacheHits, int64(0))
}

// TestPipeQueryAgnostic verifies IsQueryAgnostic() returns config value.
func TestPipeQueryAgnostic(t *testing.T) {
	testCases := []struct {
		name          string
		config        *config.Config
		queryAgnostic bool
	}{
		{
			name:          "cmprsr_model_is_query_agnostic",
			config:        fixtures.CmprsrConfig(),
			queryAgnostic: true,
		},
		{
			name:          "openai_model_is_query_agnostic",
			config:        fixtures.OpenAIConfig(),
			queryAgnostic: true,
		},
		{
			name:          "reranker_model_is_NOT_query_agnostic",
			config:        fixtures.RerankerConfig(),
			queryAgnostic: false,
		},
		{
			name:          "custom_query_agnostic_true",
			config:        fixtures.TestConfigWithModelAndQuery(config.StrategyCompresr, "custom_model", 256, false, true),
			queryAgnostic: true,
		},
		{
			name:          "custom_query_agnostic_false",
			config:        fixtures.TestConfigWithModelAndQuery(config.StrategyCompresr, "custom_model", 256, false, false),
			queryAgnostic: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pipe := tooloutput.New(tc.config, fixtures.TestStore())
			assert.Equal(t, tc.queryAgnostic, pipe.IsQueryAgnostic(),
				"IsQueryAgnostic() should return %v for %s", tc.queryAgnostic, tc.name)
		})
	}
}
