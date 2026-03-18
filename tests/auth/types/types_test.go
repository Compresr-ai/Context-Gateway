package types_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	authtypes "github.com/compresr/context-gateway/internal/auth/types"
)

func TestCaptureFromHeaders_APIKey(t *testing.T) {
	h := http.Header{}
	h.Set("x-api-key", "sk-ant-api123")

	got := authtypes.CaptureFromHeaders(h)

	assert.Equal(t, "sk-ant-api123", got.Token)
	assert.True(t, got.IsXAPIKey)
	assert.Empty(t, got.BetaHeader)
	assert.Empty(t, got.Endpoint)
	assert.True(t, got.HasAuth())
}

func TestCaptureFromHeaders_Bearer(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-ant-oat456")

	got := authtypes.CaptureFromHeaders(h)

	assert.Equal(t, "sk-ant-oat456", got.Token)
	assert.False(t, got.IsXAPIKey)
	assert.Empty(t, got.BetaHeader)
	assert.True(t, got.HasAuth())
}

func TestCaptureFromHeaders_APIKeyTakesPriorityOverBearer(t *testing.T) {
	h := http.Header{}
	h.Set("x-api-key", "sk-ant-api123")
	h.Set("Authorization", "Bearer sk-ant-oat456")

	got := authtypes.CaptureFromHeaders(h)

	assert.Equal(t, "sk-ant-api123", got.Token)
	assert.True(t, got.IsXAPIKey, "x-api-key must win over Bearer")
}

func TestCaptureFromHeaders_BetaHeader(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-ant-oat456")
	h.Set("anthropic-beta", "max-tokens-3-5-sonnet-2024-07-15")

	got := authtypes.CaptureFromHeaders(h)

	assert.Equal(t, "max-tokens-3-5-sonnet-2024-07-15", got.BetaHeader)
	assert.True(t, got.HasAuth())
}

func TestCaptureFromHeaders_Endpoint(t *testing.T) {
	h := http.Header{}
	h.Set("x-api-key", "sk-ant-api123")
	h.Set("X-Target-URL", "https://api.anthropic.com/v1/messages")

	got := authtypes.CaptureFromHeaders(h)

	assert.Equal(t, "https://api.anthropic.com/v1/messages", got.Endpoint)
}

func TestCaptureFromHeaders_AllFields(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-ant-oat456")
	h.Set("anthropic-beta", "interleaved-thinking-2025-05-14")
	h.Set("X-Target-URL", "https://api.anthropic.com/v1/messages")

	got := authtypes.CaptureFromHeaders(h)

	assert.Equal(t, "sk-ant-oat456", got.Token)
	assert.False(t, got.IsXAPIKey)
	assert.Equal(t, "interleaved-thinking-2025-05-14", got.BetaHeader)
	assert.Equal(t, "https://api.anthropic.com/v1/messages", got.Endpoint)
	assert.True(t, got.HasAuth())
}

func TestCaptureFromHeaders_NoAuth(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")

	got := authtypes.CaptureFromHeaders(h)

	assert.Empty(t, got.Token)
	assert.False(t, got.HasAuth())
}

func TestCaptureFromHeaders_NonBearerAuthIgnored(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Basic dXNlcjpwYXNz")

	got := authtypes.CaptureFromHeaders(h)

	assert.Empty(t, got.Token)
	assert.False(t, got.HasAuth())
}

// AZURE api-key HEADER

func TestCaptureFromHeaders_AzureAPIKey(t *testing.T) {
	h := http.Header{}
	h.Set("api-key", "azure-key-abc123")

	got := authtypes.CaptureFromHeaders(h)

	assert.Equal(t, "azure-key-abc123", got.Token)
	assert.True(t, got.IsXAPIKey, "Azure api-key must set IsXAPIKey=true")
	assert.True(t, got.HasAuth())
}

func TestCaptureFromHeaders_AzureAPIKeyPriorityOverBearer(t *testing.T) {
	h := http.Header{}
	h.Set("api-key", "azure-key-abc123")
	h.Set("Authorization", "Bearer entra-oauth-token")

	got := authtypes.CaptureFromHeaders(h)

	assert.Equal(t, "azure-key-abc123", got.Token)
	assert.True(t, got.IsXAPIKey, "api-key must win over Authorization: Bearer")
}

func TestCaptureFromHeaders_XAPIKeyPriorityOverAzureAPIKey(t *testing.T) {
	h := http.Header{}
	h.Set("x-api-key", "anthropic-key")
	h.Set("api-key", "azure-key")

	got := authtypes.CaptureFromHeaders(h)

	assert.Equal(t, "anthropic-key", got.Token)
	assert.True(t, got.IsXAPIKey, "x-api-key must win over api-key")
}

func TestCaptureFromHeaders_AllThreeHeaders_XAPIKeyWins(t *testing.T) {
	// All three headers present — x-api-key has highest priority
	h := http.Header{}
	h.Set("x-api-key", "anthropic-key")
	h.Set("api-key", "azure-key")
	h.Set("Authorization", "Bearer oauth-token")

	got := authtypes.CaptureFromHeaders(h)

	assert.Equal(t, "anthropic-key", got.Token)
	assert.True(t, got.IsXAPIKey)
}

func TestCaptureFromHeaders_BetaHeaderWithoutAuth(t *testing.T) {
	// Beta header captured even when no auth present (valid edge case)
	h := http.Header{}
	h.Set("anthropic-beta", "prompt-caching-2024-07-31")

	got := authtypes.CaptureFromHeaders(h)

	assert.Equal(t, "prompt-caching-2024-07-31", got.BetaHeader)
	assert.False(t, got.HasAuth())
}

func TestCaptureFromHeaders_EndpointWithoutAuth(t *testing.T) {
	// Endpoint captured even when no auth present
	h := http.Header{}
	h.Set("X-Target-URL", "https://custom.endpoint.com/v1/messages")

	got := authtypes.CaptureFromHeaders(h)

	assert.Equal(t, "https://custom.endpoint.com/v1/messages", got.Endpoint)
	assert.False(t, got.HasAuth())
}

func TestCapturedAuth_HasAuth(t *testing.T) {
	tests := []struct {
		name string
		auth authtypes.CapturedAuth
		want bool
	}{
		{"empty", authtypes.CapturedAuth{}, false},
		{"token only", authtypes.CapturedAuth{Token: "sk-ant-123"}, true},
		{"api key user", authtypes.CapturedAuth{Token: "sk-ant-123", IsXAPIKey: true}, true},
		{"bearer user", authtypes.CapturedAuth{Token: "sk-ant-oat456", IsXAPIKey: false}, true},
		{"endpoint only no token", authtypes.CapturedAuth{Endpoint: "https://api.anthropic.com"}, false},
		{"beta only no token", authtypes.CapturedAuth{BetaHeader: "some-beta"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.auth.HasAuth())
		})
	}
}
