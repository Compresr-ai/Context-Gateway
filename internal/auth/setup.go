package auth

import (
	"os"
	"strings"

	"github.com/compresr/context-gateway/internal/adapters"
	authAnthropic "github.com/compresr/context-gateway/internal/auth/anthropic"
	authOpenAI "github.com/compresr/context-gateway/internal/auth/openai"
	"github.com/compresr/context-gateway/internal/auth/types"
	"github.com/compresr/context-gateway/internal/config"
)

// SetupRegistry creates and initializes an auth registry from gateway config.
func SetupRegistry(cfg *config.Config) (*Registry, error) {
	registry := NewRegistry()

	// Register all supported providers
	registry.Register(adapters.ProviderAnthropic, authAnthropic.New())
	registry.Register(adapters.ProviderOpenAI, authOpenAI.New())

	// Build auth configs from gateway config
	authConfigs := buildAuthConfigs(cfg)

	// Initialize all handlers
	if err := registry.Initialize(authConfigs); err != nil {
		return nil, err
	}

	return registry, nil
}

// buildAuthConfigs extracts auth configuration from gateway config.
// Scans all providers in the config and maps them to their corresponding
// auth handlers based on provider type detection.
func buildAuthConfigs(cfg *config.Config) map[adapters.Provider]types.AuthConfig {
	configs := make(map[adapters.Provider]types.AuthConfig)

	// Initialize with defaults (will be overwritten if provider configs exist)
	anthropicCfg := types.AuthConfig{Mode: types.AuthModeAPIKey}
	openaiCfg := types.AuthConfig{Mode: types.AuthModeAPIKey}

	// Scan all provider configs and categorize them
	for name, provCfg := range cfg.Providers {
		providerType := inferProviderType(name, provCfg)
		apiKey := resolveEnvVar(provCfg.APIKey)
		authMode := parseAuthFromConfig(provCfg.Auth, apiKey)

		switch providerType {
		case adapters.ProviderAnthropic:
			// Use the first anthropic provider found, or merge if multiple
			if anthropicCfg.APIKey == "" || authMode == types.AuthModeSubscription {
				anthropicCfg.APIKey = apiKey
				anthropicCfg.Mode = authMode
			}
		case adapters.ProviderOpenAI:
			// Use the first openai provider found, or merge if multiple
			if openaiCfg.APIKey == "" || authMode == types.AuthModeSubscription {
				openaiCfg.APIKey = apiKey
				openaiCfg.Mode = authMode
			}
		}
	}

	configs[adapters.ProviderAnthropic] = anthropicCfg
	configs[adapters.ProviderOpenAI] = openaiCfg

	return configs
}

// inferProviderType determines the provider type from the config name or settings.
// This allows flexible provider naming in configs (e.g., "semantic_summarization", "anthropic", etc.)
func inferProviderType(name string, cfg config.ProviderConfig) adapters.Provider {
	nameLower := strings.ToLower(name)

	// Direct name matches
	if strings.Contains(nameLower, "anthropic") || strings.Contains(nameLower, "claude") {
		return adapters.ProviderAnthropic
	}
	if strings.Contains(nameLower, "openai") || strings.Contains(nameLower, "gpt") {
		return adapters.ProviderOpenAI
	}

	// Infer from model name
	modelLower := strings.ToLower(cfg.Model)
	if strings.Contains(modelLower, "claude") {
		return adapters.ProviderAnthropic
	}
	if strings.Contains(modelLower, "gpt") || strings.Contains(modelLower, "o1") || strings.Contains(modelLower, "o3") {
		return adapters.ProviderOpenAI
	}

	// Infer from endpoint
	endpointLower := strings.ToLower(cfg.Endpoint)
	if strings.Contains(endpointLower, "anthropic.com") {
		return adapters.ProviderAnthropic
	}
	if strings.Contains(endpointLower, "openai.com") {
		return adapters.ProviderOpenAI
	}

	// Default to OpenAI for unknown providers (OpenAI-compatible is most common)
	return adapters.ProviderOpenAI
}

// parseAuthFromConfig converts config auth string to AuthMode.
func parseAuthFromConfig(authStr string, apiKey string) types.AuthMode {
	switch authStr {
	case "oauth", "subscription":
		if apiKey != "" {
			return types.AuthModeBoth // Has API key fallback
		}
		return types.AuthModeSubscription
	case "api_key":
		return types.AuthModeAPIKey
	case "both":
		return types.AuthModeBoth
	default:
		// Infer from API key presence: if API key exists, allow subscription fallback
		if apiKey != "" {
			return types.AuthModeBoth // Enables subscription -> API key fallback
		}
		return types.AuthModeSubscription
	}
}

// resolveEnvVar expands ${VAR:-default} syntax in config values.
func resolveEnvVar(value string) string {
	if !strings.HasPrefix(value, "${") {
		return value
	}

	// Parse ${VAR:-default} or ${VAR}
	content := strings.TrimPrefix(value, "${")
	content = strings.TrimSuffix(content, "}")

	var varName, defaultVal string
	if idx := strings.Index(content, ":-"); idx != -1 {
		varName = content[:idx]
		defaultVal = content[idx+2:]
	} else {
		varName = content
	}

	if envVal := os.Getenv(varName); envVal != "" {
		return envVal
	}
	return defaultVal
}
