// Package gateway - dashboard.go serves the centralized React dashboard SPA at /dashboard.
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

	// Bind the port and keep the listener open to avoid race conditions.
	// We use Serve(ln) instead of ListenAndServe() to prevent TOCTOU bugs
	// where another process could grab the port between close and re-bind.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Debug().Int("port", dashPort).Msg("dashboard port already in use (another instance serving)")
		return
	}

	dashMux := http.NewServeMux()
	g.setupDashboardRoutes(dashMux)

	g.dashboardServer = &http.Server{
		Addr:           addr,
		Handler:        g.panicRecovery(g.rateLimit(g.loggingMiddleware(g.security(dashMux)))),
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   60 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	g.dashboardStarted = true

	go func() {
		log.Info().Int("port", dashPort).Msg("centralized dashboard server starting")
		// Use Serve(ln) with the already-bound listener instead of ListenAndServe()
		if err := g.dashboardServer.Serve(ln); err != nil && err != http.ErrServerClosed {
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
		ID           string   `json:"id"`
		Cost         float64  `json:"cost"`
		Cap          float64  `json:"cap"`
		RequestCount int      `json:"request_count"`
		Models       []string `json:"models"`
		CreatedAt    string   `json:"created_at"`
		LastUpdated  string   `json:"last_updated"`
		GatewayPort  int      `json:"gateway_port"`         // Which gateway instance owns this session
		Active       bool     `json:"active"`               // Whether the gateway is currently running
		AgentName    string   `json:"agent_name,omitempty"` // Human-readable name from registry
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
		KeptToolCount         int     `json:"filtered_tool_count,omitempty"`
		ToolDiscoveryTokens   int     `json:"tool_discovery_tokens,omitempty"`
		ToolDiscoveryCostUSD  float64 `json:"tool_discovery_cost_usd,omitempty"`
		ToolDiscoveryPct      float64 `json:"tool_discovery_pct,omitempty"`
		// Session activity counters
		UserTurns          int `json:"user_turns,omitempty"`
		CompactionTriggers int `json:"compaction_triggers,omitempty"`
		ToolSearchCalls    int `json:"tool_search_calls,omitempty"`
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
		GlobalSavings *savingsJSON      `json:"global_savings,omitempty"`
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

	// Fallback: if registry discovery returns nothing (health check timing, registry not
	// yet populated, etc.), include the current instance's own port so savings data is
	// always visible even when discovery is temporarily unavailable.
	if len(registryInstances) == 0 && g.config.Server.Port > 0 {
		registryInstances = []dashboard.Instance{{
			Port:      g.config.Server.Port,
			AgentName: g.config.Monitoring.AgentName,
		}}
	}

	resp := aggregatedResponse{
		Sessions:    make([]sessionJSON, 0),
		ActivePorts: make([]int, 0),
	}

	// Aggregate savings
	var totalSavings savingsJSON
	var totalGlobalSavings savingsJSON
	var totalGatewayStats gatewayStatsJSON
	hasSavings := false
	hasGlobalSavings := false
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
		// defer close on all exit paths — ReadAll/Unmarshal errors would otherwise
		// skip the close and leak the HTTP connection.
		defer gwResp.Body.Close() //nolint:gocritic // not accumulating; loop iteration count is small and bounded

		if gwResp.StatusCode != http.StatusOK {
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
			GlobalSavings *savingsJSON      `json:"global_savings"`
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

		// Merge sessions: deduplicate by ID, keeping the best data.
		// If multiple gateways report the same session (from shared logs dir),
		// we keep the one with Active=true, or the one with higher cost if both inactive.
		for _, s := range gwData.Sessions {
			sess := sessionJSON{
				ID:           s.ID,
				Cost:         s.Cost,
				Cap:          s.Cap,
				RequestCount: s.RequestCount,
				Models:       s.Models,
				CreatedAt:    s.CreatedAt,
				LastUpdated:  s.LastUpdated,
				GatewayPort:  port,
				Active:       s.Active,
				AgentName:    nameByPort[port],
			}
			// Check if we already have this session
			found := false
			for i, existing := range resp.Sessions {
				if existing.ID == s.ID {
					found = true
					// Prefer: Active > Inactive, then higher cost/requests
					if s.Active && !existing.Active {
						resp.Sessions[i] = sess
					} else if !s.Active && existing.Active {
						// Keep existing (it's active)
					} else if s.Cost > existing.Cost || s.RequestCount > existing.RequestCount {
						resp.Sessions[i] = sess
					}
					break
				}
			}
			if !found {
				resp.Sessions = append(resp.Sessions, sess)
			}
		}

		// Don't sum TotalCost/TotalRequests here - they come from the same disk data
		// and would be double-counted. We'll calculate from deduplicated sessions below.
		if gwData.Enabled {
			resp.Enabled = true
		}
		if gwData.SessionCap > resp.SessionCap {
			resp.SessionCap = gwData.SessionCap
		}
		if gwData.GlobalCap > resp.GlobalCap {
			resp.GlobalCap = gwData.GlobalCap
		}

		// For savings/gateway stats, take the max values since all gateways read same logs.
		// This prevents double-counting when multiple gateways share the same logs directory.
		if gwData.Savings != nil {
			hasSavings = true
			if gwData.Savings.TotalRequests > totalSavings.TotalRequests {
				totalSavings.TotalRequests = gwData.Savings.TotalRequests
			}
			if gwData.Savings.CompressedRequests > totalSavings.CompressedRequests {
				totalSavings.CompressedRequests = gwData.Savings.CompressedRequests
			}
			if gwData.Savings.TokensSaved > totalSavings.TokensSaved {
				totalSavings.TokensSaved = gwData.Savings.TokensSaved
			}
			if gwData.Savings.TokenSavedPct > totalSavings.TokenSavedPct {
				totalSavings.TokenSavedPct = gwData.Savings.TokenSavedPct
			}
			if gwData.Savings.CompressionRatio > totalSavings.CompressionRatio {
				totalSavings.CompressionRatio = gwData.Savings.CompressionRatio
			}
			if gwData.Savings.BilledSpendUSD > totalSavings.BilledSpendUSD {
				totalSavings.BilledSpendUSD = gwData.Savings.BilledSpendUSD
			}
			if gwData.Savings.CostSavedUSD > totalSavings.CostSavedUSD {
				totalSavings.CostSavedUSD = gwData.Savings.CostSavedUSD
			}
			if gwData.Savings.OriginalCostUSD > totalSavings.OriginalCostUSD {
				totalSavings.OriginalCostUSD = gwData.Savings.OriginalCostUSD
			}
			if gwData.Savings.CompressedCostUSD > totalSavings.CompressedCostUSD {
				totalSavings.CompressedCostUSD = gwData.Savings.CompressedCostUSD
			}
			if gwData.Savings.ToolDiscoveryRequests > totalSavings.ToolDiscoveryRequests {
				totalSavings.ToolDiscoveryRequests = gwData.Savings.ToolDiscoveryRequests
			}
			if gwData.Savings.OriginalToolCount > totalSavings.OriginalToolCount {
				totalSavings.OriginalToolCount = gwData.Savings.OriginalToolCount
			}
			if gwData.Savings.KeptToolCount > totalSavings.KeptToolCount {
				totalSavings.KeptToolCount = gwData.Savings.KeptToolCount
			}
			if gwData.Savings.ToolDiscoveryTokens > totalSavings.ToolDiscoveryTokens {
				totalSavings.ToolDiscoveryTokens = gwData.Savings.ToolDiscoveryTokens
			}
			if gwData.Savings.ToolDiscoveryCostUSD > totalSavings.ToolDiscoveryCostUSD {
				totalSavings.ToolDiscoveryCostUSD = gwData.Savings.ToolDiscoveryCostUSD
			}
			if gwData.Savings.ToolDiscoveryPct > totalSavings.ToolDiscoveryPct {
				totalSavings.ToolDiscoveryPct = gwData.Savings.ToolDiscoveryPct
			}
			if gwData.Savings.UserTurns > totalSavings.UserTurns {
				totalSavings.UserTurns = gwData.Savings.UserTurns
			}
			if gwData.Savings.CompactionTriggers > totalSavings.CompactionTriggers {
				totalSavings.CompactionTriggers = gwData.Savings.CompactionTriggers
			}
			if gwData.Savings.ToolSearchCalls > totalSavings.ToolSearchCalls {
				totalSavings.ToolSearchCalls = gwData.Savings.ToolSearchCalls
			}
		}

		// Aggregate global savings - always represents full totals regardless of session selection
		if gwData.GlobalSavings != nil {
			hasGlobalSavings = true
			if gwData.GlobalSavings.TotalRequests > totalGlobalSavings.TotalRequests {
				totalGlobalSavings.TotalRequests = gwData.GlobalSavings.TotalRequests
			}
			if gwData.GlobalSavings.CompressedRequests > totalGlobalSavings.CompressedRequests {
				totalGlobalSavings.CompressedRequests = gwData.GlobalSavings.CompressedRequests
			}
			if gwData.GlobalSavings.TokensSaved > totalGlobalSavings.TokensSaved {
				totalGlobalSavings.TokensSaved = gwData.GlobalSavings.TokensSaved
			}
			if gwData.GlobalSavings.TokenSavedPct > totalGlobalSavings.TokenSavedPct {
				totalGlobalSavings.TokenSavedPct = gwData.GlobalSavings.TokenSavedPct
			}
			if gwData.GlobalSavings.CompressionRatio > totalGlobalSavings.CompressionRatio {
				totalGlobalSavings.CompressionRatio = gwData.GlobalSavings.CompressionRatio
			}
			if gwData.GlobalSavings.BilledSpendUSD > totalGlobalSavings.BilledSpendUSD {
				totalGlobalSavings.BilledSpendUSD = gwData.GlobalSavings.BilledSpendUSD
			}
			if gwData.GlobalSavings.CostSavedUSD > totalGlobalSavings.CostSavedUSD {
				totalGlobalSavings.CostSavedUSD = gwData.GlobalSavings.CostSavedUSD
			}
			if gwData.GlobalSavings.OriginalCostUSD > totalGlobalSavings.OriginalCostUSD {
				totalGlobalSavings.OriginalCostUSD = gwData.GlobalSavings.OriginalCostUSD
			}
			if gwData.GlobalSavings.CompressedCostUSD > totalGlobalSavings.CompressedCostUSD {
				totalGlobalSavings.CompressedCostUSD = gwData.GlobalSavings.CompressedCostUSD
			}
			if gwData.GlobalSavings.ToolDiscoveryRequests > totalGlobalSavings.ToolDiscoveryRequests {
				totalGlobalSavings.ToolDiscoveryRequests = gwData.GlobalSavings.ToolDiscoveryRequests
			}
			if gwData.GlobalSavings.OriginalToolCount > totalGlobalSavings.OriginalToolCount {
				totalGlobalSavings.OriginalToolCount = gwData.GlobalSavings.OriginalToolCount
			}
			if gwData.GlobalSavings.KeptToolCount > totalGlobalSavings.KeptToolCount {
				totalGlobalSavings.KeptToolCount = gwData.GlobalSavings.KeptToolCount
			}
			if gwData.GlobalSavings.ToolDiscoveryTokens > totalGlobalSavings.ToolDiscoveryTokens {
				totalGlobalSavings.ToolDiscoveryTokens = gwData.GlobalSavings.ToolDiscoveryTokens
			}
			if gwData.GlobalSavings.ToolDiscoveryCostUSD > totalGlobalSavings.ToolDiscoveryCostUSD {
				totalGlobalSavings.ToolDiscoveryCostUSD = gwData.GlobalSavings.ToolDiscoveryCostUSD
			}
			if gwData.GlobalSavings.ToolDiscoveryPct > totalGlobalSavings.ToolDiscoveryPct {
				totalGlobalSavings.ToolDiscoveryPct = gwData.GlobalSavings.ToolDiscoveryPct
			}
			if gwData.GlobalSavings.UserTurns > totalGlobalSavings.UserTurns {
				totalGlobalSavings.UserTurns = gwData.GlobalSavings.UserTurns
			}
			if gwData.GlobalSavings.CompactionTriggers > totalGlobalSavings.CompactionTriggers {
				totalGlobalSavings.CompactionTriggers = gwData.GlobalSavings.CompactionTriggers
			}
			if gwData.GlobalSavings.ToolSearchCalls > totalGlobalSavings.ToolSearchCalls {
				totalGlobalSavings.ToolSearchCalls = gwData.GlobalSavings.ToolSearchCalls
			}
		}

		// Aggregate gateway stats - take max since all read same data
		if gwData.Gateway != nil {
			hasGateway = true
			if gwData.Gateway.TotalRequests > totalGatewayStats.TotalRequests {
				totalGatewayStats.TotalRequests = gwData.Gateway.TotalRequests
			}
			if gwData.Gateway.SuccessfulRequests > totalGatewayStats.SuccessfulRequests {
				totalGatewayStats.SuccessfulRequests = gwData.Gateway.SuccessfulRequests
			}
			if gwData.Gateway.Compressions > totalGatewayStats.Compressions {
				totalGatewayStats.Compressions = gwData.Gateway.Compressions
			}
			if gwData.Gateway.CacheHits > totalGatewayStats.CacheHits {
				totalGatewayStats.CacheHits = gwData.Gateway.CacheHits
			}
			if gwData.Gateway.CacheMisses > totalGatewayStats.CacheMisses {
				totalGatewayStats.CacheMisses = gwData.Gateway.CacheMisses
			}
		}
	}

	// Calculate totals from deduplicated sessions
	for _, s := range resp.Sessions {
		resp.TotalCost += s.Cost
		resp.TotalRequests += s.RequestCount
	}

	// Pass through savings as-is — CompressionRatio, TokenSavedPct, and ToolDiscoveryPct
	// are already correct values taken as max from the gateway's savings report above.
	// Do NOT recompute them here: the old formulas (requests/requests ratios) were wrong.
	if hasSavings {
		resp.Savings = &totalSavings
	}
	if hasGlobalSavings {
		resp.GlobalSavings = &totalGlobalSavings
	}

	if hasGateway {
		totalGatewayStats.Uptime = time.Since(gatewayStartTime).Truncate(time.Second).String()
		resp.Gateway = &totalGatewayStats
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "http://localhost:18080")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Error().Err(err).Msg("Failed to encode aggregated dashboard response")
	}
}
