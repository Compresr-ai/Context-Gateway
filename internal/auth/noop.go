package auth

import (
	"net/http"

	"github.com/compresr/context-gateway/internal/auth/types"
)

// NoOpHandler is a default handler for providers without special auth handling.
// It simply passes through requests without any auth management.
type NoOpHandler struct {
	provider string
	cfg      types.AuthConfig
}

// Name returns the provider name.
func (h *NoOpHandler) Name() string {
	return h.provider
}

// Initialize stores the config but doesn't do any setup.
func (h *NoOpHandler) Initialize(cfg types.AuthConfig) error {
	h.cfg = cfg
	return nil
}

// GetAuthMode returns the configured auth mode.
func (h *NoOpHandler) GetAuthMode() types.AuthMode {
	return h.cfg.Mode
}

// ShouldFallback always returns false - no fallback logic.
func (h *NoOpHandler) ShouldFallback(statusCode int, responseBody []byte) types.FallbackResult {
	return types.FallbackResult{ShouldFallback: false}
}

// GetFallbackHeaders returns nil - no fallback configured.
func (h *NoOpHandler) GetFallbackHeaders() map[string]string {
	return nil
}

// HasFallback returns false.
func (h *NoOpHandler) HasFallback() bool {
	return false
}

// DetectAuthMode returns generic detection.
func (h *NoOpHandler) DetectAuthMode(headers http.Header) (string, bool) {
	if headers.Get(types.HeaderXAPIKey) != "" {
		return "api_key", false
	}
	if headers.Get(types.HeaderAuthorization) != "" {
		return "bearer", false
	}
	return "none", false
}

// Stop is a no-op.
func (h *NoOpHandler) Stop() {}

// Verify NoOpHandler implements Handler interface.
var _ types.Handler = (*NoOpHandler)(nil)
