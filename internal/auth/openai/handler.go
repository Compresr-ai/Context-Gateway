// Package openai provides OpenAI/Codex-specific authentication handling.
//
// OpenAI auth is passthrough - the CLI (Codex) handles OAuth:
//   - Gateway does NOT manage OAuth tokens (CLI handles login/refresh)
//   - Gateway captures Bearer token from requests and passes through
//   - Falls back to API key on auth errors (401) AND quota errors (429, 402, 403)
//   - Key difference from Anthropic: 401 triggers fallback (expired CLI token)
package openai

import (
	"net/http"
	"strings"
	"sync"

	"github.com/compresr/context-gateway/internal/auth/types"
)

// Handler implements types.Handler for OpenAI/Codex.
type Handler struct {
	mu  sync.RWMutex
	cfg types.AuthConfig
}

// New creates a new OpenAI auth handler.
func New() *Handler {
	return &Handler{}
}

// Name returns "openai".
func (h *Handler) Name() string {
	return "openai"
}

// Initialize stores the configuration.
// For OpenAI, we don't manage OAuth - just store the fallback API key.
func (h *Handler) Initialize(cfg types.AuthConfig) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg = cfg
	// For subscription mode, we assume CLI OAuth is working until we see 401
	if cfg.Mode == types.AuthModeSubscription || cfg.Mode == types.AuthModeBoth {
		h.cfg.SubscriptionOK = true
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
// OpenAI/Codex fallback triggers on:
//   - 401 Unauthorized (expired/invalid CLI OAuth token) - KEY DIFFERENCE from Anthropic
//   - 429 Rate limit
//   - 402 Payment required
//   - 403 Forbidden (quota exceeded)
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

	msg := strings.ToLower(string(responseBody))

	// 401 Unauthorized - CLI OAuth token expired/invalid
	// This is the KEY DIFFERENCE from Anthropic: we trigger fallback on 401
	// because we can't refresh the token (CLI manages OAuth)
	if statusCode == 401 {
		authSignals := []string{
			"unauthorized",
			"invalid_api_key",
			"authentication",
			"invalid_token",
			"token_expired",
			"expired",
			"invalid credentials",
		}
		for _, signal := range authSignals {
			if strings.Contains(msg, signal) {
				return types.FallbackResult{
					ShouldFallback: true,
					Reason:         "CLI OAuth token expired/invalid (401)",
					Headers:        h.GetFallbackHeaders(),
				}
			}
		}
		// Even without specific signal, 401 strongly suggests auth failure
		return types.FallbackResult{
			ShouldFallback: true,
			Reason:         "authentication failed (401)",
			Headers:        h.GetFallbackHeaders(),
		}
	}

	// Quota/rate limit status codes (same as before)
	validStatusCodes := map[int]bool{
		429: true, // Rate limit
		402: true, // Payment required
		403: true, // Forbidden (can indicate quota)
	}

	if !validStatusCodes[statusCode] {
		return types.FallbackResult{ShouldFallback: false}
	}

	// Check for quota exhaustion signals
	quotaSignals := []string{
		"insufficient_quota",
		"rate_limit_exceeded",
		"billing_hard_limit_reached",
		"quota exceeded",
		"rate limit",
		"usage limit",
	}

	for _, signal := range quotaSignals {
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

	// OpenAI uses Authorization: Bearer for API keys
	return map[string]string{
		types.HeaderAuthorization: "Bearer " + apiKey,
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
	bearer := types.BearerToken(headers.Get("Authorization"))

	if bearer != "" {
		// OpenAI API keys start with sk-
		if strings.HasPrefix(bearer, "sk-") {
			return "api_key", false
		}
		// Non-sk bearer token = OAuth (Codex CLI, ChatGPT Plus)
		return "subscription", true
	}

	return "none", false
}

// Stop is a no-op for OpenAI (no background processes).
func (h *Handler) Stop() {}

// Verify Handler implements types.Handler interface.
var _ types.Handler = (*Handler)(nil)
