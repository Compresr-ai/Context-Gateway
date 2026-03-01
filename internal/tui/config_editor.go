package tui

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/compresr/context-gateway/internal/config"
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
		data, err := os.ReadFile(path) // #nosec G304 -- trusted config paths
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

// =============================================================================
// COMPRESR API MODELS
// =============================================================================

// CompresrModelInfo contains information about a Compresr API model.
type CompresrModelInfo struct {
	Name        string
	Description string
	Recommended bool
}

// CompresrServiceInfo contains information about a Compresr service (tool_discovery or tool_output).
type CompresrServiceInfo struct {
	DefaultModel string
	Models       []CompresrModelInfo
}

// CompresrConfig contains the Compresr API configuration.
type CompresrConfig struct {
	DisplayName    string
	EnvVar         string
	BaseURLEnv     string
	DefaultBaseURL string
	ToolDiscovery  CompresrServiceInfo
	ToolOutput     CompresrServiceInfo
	History        CompresrServiceInfo // HCC models for compact/preemptive
}

// compresrModelsConfig represents the compresr_models.yaml configuration
type compresrModelsConfig struct {
	Compresr struct {
		DisplayName    string `yaml:"display_name"`
		EnvVar         string `yaml:"env_var"`
		BaseURLEnv     string `yaml:"base_url_env"`
		DefaultBaseURL string `yaml:"default_base_url"`
		ToolDiscovery  struct {
			DefaultModel string `yaml:"default_model"`
			Models       []struct {
				Name        string `yaml:"name"`
				Description string `yaml:"description"`
				Recommended bool   `yaml:"recommended"`
			} `yaml:"models"`
		} `yaml:"tool_discovery"`
		ToolOutput struct {
			DefaultModel string `yaml:"default_model"`
			Models       []struct {
				Name        string `yaml:"name"`
				Description string `yaml:"description"`
				Recommended bool   `yaml:"recommended"`
			} `yaml:"models"`
		} `yaml:"tool_output"`
		SemanticSummarization struct {
			DefaultModel string `yaml:"default_model"`
			Models       []struct {
				Name        string `yaml:"name"`
				Description string `yaml:"description"`
				Recommended bool   `yaml:"recommended"`
			} `yaml:"models"`
		} `yaml:"semantic_summarization"`
	} `yaml:"compresr"`
}

// DefaultCompresrConfig is the fallback if compresr_models.yaml cannot be loaded
var DefaultCompresrConfig = CompresrConfig{
	DisplayName:    "Compresr",
	EnvVar:         "COMPRESR_API_KEY",
	BaseURLEnv:     "COMPRESR_BASE_URL",
	DefaultBaseURL: config.DefaultCompresrAPIBaseURL,
	ToolDiscovery: CompresrServiceInfo{
		DefaultModel: "tool-selector-v1",
		Models: []CompresrModelInfo{
			{Name: "tool-selector-v1", Description: "Fast, accurate tool selection", Recommended: true},
			{Name: "tool-selector-lite", Description: "Ultra-fast, basic filtering"},
			{Name: "tool-selector-pro", Description: "Most accurate, slower"},
		},
	},
	ToolOutput: CompresrServiceInfo{
		DefaultModel: "compressor-v1",
		Models: []CompresrModelInfo{
			{Name: "compressor-v1", Description: "Balanced compression", Recommended: true},
			{Name: "compressor-lite", Description: "Fast, basic compression"},
			{Name: "compressor-pro", Description: "Best compression ratio"},
		},
	},
	History: CompresrServiceInfo{
		DefaultModel: "hcc_espresso_v1",
		Models: []CompresrModelInfo{
			{Name: "hcc_espresso_v1", Description: "Lingua-based history compression", Recommended: true},
		},
	},
}

// CompresrModels is loaded from compresr_models.yaml or falls back to defaults
var CompresrModels = loadCompresrModels()

// loadCompresrModels loads Compresr model definitions from compresr_models.yaml
func loadCompresrModels() CompresrConfig {
	tryLoad := func(path string) *CompresrConfig {
		data, err := os.ReadFile(path) // #nosec G304 -- trusted config paths
		if err != nil {
			return nil
		}

		var cfg compresrModelsConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil
		}

		// Convert to CompresrConfig
		result := CompresrConfig{
			DisplayName:    cfg.Compresr.DisplayName,
			EnvVar:         cfg.Compresr.EnvVar,
			BaseURLEnv:     cfg.Compresr.BaseURLEnv,
			DefaultBaseURL: cfg.Compresr.DefaultBaseURL,
		}

		// Tool Discovery models
		result.ToolDiscovery.DefaultModel = cfg.Compresr.ToolDiscovery.DefaultModel
		for _, m := range cfg.Compresr.ToolDiscovery.Models {
			result.ToolDiscovery.Models = append(result.ToolDiscovery.Models, CompresrModelInfo{
				Name:        m.Name,
				Description: m.Description,
				Recommended: m.Recommended,
			})
		}

		// Tool Output models
		result.ToolOutput.DefaultModel = cfg.Compresr.ToolOutput.DefaultModel
		for _, m := range cfg.Compresr.ToolOutput.Models {
			result.ToolOutput.Models = append(result.ToolOutput.Models, CompresrModelInfo{
				Name:        m.Name,
				Description: m.Description,
				Recommended: m.Recommended,
			})
		}

		// History / Semantic Summarization models
		result.History.DefaultModel = cfg.Compresr.SemanticSummarization.DefaultModel
		for _, m := range cfg.Compresr.SemanticSummarization.Models {
			result.History.Models = append(result.History.Models, CompresrModelInfo{
				Name:        m.Name,
				Description: m.Description,
				Recommended: m.Recommended,
			})
		}

		return &result
	}

	if homeDir, err := os.UserHomeDir(); err == nil {
		userPath := filepath.Join(homeDir, ".config", "context-gateway", "compresr_models.yaml")
		if cfg := tryLoad(userPath); cfg != nil {
			return *cfg
		}
	}

	if cfg := tryLoad("internal/compresr/compresr_models.yaml"); cfg != nil {
		return *cfg
	}

	// Fallback to defaults
	return DefaultCompresrConfig
}
