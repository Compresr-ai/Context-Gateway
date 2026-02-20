package gateway

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/config"
)

const (
	defaultAuthFallbackTTL = time.Hour
)

// authFallbackStore keeps per-session auth mode for subscription->api key fallback.
type authFallbackStore struct {
	mu       sync.RWMutex
	sessions map[string]time.Time // session_id -> last fallback time
	ttl      time.Duration
}

func newAuthFallbackStore(ttl time.Duration) *authFallbackStore {
	if ttl <= 0 {
		ttl = defaultAuthFallbackTTL
	}
	s := &authFallbackStore{
		sessions: make(map[string]time.Time),
		ttl:      ttl,
	}
	go s.cleanupLoop()
	return s
}

func (s *authFallbackStore) MarkAPIKeyMode(sessionID string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = time.Now()
}

func (s *authFallbackStore) ShouldUseAPIKeyMode(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	s.mu.RLock()
	t, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Since(t) > s.ttl {
		s.mu.Lock()
		delete(s.sessions, sessionID)
		s.mu.Unlock()
		return false
	}
	return true
}

func (s *authFallbackStore) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.cleanup()
	}
}

func (s *authFallbackStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, t := range s.sessions {
		if now.Sub(t) > s.ttl {
			delete(s.sessions, id)
		}
	}
}

// providerExhaustionSignals defines provider-specific error patterns for subscription/quota exhaustion.
var providerExhaustionSignals = map[adapters.Provider]struct {
	statusCodesMatters bool             // if true, check status codes first
	statusCodesSet     map[int]struct{} // status codes that suggest exhaustion
	signals            []string         // error message patterns
}{
	adapters.ProviderAnthropic: {
		statusCodesMatters: true,
		statusCodesSet:     map[int]struct{}{429: {}, 529: {}, 402: {}},
		signals: []string{
			"rate_limit_error",
			"rate limit",
			"overloaded_error",
			"quota exceeded",
			"credit balance",
			"billing",
			"usage limit",
			"subscription",
		},
	},
	adapters.ProviderOpenAI: {
		statusCodesMatters: true,
		statusCodesSet:     map[int]struct{}{429: {}, 402: {}, 403: {}},
		signals: []string{
			"insufficient_quota",
			"rate_limit_exceeded",
			"billing_hard_limit_reached",
			"quota exceeded",
			"rate limit",
		},
	},
	adapters.ProviderGemini: {
		statusCodesMatters: true,
		statusCodesSet:     map[int]struct{}{429: {}, 403: {}},
		signals: []string{
			"quota_exceeded",
			"rate_limit",
			"resource_exhausted",
		},
	},
	adapters.ProviderBedrock: {
		statusCodesMatters: true,
		statusCodesSet:     map[int]struct{}{429: {}, 400: {}},
		signals: []string{
			"throttlingexception",
			"servicequotaexceededexception",
			"modelstreamexception",
		},
	},
}

// fallbackSignals are generic patterns used when provider is unknown or as a secondary check.
var fallbackSignals = []string{
	"rate limit",
	"quota",
	"credit balance",
	"billing",
	"usage limit",
	"too many requests",
	"exceeded",
}

func isLikelySubscriptionExhausted(provider adapters.Provider, statusCode int, responseBody []byte) bool {
	msg := strings.ToLower(string(responseBody))

	// Provider-specific matching
	if config, ok := providerExhaustionSignals[provider]; ok {
		if config.statusCodesMatters {
			if _, validStatus := config.statusCodesSet[statusCode]; !validStatus {
				return false
			}
		}
		for _, signal := range config.signals {
			if strings.Contains(msg, signal) {
				return true
			}
		}
		return false
	}

	// Fallback for unknown providers: use common status codes and generic signals
	if statusCode != 402 && statusCode != 403 && statusCode != 429 {
		return false
	}
	for _, signal := range fallbackSignals {
		if strings.Contains(msg, signal) {
			return true
		}
	}
	return false
}

func bearerTokenValue(authHeader string) string {
	if authHeader == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(authHeader, prefix))
}

// Providers that support subscription->api_key fallback.
var subscriptionFallbackProviders = map[adapters.Provider]bool{
	adapters.ProviderAnthropic: true,
}

// detectAuthMode classifies incoming auth for telemetry and fallback decisions.
// Returns (mode, isSubscriptionAuth).
func detectAuthMode(provider adapters.Provider, headers http.Header) (string, bool) {
	xAPI := strings.TrimSpace(headers.Get("x-api-key"))
	googAPI := strings.TrimSpace(headers.Get("x-goog-api-key"))
	apiKey := strings.TrimSpace(headers.Get("api-key"))
	bearer := bearerTokenValue(headers.Get("Authorization"))

	// Provider-specific classification.
	switch provider {
	case adapters.ProviderAnthropic:
		if xAPI != "" {
			return "api_key", false
		}
		if bearer != "" {
			// Anthropic subscription OAuth tokens use sk-ant-oat... prefixes.
			if strings.HasPrefix(bearer, "sk-ant-oat") {
				return "subscription", true
			}
			// Anthropic API keys may be sent in Authorization by some clients.
			if strings.HasPrefix(bearer, "sk-ant-") {
				return "api_key", false
			}
			return "bearer", false
		}
		return "none", false
	case adapters.ProviderGemini:
		if googAPI != "" || apiKey != "" {
			return "api_key", false
		}
		if bearer != "" {
			return "bearer", false
		}
		return "none", false
	default:
		if xAPI != "" || googAPI != "" || apiKey != "" {
			return "api_key", false
		}
		if bearer != "" {
			return "bearer", false
		}
		return "none", false
	}
}

func resolveFallbackAPIKey(provider adapters.Provider, providers config.ProvidersConfig) string {
	if !subscriptionFallbackProviders[provider] {
		return ""
	}
	p, ok := providers[provider.String()]
	if !ok {
		return ""
	}
	return strings.TrimSpace(p.APIKey)
}
