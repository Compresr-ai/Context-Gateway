// Provider identification - centralized single entry point.
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
//  2. Bedrock URL path patterns — checked before header signals because AWS SDK clients
//     may forward an anthropic-version header alongside Bedrock requests, causing
//     misidentification if the header check fires first.
//  3. anthropic-version header (definitive for direct Anthropic API)
//  4. API key patterns (sk-ant- for Anthropic, sk- for OpenAI)
//  5. Path patterns (/v1/messages for Anthropic, /v1/chat/completions for OpenAI)
//  6. Default to OpenAI (most common format)
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
		case "litellm":
			return ProviderLiteLLM
		case "minimax":
			return ProviderMiniMax
		}
	}

	// 2. Bedrock: URL path patterns checked BEFORE header signals.
	// AWS SDK clients often forward anthropic-version alongside Bedrock requests;
	// path detection is more reliable and must take precedence.
	if strings.Contains(path, "/model/") &&
		(strings.HasSuffix(path, "/invoke") ||
			strings.HasSuffix(path, "/invoke-with-response-stream") ||
			strings.HasSuffix(path, "/converse") ||
			strings.HasSuffix(path, "/converse-stream")) {
		return ProviderBedrock
	}

	// 3. anthropic-version header is definitive for direct Anthropic API
	// Claude CLI/SDK always sends this header
	if headers.Get("anthropic-version") != "" {
		return ProviderAnthropic
	}

	// 4. Check x-api-key for Anthropic key pattern
	if strings.HasPrefix(headers.Get("x-api-key"), "sk-ant-") {
		return ProviderAnthropic
	}

	// 5. Check Authorization header - distinguish sk-ant- (Anthropic) from sk- (OpenAI)
	if auth := headers.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer sk-ant-") {
			return ProviderAnthropic
		}
	}

	// 6. Path-based detection
	if strings.HasSuffix(path, "/v1/messages") {
		return ProviderAnthropic
	}
	if strings.HasSuffix(path, "/v1/chat/completions") ||
		strings.HasSuffix(path, "/v1/completions") ||
		strings.HasSuffix(path, "/chat/completions") ||
		strings.HasSuffix(path, "/v1/responses") ||
		strings.HasSuffix(path, "/responses") {
		return ProviderOpenAI
	}

	// 7. Check Gemini
	if strings.Contains(path, "generativelanguage.googleapis.com") ||
		headers.Get("x-goog-api-key") != "" {
		return ProviderGemini
	}

	// 8. Check Ollama
	if strings.HasSuffix(path, "/api/chat") ||
		strings.HasSuffix(path, "/api/generate") {
		return ProviderOllama
	}

	// Default to OpenAI format (most common)
	return ProviderOpenAI
}
