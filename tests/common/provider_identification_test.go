package common

import (
	"net/http"
	"testing"

	"github.com/compresr/context-gateway/internal/adapters"
)

// =============================================================================
// PROVIDER IDENTIFICATION TESTS
// =============================================================================

// TestProviderIdentification_ExplicitHeader tests provider detection via X-Provider header
func TestProviderIdentification_ExplicitHeader(t *testing.T) {
	registry := adapters.NewRegistry()

	tests := []struct {
		name         string
		headerValue  string
		expectedName string
	}{
		{
			name:         "X-Provider: anthropic",
			headerValue:  "anthropic",
			expectedName: "anthropic",
		},
		{
			name:         "X-Provider: openai",
			headerValue:  "openai",
			expectedName: "openai",
		},
		{
			name:         "X-Provider: gemini",
			headerValue:  "gemini",
			expectedName: "gemini",
		},
		{
			name:         "X-Provider: unknown (falls back to openai)",
			headerValue:  "unknown",
			expectedName: "openai", // Falls back to OpenAI
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			headers.Set("X-Provider", tt.headerValue)

			_, adapter := adapters.IdentifyAndGetAdapter(registry, "/some/path", headers)

			if adapter == nil {
				t.Fatalf("Expected adapter, got nil")
			}
			if adapter.Name() != tt.expectedName {
				t.Errorf("Expected adapter %s, got %s", tt.expectedName, adapter.Name())
			}
		})
	}
}

// TestProviderIdentification_PathBased tests provider detection via URL path patterns
func TestProviderIdentification_PathBased(t *testing.T) {
	registry := adapters.NewRegistry()

	tests := []struct {
		name         string
		path         string
		expectedName string
	}{
		{
			name:         "Anthropic messages path",
			path:         "/v1/messages",
			expectedName: "anthropic",
		},
		{
			name:         "Anthropic messages with base path",
			path:         "/api/v1/messages",
			expectedName: "anthropic",
		},
		{
			name:         "OpenAI chat completions path",
			path:         "/v1/chat/completions",
			expectedName: "openai",
		},
		{
			name:         "OpenAI responses path (falls back to openai)",
			path:         "/v1/responses",
			expectedName: "openai",
		},
		{
			name:         "Unknown path falls back to openai",
			path:         "/unknown/endpoint",
			expectedName: "openai",
		},
		{
			name:         "Empty path falls back to openai",
			path:         "",
			expectedName: "openai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}

			_, adapter := adapters.IdentifyAndGetAdapter(registry, tt.path, headers)

			if adapter == nil {
				t.Fatalf("Expected adapter, got nil")
			}
			if adapter.Name() != tt.expectedName {
				t.Errorf("Expected adapter %s, got %s", tt.expectedName, adapter.Name())
			}
		})
	}
}

// TestProviderIdentification_APIHeaders tests provider detection via API-specific headers
func TestProviderIdentification_APIHeaders(t *testing.T) {
	registry := adapters.NewRegistry()

	tests := []struct {
		name         string
		headers      map[string]string
		path         string
		expectedName string
	}{
		{
			name: "Anthropic x-api-key + anthropic-version headers",
			headers: map[string]string{
				"x-api-key":         "sk-ant-api03-xxxx",
				"anthropic-version": "2023-06-01",
			},
			path:         "/some/generic/path",
			expectedName: "anthropic",
		},
		{
			name: "No identifying headers falls back to openai",
			headers: map[string]string{
				"Authorization": "Bearer sk-xxxx",
			},
			path:         "/some/generic/path",
			expectedName: "openai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			for k, v := range tt.headers {
				headers.Set(k, v)
			}

			_, adapter := adapters.IdentifyAndGetAdapter(registry, tt.path, headers)

			if adapter == nil {
				t.Fatalf("Expected adapter, got nil")
			}
			if adapter.Name() != tt.expectedName {
				t.Errorf("Expected adapter %s, got %s", tt.expectedName, adapter.Name())
			}
		})
	}
}

// TestProviderIdentification_Precedence tests that X-Provider header takes precedence
func TestProviderIdentification_Precedence(t *testing.T) {
	registry := adapters.NewRegistry()

	t.Run("X-Provider overrides path detection", func(t *testing.T) {
		headers := http.Header{}
		headers.Set("X-Provider", "openai")

		// Even though path suggests Anthropic, header takes precedence
		_, adapter := adapters.IdentifyAndGetAdapter(registry, "/v1/messages", headers)

		if adapter == nil {
			t.Fatal("Expected adapter, got nil")
		}
		if adapter.Name() != "openai" {
			t.Errorf("Expected openai (from header), got %s", adapter.Name())
		}
	})

	t.Run("X-Provider overrides API header detection", func(t *testing.T) {
		headers := http.Header{}
		headers.Set("X-Provider", "anthropic")
		headers.Set("x-api-key", "sk-ant-xxxx")
		headers.Set("anthropic-version", "2023-06-01")

		_, adapter := adapters.IdentifyAndGetAdapter(registry, "/generic/path", headers)

		if adapter == nil {
			t.Fatal("Expected adapter, got nil")
		}
		if adapter.Name() != "anthropic" {
			t.Errorf("Expected anthropic (from X-Provider), got %s", adapter.Name())
		}
	})
}

// =============================================================================
// ADAPTER REGISTRY TESTS
// =============================================================================

// TestAdapterRegistry_BuiltInAdapters tests that both adapters are registered
func TestAdapterRegistry_BuiltInAdapters(t *testing.T) {
	registry := adapters.NewRegistry()

	// Check both main adapters exist
	anthropic := registry.Get("anthropic")
	if anthropic == nil {
		t.Error("Anthropic adapter not found in registry")
	}
	if anthropic != nil && anthropic.Name() != "anthropic" {
		t.Errorf("Expected anthropic, got %s", anthropic.Name())
	}

	openai := registry.Get("openai")
	if openai == nil {
		t.Error("OpenAI adapter not found in registry")
	}
	if openai != nil && openai.Name() != "openai" {
		t.Errorf("Expected openai, got %s", openai.Name())
	}
}

// TestAdapterRegistry_GetByName tests retrieving specific adapters
func TestAdapterRegistry_GetByName(t *testing.T) {
	registry := adapters.NewRegistry()

	tests := []struct {
		name         string
		adapterName  string
		shouldExist  bool
		expectedType string
	}{
		{"anthropic exists", "anthropic", true, "anthropic"},
		{"openai exists", "openai", true, "openai"},
		{"unknown does not exist", "unknown", false, ""},
		{"empty name does not exist", "", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := registry.Get(tt.adapterName)

			if tt.shouldExist {
				if adapter == nil {
					t.Fatalf("Expected adapter %s to exist", tt.adapterName)
				}
				if adapter.Name() != tt.expectedType {
					t.Errorf("Expected adapter name %s, got %s", tt.expectedType, adapter.Name())
				}
			} else {
				if adapter != nil {
					t.Errorf("Expected nil for %s, got %s", tt.adapterName, adapter.Name())
				}
			}
		})
	}
}
