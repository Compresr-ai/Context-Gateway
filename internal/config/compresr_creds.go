package config

import "strings"

// CompresrCredsConfig centralizes Compresr API credentials at the config root.
// When set, all pipe compresr sub-sections inherit the api_key.
type CompresrCredsConfig struct {
	APIKey string `yaml:"api_key,omitempty"` // Compresr API key (supports ${VAR:-} syntax)
}

// HasLiteralKey returns true if the api_key is a raw secret rather than an env var reference.
func (c CompresrCredsConfig) HasLiteralKey() bool {
	return c.APIKey != "" && !strings.Contains(c.APIKey, "${")
}

// ScanLiteralKeys returns human-readable warnings for any literal API keys found in the
// config. Key values are never included in the output — only provider/field names.
func ScanLiteralKeys(cfg *Config) []string {
	var warnings []string

	// Check LLM providers
	for name, p := range cfg.Providers {
		if p.Auth == "oauth" || p.Auth == "bedrock" {
			continue // These auth methods don't use api_key
		}
		if p.ProviderAuth != "" && !strings.Contains(p.ProviderAuth, "${") {
			warnings = append(warnings,
				`provider "`+name+`": api_key is a literal value — move it to an env var and reference as ${`+ProviderEnvVar(name)+`:-}`)
		}
	}

	// Check top-level compresr credentials
	if cfg.CompresrCreds.HasLiteralKey() {
		warnings = append(warnings,
			"compresr: api_key is a literal value — move it to ~/.config/context-gateway/.env as COMPRESR_API_KEY=<your-key>")
	}

	return warnings
}

// ProviderEnvVar returns the conventional environment variable name for a known provider.
// Exported for use by the migration command.
func ProviderEnvVar(provider string) string {
	switch strings.ToLower(provider) {
	case ProviderAnthropic:
		return "ANTHROPIC_API_KEY"
	case ProviderGemini:
		return "GEMINI_API_KEY"
	case ProviderOpenAI:
		return "OPENAI_API_KEY"
	default:
		return strings.ToUpper(strings.ReplaceAll(provider, "-", "_")) + "_API_KEY"
	}
}
