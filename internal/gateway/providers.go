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
}

// GetProviderByPath returns the provider config that matches the path.
func GetProviderByPath(path string) *ProviderConfig {
	for _, p := range Providers {
		for _, prefix := range p.Paths {
			if strings.Contains(path, prefix) {
				cfg := p // Copy to avoid returning pointer to loop variable
				return &cfg
			}
		}
	}
	return nil
}
