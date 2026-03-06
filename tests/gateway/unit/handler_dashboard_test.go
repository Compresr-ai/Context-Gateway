package unit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dashboardConfig() *config.Config {
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

type dashboardResponse struct {
	TotalCost float64 `json:"total_cost"`
	Savings   *struct {
		BilledSpendUSD    float64 `json:"billed_spend_usd"`
		CompressedCostUSD float64 `json:"compressed_cost_usd"`
		CostSavedUSD      float64 `json:"cost_saved_usd"`
		OriginalCostUSD   float64 `json:"original_cost_usd"`
	} `json:"savings"`
}

func TestHandleDashboardAPI_BilledSpendMatchesTotalCost(t *testing.T) {
	model := "claude-sonnet-4-5"
	sessionID := "session_test_1"

	cfg := dashboardConfig()
	gw := gateway.New(cfg)
	defer gw.Shutdown(context.Background())

	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	gw.CostTracker().RecordUsage(sessionID, model, 1000, 100, 0, 0)
	gw.SavingsTracker().RecordRequest(&monitoring.RequestEvent{
		Model:           model,
		CompressionUsed: true,
		InputTokens:     1000,
		OutputTokens:    100,
		Success:         true,
	}, sessionID)

	gw.SavingsTracker().RecordToolOutputCompression(monitoring.CompressionComparison{
		ProviderModel:   model,
		OriginalBytes:   400,
		CompressedBytes: 200,
		Status:          "compressed",
	}, sessionID)

	resp, err := http.Get(gwServer.URL + "/api/dashboard")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, 200, resp.StatusCode)

	var result dashboardResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.NotNil(t, result.Savings)

	assert.InDelta(t, result.TotalCost, result.Savings.BilledSpendUSD, 1e-12)
	assert.InDelta(t, result.TotalCost, result.Savings.CompressedCostUSD, 1e-12)
	assert.InDelta(t, result.Savings.CompressedCostUSD+result.Savings.CostSavedUSD, result.Savings.OriginalCostUSD, 1e-12)
}

func TestHandleDashboardAPI_NoSavingsStillShowsBilledSpend(t *testing.T) {
	cfg := dashboardConfig()
	gw := gateway.New(cfg)
	defer gw.Shutdown(context.Background())

	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	gw.CostTracker().RecordUsage("session_test_2", "claude-sonnet-4-5", 1000, 100, 0, 0)

	resp, err := http.Get(gwServer.URL + "/api/dashboard")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, 200, resp.StatusCode)

	var result dashboardResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.NotNil(t, result.Savings)

	assert.InDelta(t, result.TotalCost, result.Savings.BilledSpendUSD, 1e-12)
	assert.InDelta(t, result.TotalCost, result.Savings.CompressedCostUSD, 1e-12)
	assert.InDelta(t, result.TotalCost, result.Savings.OriginalCostUSD, 1e-12)
}

func TestHandleDashboardAPI_BilledSpendUsesGlobalScopeByDefault(t *testing.T) {
	cfg := dashboardConfig()
	gw := gateway.New(cfg)
	defer gw.Shutdown(context.Background())

	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	gw.CostTracker().RecordUsage("hash_a", "claude-sonnet-4-5", 1000, 100, 0, 0)
	gw.CostTracker().RecordUsage("hash_b", "claude-sonnet-4-5", 2000, 200, 0, 0)

	gw.SavingsTracker().RecordRequest(&monitoring.RequestEvent{
		Model:           "claude-sonnet-4-5",
		CompressionUsed: true,
		InputTokens:     1000,
		OutputTokens:    100,
		Success:         true,
	}, "hash_a")

	resp, err := http.Get(gwServer.URL + "/api/dashboard")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, 200, resp.StatusCode)

	var result dashboardResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.NotNil(t, result.Savings)

	assert.InDelta(t, result.TotalCost, result.Savings.BilledSpendUSD, 1e-12)
	assert.True(t, result.TotalCost > 0, "total cost should be positive")
}

func TestHandleDashboardAPI_BilledSpendUsesGlobalScopeWhenRequested(t *testing.T) {
	cfg := dashboardConfig()
	gw := gateway.New(cfg)
	defer gw.Shutdown(context.Background())

	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	gw.CostTracker().RecordUsage("hash_a", "claude-sonnet-4-5", 1000, 100, 0, 0)
	gw.CostTracker().RecordUsage("hash_b", "claude-sonnet-4-5", 2000, 200, 0, 0)

	resp, err := http.Get(gwServer.URL + "/api/dashboard?session=all")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, 200, resp.StatusCode)

	var result dashboardResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.NotNil(t, result.Savings)

	assert.InDelta(t, result.TotalCost, result.Savings.BilledSpendUSD, 1e-12)
}

func TestHandleDashboardAPI_CacheAwareSavingsValuation(t *testing.T) {
	model := "claude-sonnet-4-5"
	sessionID := "session_cache_test"

	cfg := dashboardConfig()
	gw := gateway.New(cfg)
	defer gw.Shutdown(context.Background())

	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	inputTokens := 10_000
	cacheReadTokens := 100_000
	outputTokens := 1000

	gw.CostTracker().RecordUsage(sessionID, model, inputTokens, outputTokens, 0, cacheReadTokens)
	gw.SavingsTracker().RecordRequest(&monitoring.RequestEvent{
		Model:                model,
		CompressionUsed:      true,
		InputTokens:          inputTokens,
		OutputTokens:         outputTokens,
		CacheReadInputTokens: cacheReadTokens,
		Success:              true,
	}, sessionID)

	gw.SavingsTracker().RecordToolOutputCompression(monitoring.CompressionComparison{
		ProviderModel:   model,
		OriginalBytes:   200,
		CompressedBytes: 0,
		Status:          "compressed",
	}, sessionID)

	resp, err := http.Get(gwServer.URL + "/api/dashboard")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, 200, resp.StatusCode)

	var result dashboardResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.NotNil(t, result.Savings)

	fullPriceSavings := 50.0 / 1_000_000 * 3.0
	assert.Less(t, result.Savings.CostSavedUSD, fullPriceSavings,
		"Savings should be valued below full input price in cache-heavy sessions")
	assert.Greater(t, result.Savings.CostSavedUSD, 0.0, "Should have some savings")

	assert.InDelta(t, result.Savings.CompressedCostUSD+result.Savings.CostSavedUSD,
		result.Savings.OriginalCostUSD, 0.001)
}
