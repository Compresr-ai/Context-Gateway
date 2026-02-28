// Package monitoring - savings.go tracks compression savings in real-time.
//
// DESIGN: SavingsTracker accumulates metrics as requests flow through the gateway.
// When /savings is detected, the pre-computed report is returned instantly.
//
// Tracked metrics:
//   - Total requests and compressed requests count
//   - Original vs compressed tokens
//   - USD cost savings (using pricing.go rates)
//   - Compression ratio
package monitoring

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/compresr/context-gateway/internal/costcontrol"
)

// SavingsTracker accumulates compression savings in memory.
type SavingsTracker struct {
	mu sync.RWMutex

	// Request counts
	TotalRequests      int
	CompressedRequests int

	// Token counts (cumulative) - Tool Output Compression
	OriginalTokens   int
	CompressedTokens int

	// Tool Discovery stats
	ToolDiscoveryRequests int
	OriginalToolCount     int
	FilteredToolCount     int
	ToolDiscoveryBytes    int // Original bytes of tools
	FilteredToolBytes     int // Bytes after filtering

	// Expand penalty (tokens sent back due to expand_context calls)
	ExpandPenaltyTokens int

	// Model tracking for cost calculation
	ModelUsage map[string]ModelUsageStats

	// Per-session tracking
	Sessions map[string]*SessionSavings

	stopCh chan struct{}
}

// SessionSavings holds savings data for a single session.
type SessionSavings struct {
	TotalRequests      int
	CompressedRequests int
	OriginalTokens     int
	CompressedTokens   int
	ModelUsage         map[string]ModelUsageStats

	ToolDiscoveryRequests int
	OriginalToolCount     int
	FilteredToolCount     int
	ToolDiscoveryBytes    int
	FilteredToolBytes     int

	ExpandPenaltyTokens int

	LastUpdated time.Time
}

const savingsSessionTTL = 24 * time.Hour

// ModelUsageStats tracks usage per model for accurate cost calculation.
type ModelUsageStats struct {
	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
	RequestCount        int
}

// SavingsReport is the computed savings summary.
type SavingsReport struct {
	TotalRequests       int
	CompressedRequests  int
	PassthroughRequests int

	// Tool Output Compression
	OriginalTokens   int
	CompressedTokens int
	TokensSaved      int
	TokenSavedPct    float64

	// Tool Discovery (tool filtering)
	ToolDiscoveryRequests int
	OriginalToolCount     int
	FilteredToolCount     int
	ToolDiscoveryTokens   int // Tokens saved by filtering tools
	ToolDiscoveryPct      float64

	// Combined totals
	TotalTokensSaved int
	TotalSavedPct    float64

	// Expand penalty
	ExpandPenaltyTokens  int
	ExpandPenaltyCostUSD float64

	OriginalCostUSD   float64
	CompressedCostUSD float64
	CostSavedUSD      float64
	CostSavedPct      float64

	AvgCompressionRatio float64
}

// NewSavingsTracker creates a new tracker with background cleanup.
func NewSavingsTracker() *SavingsTracker {
	t := &SavingsTracker{
		ModelUsage: make(map[string]ModelUsageStats),
		Sessions:   make(map[string]*SessionSavings),
		stopCh:     make(chan struct{}),
	}
	go t.cleanup()
	return t
}

// Stop stops the background cleanup goroutine.
func (t *SavingsTracker) Stop() {
	select {
	case <-t.stopCh:
	default:
		close(t.stopCh)
	}
}

// getOrCreateSession returns the session for the given ID, creating it if needed.
// Caller must hold the write lock.
func (t *SavingsTracker) getOrCreateSession(sessionID string) *SessionSavings {
	ss := t.Sessions[sessionID]
	if ss == nil {
		ss = &SessionSavings{ModelUsage: make(map[string]ModelUsageStats)}
		t.Sessions[sessionID] = ss
	}
	return ss
}

func (t *SavingsTracker) cleanup() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-t.stopCh:
			return
		case <-ticker.C:
			t.mu.Lock()
			now := time.Now()
			for id, ss := range t.Sessions {
				if now.Sub(ss.LastUpdated) > savingsSessionTTL {
					delete(t.Sessions, id)
				}
			}
			t.mu.Unlock()
		}
	}
}

// RecordRequest updates the tracker with a new request (global only).
func (t *SavingsTracker) RecordRequest(event *RequestEvent) {
	t.RecordRequestWithSession(event, "")
}

// RecordRequestWithSession updates both global and per-session trackers.
func (t *SavingsTracker) RecordRequestWithSession(event *RequestEvent, sessionID string) {
	if event == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Global tracking
	t.TotalRequests++
	if event.CompressionUsed {
		t.CompressedRequests++
	}
	t.OriginalTokens += event.OriginalTokens
	t.CompressedTokens += event.CompressedTokens

	model := event.Model
	if model == "" {
		model = "unknown"
	}
	stats := t.ModelUsage[model]
	stats.InputTokens += event.InputTokens
	stats.OutputTokens += event.OutputTokens
	stats.CacheCreationTokens += event.CacheCreationInputTokens
	stats.CacheReadTokens += event.CacheReadInputTokens
	stats.RequestCount++
	t.ModelUsage[model] = stats

	// Per-session tracking
	if sessionID != "" {
		ss := t.getOrCreateSession(sessionID)
		ss.TotalRequests++
		if event.CompressionUsed {
			ss.CompressedRequests++
		}
		ss.OriginalTokens += event.OriginalTokens
		ss.CompressedTokens += event.CompressedTokens
		mStats := ss.ModelUsage[model]
		mStats.InputTokens += event.InputTokens
		mStats.OutputTokens += event.OutputTokens
		mStats.CacheCreationTokens += event.CacheCreationInputTokens
		mStats.CacheReadTokens += event.CacheReadInputTokens
		mStats.RequestCount++
		ss.ModelUsage[model] = mStats
		ss.LastUpdated = time.Now()
	}
}

// RecordToolDiscovery updates the tracker with tool discovery (filtering) stats.
func (t *SavingsTracker) RecordToolDiscovery(comparison CompressionComparison) {
	t.RecordToolDiscoveryWithSession(comparison, "")
}

// RecordToolDiscoveryWithSession updates global and per-session tool discovery stats.
func (t *SavingsTracker) RecordToolDiscoveryWithSession(comparison CompressionComparison, sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.ToolDiscoveryRequests++
	t.OriginalToolCount += len(comparison.AllTools)
	t.FilteredToolCount += len(comparison.SelectedTools)
	t.ToolDiscoveryBytes += comparison.OriginalBytes
	t.FilteredToolBytes += comparison.CompressedBytes

	if sessionID != "" {
		ss := t.getOrCreateSession(sessionID)
		ss.ToolDiscoveryRequests++
		ss.OriginalToolCount += len(comparison.AllTools)
		ss.FilteredToolCount += len(comparison.SelectedTools)
		ss.ToolDiscoveryBytes += comparison.OriginalBytes
		ss.FilteredToolBytes += comparison.CompressedBytes
		ss.LastUpdated = time.Now()
	}
}

// RecordExpandPenalty records tokens that were re-sent due to expand_context calls.
// These negate compression savings since the full content was sent anyway.
func (t *SavingsTracker) RecordExpandPenalty(penaltyTokens int, sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.ExpandPenaltyTokens += penaltyTokens

	if sessionID != "" {
		ss := t.getOrCreateSession(sessionID)
		ss.ExpandPenaltyTokens += penaltyTokens
		ss.LastUpdated = time.Now()
	}
}

// GetReportForSession computes savings for a specific session.
func (t *SavingsTracker) GetReportForSession(sessionID string) SavingsReport {
	t.mu.RLock()
	defer t.mu.RUnlock()

	ss := t.Sessions[sessionID]
	if ss == nil {
		return SavingsReport{}
	}

	return t.computeReport(ss.TotalRequests, ss.CompressedRequests,
		ss.OriginalTokens, ss.CompressedTokens,
		ss.ToolDiscoveryRequests, ss.OriginalToolCount, ss.FilteredToolCount,
		ss.ToolDiscoveryBytes, ss.FilteredToolBytes, ss.ExpandPenaltyTokens, ss.ModelUsage)
}

// SessionIDs returns all tracked session IDs.
func (t *SavingsTracker) SessionIDs() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	ids := make([]string, 0, len(t.Sessions))
	for id := range t.Sessions {
		ids = append(ids, id)
	}
	return ids
}

// GetReport computes the current savings summary (global, all sessions).
func (t *SavingsTracker) GetReport() SavingsReport {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.computeReport(t.TotalRequests, t.CompressedRequests,
		t.OriginalTokens, t.CompressedTokens,
		t.ToolDiscoveryRequests, t.OriginalToolCount, t.FilteredToolCount,
		t.ToolDiscoveryBytes, t.FilteredToolBytes, t.ExpandPenaltyTokens, t.ModelUsage)
}

// computeReport builds a SavingsReport from raw data. Must be called under lock.
func (t *SavingsTracker) computeReport(totalReqs, compressedReqs, origTokens, compTokens,
	tdRequests, tdOrigCount, tdFilteredCount, tdBytes, tdFilteredBytes, expandPenaltyTokens int,
	modelUsage map[string]ModelUsageStats) SavingsReport {

	report := SavingsReport{
		TotalRequests:         totalReqs,
		CompressedRequests:    compressedReqs,
		PassthroughRequests:   totalReqs - compressedReqs,
		OriginalTokens:        origTokens,
		CompressedTokens:      compTokens,
		TokensSaved:           origTokens - compTokens,
		ToolDiscoveryRequests: tdRequests,
		OriginalToolCount:     tdOrigCount,
		FilteredToolCount:     tdFilteredCount,
	}

	if tdBytes > 0 {
		report.ToolDiscoveryTokens = (tdBytes - tdFilteredBytes) / 4
		report.ToolDiscoveryPct = float64(tdBytes-tdFilteredBytes) / float64(tdBytes) * 100
	}

	if origTokens > 0 {
		report.TokenSavedPct = float64(report.TokensSaved) / float64(origTokens) * 100
		report.AvgCompressionRatio = float64(origTokens) / float64(compTokens)
	}

	report.ExpandPenaltyTokens = expandPenaltyTokens
	report.TotalTokensSaved = report.TokensSaved + report.ToolDiscoveryTokens - expandPenaltyTokens
	if report.TotalTokensSaved < 0 {
		report.TotalTokensSaved = 0
	}
	totalOriginal := origTokens + (tdBytes / 4)
	if totalOriginal > 0 {
		report.TotalSavedPct = float64(report.TotalTokensSaved) / float64(totalOriginal) * 100
	}

	// Cost calculation:
	// CompressedCostUSD = actual API spend (what the provider charged for the compressed request)
	// CostSavedUSD = tokens saved * dominant model's input price per token
	// OriginalCostUSD = CompressedCostUSD + CostSavedUSD (what we would have paid uncompressed)
	for model, stats := range modelUsage {
		pricing := costcontrol.GetModelPricing(model)
		report.CompressedCostUSD += costcontrol.CalculateCostWithCache(
			stats.InputTokens, stats.OutputTokens,
			stats.CacheCreationTokens, stats.CacheReadTokens, pricing)
	}

	// Find the dominant model's input pricing (model with most requests) for saved token cost
	var dominantPricing costcontrol.ModelPricing
	maxReqs := 0
	for model, stats := range modelUsage {
		if stats.RequestCount > maxReqs {
			maxReqs = stats.RequestCount
			dominantPricing = costcontrol.GetModelPricing(model)
		}
	}

	if report.ExpandPenaltyTokens > 0 {
		report.ExpandPenaltyCostUSD = float64(report.ExpandPenaltyTokens) / 1_000_000 * dominantPricing.InputPerMTok
	}

	if report.TotalTokensSaved > 0 {
		report.CostSavedUSD = float64(report.TotalTokensSaved) / 1_000_000 * dominantPricing.InputPerMTok
	}

	report.OriginalCostUSD = report.CompressedCostUSD + report.CostSavedUSD
	if report.OriginalCostUSD > 0 {
		report.CostSavedPct = report.CostSavedUSD / report.OriginalCostUSD * 100
	}

	return report
}

// GetSavingsSummary returns a quick summary for CLI display.
// Returns tokensSaved, costSavedUSD, compressedRequests, totalRequests.
func (t *SavingsTracker) GetSavingsSummary() (int, float64, int, int) {
	r := t.GetReport()
	return r.TotalTokensSaved, r.CostSavedUSD, r.CompressedRequests, r.TotalRequests
}

// GetCostBreakdown returns original, compressed, and saved cost in USD.
func (t *SavingsTracker) GetCostBreakdown() (float64, float64, float64) {
	r := t.GetReport()
	return r.OriginalCostUSD, r.CompressedCostUSD, r.CostSavedUSD
}

// GetCompressionStats returns compression and tool discovery statistics.
func (t *SavingsTracker) GetCompressionStats() (int, int, int, int, int) {
	r := t.GetReport()
	return r.CompressedRequests, r.TotalRequests, r.ToolDiscoveryRequests, r.OriginalToolCount, r.FilteredToolCount
}

// GetDetailedSummary returns a detailed savings breakdown for the TUI status bar.
func (t *SavingsTracker) GetDetailedSummary() DetailedSavings {
	r := t.GetReport()
	return DetailedSavings{
		OriginalCostUSD:       r.OriginalCostUSD,
		CompressedCostUSD:     r.CompressedCostUSD,
		CostSavedUSD:          r.CostSavedUSD,
		OriginalTokens:        r.OriginalTokens,
		CompressedTokens:      r.CompressedTokens,
		TokensSaved:           r.TotalTokensSaved,
		TotalRequests:         r.TotalRequests,
		CompressedRequests:    r.CompressedRequests,
		ToolDiscoveryRequests: r.ToolDiscoveryRequests,
		OriginalToolCount:     r.OriginalToolCount,
		FilteredToolCount:     r.FilteredToolCount,
		ExpandPenaltyTokens:   r.ExpandPenaltyTokens,
	}
}

// DetailedSavings contains the full savings breakdown for display.
type DetailedSavings struct {
	OriginalCostUSD   float64
	CompressedCostUSD float64
	CostSavedUSD      float64

	OriginalTokens   int
	CompressedTokens int
	TokensSaved      int

	TotalRequests      int
	CompressedRequests int

	ToolDiscoveryRequests int
	OriginalToolCount     int
	FilteredToolCount     int
	ExpandPenaltyTokens   int
}

// FormatReport returns a formatted string for display.
func (t *SavingsTracker) FormatReport() string {
	r := t.GetReport()

	var sb strings.Builder
	sb.WriteString("💰 Savings Report\n")
	sb.WriteString("═══════════════════════════════════════════════\n")

	// ── Cost ──
	sb.WriteString("\n💵 Cost\n")
	sb.WriteString("─────────────────────────────────────────────────\n")
	fmt.Fprintf(&sb, "  Actual spend:       $%.4f\n", r.CompressedCostUSD)
	fmt.Fprintf(&sb, "  You saved:          $%.4f", r.CostSavedUSD)
	if r.CostSavedPct > 0 {
		fmt.Fprintf(&sb, "  (%.1f%%)", r.CostSavedPct)
	}
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "  Without proxy:      $%.4f\n", r.OriginalCostUSD)

	// ── Details ──
	sb.WriteString("\n📊 Details\n")
	sb.WriteString("─────────────────────────────────────────────────\n")
	fmt.Fprintf(&sb, "  Compressed requests:  %d / %d\n", r.CompressedRequests, r.TotalRequests)
	fmt.Fprintf(&sb, "  Tokens saved:         %d", r.TotalTokensSaved)
	if r.TotalSavedPct > 0 {
		fmt.Fprintf(&sb, "  (%.1f%%)", r.TotalSavedPct)
	}
	sb.WriteString("\n")
	if r.ToolDiscoveryRequests > 0 {
		fmt.Fprintf(&sb, "  Tools filtered:       %d → %d tools (%d tokens saved)\n", r.OriginalToolCount, r.FilteredToolCount, r.ToolDiscoveryTokens)
	}
	if r.ExpandPenaltyTokens > 0 {
		fmt.Fprintf(&sb, "  Expand penalty:       -%d tokens (-$%.4f)\n", r.ExpandPenaltyTokens, r.ExpandPenaltyCostUSD)
	}

	sb.WriteString("═══════════════════════════════════════════════\n")

	return sb.String()
}

// UnifiedReportData provides extra context for the unified /savings report.
type UnifiedReportData struct {
	// Cost data
	TotalSessionCost float64
	SessionCount     int
	// Expand context data
	ExpandTotal    int
	ExpandFound    int
	ExpandNotFound int
	// Account balance (from compresr API)
	BalanceAvailable     bool
	Tier                 string
	CreditsRemainingUSD  float64
	CreditsUsedThisMonth float64
	MonthlyBudgetUSD     float64
	IsAdmin              bool
	// Dashboard link
	DashboardURL string
}

// FormatUnifiedReportForSession returns a formatted string scoped to a specific session.
func (t *SavingsTracker) FormatUnifiedReportForSession(sessionID string, extra UnifiedReportData) string {
	r := t.GetReportForSession(sessionID)
	return formatUnifiedReport(r, extra)
}

// FormatUnifiedReport returns a formatted string combining savings, cost, and expand data.
func (t *SavingsTracker) FormatUnifiedReport(extra UnifiedReportData) string {
	r := t.GetReport()
	return formatUnifiedReport(r, extra)
}

// formatUnifiedReport formats a SavingsReport with extra unified data.
func formatUnifiedReport(r SavingsReport, extra UnifiedReportData) string {

	var sb strings.Builder
	sb.WriteString("💰 Savings Report\n")
	sb.WriteString("═══════════════════════════════════════════════\n")

	// ── Row 1: Cost ──
	sb.WriteString("\n💵 Cost\n")
	sb.WriteString("─────────────────────────────────────────────────\n")
	fmt.Fprintf(&sb, "  Actual spend:       $%.4f\n", r.CompressedCostUSD)
	fmt.Fprintf(&sb, "  You saved:          $%.4f", r.CostSavedUSD)
	if r.CostSavedPct > 0 {
		fmt.Fprintf(&sb, "  (%.1f%%)", r.CostSavedPct)
	}
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "  Without proxy:      $%.4f\n", r.OriginalCostUSD)

	// ── Row 2: Balance ──
	if extra.BalanceAvailable {
		sb.WriteString("\n💳 Balance\n")
		sb.WriteString("─────────────────────────────────────────────────\n")
		if extra.IsAdmin {
			fmt.Fprintf(&sb, "  Plan:               %s (unlimited)\n", formatTier(extra.Tier))
		} else if extra.MonthlyBudgetUSD > 0 {
			totalCredits := extra.CreditsRemainingUSD + extra.CreditsUsedThisMonth
			fmt.Fprintf(&sb, "  Plan:               %s\n", formatTier(extra.Tier))
			fmt.Fprintf(&sb, "  Credits remaining:  $%.2f / $%.2f\n", extra.CreditsRemainingUSD, totalCredits)
		} else {
			fmt.Fprintf(&sb, "  Plan:               %s\n", formatTier(extra.Tier))
			fmt.Fprintf(&sb, "  Credits remaining:  $%.2f\n", extra.CreditsRemainingUSD)
		}
	}

	// ── Row 3: Detailed Stats ──
	sb.WriteString("\n📊 Details\n")
	sb.WriteString("─────────────────────────────────────────────────\n")
	fmt.Fprintf(&sb, "  Compressed requests:  %d / %d\n", r.CompressedRequests, r.TotalRequests)
	fmt.Fprintf(&sb, "  Tokens saved:         %d", r.TotalTokensSaved)
	if r.TotalSavedPct > 0 {
		fmt.Fprintf(&sb, "  (%.1f%%)", r.TotalSavedPct)
	}
	sb.WriteString("\n")
	if extra.ExpandTotal > 0 {
		fmt.Fprintf(&sb, "  Expand context:       %d calls (%d found, %d missed)\n", extra.ExpandTotal, extra.ExpandFound, extra.ExpandNotFound)
	}
	if r.ToolDiscoveryRequests > 0 {
		fmt.Fprintf(&sb, "  Tools filtered:       %d → %d tools (%d tokens saved)\n", r.OriginalToolCount, r.FilteredToolCount, r.ToolDiscoveryTokens)
	}
	if r.ExpandPenaltyTokens > 0 {
		fmt.Fprintf(&sb, "  Expand penalty:       -%d tokens (-$%.4f)\n", r.ExpandPenaltyTokens, r.ExpandPenaltyCostUSD)
	}

	sb.WriteString("═══════════════════════════════════════════════\n")

	if extra.DashboardURL != "" {
		sb.WriteString("📊 Dashboard: " + extra.DashboardURL + "\n")
	}

	return sb.String()
}

// formatTier returns a display-friendly tier name.
func formatTier(tier string) string {
	switch tier {
	case "free":
		return "Free"
	case "pro":
		return "Pro"
	case "business":
		return "Business"
	case "enterprise":
		return "Enterprise"
	default:
		return tier
	}
}

// IsSavingsRequest detects if a message is exactly the /savings command.
// Only matches the literal "/savings" command, not messages that mention it.
func IsSavingsRequest(content string) bool {
	content = strings.ToLower(strings.TrimSpace(content))
	return content == "/savings"
}

// BuildSavingsResponse creates a synthetic Anthropic API response with the savings report.
// This is returned directly to the client without hitting the API.
func BuildSavingsResponse(report string, model string) []byte {
	resp := map[string]interface{}{
		"id":            fmt.Sprintf("msg_savings_%d", time.Now().UnixNano()),
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"content": []map[string]interface{}{
			{"type": "text", "text": report},
		},
		"usage": map[string]interface{}{
			"input_tokens":  0,
			"output_tokens": len(report) / 4,
		},
	}
	data, _ := json.Marshal(resp)
	return data
}
