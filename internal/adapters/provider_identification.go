// Provider identification - centralized single entry point.
//
// DESIGN: Provider identification is centralized here:
//   - IdentifyAndGetAdapter(registry, path, headers) is the SINGLE entry point
//     It detects provider AND returns the corresponding adapter in one call.
//
// The gateway calls IdentifyAndGetAdapter() once per request.
// No other code should detect providers - this is the single source of truth.
package adapters

import (
	"net/http"
	"strings"
)

// IdentifyAndGetAdapter is the SINGLE entry point for provider detection.
// It detects the provider from request path/headers AND returns the adapter.
// This centralizes all provider identification logic in one place.
//
// Returns: (provider, adapter) - adapter is never nil (falls back to OpenAI)
func IdentifyAndGetAdapter(registry *Registry, path string, headers http.Header) (Provider, Adapter) {
	provider := detectProvider(path, headers)
	adapter := registry.Get(provider.String())
	if adapter == nil {
		// Fallback to OpenAI adapter (most common format)
		adapter = registry.Get(ProviderOpenAI.String())
	}
	return provider, adapter
}

// detectProvider identifies the provider from request path and headers.
// This is internal - external code should use IdentifyAndGetAdapter().
//
// Detection priority:
//  1. Explicit X-Provider header (highest priority)
//  2. anthropic-version header (definitive Anthropic signal)
//  3. API key patterns (sk-ant- for Anthropic, sk- for OpenAI)
//  4. Path patterns (/v1/messages for Anthropic, /v1/chat/completions for OpenAI)
//  5. Default to OpenAI (most common format)
func detectProvider(path string, headers http.Header) Provider {
	// 1. Explicit X-Provider header (highest priority)
	if p := headers.Get("X-Provider"); p != "" {
		switch strings.ToLower(p) {
		case "anthropic":
			return ProviderAnthropic
		case "openai":
			return ProviderOpenAI
		case "gemini":
			return ProviderGemini
		case "bedrock":
			return ProviderBedrock
		case "ollama":
			return ProviderOllama
		}
	}

	// 1b. Bedrock: URL path patterns (/model/xxx/invoke or /model/xxx/converse)
	// Must check before anthropic-version since Bedrock requests may also set that header
	if strings.Contains(path, "/model/") &&
		(strings.HasSuffix(path, "/invoke") ||
			strings.HasSuffix(path, "/invoke-with-response-stream") ||
			strings.HasSuffix(path, "/converse") ||
			strings.HasSuffix(path, "/converse-stream")) {
		return ProviderBedrock
	}

	// 1c. Bedrock: X-Amz-Date header (SigV4 indicator from AWS SDK)
	if headers.Get("X-Amz-Date") != "" {
		return ProviderBedrock
	}

	// 2. anthropic-version header is definitive for Anthropic
	// Claude CLI/SDK always sends this header
	if headers.Get("anthropic-version") != "" {
		return ProviderAnthropic
	}

	// 3. Check x-api-key for Anthropic key pattern
	if strings.HasPrefix(headers.Get("x-api-key"), "sk-ant-") {
		return ProviderAnthropic
	}

	// 4. Check Authorization header - distinguish sk-ant- (Anthropic) from sk- (OpenAI)
	if auth := headers.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer sk-ant-") {
			return ProviderAnthropic
		}
	}

	// 5. Path-based detection
	if strings.HasSuffix(path, "/v1/messages") {
		return ProviderAnthropic
	}
	if strings.HasSuffix(path, "/v1/chat/completions") ||
		strings.HasSuffix(path, "/v1/completions") ||
		strings.HasSuffix(path, "/chat/completions") {
		return ProviderOpenAI
	}

	// 6. Check Gemini
	if strings.Contains(path, "generativelanguage.googleapis.com") ||
		headers.Get("x-goog-api-key") != "" {
		return ProviderGemini
	}

	// 7. Check Ollama
	if strings.HasSuffix(path, "/api/chat") ||
		strings.HasSuffix(path, "/api/generate") {
		return ProviderOllama
	}

	// Default to OpenAI format (most common)
	return ProviderOpenAI
}
