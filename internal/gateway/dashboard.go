// Package gateway - dashboard.go serves the centralized React dashboard SPA at /dashboard.
//
// DESIGN: The dashboard runs on a fixed port (18080), separate from gateway proxy ports
// (18081-18090). Only the first gateway instance to start claims the dashboard port.
// The dashboard aggregates data from ALL active gateway instances by querying their
// /api/dashboard endpoints, and reads prompt history from the shared SQLite database.
package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/dashboard"
)

// tryStartDashboardServer attempts to bind the centralized dashboard port (18080).
// If successful, starts the dashboard HTTP server in a background goroutine.
// If the port is already taken (another gateway instance), skips gracefully.
func (g *Gateway) tryStartDashboardServer() {
	dashPort := config.DefaultDashboardPort
	addr := fmt.Sprintf(":%d", dashPort)

	// Test if port is available before creating the server
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Debug().Int("port", dashPort).Msg("dashboard port already in use (another instance serving)")
		return
	}
	if err := ln.Close(); err != nil {
		log.Warn().Err(err).Msg("dashboard: failed to close probe listener")
		return
	}

	dashMux := http.NewServeMux()
	g.setupDashboardRoutes(dashMux)

	g.dashboardServer = &http.Server{
		Addr:           addr,
		Handler:        g.panicRecovery(g.loggingMiddleware(g.security(dashMux))),
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   60 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	g.dashboardStarted = true

	go func() {
		log.Info().Int("port", dashPort).Msg("centralized dashboard server starting")
		if err := g.dashboardServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Int("port", dashPort).Msg("dashboard server error")
		}
	}()
}

// handleDashboard serves the React dashboard SPA at /dashboard/.
// Restricted to localhost to prevent external access.
func (g *Gateway) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		g.writeError(w, "forbidden", http.StatusForbidden)
		return
	}
	// Redirect /dashboard to /dashboard/ so relative asset paths work
	if r.URL.Path == "/dashboard" {
		http.Redirect(w, r, "/dashboard/", http.StatusMovedPermanently)
		return
	}

	// Serve embedded React SPA if available
	if g.dashboardFS != nil {
		http.StripPrefix("/dashboard", g.dashboardFS).ServeHTTP(w, r)
		return
	}

	// Fallback: show minimal HTML that links to the JSON API
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>Context Gateway Dashboard</title>
<style>
  body { font-family: system-ui, sans-serif; background: #0a0a0a; color: #fff; display: flex; align-items: center; justify-content: center; min-height: 100vh; margin: 0; }
  .container { text-align: center; padding: 48px; }
  h1 { font-size: 24px; margin-bottom: 16px; }
  p { color: #9ca3af; margin-bottom: 24px; }
  a { color: #22c55e; text-decoration: none; font-family: monospace; }
  a:hover { text-decoration: underline; }
</style>
</head>
<body>
<div class="container">
  <h1>Context Gateway</h1>
  <p>Dashboard SPA not embedded. View raw data:</p>
  <a href="/api/dashboard">/api/dashboard</a> (JSON) &nbsp;|&nbsp;
  <a href="/api/prompts">/api/prompts</a> (prompts)
</div>
</body>
</html>`))
}

// handleAggregatedDashboardAPI aggregates dashboard data from ALL active gateway instances.
// Uses the instance registry (same as the monitor tab) to discover running instances,
// ensuring both tabs see the same set of gateways regardless of port.
func (g *Gateway) handleAggregatedDashboardAPI(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		g.writeError(w, "forbidden", http.StatusForbidden)
		return
	}

	type sessionJSON struct {
		ID           string  `json:"id"`
		Cost         float64 `json:"cost"`
		Cap          float64 `json:"cap"`
		RequestCount int     `json:"request_count"`
		Model        string  `json:"model"`
		CreatedAt    string  `json:"created_at"`
		LastUpdated  string  `json:"last_updated"`
		GatewayPort  int     `json:"gateway_port"`         // Which gateway instance owns this session
		Active       bool    `json:"active"`               // Whether the gateway is currently running
		AgentName    string  `json:"agent_name,omitempty"` // Human-readable name from registry
	}

	type savingsJSON struct {
		TotalRequests         int     `json:"total_requests"`
		CompressedRequests    int     `json:"compressed_requests"`
		TokensSaved           int     `json:"tokens_saved"`
		TokenSavedPct         float64 `json:"token_saved_pct"`
		BilledSpendUSD        float64 `json:"billed_spend_usd"`
		CostSavedUSD          float64 `json:"cost_saved_usd"`
		OriginalCostUSD       float64 `json:"original_cost_usd"`
		CompressedCostUSD     float64 `json:"compressed_cost_usd"`
		CompressionRatio      float64 `json:"compression_ratio"`
		ToolDiscoveryRequests int     `json:"tool_discovery_requests,omitempty"`
		OriginalToolCount     int     `json:"original_tool_count,omitempty"`
		FilteredToolCount     int     `json:"filtered_tool_count,omitempty"`
		ToolDiscoveryTokens   int     `json:"tool_discovery_tokens,omitempty"`
		ToolDiscoveryCostUSD  float64 `json:"tool_discovery_cost_usd,omitempty"`
		ToolDiscoveryPct      float64 `json:"tool_discovery_pct,omitempty"`
	}

	type gatewayStatsJSON struct {
		Uptime             string `json:"uptime"`
		TotalRequests      int64  `json:"total_requests"`
		SuccessfulRequests int64  `json:"successful_requests"`
		Compressions       int64  `json:"compressions"`
		CacheHits          int64  `json:"cache_hits"`
		CacheMisses        int64  `json:"cache_misses"`
	}

	type aggregatedResponse struct {
		Sessions      []sessionJSON     `json:"sessions"`
		TotalCost     float64           `json:"total_cost"`
		TotalRequests int               `json:"total_requests"`
		SessionCap    float64           `json:"session_cap"`
		GlobalCap     float64           `json:"global_cap"`
		Enabled       bool              `json:"enabled"`
		Savings       *savingsJSON      `json:"savings,omitempty"`
		Gateway       *gatewayStatsJSON `json:"gateway,omitempty"`
		ActivePorts   []int             `json:"active_ports"`
	}

	// Use instance registry for discovery — same source as handleAggregatedMonitorAPI.
	// This ensures savings and monitor tabs see the same set of gateway instances.
	registryInstances := dashboard.DiscoverInstances()
	client := &http.Client{Timeout: 2 * time.Second}

	// Build port -> agent name lookup from registry
	nameByPort := make(map[int]string, len(registryInstances))
	for _, inst := range registryInstances {
		nameByPort[inst.Port] = inst.AgentName
	}

	resp := aggregatedResponse{
		Sessions:    make([]sessionJSON, 0),
		ActivePorts: make([]int, 0),
	}

	// Aggregate savings
	var totalSavings savingsJSON
	var totalGatewayStats gatewayStatsJSON
	hasSavings := false
	hasGateway := false

	requestedSession := r.URL.Query().Get("session")

	for _, inst := range registryInstances {
		port := inst.Port
		target := &neturl.URL{
			Scheme: "http",
			Host:   fmt.Sprintf("127.0.0.1:%d", port),
			Path:   "/api/dashboard",
		}
		if requestedSession != "" {
			target.RawQuery = "session=" + neturl.QueryEscape(requestedSession)
		}

		gwResp, err := client.Get(target.String())
		if err != nil {
			continue // Instance not reachable
		}

		if gwResp.StatusCode != http.StatusOK {
			if closeErr := gwResp.Body.Close(); closeErr != nil {
				log.Debug().Err(closeErr).Msg("dashboard: failed to close non-OK response body")
			}
			continue
		}

		var gwData struct {
			Sessions      []sessionJSON     `json:"sessions"`
			TotalCost     float64           `json:"total_cost"`
			TotalRequests int               `json:"total_requests"`
			SessionCap    float64           `json:"session_cap"`
			GlobalCap     float64           `json:"global_cap"`
			Enabled       bool              `json:"enabled"`
			Savings       *savingsJSON      `json:"savings"`
			Gateway       *gatewayStatsJSON `json:"gateway"`
		}

		body, err := io.ReadAll(gwResp.Body)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(body, &gwData); err != nil {
			continue
		}

		resp.ActivePorts = append(resp.ActivePorts, port)

		// Merge sessions: use TotalCost (includes subagent sessions) for the main session card,
		// and attach the agent name from the registry.
		for _, s := range gwData.Sessions {
			resp.Sessions = append(resp.Sessions, sessionJSON{
				ID:           s.ID,
				Cost:         gwData.TotalCost, // Aggregated cost including all subsessions
				Cap:          s.Cap,
				RequestCount: gwData.TotalRequests, // Aggregated request count
				Model:        s.Model,
				CreatedAt:    s.CreatedAt,
				LastUpdated:  s.LastUpdated,
				GatewayPort:  port,
				Active:       true,
				AgentName:    nameByPort[port],
			})
		}

		resp.TotalCost += gwData.TotalCost
		resp.TotalRequests += gwData.TotalRequests
		if gwData.Enabled {
			resp.Enabled = true
		}
		if gwData.SessionCap > resp.SessionCap {
			resp.SessionCap = gwData.SessionCap
		}
		if gwData.GlobalCap > resp.GlobalCap {
			resp.GlobalCap = gwData.GlobalCap
		}

		// Aggregate savings
		if gwData.Savings != nil {
			hasSavings = true
			totalSavings.TotalRequests += gwData.Savings.TotalRequests
			totalSavings.CompressedRequests += gwData.Savings.CompressedRequests
			// totalSavings.TokensSaved += gwData.Savings.TokensSaved
			totalSavings.BilledSpendUSD += gwData.Savings.BilledSpendUSD
			totalSavings.CostSavedUSD += gwData.Savings.CostSavedUSD
			totalSavings.OriginalCostUSD += gwData.Savings.OriginalCostUSD
			totalSavings.CompressedCostUSD += gwData.Savings.CompressedCostUSD
			totalSavings.ToolDiscoveryRequests += gwData.Savings.ToolDiscoveryRequests
			totalSavings.OriginalToolCount += gwData.Savings.OriginalToolCount
			totalSavings.FilteredToolCount += gwData.Savings.FilteredToolCount
			totalSavings.ToolDiscoveryTokens += gwData.Savings.ToolDiscoveryTokens
			totalSavings.ToolDiscoveryCostUSD += gwData.Savings.ToolDiscoveryCostUSD
		}

		// Aggregate gateway stats
		if gwData.Gateway != nil {
			hasGateway = true
			totalGatewayStats.TotalRequests += gwData.Gateway.TotalRequests
			totalGatewayStats.SuccessfulRequests += gwData.Gateway.SuccessfulRequests
			totalGatewayStats.Compressions += gwData.Gateway.Compressions
			totalGatewayStats.CacheHits += gwData.Gateway.CacheHits
			totalGatewayStats.CacheMisses += gwData.Gateway.CacheMisses
		}
	}

	// Compute derived savings metrics
	if hasSavings {
		if totalSavings.TotalRequests > 0 {
			totalSavings.CompressionRatio = float64(totalSavings.CompressedRequests) / float64(totalSavings.TotalRequests)
		}
		// originalTokens := totalSavings.TokensSaved // approximate
		// if originalTokens > 0 {
		// 	totalSavings.TokenSavedPct = float64(totalSavings.TokensSaved) / float64(originalTokens) * 100
		// }
		if totalSavings.ToolDiscoveryRequests > 0 && totalSavings.TotalRequests > 0 {
			totalSavings.ToolDiscoveryPct = float64(totalSavings.ToolDiscoveryRequests) / float64(totalSavings.TotalRequests) * 100
		}
		resp.Savings = &totalSavings
	}

	if hasGateway {
		totalGatewayStats.Uptime = time.Since(gatewayStartTime).Truncate(time.Second).String()
		resp.Gateway = &totalGatewayStats
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Error().Err(err).Msg("Failed to encode aggregated dashboard response")
	}
}
