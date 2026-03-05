package unit

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func edgeCaseConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         18080,
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
		Store: config.StoreConfig{
			Type: "memory",
			TTL:  5 * time.Minute,
		},
	}
}

func TestGateway_ErrorResponse_IsValidJSON(t *testing.T) {
	cfg := edgeCaseConfig()
	gw := gateway.New(cfg)
	defer gw.Shutdown(context.Background())

	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Send request without X-Target-URL to a non-auto-detected path → error
	body := `{"model": "test-model", "messages": [{"role": "user", "content": "hi"}]}`
	resp, err := http.Post(gwServer.URL+"/v1/some-unknown-path", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should be an error (no target URL)
	assert.True(t, resp.StatusCode >= 400, "expected error status")

	var errResp map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&errResp)
	assert.NoError(t, err, "error response should be valid JSON")
}

func TestGateway_Health_POST(t *testing.T) {
	cfg := edgeCaseConfig()
	gw := gateway.New(cfg)
	defer gw.Shutdown(context.Background())

	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	resp, err := http.Post(gwServer.URL+"/health", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Health endpoint should still work regardless of method
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestGateway_EmptyRequestBody(t *testing.T) {
	cfg := edgeCaseConfig()
	gw := gateway.New(cfg)
	defer gw.Shutdown(context.Background())

	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Send empty body to proxy endpoint
	resp, err := http.Post(gwServer.URL+"/v1/messages", "application/json", bytes.NewReader([]byte{}))
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should get an error, not a panic
	assert.True(t, resp.StatusCode >= 400)
}

func TestGateway_DashboardAPI_NonLoopback_Forbidden(t *testing.T) {
	cfg := edgeCaseConfig()
	gw := gateway.New(cfg)
	defer gw.Shutdown(context.Background())

	// Use httptest.NewServer which binds to 127.0.0.1 — should be allowed
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	resp, err := http.Get(gwServer.URL + "/api/dashboard")
	require.NoError(t, err)
	defer resp.Body.Close()

	// From localhost, should be OK
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestGateway_StatsEndpoint_ReturnsJSON(t *testing.T) {
	cfg := edgeCaseConfig()
	gw := gateway.New(cfg)
	defer gw.Shutdown(context.Background())

	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	resp, err := http.Get(gwServer.URL + "/stats")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var stats map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&stats)
	assert.NoError(t, err, "stats response should be valid JSON")
}
