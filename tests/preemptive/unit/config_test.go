package preemptive_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/preemptive"
)

// =============================================================================
// CONFIG VALIDATION TESTS
// =============================================================================

func TestConfig_Validate_Disabled(t *testing.T) {
	cfg := preemptive.Config{
		Enabled: false,
	}

	err := cfg.Validate()
	assert.NoError(t, err, "disabled config should not require validation")
}

func TestConfig_Validate_ValidConfig(t *testing.T) {
	cfg := preemptive.Config{
		Enabled:          true,
		TriggerThreshold: 80.0,
		Summarizer: preemptive.SummarizerConfig{
			Model:           "claude-haiku-4-5",
			APISecret:       "test-api-key",
			MaxTokens:       4096,
			Timeout:         60 * time.Second,
			KeepRecentCount: 10,
		},
		Session: preemptive.SessionConfig{
			SummaryTTL:       2 * time.Hour,
			HashMessageCount: 3,
		},
	}

	err := cfg.Validate()
	assert.NoError(t, err)
}

func TestConfig_Validate_InvalidThreshold(t *testing.T) {
	tests := []struct {
		name      string
		threshold float64
		expectErr bool
	}{
		{"zero", 0, true},
		{"negative", -10, true},
		{"over_100", 150, true},
		{"valid_low", 0.1, false},
		{"valid_80", 80.0, false},
		{"valid_100", 100.0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.TriggerThreshold = tt.threshold

			err := cfg.Validate()
			if tt.expectErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "trigger_threshold")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestConfig_Validate_MissingSummarizerModel(t *testing.T) {
	cfg := validConfig()
	cfg.Summarizer.Model = ""

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model is required")
}

func TestConfig_Validate_MissingAPIKey(t *testing.T) {
	cfg := validConfig()
	cfg.Summarizer.APISecret = ""

	// API key is optional - can be captured from incoming requests (Max/Pro/Teams users)
	err := cfg.Validate()
	assert.NoError(t, err)
}

func TestConfig_Validate_InvalidMaxTokens(t *testing.T) {
	cfg := validConfig()
	cfg.Summarizer.MaxTokens = 0

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_tokens")
}

func TestConfig_Validate_InvalidTimeout(t *testing.T) {
	cfg := validConfig()
	cfg.Summarizer.Timeout = 0

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
}

func TestConfig_Validate_InvalidSessionTTL(t *testing.T) {
	cfg := validConfig()
	cfg.Session.SummaryTTL = 0

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "summary_ttl")
}

func TestConfig_Validate_InvalidHashMessageCount(t *testing.T) {
	cfg := validConfig()
	cfg.Session.HashMessageCount = 0

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hash_message_count")
}

func TestDefaultConfig(t *testing.T) {
	cfg := preemptive.DefaultConfig()

	// Should be disabled by default
	assert.False(t, cfg.Enabled)

	// Check default values
	assert.Equal(t, 80.0, cfg.TriggerThreshold)
	assert.Equal(t, "claude-haiku-4-5", cfg.Summarizer.Model)
	assert.Equal(t, 4096, cfg.Summarizer.MaxTokens)
	assert.Equal(t, 60*time.Second, cfg.Summarizer.Timeout)
	assert.Equal(t, 20000, cfg.Summarizer.KeepRecentTokens)
	assert.Equal(t, 0, cfg.Summarizer.KeepRecentCount) // Now using token-based
	assert.Equal(t, 2*time.Hour, cfg.Session.SummaryTTL)
	assert.Equal(t, 3, cfg.Session.HashMessageCount)

	// Claude Code detector should be enabled by default
	assert.True(t, cfg.Detectors.ClaudeCode.Enabled)

	// Generic detector should be enabled
	assert.True(t, cfg.Detectors.Generic.Enabled)
	assert.Equal(t, "X-Request-Compaction", cfg.Detectors.Generic.HeaderName)
}

// =============================================================================
// HELPERS
// =============================================================================

func validConfig() preemptive.Config {
	return preemptive.Config{
		Enabled:          true,
		TriggerThreshold: 80.0,
		Summarizer: preemptive.SummarizerConfig{
			Model:           "claude-haiku-4-5",
			APISecret:       "test-api-key",
			MaxTokens:       4096,
			Timeout:         60 * time.Second,
			KeepRecentCount: 10,
		},
		Session: preemptive.SessionConfig{
			SummaryTTL:       2 * time.Hour,
			HashMessageCount: 3,
		},
	}
}
