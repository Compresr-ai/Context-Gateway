// Request utilities - URL detection and request patching.
//
// DESIGN:
//   - autoDetectTargetURL(): Infer upstream URL from headers/path
//   - isNonLLMEndpoint():   Skip compression for non-LLM paths
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

// normalizeChatGPTPath transforms paths for ChatGPT subscription endpoint.
// ChatGPT subscription uses /backend-api/codex/responses instead of /v1/responses
func normalizeChatGPTPath(path string) string {
	// Map /responses/* or /v1/responses/* to /codex/responses/*
	if path == "/responses" || path == "/v1/responses" {
		return "/codex/responses"
	}
	if strings.HasPrefix(path, "/responses/") {
		return "/codex" + path // /responses/compact → /codex/responses/compact
	}
	if strings.HasPrefix(path, "/v1/responses/") {
		return "/codex" + strings.TrimPrefix(path, "/v1") // /v1/responses/compact → /codex/responses/compact
	}
	return path
}

// isChatGPTSubscription checks if this is a ChatGPT subscription request (non-API key bearer token)
func isChatGPTSubscription(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	// API keys start with sk-, subscription tokens don't
	return !strings.HasPrefix(token, "sk-")
}

// autoDetectTargetURL determines the upstream URL based on request characteristics.
func (g *Gateway) autoDetectTargetURL(r *http.Request) string {
	path := r.URL.Path

	// 0. Bedrock: path-based detection (/model/xxx/invoke or /model/xxx/converse)
	if g.isBedrockRequest(path) && g.bedrockSigner != nil && g.bedrockSigner.IsConfigured() {
		return g.bedrockSigner.BuildTargetURL(path)
	}

	// 1. Anthropic: anthropic-version header is definitive
	if r.Header.Get("anthropic-version") != "" {
		return getProviderBaseURL("anthropic") + path
	}

	// 2. Check x-api-key for Anthropic pattern (sk-ant-)
	if strings.HasPrefix(r.Header.Get("x-api-key"), "sk-ant-") {
		return getProviderBaseURL("anthropic") + path
	}

	// 3. Check Authorization header - distinguish providers by API key prefix
	if auth := r.Header.Get("Authorization"); auth != "" {
		// Anthropic: Bearer sk-ant-xxx
		if strings.HasPrefix(auth, "Bearer sk-ant-") {
			return getProviderBaseURL("anthropic") + path
		}
		// OpenRouter: Bearer sk-or-xxx
		if strings.HasPrefix(auth, "Bearer sk-or-") {
			path = normalizeOpenAIPath(path)
			return getProviderBaseURL("openrouter") + path
		}
		// OpenAI: Bearer sk-xxx (but not sk-ant- or sk-or-)
		// Always route API keys to api.openai.com regardless of OPENAI_PROVIDER_URL
		if strings.HasPrefix(auth, "Bearer sk-") {
			path = normalizeOpenAIPath(path)
			return "https://api.openai.com" + path
		}
		// ChatGPT subscription: Bearer token without sk- prefix
		// Always route subscription tokens to chatgpt.com regardless of OPENAI_PROVIDER_URL
		if isChatGPTSubscription(r) {
			return "https://chatgpt.com/backend-api" + normalizeChatGPTPath(path)
		}
	}

	// 4. Match by path using provider configuration
	if provider := GetProviderByPath(path); provider != nil {
		// For OpenAI paths, use token-based detection to choose endpoint
		if provider.Name == "openai" {
			auth := r.Header.Get("Authorization")
			// API key: route to api.openai.com
			if strings.HasPrefix(auth, "Bearer sk-") {
				return "https://api.openai.com" + normalizeOpenAIPath(path)
			}
			// Subscription token: route to chatgpt.com
			if isChatGPTSubscription(r) {
				return "https://chatgpt.com/backend-api" + normalizeChatGPTPath(path)
			}
		}
		// Normalize path for OpenAI and OpenRouter (API key auth)
		normalizedPath := path
		if provider.Name == "openai" || provider.Name == "openrouter" {
			normalizedPath = normalizeOpenAIPath(path)
		}
		return provider.BaseURL + normalizedPath
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
