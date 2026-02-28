package unit

import (
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
	"github.com/compresr/context-gateway/internal/preemptive"
)

func TestResolveProviderEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     string
	}{
		{
			name:     "anthropic",
			provider: "anthropic",
			model:    "claude-haiku-4-5",
			want:     "https://api.anthropic.com/v1/messages",
		},
		{
			name:     "gemini",
			provider: "gemini",
			model:    "gemini-2.0-flash",
			want:     "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent",
		},
		{
			name:     "openai",
			provider: "openai",
			model:    "gpt-4o-mini",
			want:     "https://api.openai.com/v1/chat/completions",
		},
		{
			name:     "unknown defaults to openai",
			provider: "custom",
			model:    "some-model",
			want:     "https://api.openai.com/v1/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := config.ResolveProviderEndpoint(tt.provider, tt.model)
			if got != tt.want {
				t.Errorf("ResolveProviderEndpoint(%q, %q) = %q, want %q", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}

func TestProviderConfig_GetEndpoint(t *testing.T) {
	tests := []struct {
		name         string
		cfg          config.ProviderConfig
		providerName string
		want         string
	}{
		{
			name: "custom endpoint overrides auto-resolve",
			cfg: config.ProviderConfig{
				Model:    "claude-haiku-4-5",
				Endpoint: "https://my-proxy.example.com/v1/messages",
			},
			providerName: "anthropic",
			want:         "https://my-proxy.example.com/v1/messages",
		},
		{
			name: "no endpoint auto-resolves",
			cfg: config.ProviderConfig{
				Model: "claude-haiku-4-5",
			},
			providerName: "anthropic",
			want:         "https://api.anthropic.com/v1/messages",
		},
		{
			name: "gemini auto-resolves with model in URL",
			cfg: config.ProviderConfig{
				Model: "gemini-flash-2.0",
			},
			providerName: "gemini",
			want:         "https://generativelanguage.googleapis.com/v1beta/models/gemini-flash-2.0:generateContent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.GetEndpoint(tt.providerName)
			if got != tt.want {
				t.Errorf("GetEndpoint(%q) = %q, want %q", tt.providerName, got, tt.want)
			}
		})
	}
}

func TestProvidersConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.ProvidersConfig
		wantErr bool
	}{
		{
			name: "valid provider",
			cfg: config.ProvidersConfig{
				"anthropic": {
					APIKey: "sk-ant-xxx",
					Model:  "claude-haiku-4-5",
				},
			},
			wantErr: false,
		},
		{
			name: "missing model",
			cfg: config.ProvidersConfig{
				"anthropic": {
					APIKey: "sk-ant-xxx",
				},
			},
			wantErr: true,
		},
		{
			name: "empty api key is ok (captured from requests)",
			cfg: config.ProvidersConfig{
				"anthropic": {
					Model: "claude-haiku-4-5",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfig_ValidateUsedProviders(t *testing.T) {
	baseConfig := func() *config.Config {
		return &config.Config{
			Server: config.ServerConfig{
				Port:         8080,
				ReadTimeout:  30 * time.Second,
				WriteTimeout: 30 * time.Second,
			},
			Store: config.StoreConfig{
				Type: "memory",
				TTL:  time.Hour,
			},
			Providers: config.ProvidersConfig{
				"gemini": {
					APIKey: "test-key",
					Model:  "gemini-2.0-flash",
				},
			},
			Pipes: pipes.Config{
				ToolOutput: pipes.ToolOutputConfig{
					Enabled: false,
				},
				ToolDiscovery: pipes.ToolDiscoveryConfig{
					Enabled: false,
				},
			},
			Preemptive: preemptive.Config{
				Enabled: false,
			},
		}
	}

	t.Run("valid: referenced provider exists", func(t *testing.T) {
		cfg := baseConfig()
		cfg.Pipes.ToolOutput.Enabled = true
		cfg.Pipes.ToolOutput.Strategy = "api"
		cfg.Pipes.ToolOutput.Provider = "gemini"

		err := cfg.ValidateUsedProviders()
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("invalid: referenced provider not defined", func(t *testing.T) {
		cfg := baseConfig()
		cfg.Pipes.ToolOutput.Enabled = true
		cfg.Pipes.ToolOutput.Strategy = "api"
		cfg.Pipes.ToolOutput.Provider = "openai" // not in providers

		err := cfg.ValidateUsedProviders()
		if err == nil {
			t.Error("expected error for undefined provider")
		}
	})

	t.Run("valid: no provider reference (uses inline API)", func(t *testing.T) {
		cfg := baseConfig()
		cfg.Pipes.ToolOutput.Enabled = true
		cfg.Pipes.ToolOutput.Strategy = "api"
		cfg.Pipes.ToolOutput.Compresr = pipes.CompresrConfig{
			Endpoint: "https://example.com",
		}

		err := cfg.ValidateUsedProviders()
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestGetUsedProviderNames(t *testing.T) {
	cfg := &config.Config{
		Pipes: pipes.Config{
			ToolOutput: pipes.ToolOutputConfig{
				Provider: "gemini",
			},
			ToolDiscovery: pipes.ToolDiscoveryConfig{
				Provider: "anthropic",
			},
		},
		Preemptive: preemptive.Config{
			Summarizer: preemptive.SummarizerConfig{
				Provider: "gemini",
			},
		},
	}

	names := config.GetUsedProviderNames(cfg)

	if len(names) != 2 {
		t.Errorf("expected 2 unique providers, got %d: %v", len(names), names)
	}

	found := make(map[string]bool)
	for _, n := range names {
		found[n] = true
	}
	if !found["gemini"] {
		t.Error("expected 'gemini' in used providers")
	}
	if !found["anthropic"] {
		t.Error("expected 'anthropic' in used providers")
	}
}

func TestConfig_ResolveProvider(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			"gemini": {
				APIKey: "test-gemini-key",
				Model:  "gemini-2.0-flash",
			},
			"anthropic": {
				APIKey:   "test-anthropic-key",
				Model:    "claude-haiku-4-5",
				Endpoint: "https://custom-proxy.example.com/v1/messages",
			},
		},
	}

	t.Run("resolve gemini (auto endpoint)", func(t *testing.T) {
		resolved, err := cfg.ResolveProvider("gemini")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved.Provider != "gemini" {
			t.Errorf("Provider = %q, want %q", resolved.Provider, "gemini")
		}
		if resolved.Model != "gemini-2.0-flash" {
			t.Errorf("Model = %q, want %q", resolved.Model, "gemini-2.0-flash")
		}
		if resolved.APIKey != "test-gemini-key" {
			t.Errorf("APIKey = %q, want %q", resolved.APIKey, "test-gemini-key")
		}
		expectedEndpoint := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent"
		if resolved.Endpoint != expectedEndpoint {
			t.Errorf("Endpoint = %q, want %q", resolved.Endpoint, expectedEndpoint)
		}
	})

	t.Run("resolve anthropic (custom endpoint)", func(t *testing.T) {
		resolved, err := cfg.ResolveProvider("anthropic")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved.Endpoint != "https://custom-proxy.example.com/v1/messages" {
			t.Errorf("Endpoint = %q, want custom proxy", resolved.Endpoint)
		}
	})

	t.Run("error: provider not found", func(t *testing.T) {
		_, err := cfg.ResolveProvider("openai")
		if err == nil {
			t.Error("expected error for undefined provider")
		}
	})

	t.Run("error: empty provider name", func(t *testing.T) {
		_, err := cfg.ResolveProvider("")
		if err == nil {
			t.Error("expected error for empty provider name")
		}
	})
}
