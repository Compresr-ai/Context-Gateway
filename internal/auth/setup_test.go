package auth

import (
	"testing"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/auth/types"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInferProviderType(t *testing.T) {
	tests := []struct {
		name           string
		providerName   string
		providerConfig config.ProviderConfig
		expected       adapters.Provider
	}{
		{
			name:         "Direct anthropic name match",
			providerName: "anthropic",
			providerConfig: config.ProviderConfig{
				Model: "claude-haiku-4-5",
			},
			expected: adapters.ProviderAnthropic,
		},
		{
			name:         "Claude in name",
			providerName: "claude_summarization",
			providerConfig: config.ProviderConfig{
				Model: "claude-sonnet-4",
			},
			expected: adapters.ProviderAnthropic,
		},
		{
			name:         "Claude in model name",
			providerName: "semantic_summarization",
			providerConfig: config.ProviderConfig{
				Model: "claude-haiku-4-5",
			},
			expected: adapters.ProviderAnthropic,
		},
		{
			name:         "Anthropic endpoint",
			providerName: "my_provider",
			providerConfig: config.ProviderConfig{
				Model:    "claude-3",
				Endpoint: "https://api.anthropic.com/v1/messages",
			},
			expected: adapters.ProviderAnthropic,
		},
		{
			name:         "Direct openai name match",
			providerName: "openai",
			providerConfig: config.ProviderConfig{
				Model: "gpt-4o",
			},
			expected: adapters.ProviderOpenAI,
		},
		{
			name:         "GPT in model name",
			providerName: "llm_provider",
			providerConfig: config.ProviderConfig{
				Model: "gpt-4o-mini",
			},
			expected: adapters.ProviderOpenAI,
		},
		{
			name:         "O1 model",
			providerName: "reasoning_model",
			providerConfig: config.ProviderConfig{
				Model: "o1-preview",
			},
			expected: adapters.ProviderOpenAI,
		},
		{
			name:         "OpenAI endpoint",
			providerName: "custom",
			providerConfig: config.ProviderConfig{
				Model:    "gpt-4",
				Endpoint: "https://api.openai.com/v1/chat/completions",
			},
			expected: adapters.ProviderOpenAI,
		},
		{
			name:         "Unknown defaults to OpenAI",
			providerName: "unknown_provider",
			providerConfig: config.ProviderConfig{
				Model: "some-model",
			},
			expected: adapters.ProviderOpenAI,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := inferProviderType(tt.providerName, tt.providerConfig)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildAuthConfigs(t *testing.T) {
	tests := []struct {
		name                  string
		providers             config.ProvidersConfig
		expectedAnthropicKey  string
		expectedAnthropicMode types.AuthMode
		expectedOpenAIKey     string
		expectedOpenAIMode    types.AuthMode
	}{
		{
			name: "Standard provider names",
			providers: config.ProvidersConfig{
				"anthropic": {
					APIKey: "sk-ant-test",
					Model:  "claude-haiku-4-5",
				},
				"openai": {
					APIKey: "sk-openai-test",
					Model:  "gpt-4o",
				},
			},
			expectedAnthropicKey:  "sk-ant-test",
			expectedAnthropicMode: types.AuthModeBoth,
			expectedOpenAIKey:     "sk-openai-test",
			expectedOpenAIMode:    types.AuthModeBoth,
		},
		{
			name: "Custom provider names with claude model",
			providers: config.ProvidersConfig{
				"semantic_summarization": {
					APIKey: "sk-ant-custom",
					Model:  "claude-haiku-4-5",
				},
			},
			expectedAnthropicKey:  "sk-ant-custom",
			expectedAnthropicMode: types.AuthModeBoth,
			expectedOpenAIKey:     "",
			expectedOpenAIMode:    types.AuthModeAPIKey,
		},
		{
			name: "OAuth mode for anthropic",
			providers: config.ProvidersConfig{
				"anthropic": {
					Auth:  "oauth",
					Model: "claude-haiku-4-5",
				},
			},
			expectedAnthropicKey:  "",
			expectedAnthropicMode: types.AuthModeSubscription,
			expectedOpenAIKey:     "",
			expectedOpenAIMode:    types.AuthModeAPIKey,
		},
		{
			name: "Both mode with API key fallback",
			providers: config.ProvidersConfig{
				"openai": {
					Auth:   "subscription",
					APIKey: "sk-fallback",
					Model:  "gpt-4o",
				},
			},
			expectedAnthropicKey:  "",
			expectedAnthropicMode: types.AuthModeAPIKey,
			expectedOpenAIKey:     "sk-fallback",
			expectedOpenAIMode:    types.AuthModeBoth,
		},
		{
			name: "Multiple providers - first wins",
			providers: config.ProvidersConfig{
				"anthropic_1": {
					APIKey: "sk-ant-first",
					Model:  "claude-haiku-4-5",
				},
				"anthropic_2": {
					APIKey: "sk-ant-second",
					Model:  "claude-sonnet-4",
				},
			},
			expectedAnthropicKey:  "sk-ant-first",
			expectedAnthropicMode: types.AuthModeBoth,
			expectedOpenAIKey:     "",
			expectedOpenAIMode:    types.AuthModeAPIKey,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Providers: tt.providers,
			}

			configs := buildAuthConfigs(cfg)

			// Check Anthropic config
			anthropicCfg, ok := configs[adapters.ProviderAnthropic]
			require.True(t, ok, "Anthropic config should always be present")
			assert.Equal(t, tt.expectedAnthropicKey, anthropicCfg.APIKey)
			assert.Equal(t, tt.expectedAnthropicMode, anthropicCfg.Mode)

			// Check OpenAI config
			openaiCfg, ok := configs[adapters.ProviderOpenAI]
			require.True(t, ok, "OpenAI config should always be present")
			assert.Equal(t, tt.expectedOpenAIKey, openaiCfg.APIKey)
			assert.Equal(t, tt.expectedOpenAIMode, openaiCfg.Mode)
		})
	}
}

func TestSetupRegistry(t *testing.T) {
	t.Run("Registry initializes with providers", func(t *testing.T) {
		cfg := &config.Config{
			Providers: config.ProvidersConfig{
				"anthropic": {
					APIKey: "sk-ant-test",
					Model:  "claude-haiku-4-5",
				},
				"openai": {
					APIKey: "sk-openai-test",
					Model:  "gpt-4o",
				},
			},
		}

		registry, err := SetupRegistry(cfg)
		require.NoError(t, err)
		require.NotNil(t, registry)

		// Verify handlers are registered
		anthropicHandler := registry.Get(adapters.ProviderAnthropic)
		require.NotNil(t, anthropicHandler)
		assert.Equal(t, "anthropic", anthropicHandler.Name())

		openaiHandler := registry.Get(adapters.ProviderOpenAI)
		require.NotNil(t, openaiHandler)
		assert.Equal(t, "openai", openaiHandler.Name())
	})

	t.Run("Registry works with custom provider names", func(t *testing.T) {
		cfg := &config.Config{
			Providers: config.ProvidersConfig{
				"semantic_summarization": {
					APIKey: "sk-ant-test",
					Model:  "claude-haiku-4-5",
				},
			},
		}

		registry, err := SetupRegistry(cfg)
		require.NoError(t, err)

		// Should still have anthropic handler configured
		anthropicHandler := registry.Get(adapters.ProviderAnthropic)
		require.NotNil(t, anthropicHandler)
		assert.Equal(t, types.AuthModeBoth, anthropicHandler.GetAuthMode())
	})
}

func TestParseAuthFromConfig(t *testing.T) {
	tests := []struct {
		name     string
		authStr  string
		apiKey   string
		expected types.AuthMode
	}{
		{
			name:     "Explicit oauth without API key",
			authStr:  "oauth",
			apiKey:   "",
			expected: types.AuthModeSubscription,
		},
		{
			name:     "Explicit oauth with API key fallback",
			authStr:  "oauth",
			apiKey:   "sk-ant-test",
			expected: types.AuthModeBoth,
		},
		{
			name:     "Subscription mode without API key",
			authStr:  "subscription",
			apiKey:   "",
			expected: types.AuthModeSubscription,
		},
		{
			name:     "Subscription mode with API key",
			authStr:  "subscription",
			apiKey:   "sk-test",
			expected: types.AuthModeBoth,
		},
		{
			name:     "Explicit api_key mode",
			authStr:  "api_key",
			apiKey:   "sk-test",
			expected: types.AuthModeAPIKey,
		},
		{
			name:     "Explicit both mode",
			authStr:  "both",
			apiKey:   "",
			expected: types.AuthModeBoth,
		},
		{
			name:     "Empty auth with API key defaults to both",
			authStr:  "",
			apiKey:   "sk-test",
			expected: types.AuthModeBoth,
		},
		{
			name:     "Empty auth without API key defaults to subscription",
			authStr:  "",
			apiKey:   "",
			expected: types.AuthModeSubscription,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseAuthFromConfig(tt.authStr, tt.apiKey)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestResolveEnvVar(t *testing.T) {
	// Set test env var
	t.Setenv("TEST_API_KEY", "test-key-value")

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "No env var syntax",
			input:    "plain-text",
			expected: "plain-text",
		},
		{
			name:     "Env var with value",
			input:    "${TEST_API_KEY}",
			expected: "test-key-value",
		},
		{
			name:     "Env var missing uses default",
			input:    "${MISSING_VAR:-default-value}",
			expected: "default-value",
		},
		{
			name:     "Env var present ignores default",
			input:    "${TEST_API_KEY:-ignored}",
			expected: "test-key-value",
		},
		{
			name:     "Empty string stays empty",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolveEnvVar(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
