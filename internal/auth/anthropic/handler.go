// Package anthropic provides Anthropic-specific authentication handling.
//
// Anthropic auth is managed by the gateway:
//   - Loads OAuth tokens from macOS Keychain or ~/.claude/.credentials.json
//   - Automatically refreshes tokens before expiry
//   - Falls back to API key on quota/rate limit errors (429, 529, 402)
//   - Does NOT fall back on 401 (token refresh handles auth errors)
package anthropic

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/compresr/context-gateway/internal/auth/types"
	"github.com/compresr/context-gateway/internal/oauth"
)

// Handler implements types.Handler for Anthropic.
type Handler struct {
	mu           sync.RWMutex
	cfg          types.AuthConfig
	tokenManager *oauth.TokenManager
	started      bool
}

// New creates a new Anthropic auth handler.
func New() *Handler {
	return &Handler{
		tokenManager: oauth.NewTokenManager(),
	}
}

// Name returns "anthropic".
func (h *Handler) Name() string {
	return "anthropic"
}

// Initialize sets up OAuth token management.
// For subscription mode: loads tokens from Keychain, starts background refresh.
// For API key mode: just stores the API key.
func (h *Handler) Initialize(cfg types.AuthConfig) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.cfg = cfg

	// For subscription or both modes, try to initialize OAuth
	if cfg.Mode == types.AuthModeSubscription || cfg.Mode == types.AuthModeBoth {
		if err := h.tokenManager.Initialize(); err != nil {
			// If we're in "both" mode and OAuth fails, that's OK - we have API key fallback
			if cfg.Mode == types.AuthModeBoth {
				h.cfg.SubscriptionOK = false
			} else {
				return err
			}
		} else {
			h.cfg.SubscriptionOK = h.tokenManager.HasCredentials()
		}

		// Start background refresh if we have OAuth credentials
		if h.tokenManager.HasCredentials() && !h.started {
			h.tokenManager.StartBackgroundRefresh(context.Background())
			h.started = true
		}
	}

	return nil
}

// GetAuthMode returns the configured auth mode.
func (h *Handler) GetAuthMode() types.AuthMode {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cfg.Mode
}

// ShouldFallback checks if we should fall back to API key.
// Anthropic fallback triggers on quota/rate errors: 429, 529, 402.
// Does NOT trigger on 401 - OAuth refresh handles that.
func (h *Handler) ShouldFallback(statusCode int, responseBody []byte) types.FallbackResult {
	// Only consider fallback if we have an API key configured
	if !h.HasFallback() {
		return types.FallbackResult{ShouldFallback: false}
	}

	// Only in subscription or both mode
	mode := h.GetAuthMode()
	if mode != types.AuthModeSubscription && mode != types.AuthModeBoth {
		return types.FallbackResult{ShouldFallback: false}
	}

	// Status codes that indicate quota/rate limit issues
	validStatusCodes := map[int]bool{
		429: true, // Rate limit
		529: true, // Overloaded
		402: true, // Payment required / quota exceeded
	}

	if !validStatusCodes[statusCode] {
		return types.FallbackResult{ShouldFallback: false}
	}

	// Check for exhaustion signals in response body
	msg := strings.ToLower(string(responseBody))
	signals := []string{
		"rate_limit_error",
		"rate limit",
		"overloaded_error",
		"quota exceeded",
		"credit balance",
		"billing",
		"usage limit",
		"subscription",
	}

	for _, signal := range signals {
		if strings.Contains(msg, signal) {
			return types.FallbackResult{
				ShouldFallback: true,
				Reason:         "subscription quota/rate limit exceeded",
				Headers:        h.GetFallbackHeaders(),
			}
		}
	}

	return types.FallbackResult{ShouldFallback: false}
}

// GetFallbackHeaders returns headers for API key authentication.
func (h *Handler) GetFallbackHeaders() map[string]string {
	h.mu.RLock()
	apiKey := h.cfg.APIKey
	h.mu.RUnlock()

	if apiKey == "" {
		return nil
	}

	return map[string]string{
		types.HeaderXAPIKey: apiKey,
	}
}

// HasFallback returns true if API key fallback is configured.
func (h *Handler) HasFallback() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cfg.APIKey != "" && (h.cfg.Mode == types.AuthModeBoth || h.cfg.Mode == types.AuthModeSubscription)
}

// DetectAuthMode classifies the auth type from request headers.
func (h *Handler) DetectAuthMode(headers http.Header) (string, bool) {
	xAPIKey := strings.TrimSpace(headers.Get("x-api-key"))
	bearer := types.BearerToken(headers.Get("Authorization"))

	if xAPIKey != "" {
		return "api_key", false
	}

	if bearer != "" {
		// Anthropic subscription OAuth tokens use sk-ant-oat... prefixes
		if strings.HasPrefix(bearer, "sk-ant-oat") {
			return "subscription", true
		}
		// Anthropic API keys may be sent in Authorization by some clients
		if strings.HasPrefix(bearer, "sk-ant-") {
			return "api_key", false
		}
		return "bearer", false
	}

	return "none", false
}

// GetOAuthToken returns the current OAuth token if available.
// Used by gateway to inject auth into requests.
func (h *Handler) GetOAuthToken() string {
	if h.tokenManager == nil {
		return ""
	}
	return h.tokenManager.GetAccessToken()
}

// HasOAuthCredentials returns true if OAuth credentials are loaded.
func (h *Handler) HasOAuthCredentials() bool {
	if h.tokenManager == nil {
		return false
	}
	return h.tokenManager.HasCredentials()
}

// ForceRefresh forces an OAuth token refresh.
// Useful after receiving unexpected auth errors.
func (h *Handler) ForceRefresh() error {
	if h.tokenManager == nil {
		return nil
	}
	return h.tokenManager.ForceRefresh()
}

// Stop cleans up background goroutines.
func (h *Handler) Stop() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.tokenManager != nil && h.started {
		h.tokenManager.StopBackgroundRefresh()
		h.started = false
	}
}

// Verify Handler implements types.Handler interface.
var _ types.Handler = (*Handler)(nil)
