// Dashboard API endpoints, savings reporting, and cost control responses.
package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/costcontrol"
	"github.com/compresr/context-gateway/internal/dashboard"
	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/compresr/context-gateway/internal/prompthistory"
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

	data.DashboardURL = fmt.Sprintf("http://localhost:%d/dashboard", config.DefaultDashboardPort)
	return data
}

// getSavingsReport returns the savings report from the LogAggregator.
// The LogAggregator is the single source of truth for savings data — it parses
// the authoritative JSONL telemetry files on disk. The SavingsTracker records
// real-time events but is not used as a report source (BUG-011 fix).
// sessionID: "all" for global, specific session name, or "" to default to current session.
func (g *Gateway) getSavingsReport(sessionID string) monitoring.SavingsReport {
	// Default to current session if no session specified and we have one
	if sessionID == "" && g.getCurrentSessionID() != "" {
		sessionID = g.getCurrentSessionID()
	}

	// "all" means global report (across all sessions)
	useGlobal := sessionID == "" || sessionID == "all"

	// LogAggregator is the single source of truth — always use it.
	if g.aggregator != nil {
		if useGlobal {
			return g.aggregator.GetReport()
		}
		return g.aggregator.GetReportForSession(sessionID)
	}

	return monitoring.SavingsReport{}
}

func hasSavingsData(sr monitoring.SavingsReport) bool {
	return sr.TotalRequests > 0 ||
		sr.TokensSaved > 0 || // Tool output compression
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
		ID           string   `json:"id"`
		Cost         float64  `json:"cost"`
		Cap          float64  `json:"cap"`
		RequestCount int      `json:"request_count"`
		Models       []string `json:"models"`
		CreatedAt    string   `json:"created_at"`
		LastUpdated  string   `json:"last_updated"`
		AgentName    string   `json:"agent_name,omitempty"`
		Active       bool     `json:"active,omitempty"`
		GatewayPort  int      `json:"gateway_port,omitempty"`
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
		KeptToolCount         int     `json:"filtered_tool_count,omitempty"`
		ToolDiscoveryTokens   int     `json:"tool_discovery_tokens,omitempty"`
		ToolDiscoveryCostUSD  float64 `json:"tool_discovery_cost_usd,omitempty"`
		ToolDiscoveryPct      float64 `json:"tool_discovery_pct,omitempty"`
		// Session activity counters
		UserTurns          int `json:"user_turns,omitempty"`
		CompactionTriggers int `json:"compaction_triggers,omitempty"`
		ToolSearchCalls    int `json:"tool_search_calls,omitempty"`
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
		UserTurns          int64  `json:"user_turns"`
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
		GlobalSavings *savingsJSON      `json:"global_savings,omitempty"`
		Expand        *expandJSON       `json:"expand,omitempty"`
		Search        *searchJSON       `json:"search,omitempty"`
		Gateway       *gatewayStatsJSON `json:"gateway,omitempty"`
		HiddenTabs    []string          `json:"hidden_tabs,omitempty"`
		ActivePorts   []int             `json:"active_ports,omitempty"`
	}

	resp := dashboardResponse{
		HiddenTabs: g.cfg().Dashboard.HiddenTabs,
	}
	requestedSessionID := r.URL.Query().Get("session")

	// Use global scope when no specific session is requested.
	useGlobalScope := requestedSessionID == "" || requestedSessionID == "all"
	scopedBilledSpend := 0.0

	if g.costTracker != nil {
		cfg := g.costTracker.Config()
		resp.Enabled = cfg.Enabled
		resp.SessionCap = cfg.SessionCap
		resp.GlobalCap = cfg.GlobalCap
	}

	// Always build the session list from disk (aggregator) so the dropdown
	// is stable regardless of which scope is selected.
	var sr monitoring.SavingsReport
	var allReport monitoring.SavingsReport
	var sessionReports map[string]*monitoring.SavingsReport
	var sessionMetas map[string]*monitoring.SessionMeta

	currentSessionID := g.getCurrentSessionID()

	if g.aggregator != nil {
		allReport, sessionReports, sessionMetas = g.aggregator.GetAllSessionsReport()

		// Build session list from ALL on-disk sessions (always stable).
		// Always include the active session even if empty (just started).
		for sessionID, sessionReport := range sessionReports {
			if sessionReport.TotalRequests == 0 && sessionReport.CompressedCostUSD == 0 && sessionID != currentSessionID {
				continue
			}
			sj := sessionJSON{
				ID:          sessionID,
				GatewayPort: g.cfg().Server.Port,
			}
			if sessionID == currentSessionID {
				sj.Active = true
			}
			if meta, ok := sessionMetas[sessionID]; ok {
				sj.Models = meta.Models
				if !meta.CreatedAt.IsZero() {
					sj.CreatedAt = meta.CreatedAt.Format(time.RFC3339)
				}
				if !meta.LastTimestamp.IsZero() {
					sj.LastUpdated = meta.LastTimestamp.Format(time.RFC3339)
				}
				// Use total cost/requests (all agents) for session card display.
				sj.Cost = meta.AllRequestsCostUSD
				sj.RequestCount = meta.AllRequestsCount
			}
			// If meta didn't have cost data, fall back to savings report.
			if sj.Cost == 0 {
				sj.Cost = sessionReport.CompressedCostUSD
			}
			if sj.RequestCount == 0 {
				sj.RequestCount = sessionReport.TotalRequests
			}
			resp.Sessions = append(resp.Sessions, sj)
			resp.TotalCost += sj.Cost
		}
	}

	// Sort sessions for stable ordering: active first, then newest-first by session ID.
	// Session IDs embed a timestamp (e.g. session_21_20260313_230747), so lexicographic
	// descending sort gives newest-first. This prevents random map-iteration reordering
	// on each poll.
	sort.Slice(resp.Sessions, func(i, j int) bool {
		a, b := resp.Sessions[i], resp.Sessions[j]
		if a.Active != b.Active {
			return a.Active // active first
		}
		return a.ID > b.ID // newest-first (session IDs are timestamp-prefixed)
	})

	// Populate active ports and merge sessions from all running gateway instances.
	// This allows a single dashboard view to show sessions from all running gateways.
	ownPort := g.cfg().Server.Port
	seenSessionIDs := make(map[string]bool, len(resp.Sessions))
	for _, s := range resp.Sessions {
		seenSessionIDs[s.ID] = true
	}
	for _, inst := range dashboard.DiscoverInstances() {
		resp.ActivePorts = append(resp.ActivePorts, inst.Port)
		if inst.Port == ownPort {
			continue // already have our own sessions
		}
		// Fetch sessions from peer gateway.
		peerURL := fmt.Sprintf("http://127.0.0.1:%d/api/dashboard", inst.Port)
		peerResp, peerErr := g.peerHTTPClient.Get(peerURL)
		if peerErr != nil {
			continue
		}
		var peerData struct {
			Sessions []sessionJSON `json:"sessions"`
		}
		if peerResp.StatusCode != http.StatusOK {
			_ = peerResp.Body.Close()
			continue
		}
		peerBody, _ := io.ReadAll(io.LimitReader(peerResp.Body, 1<<20))
		_ = peerResp.Body.Close()
		if json.Unmarshal(peerBody, &peerData) != nil {
			continue
		}
		for _, ps := range peerData.Sessions {
			if !seenSessionIDs[ps.ID] {
				seenSessionIDs[ps.ID] = true
				resp.Sessions = append(resp.Sessions, ps)
				resp.TotalCost += ps.Cost
			}
		}
	}

	// When the aggregator has no disk data yet (e.g. fresh start or in tests),
	// fall back to the cost tracker's in-memory total so the dashboard still
	// shows cost data and the savings card is rendered.
	if resp.TotalCost == 0 && g.costTracker != nil {
		resp.TotalCost = g.costTracker.GetGlobalCost()
	}

	// Now scope the savings data based on the selected session.
	if useGlobalScope {
		sr = allReport
		scopedBilledSpend = resp.TotalCost
	} else {
		// Specific session selected — use that session's report from disk.
		if sessionReports != nil {
			if sessReport, ok := sessionReports[requestedSessionID]; ok {
				sr = *sessReport
				// Prefer AllRequestsCostUSD from meta: it includes ALL requests (main
				// agent + sub-agents) and is the true total cost shown on the session card.
				// sessReport.CompressedCostUSD only accumulates IsMainAgent=true requests,
				// so it drastically understates cost for sub-agent-heavy sessions.
				if meta, ok := sessionMetas[requestedSessionID]; ok && meta.AllRequestsCostUSD > 0 {
					scopedBilledSpend = meta.AllRequestsCostUSD
				} else {
					scopedBilledSpend = sessReport.CompressedCostUSD
				}
			}
		}
		// Fall back to aggregator's per-session cache if not found in all-sessions.
		if !hasSavingsData(sr) {
			sr = g.getSavingsReport(requestedSessionID)
		}
		// Always prefer the cost tracker's authoritative per-session spend — it tracks
		// all requests (including non-compressed), not just compressed ones.
		if g.costTracker != nil {
			if sessionSpend := g.costTracker.GetSessionCost(requestedSessionID); sessionSpend > 0 {
				scopedBilledSpend = sessionSpend
			}
		}
	}

	// Use savings report's request count (filtered to main agent only).
	resp.TotalRequests = sr.TotalRequests

	// Always show savings card when we have spend or savings data.
	if hasSavingsData(sr) || scopedBilledSpend > 0 {
		resp.Savings = &savingsJSON{
			TotalRequests:         sr.TotalRequests,
			CompressedRequests:    sr.CompressedRequests,
			TokensSaved:           sr.TokensSaved, // Tool output compression only (not total)
			TokenSavedPct:         sr.TotalSavedPct,
			BilledSpendUSD:        scopedBilledSpend,
			CostSavedUSD:          sr.CostSavedUSD,
			CompressedCostUSD:     scopedBilledSpend,
			OriginalCostUSD:       scopedBilledSpend + sr.CostSavedUSD,
			CompressionRatio:      sr.AvgCompressionRatio,
			ToolDiscoveryRequests: sr.ToolDiscoveryRequests,
			OriginalToolCount:     sr.OriginalToolCount,
			KeptToolCount:         sr.KeptToolCount,
			ToolDiscoveryTokens:   sr.ToolDiscoveryTokens,
			ToolDiscoveryCostUSD:  sr.ToolDiscoveryCostUSD,
			ToolDiscoveryPct:      sr.ToolDiscoveryPct,
			UserTurns:             sr.UserTurns,
			CompactionTriggers:    sr.CompactionTriggers,
			ToolSearchCalls:       sr.ToolSearchCalls,
		}
	}

	// Always include global savings (for summary cards that don't change on session select).
	if hasSavingsData(allReport) || resp.TotalCost > 0 {
		resp.GlobalSavings = &savingsJSON{
			TotalRequests:         allReport.TotalRequests,
			CompressedRequests:    allReport.CompressedRequests,
			TokensSaved:           allReport.TokensSaved, // Tool output compression only (not total)
			TokenSavedPct:         allReport.TotalSavedPct,
			BilledSpendUSD:        resp.TotalCost,
			CostSavedUSD:          allReport.CostSavedUSD,
			CompressedCostUSD:     resp.TotalCost,
			OriginalCostUSD:       resp.TotalCost + allReport.CostSavedUSD,
			CompressionRatio:      allReport.AvgCompressionRatio,
			ToolDiscoveryRequests: allReport.ToolDiscoveryRequests,
			OriginalToolCount:     allReport.OriginalToolCount,
			KeptToolCount:         allReport.KeptToolCount,
			ToolDiscoveryTokens:   allReport.ToolDiscoveryTokens,
			ToolDiscoveryCostUSD:  allReport.ToolDiscoveryCostUSD,
			ToolDiscoveryPct:      allReport.ToolDiscoveryPct,
			UserTurns:             allReport.UserTurns,
			CompactionTriggers:    allReport.CompactionTriggers,
			ToolSearchCalls:       allReport.ToolSearchCalls,
		}
	}

	// Expand context log — scoped to the selected session when applicable.
	if g.expandLog != nil {
		var summary monitoring.ExpandSummary
		var recent []monitoring.ExpandLogEntry
		if useGlobalScope {
			summary = g.expandLog.Summary()
			recent = g.expandLog.Recent(20)
		} else {
			summary = g.expandLog.SummaryForSession(requestedSessionID)
			recent = g.expandLog.RecentForSession(requestedSessionID, 20)
		}
		resp.Expand = &expandJSON{
			Total:    summary.Total,
			Found:    summary.Found,
			NotFound: summary.NotFound,
		}
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

	// Search tool log — scoped to the selected session when applicable.
	if g.searchLog != nil {
		var summary monitoring.SearchSummary
		var recent []monitoring.SearchLogEntry
		if useGlobalScope {
			summary = g.searchLog.Summary()
			recent = g.searchLog.Recent(20)
		} else {
			summary = g.searchLog.SummaryForSession(requestedSessionID)
			recent = g.searchLog.RecentForSession(requestedSessionID, 20)
		}
		resp.Search = &searchJSON{
			Total: summary.Total,
		}
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
			UserTurns:          stats["user_turns"],
			SuccessfulRequests: stats["successes"],
			Compressions:       stats["compressions"],
			CacheHits:          stats["cache_hits"],
			CacheMisses:        stats["cache_misses"],
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "http://localhost:18080")
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
	w.Header().Set("Access-Control-Allow-Origin", "http://localhost:18080")
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
	w.Header().Set("Access-Control-Allow-Origin", "http://localhost:18080")

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
func (g *Gateway) returnBudgetExceededResponse(w http.ResponseWriter, provider string, budget costcontrol.BudgetCheckResult, sessionID string) {
	dashboardURL := fmt.Sprintf("http://localhost:%d/dashboard", config.DefaultDashboardPort)
	var msg string
	if budget.GlobalCap > 0 && budget.GlobalCost >= budget.GlobalCap {
		msg = fmt.Sprintf("Budget exceeded for session %q. Total spend: $%.4f, limit: $%.2f. "+
			"Increase the session cap in your monitor dashboard at %s.",
			sessionID, budget.GlobalCost, budget.GlobalCap, dashboardURL)
	} else {
		msg = fmt.Sprintf("Budget exceeded for session %q. Current spend: $%.4f, limit: $%.2f. "+
			"Increase the session cap in your monitor dashboard at %s.",
			sessionID, budget.CurrentCost, budget.Cap, dashboardURL)
	}

	var resp []byte
	if provider == "anthropic" {
		resp, _ = json.Marshal(map[string]any{
			"id":            "msg_budget_exceeded",
			"type":          "message",
			"role":          "assistant",
			"model":         "budget-control",
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"content":       []map[string]any{{"type": "text", "text": msg}},
			"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
		})
	} else {
		resp, _ = json.Marshal(map[string]any{
			"id":      "budget_exceeded",
			"object":  "chat.completion",
			"model":   "budget-control",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": msg}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
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

// handleDeleteSession deletes a session's log directory from disk.
// DELETE /api/session?id=SESSION_ID — removes the session folder, all its logs, and prompt history.
// The active session cannot be deleted.
func (g *Gateway) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		g.writeError(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodDelete {
		g.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessionID := r.URL.Query().Get("id")
	if sessionID == "" {
		g.writeError(w, "id required", http.StatusBadRequest)
		return
	}
	// Trim and reject empty after trim (prevents whitespace-only IDs)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		g.writeError(w, "invalid session id", http.StatusBadRequest)
		return
	}
	// Reject overly long IDs (DoS prevention)
	if len(sessionID) > 128 {
		g.writeError(w, "invalid session id", http.StatusBadRequest)
		return
	}
	// Reject URL-encoded characters (prevents encoded path traversal)
	if strings.Contains(sessionID, "%") {
		g.writeError(w, "invalid session id", http.StatusBadRequest)
		return
	}
	// Allowlist: only alphanumeric, underscore, hyphen
	for _, c := range sessionID {
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isAlphaNum && c != '_' && c != '-' {
			g.writeError(w, "invalid session id", http.StatusBadRequest)
			return
		}
	}
	// Cannot delete the currently active session
	if sessionID == g.getCurrentSessionID() {
		g.writeError(w, "cannot delete active session", http.StatusConflict)
		return
	}
	if g.aggregator == nil {
		g.writeError(w, "aggregator not available", http.StatusServiceUnavailable)
		return
	}
	sessionDir := g.aggregator.GetSessionDir(sessionID)
	if err := os.RemoveAll(sessionDir); err != nil { // #nosec G703 G705 -- sessionDir is constructed from logsDir + validated sessionID
		log.Error().Err(err).Str("session", sessionID).Msg("failed to delete session directory")
		g.writeError(w, "delete failed", http.StatusInternalServerError)
		return
	}
	// Purge the deleted session from all in-memory caches so totals are
	// immediately accurate without waiting for the next background tick.
	g.aggregator.InvalidateSession(sessionID)

	// Delete all prompt history for this session
	if g.promptHistory != nil {
		if err := g.promptHistory.EraseBySession(r.Context(), sessionID); err != nil {
			log.Warn().Err(err).Str("session", sessionID).Msg("failed to delete session prompts (non-fatal)")
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "http://localhost:18080")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleErasePrompts deletes all prompt history records.
// Restricted to localhost. Only accepts DELETE method.
func (g *Gateway) handleErasePrompts(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		g.writeError(w, "forbidden", http.StatusForbidden)
		return
	}

	if r.Method != http.MethodDelete {
		g.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if g.promptHistory == nil {
		g.writeError(w, "prompt history not available", http.StatusServiceUnavailable)
		return
	}

	if err := g.promptHistory.EraseAll(r.Context()); err != nil {
		log.Error().Err(err).Msg("failed to erase prompt history")
		g.writeError(w, "erase failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "http://localhost:18080")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleDeletePrompt deletes a single prompt by ID.
// Restricted to localhost. Only accepts DELETE method.
// Expects: DELETE /api/prompts/{id}
func (g *Gateway) handleDeletePrompt(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		g.writeError(w, "forbidden", http.StatusForbidden)
		return
	}

	if r.Method != http.MethodDelete {
		g.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if g.promptHistory == nil {
		g.writeError(w, "prompt history not available", http.StatusServiceUnavailable)
		return
	}

	// Extract ID from path: /api/prompts/{id}
	path := strings.TrimPrefix(r.URL.Path, "/api/prompts/")
	id, err := strconv.ParseInt(path, 10, 64)
	if err != nil || id <= 0 {
		g.writeError(w, "invalid prompt id", http.StatusBadRequest)
		return
	}

	if err := g.promptHistory.DeleteByID(r.Context(), id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			g.writeError(w, "prompt not found", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Int64("id", id).Msg("failed to delete prompt")
		g.writeError(w, "delete failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "http://localhost:18080")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handlePromptsAPI returns paginated prompt history for the dashboard.
// Restricted to localhost to prevent external access.
func (g *Gateway) handlePromptsAPI(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		g.writeError(w, "forbidden", http.StatusForbidden)
		return
	}

	if g.promptHistory == nil {
		g.writeError(w, "prompt history not available", http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	params := prompthistory.QueryParams{
		Search:   q.Get("q"),
		Session:  q.Get("session"),
		Model:    q.Get("model"),
		Provider: q.Get("provider"),
		Page:     page,
		Limit:    limit,
	}

	result, err := g.promptHistory.Query(r.Context(), params)
	if err != nil {
		log.Error().Err(err).Msg("prompt history query failed")
		g.writeError(w, "query failed", http.StatusInternalServerError)
		return
	}

	filters, err := g.promptHistory.FilterOptions(r.Context())
	if err != nil {
		log.Warn().Err(err).Msg("prompt history filter options failed")
		filters = &prompthistory.FilterOptions{}
	}

	type response struct {
		Prompts    []prompthistory.PromptRecord `json:"prompts"`
		Total      int                          `json:"total"`
		Page       int                          `json:"page"`
		Limit      int                          `json:"limit"`
		TotalPages int                          `json:"total_pages"`
		Filters    *prompthistory.FilterOptions `json:"filters"`
	}

	resp := response{
		Prompts:    result.Prompts,
		Total:      result.Total,
		Page:       result.Page,
		Limit:      result.Limit,
		TotalPages: result.TotalPages,
		Filters:    filters,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "http://localhost:18080")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Error().Err(err).Msg("failed to encode prompts response")
	}
}

// handleAggregatedMonitorAPI aggregates session monitoring data from ALL active gateway instances.
// Returns one entry per gateway port, merging all sessions for that port into a single card.
// The agent name comes from the instance registry (the name the user configured).
// Restricted to localhost.
func (g *Gateway) handleAggregatedMonitorAPI(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		g.writeError(w, "forbidden", http.StatusForbidden)
		return
	}

	type monitorEntryJSON struct {
		Name             string  `json:"name"`
		Port             int     `json:"port"`
		Provider         string  `json:"provider"`
		Model            string  `json:"model"`
		Status           string  `json:"status"`
		StartedAt        string  `json:"started_at"`
		LastActivityAt   string  `json:"last_activity_at"`
		RequestCount     int     `json:"request_count"`
		TokensIn         int     `json:"tokens_in"`
		TokensOut        int     `json:"tokens_out"`
		TokensSaved      int     `json:"tokens_saved"`
		CostUSD          float64 `json:"cost_usd"`
		CompressionCount int     `json:"compression_count"`
		LastUserQuery    string  `json:"last_user_query"`
		LastToolUsed     string  `json:"last_tool_used"`
		WorkingDir       string  `json:"working_dir"`
	}

	type monitorResponse struct {
		Instances []monitorEntryJSON `json:"instances"`
		Timestamp string             `json:"timestamp"`
	}

	// Build port -> registry name lookup
	registryInstances := dashboard.DiscoverInstances()
	nameByPort := make(map[int]string, len(registryInstances))
	startedByPort := make(map[int]string, len(registryInstances))
	for _, inst := range registryInstances {
		nameByPort[inst.Port] = inst.AgentName
		startedByPort[inst.Port] = inst.StartedAt.Format(time.RFC3339)
	}

	entries := make([]monitorEntryJSON, 0, len(registryInstances))

	for _, inst := range registryInstances {
		port := inst.Port
		url := fmt.Sprintf("http://127.0.0.1:%d/monitor/api/sessions", port)

		resp, err := g.monitorHTTPClient.Get(url)
		if err != nil {
			continue
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if err != nil {
			continue
		}

		var sr struct {
			Sessions []dashboard.Session `json:"sessions"`
		}
		if json.Unmarshal(body, &sr) != nil {
			continue
		}

		// Merge all sessions for this port into one entry.
		// Default to "active" because this port is in the registry (process is running).
		// Status will be overridden below once we have real session data.
		entry := monitorEntryJSON{
			Name:      nameByPort[port],
			Port:      port,
			StartedAt: startedByPort[port],
			Status:    "active",
		}

		var latestActivity time.Time
		var latestQueryTime time.Time
		for _, s := range sr.Sessions {
			entry.RequestCount += s.MainAgentRequestCount
			entry.TokensIn += s.TokensIn
			entry.TokensOut += s.TokensOut
			entry.TokensSaved += s.TokensSaved
			entry.CostUSD += s.CostUSD
			entry.CompressionCount += s.CompressionCount

			// Use the most recently active session for status/context
			if s.LastActivityAt.After(latestActivity) {
				latestActivity = s.LastActivityAt
				entry.LastActivityAt = s.LastActivityAt.Format(time.RFC3339)
				entry.LastToolUsed = s.LastToolUsed
				entry.WorkingDir = s.WorkingDir
				entry.Status = string(s.Status)
				if s.Provider != "" {
					entry.Provider = s.Provider
				}
				if s.Model != "" {
					entry.Model = s.Model
				}
			}
			// Use the query from the most recently active session that has one,
			// not just any non-empty query (which causes rotation on re-polls).
			if s.LastUserQuery != "" && s.LastActivityAt.After(latestQueryTime) {
				latestQueryTime = s.LastActivityAt
				entry.LastUserQuery = s.LastUserQuery
			}
		}

		// If no sessions yet, the gateway is starting — skip this entry entirely.
		// A "waiting_for_human" entry should only appear after at least one real
		// request has been processed; showing it on startup creates a phantom session
		// in the dashboard before any agent has connected.
		if len(sr.Sessions) == 0 {
			continue
		}

		entries = append(entries, entry)
	}

	if entries == nil {
		entries = []monitorEntryJSON{}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "http://localhost:18080")
	if err := json.NewEncoder(w).Encode(monitorResponse{
		Instances: entries,
		Timestamp: time.Now().Format(time.RFC3339),
	}); err != nil {
		log.Error().Err(err).Msg("failed to encode monitor response")
	}
}

// handleRenameInstance renames a gateway instance in the shared registry.
// PATCH /api/monitor/rename with JSON body: {"port": 18081, "name": "My Agent"}
func (g *Gateway) handleRenameInstance(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		g.writeError(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPatch {
		g.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Port int    `json:"port"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		g.writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Port <= 0 || req.Name == "" {
		g.writeError(w, "port and name are required", http.StatusBadRequest)
		return
	}

	if !dashboard.RenameInstance(req.Port, req.Name) {
		g.writeError(w, "instance not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "http://localhost:18080")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "renamed", "name": req.Name})
}

// handleInstanceConfigProxy proxies GET/PATCH config requests to a specific gateway instance.
// GET/PATCH /api/instance/config?port=18081 → http://127.0.0.1:18081/api/config
func (g *Gateway) handleInstanceConfigProxy(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		g.writeError(w, "forbidden", http.StatusForbidden)
		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodPatch {
		w.Header().Set("Allow", "GET, PATCH")
		g.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	portStr := r.URL.Query().Get("port")
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		g.writeError(w, "invalid port", http.StatusBadRequest)
		return
	}

	// Validate port against known instances to prevent local port scanning.
	allowedPorts := make(map[int]bool)
	for _, inst := range dashboard.DiscoverInstances() {
		allowedPorts[inst.Port] = true
	}
	// Also allow the gateway's own port.
	allowedPorts[g.cfg().Server.Port] = true
	if !allowedPorts[port] {
		g.writeError(w, "port not found in active instances", http.StatusBadRequest)
		return
	}

	targetURL := fmt.Sprintf("http://127.0.0.1:%d/api/config", port)

	client := &http.Client{Timeout: 5 * time.Second}

	var proxyReq *http.Request
	if r.Method == http.MethodPatch {
		body, readErr := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if readErr != nil {
			g.writeError(w, "failed to read body", http.StatusBadRequest)
			return
		}
		proxyReq, err = http.NewRequestWithContext(r.Context(), http.MethodPatch, targetURL, io.NopCloser( // #nosec G704 -- targetURL is hardcoded to 127.0.0.1
			// Use bytes.NewReader to avoid importing bytes just for this
			strings.NewReader(string(body)),
		))
		if proxyReq != nil {
			proxyReq.Header.Set("Content-Type", "application/json")
		}
	} else {
		proxyReq, err = http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL, nil) // #nosec G704 -- targetURL is hardcoded to 127.0.0.1
	}

	if err != nil {
		g.writeError(w, "failed to create proxy request", http.StatusInternalServerError)
		return
	}

	resp, err := client.Do(proxyReq) // #nosec G704 -- request targets 127.0.0.1 only
	if err != nil {
		g.writeError(w, fmt.Sprintf("instance on port %d unreachable", port), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		g.writeError(w, "failed to read instance response", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "http://localhost:18080")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}

// handleFocusTerminal brings the terminal window for a gateway instance to the foreground.
// POST /api/focus?port=18081
func (g *Gateway) handleFocusTerminal(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		g.writeError(w, "forbidden", http.StatusForbidden)
		return
	}

	portStr := r.URL.Query().Get("port")
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid port"})
		return
	}

	instances := dashboard.DiscoverInstances()
	var target *dashboard.Instance
	for _, inst := range instances {
		if inst.Port == port {
			target = &inst
			break
		}
	}
	if target == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "instance not found"})
		return
	}

	if err := dashboard.FocusTerminal(*target); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "focused", "port": portStr})
}
