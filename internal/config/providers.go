// Provider configuration for LLM services.
//
// DESIGN: Define providers once, reference everywhere.
// Endpoints are auto-resolved from provider name + model.
//
// Supported providers:
//   - anthropic: api.anthropic.com/v1/messages
//   - gemini:    generativelanguage.googleapis.com/v1beta/models/{model}:generateContent
//   - openai:    api.openai.com/v1/chat/completions
package config

import (
	"fmt"
	"strings"
)

// ProviderConfig configures a single LLM provider.
type ProviderConfig struct {
	APIKey   string `yaml:"api_key"`            // API key (supports ${VAR} syntax)
	Model    string `yaml:"model"`              // Model name (e.g., "claude-haiku-4-5", "gemini-2.0-flash")
	Endpoint string `yaml:"endpoint,omitempty"` // Optional: override auto-resolved endpoint
}

// ProvidersConfig is a map of provider names to their configurations.
type ProvidersConfig map[string]ProviderConfig

// Known provider names for endpoint auto-resolution.
const (
	ProviderAnthropic = "anthropic"
	ProviderGemini    = "gemini"
	ProviderOpenAI    = "openai"
)

// GetEndpoint returns the endpoint URL for a provider.
// If Endpoint is set, returns it. Otherwise, auto-resolves from provider name + model.
func (p ProviderConfig) GetEndpoint(providerName string) string {
	if p.Endpoint != "" {
		return p.Endpoint
	}
	return ResolveProviderEndpoint(providerName, p.Model)
}

// ResolveProviderEndpoint returns the standard endpoint URL for a provider.
// Falls back to treating unknown providers as OpenAI-compatible.
func ResolveProviderEndpoint(provider, model string) string {
	switch strings.ToLower(provider) {
	case ProviderAnthropic:
		return "https://api.anthropic.com/v1/messages"
	case ProviderGemini:
		// Gemini bakes model name into the URL
		return fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", model)
	case ProviderOpenAI:
		return "https://api.openai.com/v1/chat/completions"
	default:
		// Treat unknown providers as OpenAI-compatible
		return "https://api.openai.com/v1/chat/completions"
	}
}

// Validate validates provider configurations.
func (p ProvidersConfig) Validate() error {
	for name, cfg := range p {
		if cfg.Model == "" {
			return fmt.Errorf("provider %q: model is required", name)
		}
		// API key can be empty (captured from requests for Max/Pro users)
	}
	return nil
}

// GetUsedProviderNames returns provider names actually referenced in config.
// Used for smart key validation - only check keys that are needed.
func GetUsedProviderNames(cfg *Config) []string {
	used := make(map[string]bool)

	// Check pipes
	if cfg.Pipes.ToolOutput.Provider != "" {
		used[cfg.Pipes.ToolOutput.Provider] = true
	}
	if cfg.Pipes.ToolDiscovery.Provider != "" {
		used[cfg.Pipes.ToolDiscovery.Provider] = true
	}

	// Check preemptive
	if cfg.Preemptive.Summarizer.Provider != "" {
		used[cfg.Preemptive.Summarizer.Provider] = true
	}

	result := make([]string, 0, len(used))
	for name := range used {
		result = append(result, name)
	}
	return result
}

// ValidateUsedProviders checks that all referenced providers exist and have valid keys.
func (cfg *Config) ValidateUsedProviders() error {
	usedProviders := GetUsedProviderNames(cfg)

	for _, name := range usedProviders {
		provider, ok := cfg.Providers[name]
		if !ok {
			return fmt.Errorf("provider %q is referenced but not defined in providers section", name)
		}
		// API key validation is intentionally skipped here
		// It can be captured from incoming requests (Max/Pro users)
		_ = provider
	}

	return nil
}

// ResolveProviderSettings returns the fully-resolved settings for a provider reference.
// If providerName is empty, returns the legacy inline settings.
type ResolvedProvider struct {
	Provider string // Provider name (anthropic, gemini, openai)
	Endpoint string
	APIKey   string
	Model    string
}

// ResolveProvider resolves a provider reference to its full settings.
// Used by pipes and preemptive to get the actual endpoint/key/model.
func (cfg *Config) ResolveProvider(providerName string) (*ResolvedProvider, error) {
	if providerName == "" {
		return nil, fmt.Errorf("provider name is required")
	}

	provider, ok := cfg.Providers[providerName]
	if !ok {
		return nil, fmt.Errorf("provider %q not found in providers section", providerName)
	}

	return &ResolvedProvider{
		Provider: providerName,
		Endpoint: provider.GetEndpoint(providerName),
		APIKey:   provider.APIKey,
		Model:    provider.Model,
	}, nil
}

// ResolvePreemptiveProvider resolves provider settings for preemptive summarizer.
// If Provider reference is set, looks up and populates Model/APIKey/Endpoint.
// Returns a copy of PreemptiveConfig with resolved settings.
func (cfg *Config) ResolvePreemptiveProvider() PreemptiveConfig {
	resolved := cfg.Preemptive

	if resolved.Summarizer.Provider == "" {
		return resolved // No provider reference, use inline settings
	}

	provider, ok := cfg.Providers[resolved.Summarizer.Provider]
	if !ok {
		return resolved // Provider not found, use inline settings (validation will catch this)
	}

	// Merge provider settings into summarizer config
	// Inline settings take precedence (for partial overrides)
	if resolved.Summarizer.Model == "" {
		resolved.Summarizer.Model = provider.Model
	}
	if resolved.Summarizer.APIKey == "" {
		resolved.Summarizer.APIKey = provider.APIKey
	}
	if resolved.Summarizer.Endpoint == "" {
		resolved.Summarizer.Endpoint = provider.GetEndpoint(resolved.Summarizer.Provider)
	}

	return resolved
}
