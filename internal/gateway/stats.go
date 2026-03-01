// Package gateway - stats.go exposes aggregated metrics as JSON.
//
// GET /stats returns combined savings, cost, and operational metrics.
package gateway

import (
	"encoding/json"
	"net/http"
	"time"
)

// StatsResponse is the JSON response for GET /stats.
type StatsResponse struct {
	Uptime  string `json:"uptime"`
	Gateway struct {
		TotalRequests      int64 `json:"total_requests"`
		SuccessfulRequests int64 `json:"successful_requests"`
		Compressions       int64 `json:"compressions"`
		CacheHits          int64 `json:"cache_hits"`
		CacheMisses        int64 `json:"cache_misses"`
	} `json:"gateway"`

	Savings struct {
		TokensSaved      int     `json:"tokens_saved"`
		TokenSavedPct    float64 `json:"token_saved_pct"`
		OriginalTokens   int     `json:"original_tokens"`
		CompressedTokens int     `json:"compressed_tokens"`
		CostSavedUSD     float64 `json:"cost_saved_usd"`
	} `json:"savings"`

	ExpandContext struct {
		Total    int `json:"total"`
		Found    int `json:"found"`
		NotFound int `json:"not_found"`
	} `json:"expand_context"`
}

var gatewayStartTime = time.Now()

// handleStats returns aggregated metrics as JSON.
// Restricted to localhost to prevent external access to operational metrics.
func (g *Gateway) handleStats(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var resp StatsResponse
	resp.Uptime = time.Since(gatewayStartTime).Truncate(time.Second).String()

	// Operational metrics
	if g.metrics != nil {
		stats := g.metrics.Stats()
		resp.Gateway.TotalRequests = stats["requests"]
		resp.Gateway.SuccessfulRequests = stats["successes"]
		resp.Gateway.Compressions = stats["compressions"]
		resp.Gateway.CacheHits = stats["cache_hits"]
		resp.Gateway.CacheMisses = stats["cache_misses"]
	}

	// Savings
	if g.savings != nil {
		report := g.savings.GetReport()
		resp.Savings.TokensSaved = report.TotalTokensSaved
		resp.Savings.TokenSavedPct = report.TotalSavedPct
		resp.Savings.OriginalTokens = report.OriginalTokens
		resp.Savings.CompressedTokens = report.CompressedTokens
		resp.Savings.CostSavedUSD = report.CostSavedUSD
	}

	// Expand context
	if g.expandLog != nil {
		summary := g.expandLog.Summary()
		resp.ExpandContext.Total = summary.Total
		resp.ExpandContext.Found = summary.Found
		resp.ExpandContext.NotFound = summary.NotFound
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
