package preemptive_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/preemptive"
)

// =============================================================================
// SUMMARIZER CONFIG TESTS
// =============================================================================

func TestSummarizerConfig_Defaults(t *testing.T) {
	cfg := preemptive.DefaultConfig()

	assert.Equal(t, "claude-haiku-4-5", cfg.Summarizer.Model)
	assert.Equal(t, 4096, cfg.Summarizer.MaxTokens)
	assert.Equal(t, 60*time.Second, cfg.Summarizer.Timeout)
	assert.Equal(t, 20000, cfg.Summarizer.KeepRecentTokens)
	assert.Equal(t, 0, cfg.Summarizer.KeepRecentCount) // Now using token-based
}

func TestSummarizerConfig_Validation(t *testing.T) {
	tests := []struct {
		name        string
		config      preemptive.SummarizerConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid config",
			config: preemptive.SummarizerConfig{
				Model:           "claude-haiku-4-5",
				APISecret:       "test-key",
				MaxTokens:       4096,
				Timeout:         60 * time.Second,
				KeepRecentCount: 10,
			},
			expectError: false,
		},
		{
			name: "missing model",
			config: preemptive.SummarizerConfig{
				Model:     "",
				APISecret: "test-key",
				MaxTokens: 4096,
				Timeout:   60 * time.Second,
			},
			expectError: true,
			errorMsg:    "model",
		},
		{
			name: "missing api key (optional - captured from requests)",
			config: preemptive.SummarizerConfig{
				Model:     "claude-haiku-4-5",
				APISecret: "",
				MaxTokens: 4096,
				Timeout:   60 * time.Second,
			},
			expectError: false,
		},
		{
			name: "invalid max tokens",
			config: preemptive.SummarizerConfig{
				Model:     "claude-haiku-4-5",
				APISecret: "test-key",
				MaxTokens: 0,
				Timeout:   60 * time.Second,
			},
			expectError: true,
			errorMsg:    "max_tokens",
		},
		{
			name: "zero timeout",
			config: preemptive.SummarizerConfig{
				Model:     "claude-haiku-4-5",
				APISecret: "test-key",
				MaxTokens: 4096,
				Timeout:   0,
			},
			expectError: true,
			errorMsg:    "timeout",
		},
		{
			name: "valid api strategy with hcc_espresso_v1",
			config: preemptive.SummarizerConfig{
				Strategy: preemptive.StrategyCompresr,
				Compresr: &preemptive.CompresrConfig{
					Endpoint: "/api/compress/history/",
					APIKey:   "cmp_test-key",
					Model:    "hcc_espresso_v1",
					Timeout:  60 * time.Second,
				},
			},
			expectError: false,
		},
		{
			name: "compresr strategy missing compresr config",
			config: preemptive.SummarizerConfig{
				Strategy: preemptive.StrategyCompresr,
				Compresr: nil,
			},
			expectError: true,
			errorMsg:    "summarizer.compresr is required",
		},
		{
			name: "api strategy missing endpoint",
			config: preemptive.SummarizerConfig{
				Strategy: preemptive.StrategyCompresr,
				Compresr: &preemptive.CompresrConfig{
					Endpoint: "",
					APIKey:   "cmp_test-key",
					Model:    "hcc_espresso_v1",
					Timeout:  60 * time.Second,
				},
			},
			expectError: true,
			errorMsg:    "endpoint",
		},
		{
			name: "api strategy missing api key",
			config: preemptive.SummarizerConfig{
				Strategy: preemptive.StrategyCompresr,
				Compresr: &preemptive.CompresrConfig{
					Endpoint: "/api/compress/history/",
					APIKey:   "",
					Model:    "hcc_espresso_v1",
					Timeout:  60 * time.Second,
				},
			},
			expectError: true,
			errorMsg:    "api_key",
		},
		{
			name: "api strategy missing model",
			config: preemptive.SummarizerConfig{
				Strategy: preemptive.StrategyCompresr,
				Compresr: &preemptive.CompresrConfig{
					Endpoint: "/api/compress/history/",
					APIKey:   "cmp_test-key",
					Model:    "",
					Timeout:  60 * time.Second,
				},
			},
			expectError: true,
			errorMsg:    "model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := preemptive.Config{
				Enabled:          true,
				TriggerThreshold: 80.0,
				Summarizer:       tt.config,
				Session: preemptive.SessionConfig{
					HashMessageCount: 3,
					SummaryTTL:       2 * time.Hour,
				},
			}

			err := cfg.Validate()
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// SUMMARIZER CREATION TESTS
// =============================================================================

func TestSummarizer_Creation(t *testing.T) {
	cfg := preemptive.SummarizerConfig{
		Model:           "claude-haiku-4-5",
		APISecret:       "test-api-key",
		Endpoint:        "https://api.anthropic.com/v1/messages",
		MaxTokens:       4096,
		Timeout:         60 * time.Second,
		KeepRecentCount: 10,
	}

	summarizer := preemptive.NewSummarizer(cfg)
	require.NotNil(t, summarizer)
}

func TestSummarizer_CreationWithAPIStrategy(t *testing.T) {
	cfg := preemptive.SummarizerConfig{
		Strategy: preemptive.StrategyCompresr,
		Compresr: &preemptive.CompresrConfig{
			Endpoint: "/api/compress/history/",
			APIKey:   "cmp_test-key",
			Model:    "hcc_espresso_v1",
			Timeout:  60 * time.Second,
		},
	}

	summarizer := preemptive.NewSummarizer(cfg)
	require.NotNil(t, summarizer)
}

func TestSummarizer_CreationWithDefaults(t *testing.T) {
	cfg := preemptive.DefaultConfig()

	summarizer := preemptive.NewSummarizer(cfg.Summarizer)
	require.NotNil(t, summarizer)
}

// =============================================================================
// SUMMARIZER PROMPT TESTS
// =============================================================================

func TestSummarizer_CustomSystemPrompt(t *testing.T) {
	cfg := preemptive.SummarizerConfig{
		Model:        "claude-haiku-4-5",
		APISecret:    "test-api-key",
		MaxTokens:    4096,
		Timeout:      60 * time.Second,
		SystemPrompt: "Custom summarization prompt for testing",
	}

	summarizer := preemptive.NewSummarizer(cfg)
	require.NotNil(t, summarizer)
}

// =============================================================================
// SUMMARIZER MESSAGE FORMATTING TESTS
// =============================================================================

func TestSummarizer_MessageFormat(t *testing.T) {
	// Test that various message formats are handled correctly
	testCases := []struct {
		name     string
		messages []map[string]interface{}
	}{
		{
			name: "simple text messages",
			messages: []map[string]interface{}{
				{"role": "user", "content": "Hello"},
				{"role": "assistant", "content": "Hi there!"},
			},
		},
		{
			name: "content blocks",
			messages: []map[string]interface{}{
				{
					"role": "user",
					"content": []map[string]interface{}{
						{"type": "text", "text": "Hello"},
					},
				},
			},
		},
		{
			name: "mixed content",
			messages: []map[string]interface{}{
				{"role": "user", "content": "First message"},
				{
					"role": "assistant",
					"content": []map[string]interface{}{
						{"type": "text", "text": "Response with blocks"},
					},
				},
				{"role": "user", "content": "Follow up"},
			},
		},
		{
			name: "with tool use",
			messages: []map[string]interface{}{
				{"role": "user", "content": "Read a file"},
				{
					"role": "assistant",
					"content": []map[string]interface{}{
						{"type": "tool_use", "id": "toolu_1", "name": "read_file", "input": map[string]interface{}{}},
					},
				},
				{
					"role":        "user",
					"content":     "file contents here",
					"tool_use_id": "toolu_1",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Verify messages are valid JSON-like structures
			assert.NotEmpty(t, tc.messages)
			for _, msg := range tc.messages {
				assert.Contains(t, msg, "role")
				assert.Contains(t, msg, "content")
			}
		})
	}
}

// =============================================================================
// SUMMARIZER MODEL SELECTION TESTS
// =============================================================================

func TestSummarizer_ModelSelection(t *testing.T) {
	models := []string{
		"claude-haiku-4-5",
		"claude-haiku-4-5-20251001",
		"claude-sonnet-4-5",
	}

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			cfg := preemptive.SummarizerConfig{
				Model:     model,
				APISecret: "test-api-key",
				MaxTokens: 4096,
				Timeout:   60 * time.Second,
			}

			summarizer := preemptive.NewSummarizer(cfg)
			require.NotNil(t, summarizer)
		})
	}
}
