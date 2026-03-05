// Dashboard API endpoints, savings reporting, and cost control responses.
package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/costcontrol"
	"github.com/compresr/context-gateway/internal/monitoring"
)

// buildUnifiedReportData gathers data from cost tracker and expand log for the /savings report.
func (g *Gateway) buildUnifiedReportData() monitoring.UnifiedReportData {
	var data monitoring.UnifiedReportData

	if g.costTracker != nil {
		sessions := g.costTracker.AllSessions()
		data.SessionCount = len(sessions)
		for _, s := range sessions {
			data.TotalSessionCost += s.Cost
		}
	}

	if g.expandLog != nil {
		summary := g.expandLog.Summary()
		data.ExpandTotal = summary.Total
		data.ExpandFound = summary.Found
		data.ExpandNotFound = summary.NotFound
	}

	// Fetch account balance from cached Compresr API data (non-blocking)
	// Background refresh updates the cache every 5s for instant /savings responses
	if g.compresrClient != nil && g.compresrClient.HasAPIKey() {
		if status := g.compresrClient.GetCachedStatus(); status != nil {
			data.BalanceAvailable = true
			data.Tier = status.Tier
			data.CreditsRemainingUSD = status.CreditsRemainingUSD
			data.CreditsUsedThisMonth = status.CreditsUsedThisMonth
			data.MonthlyBudgetUSD = status.MonthlyBudgetUSD
			data.IsAdmin = status.IsAdmin
		}
	}

	data.DashboardURL = fmt.Sprintf("http://localhost:%d/costs", g.config.Server.Port)
	return data
}

// getSavingsReport returns the best available savings report.
// Prefers aggregator (log-based) but falls back to in-memory savings tracker
// when the aggregator has no data (e.g., telemetry logging is disabled).
// sessionID: "all" for global, specific session name, or "" to default to current session.
func (g *Gateway) getSavingsReport(sessionID string) monitoring.SavingsReport {
	// Default to current session if no session specified and we have one
	if sessionID == "" && g.getCurrentSessionID() != "" {
		sessionID = g.getCurrentSessionID()
	}

	// "all" means global report (across all sessions)
	useGlobal := sessionID == "" || sessionID == "all"

	// Try aggregator first
	if g.aggregator != nil {
		var sr monitoring.SavingsReport
		if useGlobal {
			sr = g.aggregator.GetReport()
		} else {
			sr = g.aggregator.GetReportForSession(sessionID)
		}
		if hasSavingsData(sr) {
			return sr
		}
	}

	// Fallback to in-memory savings tracker.
	// The savings tracker stores sessions under hash-based IDs (from ComputeSessionID)
	// which differ from the folder-based currentSessionID. Use global report as fallback
	// since all data in this gateway instance belongs to the current agent session.
	if g.savings != nil {
		if useGlobal {
			return g.savings.GetReport()
		}
		sr := g.savings.GetReportForSession(sessionID)
		if hasSavingsData(sr) {
			return sr
		}
		// Session ID didn't match (hash vs folder ID mismatch) — fall back to global.
		// This is correct because the gateway serves a single agent session.
		return g.savings.GetReport()
	}

	return monitoring.SavingsReport{}
}

func hasSavingsData(sr monitoring.SavingsReport) bool {
	return sr.TotalRequests > 0 ||
		sr.TotalTokensSaved > 0 ||
		sr.CostSavedUSD > 0 ||
		sr.CompressedCostUSD > 0 ||
		sr.OriginalCostUSD > 0 ||
		sr.OriginalTokens > 0 ||
		sr.CompressedTokens > 0 ||
		sr.ToolDiscoveryRequests > 0 ||
		sr.ToolDiscoveryTokens > 0 ||
		sr.PreemptiveSummarizationRequests > 0
}

// handleDashboardAPI returns JSON data for the React cost dashboard.
// Restricted to localhost to prevent external access to cost/usage data.
func (g *Gateway) handleDashboardAPI(w http.ResponseWriter, r *http.Request) {
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
	}

	type savingsJSON struct {
		TotalRequests      int     `json:"total_requests"`
		CompressedRequests int     `json:"compressed_requests"`
		TokensSaved        int     `json:"tokens_saved"`
		TokenSavedPct      float64 `json:"token_saved_pct"`
		// BilledSpendUSD is the authoritative spend shown in /costs (from cost tracker).
		BilledSpendUSD    float64 `json:"billed_spend_usd"`
		CostSavedUSD      float64 `json:"cost_saved_usd"`
		OriginalCostUSD   float64 `json:"original_cost_usd"`
		CompressedCostUSD float64 `json:"compressed_cost_usd"`
		CompressionRatio  float64 `json:"compression_ratio"`
		// Tool discovery stats
		ToolDiscoveryRequests int     `json:"tool_discovery_requests,omitempty"`
		OriginalToolCount     int     `json:"original_tool_count,omitempty"`
		FilteredToolCount     int     `json:"filtered_tool_count,omitempty"`
		ToolDiscoveryTokens   int     `json:"tool_discovery_tokens,omitempty"`
		ToolDiscoveryCostUSD  float64 `json:"tool_discovery_cost_usd,omitempty"`
		ToolDiscoveryPct      float64 `json:"tool_discovery_pct,omitempty"`
	}

	type expandEntryJSON struct {
		Timestamp      string `json:"timestamp"`
		RequestID      string `json:"request_id"`
		ShadowID       string `json:"shadow_id"`
		Found          bool   `json:"found"`
		ContentPreview string `json:"content_preview,omitempty"`
		ContentLength  int    `json:"content_length"`
	}

	type expandJSON struct {
		Total    int               `json:"total"`
		Found    int               `json:"found"`
		NotFound int               `json:"not_found"`
		Recent   []expandEntryJSON `json:"recent,omitempty"`
	}

	type searchEntryJSON struct {
		Timestamp     string   `json:"timestamp"`
		RequestID     string   `json:"request_id"`
		SessionID     string   `json:"session_id,omitempty"`
		Query         string   `json:"query"`
		DeferredCount int      `json:"deferred_count"`
		ResultsCount  int      `json:"results_count"`
		ToolsFound    []string `json:"tools_found"`
		Strategy      string   `json:"strategy"`
	}

	type searchJSON struct {
		Total  int               `json:"total"`
		Recent []searchEntryJSON `json:"recent,omitempty"`
	}

	type gatewayStatsJSON struct {
		Uptime             string `json:"uptime"`
		TotalRequests      int64  `json:"total_requests"`
		SuccessfulRequests int64  `json:"successful_requests"`
		Compressions       int64  `json:"compressions"`
		CacheHits          int64  `json:"cache_hits"`
		CacheMisses        int64  `json:"cache_misses"`
	}

	type dashboardResponse struct {
		Sessions      []sessionJSON     `json:"sessions"`
		TotalCost     float64           `json:"total_cost"`
		TotalRequests int               `json:"total_requests"`
		SessionCap    float64           `json:"session_cap"`
		GlobalCap     float64           `json:"global_cap"`
		Enabled       bool              `json:"enabled"`
		Savings       *savingsJSON      `json:"savings,omitempty"`
		Expand        *expandJSON       `json:"expand,omitempty"`
		Search        *searchJSON       `json:"search,omitempty"`
		Gateway       *gatewayStatsJSON `json:"gateway,omitempty"`
	}

	resp := dashboardResponse{}
	requestedSessionID := r.URL.Query().Get("session")

	// Use global scope when no specific session is requested.
	// Cost tracker session IDs and savings session IDs may use different schemes
	// (hash-based vs folder-based), so global scope is the only consistently
	// correct default and avoids apparent "jump to zero" behavior.
	useGlobalScope := requestedSessionID == "" || requestedSessionID == "all"
	scopedBilledSpend := 0.0

	if g.costTracker != nil {
		sessions := g.costTracker.AllSessions()
		cfg := g.costTracker.Config()
		resp.Enabled = cfg.Enabled
		resp.SessionCap = cfg.SessionCap
		resp.GlobalCap = cfg.GlobalCap

		for _, s := range sessions {
			resp.TotalCost += s.Cost
			resp.TotalRequests += s.RequestCount
			resp.Sessions = append(resp.Sessions, sessionJSON{
				ID:           s.ID,
				Cost:         s.Cost,
				Cap:          s.Cap,
				RequestCount: s.RequestCount,
				Model:        s.Model,
				CreatedAt:    s.CreatedAt.Format(time.RFC3339),
				LastUpdated:  s.LastUpdated.Format(time.RFC3339),
			})
		}

		if useGlobalScope {
			scopedBilledSpend = resp.TotalCost
		} else {
			scopedBilledSpend = g.costTracker.GetSessionCost(requestedSessionID)
		}
	}

	// Savings: use costTracker for authoritative spend, savings report for compression data.
	savingsScope := requestedSessionID
	if useGlobalScope {
		savingsScope = "all"
	}
	sr := g.getSavingsReport(savingsScope)

	// Always show savings card when we have spend or savings data.
	if hasSavingsData(sr) || scopedBilledSpend > 0 {
		resp.Savings = &savingsJSON{
			TotalRequests:         sr.TotalRequests,
			CompressedRequests:    sr.CompressedRequests,
			TokensSaved:           sr.TotalTokensSaved,
			TokenSavedPct:         sr.TotalSavedPct,
			BilledSpendUSD:        scopedBilledSpend,
			CostSavedUSD:          sr.CostSavedUSD,
			CompressedCostUSD:     scopedBilledSpend,
			OriginalCostUSD:       scopedBilledSpend + sr.CostSavedUSD,
			CompressionRatio:      sr.AvgCompressionRatio,
			ToolDiscoveryRequests: sr.ToolDiscoveryRequests,
			OriginalToolCount:     sr.OriginalToolCount,
			FilteredToolCount:     sr.FilteredToolCount,
			ToolDiscoveryTokens:   sr.ToolDiscoveryTokens,
			ToolDiscoveryCostUSD:  sr.ToolDiscoveryCostUSD,
			ToolDiscoveryPct:      sr.ToolDiscoveryPct,
		}
	}

	// Expand context log
	if g.expandLog != nil {
		summary := g.expandLog.Summary()
		resp.Expand = &expandJSON{
			Total:    summary.Total,
			Found:    summary.Found,
			NotFound: summary.NotFound,
		}
		// Include recent entries
		recent := g.expandLog.Recent(20)
		for _, e := range recent {
			resp.Expand.Recent = append(resp.Expand.Recent, expandEntryJSON{
				Timestamp:      e.Timestamp.Format(time.RFC3339),
				RequestID:      e.RequestID,
				ShadowID:       e.ShadowID,
				Found:          e.Found,
				ContentPreview: e.ContentPreview,
				ContentLength:  e.ContentLength,
			})
		}
	}

	// Search tool log (gateway_search_tools calls)
	if g.searchLog != nil {
		summary := g.searchLog.Summary()
		resp.Search = &searchJSON{
			Total: summary.Total,
		}
		// Include recent entries
		recent := g.searchLog.Recent(20)
		for _, e := range recent {
			resp.Search.Recent = append(resp.Search.Recent, searchEntryJSON{
				Timestamp:     e.Timestamp.Format(time.RFC3339),
				RequestID:     e.RequestID,
				SessionID:     e.SessionID,
				Query:         e.Query,
				DeferredCount: e.DeferredCount,
				ResultsCount:  e.ResultsCount,
				ToolsFound:    e.ToolsFound,
				Strategy:      e.Strategy,
			})
		}
	}

	// Gateway operational stats
	if g.metrics != nil {
		stats := g.metrics.Stats()
		resp.Gateway = &gatewayStatsJSON{
			Uptime:             time.Since(gatewayStartTime).Truncate(time.Second).String(),
			TotalRequests:      stats["requests"],
			SuccessfulRequests: stats["successes"],
			Compressions:       stats["compressions"],
			CacheHits:          stats["cache_hits"],
			CacheMisses:        stats["cache_misses"],
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Error().Err(err).Msg("Failed to encode cost stats response")
	}
}

// handleSavingsAPI returns the formatted savings report as plain text.
// This is the fast HTTP endpoint used by the /savings slash command.
// Uses LogAggregator (parses logs in background) as single source of truth.
func (g *Gateway) handleSavingsAPI(w http.ResponseWriter, r *http.Request) {
	extra := g.buildUnifiedReportData()

	// Get savings report with aggregator->savings fallback
	sr := g.getSavingsReport(r.URL.Query().Get("session"))
	// Override with costTracker's authoritative spend
	if extra.TotalSessionCost > 0 {
		sr.CompressedCostUSD = extra.TotalSessionCost
		sr.OriginalCostUSD = sr.CompressedCostUSD + sr.CostSavedUSD
	}
	report := monitoring.FormatUnifiedReportFromReport(sr, extra)

	if report == "" {
		report = "No savings data available"
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, report) // #nosec G705 -- plain text API output, not HTML
}

// handleAccountAPI returns the user's Compresr account status (tier, balance, usage).
// Restricted to localhost to prevent external access to account data.
func (g *Gateway) handleAccountAPI(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		g.writeError(w, "forbidden", http.StatusForbidden)
		return
	}
	type accountResponse struct {
		Available            bool    `json:"available"`               // Whether account data is available
		Tier                 string  `json:"tier,omitempty"`          // "free", "pro", "business", "enterprise"
		CreditsRemainingUSD  float64 `json:"credits_remaining_usd"`   // Remaining credits
		CreditsUsedThisMonth float64 `json:"credits_used_this_month"` // Credits used this billing period
		MonthlyBudgetUSD     float64 `json:"monthly_budget_usd"`      // Monthly budget (0 = unlimited)
		UsagePercent         float64 `json:"usage_percent"`           // Percentage of budget used (0-100)
		IsAdmin              bool    `json:"is_admin"`                // Admin/unlimited access
		Error                string  `json:"error,omitempty"`         // Error message if unavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Check if we have a Compresr client with an API key
	if g.compresrClient == nil || !g.compresrClient.HasAPIKey() {
		if err := json.NewEncoder(w).Encode(accountResponse{
			Available: false,
			Error:     "No COMPRESR_API_KEY configured",
		}); err != nil {
			log.Error().Err(err).Msg("Failed to encode account response")
		}
		return
	}

	// Fetch account status from Compresr API (bypass cache for real-time balance)
	status, err := g.compresrClient.GetGatewayStatusFresh()
	if err != nil {
		if encErr := json.NewEncoder(w).Encode(accountResponse{
			Available: false,
			Error:     err.Error(),
		}); encErr != nil {
			log.Error().Err(encErr).Msg("Failed to encode account response")
		}
		return
	}

	if err := json.NewEncoder(w).Encode(accountResponse{
		Available:            true,
		Tier:                 status.Tier,
		CreditsRemainingUSD:  status.CreditsRemainingUSD,
		CreditsUsedThisMonth: status.CreditsUsedThisMonth,
		MonthlyBudgetUSD:     status.MonthlyBudgetUSD,
		UsagePercent:         status.UsagePercent,
		IsAdmin:              status.IsAdmin,
	}); err != nil {
		log.Error().Err(err).Msg("Failed to encode account response")
	}
}

// returnBudgetExceededResponse writes a synthetic response when budget is exceeded.
// Returns HTTP 200 so agent clients display the message rather than retry.
func (g *Gateway) returnBudgetExceededResponse(w http.ResponseWriter, provider string, budget costcontrol.BudgetCheckResult) {
	var msg string
	if budget.GlobalCap > 0 && budget.GlobalCost >= budget.GlobalCap {
		msg = fmt.Sprintf("Global budget exceeded. Total spend: $%.4f, limit: $%.2f. "+
			"Increase the global cap in your gateway config (cost_control.global_cap).",
			budget.GlobalCost, budget.GlobalCap)
	} else {
		msg = fmt.Sprintf("Session budget exceeded. Current spend: $%.4f, limit: $%.2f. "+
			"Please start a new session or increase the budget cap in your gateway config (cost_control.session_cap).",
			budget.CurrentCost, budget.Cap)
	}

	var resp []byte
	if provider == "anthropic" {
		resp, _ = json.Marshal(map[string]interface{}{
			"id":            "msg_budget_exceeded",
			"type":          "message",
			"role":          "assistant",
			"model":         "budget-control",
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"content":       []map[string]interface{}{{"type": "text", "text": msg}},
			"usage":         map[string]interface{}{"input_tokens": 0, "output_tokens": 0},
		})
	} else {
		resp, _ = json.Marshal(map[string]interface{}{
			"id":      "budget_exceeded",
			"object":  "chat.completion",
			"model":   "budget-control",
			"choices": []map[string]interface{}{{"index": 0, "message": map[string]interface{}{"role": "assistant", "content": msg}, "finish_reason": "stop"}},
			"usage":   map[string]interface{}{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Budget-Exceeded", "true")
	w.Header().Set("X-Session-Cost", fmt.Sprintf("%.4f", budget.CurrentCost))
	w.Header().Set("X-Session-Cap", fmt.Sprintf("%.4f", budget.Cap))
	w.Header().Set("X-Global-Cost", fmt.Sprintf("%.4f", budget.GlobalCost))
	w.Header().Set("X-Global-Cap", fmt.Sprintf("%.4f", budget.GlobalCap))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp)
}
