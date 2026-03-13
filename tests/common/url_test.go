package common

import (
	"testing"

	"github.com/compresr/context-gateway/internal/pipes"
	"github.com/stretchr/testify/assert"
)

func TestNormalizeEndpointURL(t *testing.T) {
	tests := []struct {
		name string
		base string
		path string
		want string
	}{
		{
			name: "simple join",
			base: "https://api.anthropic.com",
			path: "/v1/messages",
			want: "https://api.anthropic.com/v1/messages",
		},
		{
			name: "base with trailing slash",
			base: "https://api.anthropic.com/",
			path: "/v1/messages",
			want: "https://api.anthropic.com/v1/messages",
		},
		{
			name: "base with trailing and path with leading slash",
			base: "https://api.anthropic.com/",
			path: "/v1/messages",
			want: "https://api.anthropic.com/v1/messages",
		},
		{
			name: "preserves scheme",
			base: "https://example.com",
			path: "/path",
			want: "https://example.com/path",
		},
		{
			name: "http scheme preserved",
			base: "http://localhost:8080",
			path: "/api/v1",
			want: "http://localhost:8080/api/v1",
		},
		{
			name: "empty path",
			base: "https://api.example.com",
			path: "",
			want: "https://api.example.com",
		},
		{
			name: "empty base",
			base: "",
			path: "/v1/messages",
			want: "/v1/messages",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pipes.NormalizeEndpointURL(tt.base, tt.path)
			assert.Equal(t, tt.want, got)
		})
	}
}
