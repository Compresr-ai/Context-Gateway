// Request utilities - URL detection and request patching.
//
// DESIGN:
//   - autoDetectTargetURL():       Infer upstream URL from headers/path
//   - isNonLLMEndpoint():          Skip compression for non-LLM paths
//
// NOTE: Provider detection is centralized in adapters.IdentifyAndGetAdapter()
package gateway

import (
	"net/http"
	"strings"
)

// normalizeOpenAIPath ensures paths are in /v1/... format for OpenAI API.
// Handles cases where clients send /responses instead of /v1/responses.
func normalizeOpenAIPath(path string) string {
	// Paths that need /v1 prefix if missing
	needsV1Prefix := []string{"/responses", "/chat/completions", "/completions", "/embeddings", "/models"}
	for _, p := range needsV1Prefix {
		if path == p {
			return "/v1" + path
		}
	}
	return path
}

// autoDetectTargetURL determines the upstream URL based on request characteristics.
func (g *Gateway) autoDetectTargetURL(r *http.Request) string {
	path := r.URL.Path

	// 1. Anthropic: anthropic-version header is definitive
	if r.Header.Get("anthropic-version") != "" {
		return Providers["anthropic"].BaseURL + path
	}

	// 2. Check x-api-key for Anthropic pattern (sk-ant-)
	if strings.HasPrefix(r.Header.Get("x-api-key"), "sk-ant-") {
		return Providers["anthropic"].BaseURL + path
	}

	// 3. Check Authorization header - distinguish providers by API key prefix
	if auth := r.Header.Get("Authorization"); auth != "" {
		// Anthropic: Bearer sk-ant-xxx
		if strings.HasPrefix(auth, "Bearer sk-ant-") {
			return Providers["anthropic"].BaseURL + path
		}
		// OpenRouter: Bearer sk-or-xxx
		if strings.HasPrefix(auth, "Bearer sk-or-") {
			path = normalizeOpenAIPath(path)
			return Providers["openrouter"].BaseURL + path
		}
		// OpenAI: Bearer sk-xxx (but not sk-ant- or sk-or-)
		if strings.HasPrefix(auth, "Bearer sk-") {
			path = normalizeOpenAIPath(path)
			return Providers["openai"].BaseURL + path
		}
	}

	// 4. Match by path using provider configuration
	if provider := GetProviderByPath(path); provider != nil {
		return provider.BaseURL + path
	}

	return ""
}

// isNonLLMEndpoint returns true for paths that shouldn't be processed as LLM requests.
func (g *Gateway) isNonLLMEndpoint(path string) bool {
	nonLLMPaths := []string{
		"/api/event_logging",
		"/api/telemetry",
		"/api/analytics",
	}
	for _, prefix := range nonLLMPaths {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
