package tui

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// =============================================================================
// PROVIDER DEFINITIONS
// =============================================================================

// ProviderInfo contains information about a supported LLM provider.
type ProviderInfo struct {
	Name         string
	DisplayName  string
	EnvVar       string
	Models       []string
	DefaultModel string
}

// ExternalProvidersConfig represents the external_providers.yaml configuration
// These are the LLM providers that the gateway can proxy requests to
type ExternalProvidersConfig struct {
	Providers map[string]struct {
		DisplayName  string   `yaml:"display_name"`
		EnvVar       string   `yaml:"env_var"`
		DefaultModel string   `yaml:"default_model"`
		Models       []string `yaml:"models"`
	} `yaml:"providers"`
}

// DefaultProviders is the fallback if external_providers.yaml cannot be loaded
var DefaultProviders = []ProviderInfo{
	{
		Name:         "anthropic",
		DisplayName:  "Claude Code CLI",
		EnvVar:       "ANTHROPIC_API_KEY",
		Models:       []string{"claude-haiku-4-5", "claude-sonnet-4-5", "claude-opus-4-5", "claude-opus-4-6"},
		DefaultModel: "claude-haiku-4-5",
	},
	{
		Name:         "gemini",
		DisplayName:  "Google Gemini",
		EnvVar:       "GEMINI_API_KEY",
		Models:       []string{"gemini-3-flash", "gemini-3-pro", "gemini-2.5-flash"},
		DefaultModel: "gemini-3-flash",
	},
	{
		Name:         "openai",
		DisplayName:  "OpenAI",
		EnvVar:       "OPENAI_API_KEY",
		Models:       []string{"gpt-5-nano", "gpt-5-mini", "gpt-5.2", "gpt-5.2-pro"},
		DefaultModel: "gpt-5-nano",
	},
}

// SupportedProviders is loaded from external_providers.yaml or falls back to defaults
var SupportedProviders = loadProviders()

// loadProviders loads provider definitions from external_providers.yaml
func loadProviders() []ProviderInfo {
	tryLoad := func(path string) []ProviderInfo {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		var cfg ExternalProvidersConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil
		}

		// Convert to ProviderInfo slice
		providers := []ProviderInfo{}

		// Process in specific order
		for _, name := range []string{"anthropic", "gemini", "openai"} {
			if p, ok := cfg.Providers[name]; ok {
				providers = append(providers, ProviderInfo{
					Name:         name,
					DisplayName:  p.DisplayName,
					EnvVar:       p.EnvVar,
					Models:       p.Models,
					DefaultModel: p.DefaultModel,
				})
			}
		}
		return providers
	}

	if homeDir, err := os.UserHomeDir(); err == nil {
		userPath := filepath.Join(homeDir, ".config", "context-gateway", "external_providers.yaml")
		if providers := tryLoad(userPath); len(providers) > 0 {
			return providers
		}
	}

	if providers := tryLoad("configs/external_providers.yaml"); len(providers) > 0 {
		return providers
	}

	// Fallback to defaults
	return DefaultProviders
}
