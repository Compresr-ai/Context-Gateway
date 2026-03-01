// Provider configuration - URL constants and detection rules.
//
// DESIGN: Centralized provider configuration for URL routing.
// Add new providers by extending the Providers map.
package gateway

import (
	"os"
	"strings"
)

// ProviderConfig defines URL and detection rules for a provider.
type ProviderConfig struct {
	Name        string   // Provider identifier (e.g., "anthropic", "openai")
	BaseURL     string   // Base API URL
	DefaultPath string   // Default endpoint path
	Paths       []string // Paths that identify this provider
}

// envOrDefault returns the environment variable value if set, otherwise the default.
func envOrDefault(envVar, defaultVal string) string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	return defaultVal
}

// Providers maps provider names to their configurations.
var Providers = map[string]ProviderConfig{
	"anthropic": {
		Name:        "anthropic",
		BaseURL:     envOrDefault("ANTHROPIC_PROVIDER_URL", "https://api.anthropic.com"),
		DefaultPath: "/v1/messages",
		Paths:       []string{"/v1/messages", "/api/event_logging", "/api/telemetry", "/api/analytics"},
	},
	"openai": {
		Name:        "openai",
		BaseURL:     envOrDefault("OPENAI_PROVIDER_URL", "https://api.openai.com"),
		DefaultPath: "/v1/chat/completions",
		Paths:       []string{"/v1/chat/completions", "/v1/completions", "/chat/completions", "/v1/responses", "/responses"},
	},
	"gemini": {
		Name:        "gemini",
		BaseURL:     envOrDefault("GEMINI_PROVIDER_URL", "https://generativelanguage.googleapis.com"),
		DefaultPath: "",
		Paths:       []string{},
	},
	"bedrock": {
		Name:        "bedrock",
		BaseURL:     envOrDefault("BEDROCK_PROVIDER_URL", "https://bedrock-runtime."+envOrDefault("AWS_REGION", envOrDefault("AWS_DEFAULT_REGION", "us-east-1"))+".amazonaws.com"),
		DefaultPath: "/model/anthropic.claude-sonnet-4-20250514-v1:0/invoke",
		Paths:       []string{"/model/"},
	},
	"ollama": {
		Name:        "ollama",
		BaseURL:     envOrDefault("OLLAMA_PROVIDER_URL", "http://localhost:11434"),
		DefaultPath: "/api/chat",
		Paths:       []string{"/api/chat", "/api/generate"},
	},
	"openrouter": {
		Name:        "openrouter",
		BaseURL:     envOrDefault("OPENROUTER_PROVIDER_URL", "https://openrouter.ai/api"),
		DefaultPath: "/v1/chat/completions",
		Paths:       []string{}, // Uses OpenAI paths, detected by API key prefix
	},
	"opencode": {
		Name:        "opencode",
		BaseURL:     envOrDefault("OPENCODE_PROVIDER_URL", "https://opencode.ai/zen"),
		DefaultPath: "/v1/chat/completions",
		Paths:       []string{}, // Uses OpenAI paths, detected by API key prefix
	},
}

// GetProviderByPath returns the provider config that matches the path.
// BaseURL is resolved dynamically from environment variables at call time.
func GetProviderByPath(path string) *ProviderConfig {
	for _, p := range Providers {
		for _, prefix := range p.Paths {
			if strings.Contains(path, prefix) {
				cfg := p // Copy to avoid returning pointer to loop variable
				// Dynamically resolve BaseURL from env vars at runtime
				// This allows agent-specific overrides set after package init
				cfg.BaseURL = getProviderBaseURL(cfg.Name)
				return &cfg
			}
		}
	}
	return nil
}

// getProviderBaseURL returns the base URL for a provider, checking env vars at runtime.
func getProviderBaseURL(providerName string) string {
	switch providerName {
	case "anthropic":
		return envOrDefault("ANTHROPIC_PROVIDER_URL", "https://api.anthropic.com")
	case "openai":
		return envOrDefault("OPENAI_PROVIDER_URL", "https://api.openai.com")
	case "gemini":
		return envOrDefault("GEMINI_PROVIDER_URL", "https://generativelanguage.googleapis.com")
	case "bedrock":
		return envOrDefault("BEDROCK_PROVIDER_URL", "https://bedrock-runtime."+envOrDefault("AWS_REGION", envOrDefault("AWS_DEFAULT_REGION", "us-east-1"))+".amazonaws.com")
	case "ollama":
		return envOrDefault("OLLAMA_PROVIDER_URL", "http://localhost:11434")
	case "openrouter":
		return envOrDefault("OPENROUTER_PROVIDER_URL", "https://openrouter.ai/api")
	case "opencode":
		return envOrDefault("OPENCODE_PROVIDER_URL", "https://opencode.ai/zen")
	default:
		return Providers[providerName].BaseURL
	}
}
