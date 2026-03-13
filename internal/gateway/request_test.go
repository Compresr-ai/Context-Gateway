package gateway

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// normalizeOpenAIPath
// ---------------------------------------------------------------------------

func TestNormalizeOpenAIPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "already has v1 prefix", path: "/v1/chat/completions", want: "/v1/chat/completions"},
		{name: "responses without v1", path: "/responses", want: "/v1/responses"},
		{name: "other path unchanged", path: "/v1/models", want: "/v1/models"},
		{name: "empty path", path: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeOpenAIPath(tt.path)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// isChatGPTSubscription
// ---------------------------------------------------------------------------

func TestIsChatGPTSubscription(t *testing.T) {
	tests := []struct {
		name string
		auth string
		want bool
	}{
		{name: "API key", auth: "Bearer sk-abc123def", want: false},
		{name: "subscription token", auth: "Bearer eyJhbGciOiJIUzI1NiJ9", want: true},
		{name: "no bearer prefix", auth: "sk-abc123def", want: false},
		{name: "empty auth", auth: "", want: false},
		{name: "bearer only", auth: "Bearer ", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
			if tt.auth != "" {
				r.Header.Set("Authorization", tt.auth)
			}
			got := isChatGPTSubscription(r)
			assert.Equal(t, tt.want, got)
		})
	}
}
