package unit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/stretchr/testify/assert"
)

func ssrfTestConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         18090,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled: false,
			},
			ToolDiscovery: config.ToolDiscoveryPipeConfig{
				Enabled: false,
			},
		},
	}
}

// TestSSRF_BlocksMetadataEndpoint verifies that the SSRF protection blocks
// cloud metadata endpoints (169.254.169.254).
func TestSSRF_BlocksMetadataEndpoint(t *testing.T) {
	g := gateway.New(ssrfTestConfig())

	blockedHosts := []string{
		"169.254.169.254",
		"169.254.0.1",
		"metadata.google.internal",
	}

	for _, host := range blockedHosts {
		assert.False(t, g.IsAllowedHostForTest(host),
			"host %s should be blocked (metadata endpoint)", host)
	}
}

// TestSSRF_AllowsLegitimateProviders verifies that real LLM provider hosts are allowed.
func TestSSRF_AllowsLegitimateProviders(t *testing.T) {
	g := gateway.New(ssrfTestConfig())

	allowedHosts := []string{
		"api.openai.com",
		"api.anthropic.com",
		"generativelanguage.googleapis.com",
		"us-central1-aiplatform.googleapis.com",
	}

	for _, host := range allowedHosts {
		assert.True(t, g.IsAllowedHostForTest(host),
			"host %s should be allowed (legitimate provider)", host)
	}
}

// TestSSRF_BlocksBroadAWSSubdomains verifies that arbitrary .amazonaws.com
// subdomains are no longer allowed (only specific Bedrock hosts).
func TestSSRF_BlocksBroadAWSSubdomains(t *testing.T) {
	g := gateway.New(ssrfTestConfig())

	blockedAWS := []string{
		"s3.amazonaws.com",
		"ec2.amazonaws.com",
		"evil.amazonaws.com",
	}

	for _, host := range blockedAWS {
		assert.False(t, g.IsAllowedHostForTest(host),
			"host %s should be blocked (broad AWS subdomain)", host)
	}
}

// TestSecurityHeaders verifies that security headers are set on responses.
func TestSecurityHeaders(t *testing.T) {
	g := gateway.New(ssrfTestConfig())
	handler := g.Handler()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
}
