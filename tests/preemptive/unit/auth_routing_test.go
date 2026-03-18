package preemptive_test

// Auth routing tests for the Summarizer.
//
// These tests verify the auth priority chain and correct header routing:
//   1. Configured ProviderKey → x-api-key header
//   2. Per-job SummarizeInput.Auth → honours IsXAPIKey flag
//   3. Global SetAuth captured from request → honours IsXAPIKey flag
//   4. Bearer tokens receive anthropic-beta header when BetaHeader is set
//   5. Endpoint from SetAuth / per-job Auth is forwarded correctly
//
// All tests use a mock HTTP server to inspect outbound request headers.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authtypes "github.com/compresr/context-gateway/internal/auth/types"
	"github.com/compresr/context-gateway/internal/preemptive"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// capturedHeaders stores headers received by the mock LLM server.
type capturedHeaders struct {
	mu      sync.Mutex
	headers http.Header
}

func (c *capturedHeaders) set(h http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.headers = h.Clone()
}

func (c *capturedHeaders) get() http.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.headers
}

// mockAnthropicResponse is a minimal valid Anthropic Messages API response.
func mockAnthropicResponse(text string) []byte {
	resp := map[string]interface{}{
		"id":   "msg_test123",
		"type": "message",
		"role": "assistant",
		"content": []map[string]interface{}{
			{"type": "text", "text": text},
		},
		"model":        "claude-haiku-4-5",
		"stop_reason":  "end_turn",
		"stop_sequence": nil,
		"usage": map[string]int{
			"input_tokens":  50,
			"output_tokens": 20,
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

// newMockLLMServer starts an httptest server that records request headers and
// returns a valid Anthropic response.  The returned capturedHeaders is updated
// on every request.
func newMockLLMServer(t *testing.T, cap *capturedHeaders) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.set(r.Header)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(mockAnthropicResponse("test summary"))
	}))
}

// newLLMSummarizer builds a Summarizer using the external_provider strategy
// (summarizeViaLLM) pointed at a given URL.  Provider is set explicitly to
// "anthropic" so auth header routing matches production behaviour.
func newLLMSummarizer(serverURL, providerKey string) *preemptive.Summarizer {
	cfg := preemptive.SummarizerConfig{
		Strategy:    preemptive.StrategyExternalProvider,
		Provider:    "anthropic",
		Model:       "claude-haiku-4-5",
		ProviderKey: providerKey,
		Endpoint:    serverURL,
		MaxTokens:   256,
		Timeout:     5 * time.Second,
	}
	return preemptive.NewSummarizer(cfg)
}

// twoMessages returns a SummarizeInput with 2 messages — minimum to trigger summarisation.
func twoMessages() preemptive.SummarizeInput {
	return preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "Hello, please summarize"),
			makeMessage("assistant", "Sure, here is a summary."),
		},
		KeepRecentCount: 1,
	}
}

// ---------------------------------------------------------------------------
// Auth routing: configured ProviderKey
// ---------------------------------------------------------------------------

func TestSummarizer_ConfiguredProviderKey_SentAsXAPIKey(t *testing.T) {
	cap := &capturedHeaders{}
	server := newMockLLMServer(t, cap)
	defer server.Close()

	// ProviderKey set in config → must always use x-api-key header
	s := newLLMSummarizer(server.URL, "sk-ant-configured-key")

	_, err := s.Summarize(t.Context(), twoMessages())
	require.NoError(t, err)

	h := cap.get()
	assert.Equal(t, "sk-ant-configured-key", h.Get("x-api-key"), "configured key must be in x-api-key")
	assert.Empty(t, h.Get("Authorization"), "Authorization must not be set when configured key is used")
}

func TestSummarizer_ConfiguredProviderKey_OverridesSetAuth(t *testing.T) {
	// Even if SetAuth is called with a bearer token, the configured key wins.
	cap := &capturedHeaders{}
	server := newMockLLMServer(t, cap)
	defer server.Close()

	s := newLLMSummarizer(server.URL, "sk-ant-config-wins")
	s.SetAuth(authtypes.CapturedAuth{
		Token:     "sk-ant-oat-bearer-ignored",
		IsXAPIKey: false,
	})

	_, err := s.Summarize(t.Context(), twoMessages())
	require.NoError(t, err)

	h := cap.get()
	assert.Equal(t, "sk-ant-config-wins", h.Get("x-api-key"), "configured key must win over SetAuth bearer")
	assert.Empty(t, h.Get("Authorization"), "bearer must not be set when configured key wins")
}

func TestSummarizer_ConfiguredProviderKey_OverridesPerJobAuth(t *testing.T) {
	// Per-job SummarizeInput.Auth must not override the configured provider key.
	cap := &capturedHeaders{}
	server := newMockLLMServer(t, cap)
	defer server.Close()

	s := newLLMSummarizer(server.URL, "sk-ant-config-wins")

	input := twoMessages()
	input.Auth = authtypes.CapturedAuth{
		Token:     "sk-ant-oat-per-job-ignored",
		IsXAPIKey: false,
	}

	_, err := s.Summarize(t.Context(), input)
	require.NoError(t, err)

	h := cap.get()
	assert.Equal(t, "sk-ant-config-wins", h.Get("x-api-key"), "configured key must win over per-job auth")
	assert.Empty(t, h.Get("Authorization"))
}

// ---------------------------------------------------------------------------
// Auth routing: SetAuth (global captured auth)
// ---------------------------------------------------------------------------

func TestSummarizer_SetAuth_BearerToken_RoutesToAuthorizationHeader(t *testing.T) {
	// API key users are subscription/OAuth; token must go in Authorization: Bearer
	cap := &capturedHeaders{}
	server := newMockLLMServer(t, cap)
	defer server.Close()

	s := newLLMSummarizer(server.URL, "") // no configured key
	s.SetAuth(authtypes.CapturedAuth{
		Token:     "sk-ant-oat456",
		IsXAPIKey: false,
	})

	_, err := s.Summarize(t.Context(), twoMessages())
	require.NoError(t, err)

	h := cap.get()
	assert.Equal(t, "Bearer sk-ant-oat456", h.Get("Authorization"), "bearer token must appear in Authorization header")
	assert.Empty(t, h.Get("x-api-key"), "x-api-key must not be set for bearer auth")
}

func TestSummarizer_SetAuth_APIKey_RoutesToXAPIKeyHeader(t *testing.T) {
	cap := &capturedHeaders{}
	server := newMockLLMServer(t, cap)
	defer server.Close()

	s := newLLMSummarizer(server.URL, "")
	s.SetAuth(authtypes.CapturedAuth{
		Token:     "sk-ant-api123",
		IsXAPIKey: true,
	})

	_, err := s.Summarize(t.Context(), twoMessages())
	require.NoError(t, err)

	h := cap.get()
	assert.Equal(t, "sk-ant-api123", h.Get("x-api-key"), "API key must appear in x-api-key header")
	assert.Empty(t, h.Get("Authorization"), "Authorization must not be set for API key auth")
}

func TestSummarizer_SetAuth_NoAuthIgnored(t *testing.T) {
	// SetAuth with empty Token must be ignored (HasAuth() returns false).
	// Without any auth the validate() in callLLM returns an error.
	s := newLLMSummarizer("http://unused:9999", "")
	s.SetAuth(authtypes.CapturedAuth{}) // empty — should be ignored

	_, err := s.Summarize(t.Context(), twoMessages())
	require.Error(t, err, "should fail because no auth is available")
	assert.Contains(t, err.Error(), "api key or bearer token required")
}

// ---------------------------------------------------------------------------
// Auth routing: per-job SummarizeInput.Auth overrides global SetAuth
// ---------------------------------------------------------------------------

func TestSummarizer_PerJobAuth_OverridesGlobalSetAuth(t *testing.T) {
	cap := &capturedHeaders{}
	server := newMockLLMServer(t, cap)
	defer server.Close()

	s := newLLMSummarizer(server.URL, "")
	// Global captured auth from a different session
	s.SetAuth(authtypes.CapturedAuth{
		Token:     "sk-ant-oat-global-session",
		IsXAPIKey: false,
	})

	// Per-job auth for this specific request
	input := twoMessages()
	input.Auth = authtypes.CapturedAuth{
		Token:     "sk-ant-api-per-job",
		IsXAPIKey: true,
	}

	_, err := s.Summarize(t.Context(), input)
	require.NoError(t, err)

	h := cap.get()
	assert.Equal(t, "sk-ant-api-per-job", h.Get("x-api-key"), "per-job auth must win over global SetAuth")
	assert.Empty(t, h.Get("Authorization"))
}

func TestSummarizer_PerJobAuth_BearerOverridesGlobalAPIKey(t *testing.T) {
	cap := &capturedHeaders{}
	server := newMockLLMServer(t, cap)
	defer server.Close()

	s := newLLMSummarizer(server.URL, "")
	s.SetAuth(authtypes.CapturedAuth{
		Token:     "sk-ant-api-global",
		IsXAPIKey: true,
	})

	input := twoMessages()
	input.Auth = authtypes.CapturedAuth{
		Token:     "sk-ant-oat-per-job",
		IsXAPIKey: false,
	}

	_, err := s.Summarize(t.Context(), input)
	require.NoError(t, err)

	h := cap.get()
	assert.Equal(t, "Bearer sk-ant-oat-per-job", h.Get("Authorization"), "per-job bearer must win over global API key")
	assert.Empty(t, h.Get("x-api-key"))
}

// ---------------------------------------------------------------------------
// anthropic-beta header injection
// ---------------------------------------------------------------------------

func TestSummarizer_BetaHeader_InjectedForBearerToken(t *testing.T) {
	// OAuth tokens (Max/Pro) require anthropic-beta header to unlock extended context.
	cap := &capturedHeaders{}
	server := newMockLLMServer(t, cap)
	defer server.Close()

	s := newLLMSummarizer(server.URL, "")
	s.SetAuth(authtypes.CapturedAuth{
		Token:      "sk-ant-oat456",
		IsXAPIKey:  false,
		BetaHeader: "max-tokens-3-5-sonnet-2024-07-15",
	})

	_, err := s.Summarize(t.Context(), twoMessages())
	require.NoError(t, err)

	h := cap.get()
	assert.Equal(t, "max-tokens-3-5-sonnet-2024-07-15", h.Get("anthropic-beta"),
		"anthropic-beta must be forwarded for OAuth bearer tokens")
}

func TestSummarizer_BetaHeader_NotInjectedForAPIKey(t *testing.T) {
	// x-api-key users never need the beta header (it's an OAuth-only feature).
	cap := &capturedHeaders{}
	server := newMockLLMServer(t, cap)
	defer server.Close()

	s := newLLMSummarizer(server.URL, "")
	s.SetAuth(authtypes.CapturedAuth{
		Token:      "sk-ant-api123",
		IsXAPIKey:  true,
		BetaHeader: "max-tokens-3-5-sonnet-2024-07-15", // should be ignored for API keys
	})

	_, err := s.Summarize(t.Context(), twoMessages())
	require.NoError(t, err)

	h := cap.get()
	assert.Empty(t, h.Get("anthropic-beta"),
		"anthropic-beta must NOT be set when using x-api-key")
}

func TestSummarizer_PerJobAuth_BetaHeaderFromPerJobAuth(t *testing.T) {
	// Per-job bearer + beta header must be forwarded (not global auth's beta).
	cap := &capturedHeaders{}
	server := newMockLLMServer(t, cap)
	defer server.Close()

	s := newLLMSummarizer(server.URL, "")
	s.SetAuth(authtypes.CapturedAuth{
		Token:      "sk-ant-oat-global",
		IsXAPIKey:  false,
		BetaHeader: "global-beta-header",
	})

	input := twoMessages()
	input.Auth = authtypes.CapturedAuth{
		Token:      "sk-ant-oat-per-job",
		IsXAPIKey:  false,
		BetaHeader: "interleaved-thinking-2025-05-14",
	}

	_, err := s.Summarize(t.Context(), input)
	require.NoError(t, err)

	h := cap.get()
	assert.Equal(t, "interleaved-thinking-2025-05-14", h.Get("anthropic-beta"),
		"per-job beta header must be used, not global")
}

// ---------------------------------------------------------------------------
// Endpoint routing via SetAuth
// ---------------------------------------------------------------------------

func TestSummarizer_SetAuth_EndpointUsedAsCallTarget(t *testing.T) {
	// When SetAuth provides an Endpoint, it must be used as the call target
	// (OAuth tokens are endpoint-bound).
	cap := &capturedHeaders{}
	server := newMockLLMServer(t, cap)
	defer server.Close()

	// Build a summarizer with no configured endpoint — rely on SetAuth.
	cfg := preemptive.SummarizerConfig{
		Strategy:  preemptive.StrategyExternalProvider,
		Provider:  "anthropic",
		Model:     "claude-haiku-4-5",
		MaxTokens: 256,
		Timeout:   5 * time.Second,
		// Endpoint deliberately empty — must come from SetAuth
	}
	s := preemptive.NewSummarizer(cfg)
	s.SetAuth(authtypes.CapturedAuth{
		Token:     "sk-ant-oat456",
		IsXAPIKey: false,
		Endpoint:  server.URL,
	})

	_, err := s.Summarize(t.Context(), twoMessages())
	require.NoError(t, err)

	// The fact that the server was reached (no connection-refused error) proves
	// the correct endpoint was used.
	h := cap.get()
	assert.NotEmpty(t, h, "server must have been reached via SetAuth endpoint")
	assert.Equal(t, "Bearer sk-ant-oat456", h.Get("Authorization"))
}

func TestSummarizer_PerJobAuth_EndpointOverridesSetAuthEndpoint(t *testing.T) {
	// Per-job Auth.Endpoint takes priority over global SetAuth.Endpoint.
	cap := &capturedHeaders{}
	server := newMockLLMServer(t, cap)
	defer server.Close()

	// The global captured auth points somewhere wrong (not the test server).
	// Per-job auth points to the test server — it must win.
	cfg := preemptive.SummarizerConfig{
		Strategy:  preemptive.StrategyExternalProvider,
		Provider:  "anthropic",
		Model:     "claude-haiku-4-5",
		MaxTokens: 256,
		Timeout:   5 * time.Second,
	}
	s := preemptive.NewSummarizer(cfg)
	s.SetAuth(authtypes.CapturedAuth{
		Token:     "sk-ant-oat-global",
		IsXAPIKey: false,
		Endpoint:  "http://127.0.0.1:1", // unreachable — must not be used
	})

	input := twoMessages()
	input.Auth = authtypes.CapturedAuth{
		Token:     "sk-ant-oat-per-job",
		IsXAPIKey: false,
		Endpoint:  server.URL, // per-job endpoint must win
	}

	_, err := s.Summarize(t.Context(), input)
	require.NoError(t, err)

	h := cap.get()
	assert.Equal(t, "Bearer sk-ant-oat-per-job", h.Get("Authorization"))
}

// ---------------------------------------------------------------------------
// CapturedAuth struct unit tests (not Summarizer-specific)
// ---------------------------------------------------------------------------

func TestCapturedAuth_HasAuth_PriorityRules(t *testing.T) {
	tests := []struct {
		name      string
		auth      authtypes.CapturedAuth
		wantAuth  bool
		wantXAPI  bool
		wantToken string
	}{
		{
			name:      "api key user",
			auth:      authtypes.CapturedAuth{Token: "sk-ant-api123", IsXAPIKey: true},
			wantAuth:  true,
			wantXAPI:  true,
			wantToken: "sk-ant-api123",
		},
		{
			name:      "bearer/oauth user",
			auth:      authtypes.CapturedAuth{Token: "sk-ant-oat456", IsXAPIKey: false},
			wantAuth:  true,
			wantXAPI:  false,
			wantToken: "sk-ant-oat456",
		},
		{
			name:     "empty - no auth",
			auth:     authtypes.CapturedAuth{},
			wantAuth: false,
		},
		{
			name:     "endpoint only - no auth",
			auth:     authtypes.CapturedAuth{Endpoint: "https://api.anthropic.com/v1/messages"},
			wantAuth: false,
		},
		{
			name:     "beta only - no auth",
			auth:     authtypes.CapturedAuth{BetaHeader: "max-tokens-3-5-sonnet-2024-07-15"},
			wantAuth: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantAuth, tt.auth.HasAuth())
			if tt.wantAuth {
				assert.Equal(t, tt.wantXAPI, tt.auth.IsXAPIKey)
				assert.Equal(t, tt.wantToken, tt.auth.Token)
			}
		})
	}
}
