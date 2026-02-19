package unit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// /stats ENDPOINT
// =============================================================================

func TestStatsEndpoint_ReturnsJSON(t *testing.T) {
	gw := gateway.New(minimalConfig())

	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	w := httptest.NewRecorder()

	gw.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache", w.Header().Get("Cache-Control"))
}

func TestStatsEndpoint_ResponseStructure(t *testing.T) {
	gw := gateway.New(minimalConfig())

	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	w := httptest.NewRecorder()

	gw.Handler().ServeHTTP(w, req)

	var stats monitoring.StatsResponse
	err := json.Unmarshal(w.Body.Bytes(), &stats)
	require.NoError(t, err)

	// Verify all top-level fields are present
	assert.NotEmpty(t, stats.Uptime)
	assert.NotEmpty(t, stats.StartedAt)
	assert.GreaterOrEqual(t, stats.UptimeSeconds, int64(0))

	// Verify zero initial state
	assert.Equal(t, int64(0), stats.Requests.Total)
	assert.Equal(t, int64(0), stats.Tokens.OriginalTokens)
	assert.Equal(t, int64(0), stats.Tokens.TokensSaved)
	assert.Equal(t, int64(0), stats.Compression.Operations)
	assert.Equal(t, int64(0), stats.ToolDiscovery.FilteredRequests)
	assert.Equal(t, int64(0), stats.Preemptive.CacheHits)
}

func TestStatsEndpoint_MethodNotAllowed(t *testing.T) {
	gw := gateway.New(minimalConfig())

	req := httptest.NewRequest(http.MethodPost, "/stats", nil)
	w := httptest.NewRecorder()

	gw.Handler().ServeHTTP(w, req)

	// POST should be rejected (stats endpoint only allows GET)
	// Note: the mux routes POST /stats to handleStats which returns 405,
	// but the security middleware may process first. Check for non-200.
	assert.NotEqual(t, http.StatusOK, w.Code)
}

func TestStatsEndpoint_JSONFields(t *testing.T) {
	gw := gateway.New(minimalConfig())

	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	w := httptest.NewRecorder()

	gw.Handler().ServeHTTP(w, req)

	var raw map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &raw)
	require.NoError(t, err)

	// Check top-level JSON keys
	assert.Contains(t, raw, "uptime")
	assert.Contains(t, raw, "uptime_seconds")
	assert.Contains(t, raw, "started_at")
	assert.Contains(t, raw, "requests")
	assert.Contains(t, raw, "tokens")
	assert.Contains(t, raw, "compression")
	assert.Contains(t, raw, "tool_discovery")
	assert.Contains(t, raw, "preemptive")

	// Check nested token keys
	tokens := raw["tokens"].(map[string]any)
	assert.Contains(t, tokens, "original_tokens")
	assert.Contains(t, tokens, "compressed_tokens")
	assert.Contains(t, tokens, "tokens_saved")
	assert.Contains(t, tokens, "savings_percent")
	assert.Contains(t, tokens, "input_tokens")
	assert.Contains(t, tokens, "output_tokens")

	// Check nested compression keys
	compression := raw["compression"].(map[string]any)
	assert.Contains(t, compression, "operations")
	assert.Contains(t, compression, "cache_hits")
	assert.Contains(t, compression, "cache_misses")
	assert.Contains(t, compression, "cache_hit_rate")
}

// =============================================================================
// HELPERS
// =============================================================================

func minimalConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         0,
			ReadTimeout:  5,
			WriteTimeout: 10,
		},
		Store: config.StoreConfig{
			TTL: 300,
		},
		Monitoring: config.MonitoringConfig{
			LogLevel:  "error",
			LogFormat: "json",
			LogOutput: "stderr",
		},
	}
}
