// Dashboard Integration Tests
//
// Tests verify the gateway's HTTP endpoints (/health, /stats, /api/savings)
// by spinning up a real gateway with httptest.NewServer and making HTTP requests.
package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testConfig returns a minimal gateway config for endpoint tests.
func testConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         18090,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:          false,
				Strategy:         "passthrough",
				FallbackStrategy: "passthrough",
			},
			ToolDiscovery: config.ToolDiscoveryPipeConfig{
				Enabled: false,
			},
		},
		Store: config.StoreConfig{
			Type: "memory",
			TTL:  1 * time.Hour,
		},
		Monitoring: config.MonitoringConfig{
			LogLevel:  "error",
			LogFormat: "json",
			LogOutput: "stdout",
		},
	}
}

// createTestGateway creates a gateway and returns its httptest.Server.
func createTestGateway(cfg *config.Config) *httptest.Server {
	gw := gateway.New(cfg)
	return httptest.NewServer(gw.Handler())
}

// TestIntegration_Dashboard_HealthEndpoint verifies GET /health returns
// 200 with a JSON body containing status, time, and version fields.
func TestIntegration_Dashboard_HealthEndpoint(t *testing.T) {
	ts := createTestGateway(testConfig())
	defer ts.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(ts.URL + "/health")
	require.NoError(t, err, "GET /health should not return an error")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "health endpoint should return 200")
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json", "response should be JSON")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var health map[string]interface{}
	err = json.Unmarshal(body, &health)
	require.NoError(t, err, "response should be valid JSON")

	assert.Equal(t, "ok", health["status"], "status should be 'ok'")
	assert.NotEmpty(t, health["time"], "time field should be present")
	assert.NotEmpty(t, health["version"], "version field should be present")
}

// TestIntegration_Dashboard_StatsEndpoint verifies GET /stats returns
// valid JSON with the expected top-level fields (gateway, savings, etc.).
func TestIntegration_Dashboard_StatsEndpoint(t *testing.T) {
	ts := createTestGateway(testConfig())
	defer ts.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(ts.URL + "/stats")
	require.NoError(t, err, "GET /stats should not return an error")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "stats endpoint should return 200")
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var stats map[string]interface{}
	err = json.Unmarshal(body, &stats)
	require.NoError(t, err, "response should be valid JSON")

	// Verify expected top-level fields from StatsResponse
	assert.Contains(t, stats, "uptime", "should have uptime field")
	assert.Contains(t, stats, "gateway", "should have gateway field")
	assert.Contains(t, stats, "savings", "should have savings field")
	assert.Contains(t, stats, "expand_context", "should have expand_context field")

	// Verify gateway sub-fields
	gw, ok := stats["gateway"].(map[string]interface{})
	require.True(t, ok, "gateway should be an object")
	assert.Contains(t, gw, "total_requests")
	assert.Contains(t, gw, "compressions")

	// Verify savings sub-fields
	savings, ok := stats["savings"].(map[string]interface{})
	require.True(t, ok, "savings should be an object")
	assert.Contains(t, savings, "tokens_saved")
	assert.Contains(t, savings, "cost_saved_usd")
}

// TestIntegration_Dashboard_SavingsEndpoint verifies GET /api/savings returns
// a text/plain response with savings report content.
func TestIntegration_Dashboard_SavingsEndpoint(t *testing.T) {
	ts := createTestGateway(testConfig())
	defer ts.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(ts.URL + "/api/savings")
	require.NoError(t, err, "GET /api/savings should not return an error")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "savings endpoint should return 200")
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/plain", "savings should return text/plain")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// The endpoint always returns text, even if no data is available
	assert.NotEmpty(t, string(body), "savings response should not be empty")
}
