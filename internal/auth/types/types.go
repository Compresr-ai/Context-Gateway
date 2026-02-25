// Package types defines common authentication interfaces and types.
package types

import (
	"net/http"
	"strings"
)

// =============================================================================
// AUTH MODE TYPES
// =============================================================================

// AuthMode defines the authentication strategy for a provider.
type AuthMode string

const (
	// AuthModeAPIKey uses API key authentication only.
	AuthModeAPIKey AuthMode = "api_key"

	// AuthModeSubscription uses OAuth/subscription credentials only.
	AuthModeSubscription AuthMode = "subscription"

	// AuthModeBoth uses subscription auth with API key fallback.
	AuthModeBoth AuthMode = "both"
)

// ParseAuthMode converts a string to AuthMode.
func ParseAuthMode(s string) AuthMode {
	switch strings.ToLower(s) {
	case "api_key", "apikey":
		return AuthModeAPIKey
	case "subscription", "oauth":
		return AuthModeSubscription
	case "both":
		return AuthModeBoth
	default:
		return AuthModeAPIKey
	}
}

// =============================================================================
// CONFIGURATION TYPES
// =============================================================================

// AuthConfig contains authentication configuration for a provider.
type AuthConfig struct {
	// Mode specifies the auth strategy (api_key, subscription, both).
	Mode AuthMode

	// APIKey is the fallback API key (used when Mode is AuthModeBoth or AuthModeAPIKey).
	APIKey string

	// SubscriptionOK indicates if subscription credentials are loaded and valid.
	// For Anthropic: true if OAuth tokens are in Keychain.
	// For OpenAI: assumed true until we see auth failures.
	SubscriptionOK bool
}

// =============================================================================
// FALLBACK RESULT TYPES
// =============================================================================

// FallbackResult describes whether to fall back to API key auth.
type FallbackResult struct {
	// ShouldFallback is true if we should retry with API key.
	ShouldFallback bool

	// Reason describes why fallback was triggered (for logging).
	Reason string

	// Headers contains the auth headers to use for fallback request.
	// Typically includes x-api-key or Authorization: Bearer.
	Headers map[string]string
}

// =============================================================================
// HANDLER INTERFACE
// =============================================================================

// Handler defines the interface for provider-specific auth handling.
type Handler interface {
	// Name returns the provider name ("anthropic", "openai", etc.).
	Name() string

	// Initialize sets up the handler with configuration.
	// For Anthropic: loads OAuth tokens, starts background refresh.
	// For OpenAI: just stores the API key config.
	Initialize(cfg AuthConfig) error

	// GetAuthMode returns the configured auth mode.
	GetAuthMode() AuthMode

	// ShouldFallback checks if we should fall back to API key based on response.
	// Provider-specific logic:
	// - Anthropic: triggers on 429, 529, 402 (quota/rate errors)
	// - OpenAI: triggers on 401, 429, 402, 403 (auth + quota errors)
	ShouldFallback(statusCode int, responseBody []byte) FallbackResult

	// GetFallbackHeaders returns headers to use for API key fallback.
	// Anthropic: {"x-api-key": "sk-ant-..."}
	// OpenAI: {"Authorization": "Bearer sk-..."}
	GetFallbackHeaders() map[string]string

	// HasFallback returns true if API key fallback is configured.
	HasFallback() bool

	// DetectAuthMode classifies the auth type from request headers.
	// Returns (mode, isSubscriptionAuth).
	// Used for telemetry and fallback eligibility.
	DetectAuthMode(headers http.Header) (string, bool)

	// Stop cleans up background processes (OAuth refresh, etc.).
	Stop()
}

// =============================================================================
// HEADER CONSTANTS
// =============================================================================

const (
	// HeaderAuthorization is the standard Authorization header.
	HeaderAuthorization = "Authorization"

	// HeaderXAPIKey is Anthropic's API key header.
	HeaderXAPIKey = "x-api-key"

	// HeaderContentType is the Content-Type header.
	HeaderContentType = "Content-Type"
)

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// BearerToken extracts the bearer token value from an Authorization header.
// Input: "Bearer sk-ant-..." -> Output: "sk-ant-..."
// Input: "sk-ant-..." -> Output: "sk-ant-..." (pass-through if no Bearer prefix)
func BearerToken(authHeader string) string {
	authHeader = strings.TrimSpace(authHeader)
	if authHeader == "" {
		return ""
	}

	const bearerPrefix = "Bearer "
	if strings.HasPrefix(authHeader, bearerPrefix) {
		return strings.TrimSpace(authHeader[len(bearerPrefix):])
	}

	// If no Bearer prefix, return as-is (some clients send bare tokens)
	return authHeader
}
