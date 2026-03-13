// Package monitoring - savings.go tracks compression savings in real-time.
//
// DESIGN: Follows the same pattern as costcontrol/tracker.go:
//   - Mutex-protected per-session map
//   - Background cleanup goroutine (10 min tick, 24h TTL)
//   - Reset() for new sessions
//   - Single computeReport() function for all report generation
//
// The SavingsTracker only tracks compression savings (tokens saved, cost saved).
// Actual API spend is tracked by costcontrol.Tracker — not duplicated here.
package monitoring

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/compresr/context-gateway/internal/costcontrol"
)

const savingsSessionTTL = 24 * time.Hour

// ModelUsageStats tracks usage per model for accurate cost calculation.
type ModelUsageStats struct {
	InputTokens                  int
	OutputTokens                 int
	CacheCreationTokens          int
	CacheReadTokens              int
	RequestCount                 int
	TokensSaved                  int // Tokens saved by tool output compression
	ToolDiscoveryTokens          int // Tokens saved by tool filtering
	ExpandPenaltyTokens          int // Tokens re-sent due to expand_context
	PreemptiveSummarizationSaved int // Tokens saved by preemptive summarization
}

// savingsData holds accumulated savings metrics (global or per-session).
type savingsData struct {
	TotalRequests      int
	CompressedRequests int

	// Tool output compression
	OriginalTokens   int
	CompressedTokens int

	// Tool discovery
	ToolDiscoveryRequests int
	OriginalToolCount     int
	FilteredToolCount     int
	ToolDiscoveryBytes    int
	FilteredToolBytes     int

	// Preemptive summarization
	PreemptiveSummarizationRequests int
	PreemptiveSummarizationBytes    int // Original bytes before summarization
	PreemptiveSummarizedBytes       int // Bytes after summarization

	// Expand penalty
	ExpandPenaltyTokens int

	// Per-model usage for cost calculation
	ModelUsage map[string]ModelUsageStats

	LastUpdated time.Time
}

func newSavingsData() *savingsData {
	return &savingsData{ModelUsage: make(map[string]ModelUsageStats)}
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

	// Total tokens (original) across all sources
	TotalOriginalTokens int

	// Tool Discovery (tool filtering)
	ToolDiscoveryRequests int
	OriginalToolCount     int
	FilteredToolCount     int
	ToolDiscoveryTokens   int
	ToolDiscoveryCostUSD  float64
	ToolDiscoveryPct      float64

	// Preemptive Summarization (history compaction)
	PreemptiveSummarizationRequests int
	PreemptiveSummarizationTokens   int
	PreemptiveSummarizationPct      float64

	// Combined totals
	TotalTokensSaved int
	TotalSavedPct    float64

	// Expand penalty
	ExpandPenaltyTokens  int
	ExpandPenaltyCostUSD float64

	// Cost:
	// CompressedCostUSD = actual API spend (best-effort from token usage;
	//   the handler overrides with costTracker's authoritative value).
	// CostSavedUSD = tokens we removed × effective input rate (cache-aware).
	//   First occurrence valued at full input price, subsequent at cache_read price,
	//   approximated via the observed cache mix ratio.
	// OriginalCostUSD = CompressedCostUSD + CostSavedUSD.
	OriginalCostUSD   float64
	CompressedCostUSD float64
	CostSavedUSD      float64
	CostSavedPct      float64

	AvgCompressionRatio float64
}

// SavingsTracker accumulates compression savings in memory.
type SavingsTracker struct {
	mu       sync.RWMutex
	global   *savingsData
	sessions map[string]*savingsData
	stopCh   chan struct{}
}

// NewSavingsTracker creates a new tracker with background cleanup.
func NewSavingsTracker() *SavingsTracker {
	t := &SavingsTracker{
		global:   newSavingsData(),
		sessions: make(map[string]*savingsData),
		stopCh:   make(chan struct{}),
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

// Reset zeros all accumulated metrics for a new session.
func (t *SavingsTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.global = newSavingsData()
	t.sessions = make(map[string]*savingsData)
}

// RecordRequest records API-returned token counts for cost calculation.
// sessionID may be empty for global-only tracking.
func (t *SavingsTracker) RecordRequest(event *RequestEvent, sessionID string) {
	if event == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	model := event.Model
	if model == "" {
		model = "unknown"
	}

	recordRequestInto(t.global, event, model)
	if sessionID != "" {
		recordRequestInto(t.getOrCreate(sessionID), event, model)
	}
}

// RecordToolOutputCompression records tool output compression savings.
func (t *SavingsTracker) RecordToolOutputCompression(c CompressionComparison, sessionID string) {
	bytesSaved := c.OriginalBytes - c.CompressedBytes
	if bytesSaved <= 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	model := c.ProviderModel
	if model == "" {
		model = "unknown"
	}

	recordCompressionInto(t.global, c, model)
	if sessionID != "" {
		recordCompressionInto(t.getOrCreate(sessionID), c, model)
	}
}

// RecordToolDiscovery records tool discovery (filtering) savings.
func (t *SavingsTracker) RecordToolDiscovery(c CompressionComparison, sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	model := c.ProviderModel
	if model == "" {
		model = "unknown"
	}

	recordDiscoveryInto(t.global, c, model)
	if sessionID != "" {
		recordDiscoveryInto(t.getOrCreate(sessionID), c, model)
	}
}

// RecordPreemptiveSummarization records preemptive summarization (history compaction) savings.
// originalBytes is the request body size before summarization, compressedBytes is after.
func (t *SavingsTracker) RecordPreemptiveSummarization(originalBytes, compressedBytes int, model, sessionID string) {
	bytesSaved := originalBytes - compressedBytes
	if bytesSaved <= 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if model == "" {
		model = "unknown"
	}

	recordPreemptiveInto(t.global, originalBytes, compressedBytes, model)
	if sessionID != "" {
		recordPreemptiveInto(t.getOrCreate(sessionID), originalBytes, compressedBytes, model)
	}
}

// RecordExpandPenalty records tokens re-sent due to expand_context calls.
func (t *SavingsTracker) RecordExpandPenalty(penaltyTokens int, sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.global.ExpandPenaltyTokens += penaltyTokens
	if sessionID != "" {
		sd := t.getOrCreate(sessionID)
		sd.ExpandPenaltyTokens += penaltyTokens
		sd.LastUpdated = time.Now()
	}
}

// GetReport computes the global savings summary.
func (t *SavingsTracker) GetReport() SavingsReport {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return computeReport(t.global)
}

// GetReportForSession computes savings for a specific session.
func (t *SavingsTracker) GetReportForSession(sessionID string) SavingsReport {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if sd := t.sessions[sessionID]; sd != nil {
		return computeReport(sd)
	}
	return SavingsReport{}
}

// --- TUI interface methods (satisfy tui.SavingsSource) ---

// GetSavingsSummary returns a quick summary for CLI display.
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

// SessionIDs returns all session IDs that have recorded data.
func (t *SavingsTracker) SessionIDs() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	ids := make([]string, 0, len(t.sessions))
	for id := range t.sessions {
		ids = append(ids, id)
	}
	return ids
}

// RecordRequestWithSession records a request event for both global and session tracking.
// This is an alias for RecordRequest(event, sessionID).
func (t *SavingsTracker) RecordRequestWithSession(event *RequestEvent, sessionID string) {
	t.RecordRequest(event, sessionID)
}

// GetDetailedSummary returns the full savings report (alias for GetReport).
func (t *SavingsTracker) GetDetailedSummary() SavingsReport {
	return t.GetReport()
}

// FormatReport returns a formatted savings report string.
func (t *SavingsTracker) FormatReport() string {
	return FormatUnifiedReportFromReport(t.GetReport(), UnifiedReportData{})
}

// FormatUnifiedReport returns a formatted savings report string with extra data.
func (t *SavingsTracker) FormatUnifiedReport(extra UnifiedReportData) string {
	return FormatUnifiedReportFromReport(t.GetReport(), extra)
}

// FormatUnifiedReportForSession returns a formatted savings report for a specific session.
func (t *SavingsTracker) FormatUnifiedReportForSession(sessionID string, extra UnifiedReportData) string {
	return FormatUnifiedReportFromReport(t.GetReportForSession(sessionID), extra)
}

// --- Internal helpers ---

func (t *SavingsTracker) getOrCreate(sessionID string) *savingsData {
	sd := t.sessions[sessionID]
	if sd == nil {
		sd = newSavingsData()
		t.sessions[sessionID] = sd
	}
	return sd
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
			for id, sd := range t.sessions {
				if now.Sub(sd.LastUpdated) > savingsSessionTTL {
					delete(t.sessions, id)
				}
			}
			t.mu.Unlock()
		}
	}
}

// recordRequestInto accumulates request data into a savingsData. Caller holds lock.
func recordRequestInto(sd *savingsData, event *RequestEvent, model string) {
	sd.TotalRequests++
	if event.CompressionUsed {
		sd.CompressedRequests++
		sd.OriginalTokens += event.OriginalTokens
		sd.CompressedTokens += event.CompressedTokens
	}
	stats := sd.ModelUsage[model]
	stats.InputTokens += event.InputTokens
	stats.OutputTokens += event.OutputTokens
	stats.CacheCreationTokens += event.CacheCreationInputTokens
	stats.CacheReadTokens += event.CacheReadInputTokens
	stats.RequestCount++
	sd.ModelUsage[model] = stats
	sd.LastUpdated = time.Now()
}

// recordCompressionInto accumulates tool output compression data. Caller holds lock.
func recordCompressionInto(sd *savingsData, c CompressionComparison, model string) {
	tokensSaved := (c.OriginalBytes - c.CompressedBytes) / 4
	sd.OriginalTokens += c.OriginalBytes / 4
	sd.CompressedTokens += c.CompressedBytes / 4
	stats := sd.ModelUsage[model]
	stats.TokensSaved += tokensSaved
	sd.ModelUsage[model] = stats
	sd.LastUpdated = time.Now()
}

// recordDiscoveryInto accumulates tool discovery data. Caller holds lock.
func recordDiscoveryInto(sd *savingsData, c CompressionComparison, model string) {
	sd.ToolDiscoveryRequests++
	sd.OriginalToolCount += len(c.AllTools)
	sd.FilteredToolCount += len(c.SelectedTools)
	sd.ToolDiscoveryBytes += c.OriginalBytes
	sd.FilteredToolBytes += c.CompressedBytes

	bytesSaved := c.OriginalBytes - c.CompressedBytes
	if bytesSaved > 0 {
		stats := sd.ModelUsage[model]
		stats.ToolDiscoveryTokens += bytesSaved / 4
		sd.ModelUsage[model] = stats
	}
	sd.LastUpdated = time.Now()
}

// recordPreemptiveInto accumulates preemptive summarization data. Caller holds lock.
func recordPreemptiveInto(sd *savingsData, originalBytes, compressedBytes int, model string) {
	sd.PreemptiveSummarizationRequests++
	sd.PreemptiveSummarizationBytes += originalBytes
	sd.PreemptiveSummarizedBytes += compressedBytes

	bytesSaved := originalBytes - compressedBytes
	if bytesSaved > 0 {
		stats := sd.ModelUsage[model]
		stats.PreemptiveSummarizationSaved += bytesSaved / 4
		sd.ModelUsage[model] = stats
	}
	sd.LastUpdated = time.Now()
}

// computeReport builds a SavingsReport from accumulated data.
// This is the single source of truth for savings calculation — used by both
// SavingsTracker and LogAggregator.
func computeReport(data *savingsData) SavingsReport {
	if data == nil {
		return SavingsReport{}
	}

	origTokens := data.OriginalTokens
	compTokens := data.CompressedTokens
	tokensSaved := origTokens - compTokens
	if tokensSaved < 0 {
		tokensSaved = 0
	}

	report := SavingsReport{
		TotalRequests:                   data.TotalRequests,
		CompressedRequests:              data.CompressedRequests,
		PassthroughRequests:             data.TotalRequests - data.CompressedRequests,
		OriginalTokens:                  origTokens,
		CompressedTokens:                compTokens,
		TokensSaved:                     tokensSaved,
		ToolDiscoveryRequests:           data.ToolDiscoveryRequests,
		OriginalToolCount:               data.OriginalToolCount,
		FilteredToolCount:               data.FilteredToolCount,
		PreemptiveSummarizationRequests: data.PreemptiveSummarizationRequests,
	}

	// Tool discovery percentage
	if data.ToolDiscoveryBytes > 0 {
		report.ToolDiscoveryTokens = (data.ToolDiscoveryBytes - data.FilteredToolBytes) / 4
		report.ToolDiscoveryPct = float64(data.ToolDiscoveryBytes-data.FilteredToolBytes) / float64(data.ToolDiscoveryBytes) * 100
	}

	// Preemptive summarization percentage
	if data.PreemptiveSummarizationBytes > 0 {
		report.PreemptiveSummarizationTokens = (data.PreemptiveSummarizationBytes - data.PreemptiveSummarizedBytes) / 4
		report.PreemptiveSummarizationPct = float64(data.PreemptiveSummarizationBytes-data.PreemptiveSummarizedBytes) / float64(data.PreemptiveSummarizationBytes) * 100
	}

	// Token saved percentage and compression ratio
	if origTokens > 0 {
		report.TokenSavedPct = float64(tokensSaved) / float64(origTokens) * 100
		if compTokens > 0 {
			report.AvgCompressionRatio = float64(origTokens) / float64(compTokens)
		}
	}

	// Expand penalty
	report.ExpandPenaltyTokens = data.ExpandPenaltyTokens

	// Total tokens saved = compression + tool discovery + preemptive summarization - expand penalty
	report.TotalTokensSaved = tokensSaved + report.ToolDiscoveryTokens + report.PreemptiveSummarizationTokens - data.ExpandPenaltyTokens
	if report.TotalTokensSaved < 0 {
		report.TotalTokensSaved = 0
	}

	// Total original tokens
	report.TotalOriginalTokens = origTokens + (data.ToolDiscoveryBytes / 4) + (data.PreemptiveSummarizationBytes / 4)
	if report.TotalOriginalTokens > 0 {
		report.TotalSavedPct = float64(report.TotalTokensSaved) / float64(report.TotalOriginalTokens) * 100
	}

	// Cost calculation:
	// CompressedCostUSD = actual API spend (best-effort estimate from token usage;
	//   the handler overrides with costTracker's authoritative value).
	// CostSavedUSD = tokens we removed × effective input rate.
	//   The effective rate accounts for caching: removed tokens would have followed
	//   the same caching pattern as existing input. First occurrence at full input
	//   price, subsequent at cache_read price — approximated via observed cache mix.
	for model, usage := range data.ModelUsage {
		pricing := costcontrol.GetModelPricing(model)
		effectiveRate := effectiveSavingsInputRate(pricing, usage)

		// Actual spend from recorded token usage (fallback; handler overrides with costTracker)
		report.CompressedCostUSD += costcontrol.CalculateCostWithCache(
			usage.InputTokens, usage.OutputTokens,
			usage.CacheCreationTokens, usage.CacheReadTokens, pricing)

		// Savings: tokens we removed × cache-aware effective input rate
		// Note: PreemptiveSummarization NOT included — summarization doesn't reduce cost
		modelSavings := usage.TokensSaved + usage.ToolDiscoveryTokens
		if modelSavings > 0 {
			report.CostSavedUSD += float64(modelSavings) / 1_000_000 * effectiveRate
		}

		// Expand penalty cost (at same effective rate)
		if usage.ExpandPenaltyTokens > 0 {
			report.ExpandPenaltyCostUSD += float64(usage.ExpandPenaltyTokens) / 1_000_000 * effectiveRate
		}

		// Tool discovery cost breakdown
		if usage.ToolDiscoveryTokens > 0 {
			report.ToolDiscoveryCostUSD += float64(usage.ToolDiscoveryTokens) / 1_000_000 * effectiveRate
		}
	}

	// Subtract expand penalty from savings
	report.CostSavedUSD -= report.ExpandPenaltyCostUSD
	if report.CostSavedUSD < 0 {
		report.CostSavedUSD = 0
	}

	report.OriginalCostUSD = report.CompressedCostUSD + report.CostSavedUSD
	if report.OriginalCostUSD > 0 {
		report.CostSavedPct = report.CostSavedUSD / report.OriginalCostUSD * 100
	}

	return report
}

// effectiveSavingsInputRate returns a cache-aware effective input token rate.
//
// Tokens removed by compression would have followed the same caching pattern
// as the rest of the input. On the first request they'd cost full input price;
// on subsequent requests they'd be cache reads at a fraction of that price.
// We approximate using the observed token mix:
//
//	effective_rate = input_price × weighted_cache_mix
//
// This correctly values savings at ~10% of input price for heavily-cached
// Anthropic conversations, instead of the naive full-price calculation.
func effectiveSavingsInputRate(pricing costcontrol.ModelPricing, usage ModelUsageStats) float64 {
	totalInputLike := usage.InputTokens + usage.CacheCreationTokens + usage.CacheReadTokens
	if totalInputLike <= 0 {
		return pricing.InputPerMTok
	}

	writeMult := pricing.CacheWriteMultiplier
	readMult := pricing.CacheReadMultiplier
	if writeMult == 0 {
		writeMult = 1.25
	}
	if readMult == 0 {
		readMult = 0.1
	}

	totalWeighted := float64(usage.InputTokens) +
		float64(usage.CacheCreationTokens)*writeMult +
		float64(usage.CacheReadTokens)*readMult

	return pricing.InputPerMTok * (totalWeighted / float64(totalInputLike))
}

// =============================================================================
// Formatting — standalone functions used by handlers
// =============================================================================

// UnifiedReportData provides extra context for the /savings report.
type UnifiedReportData struct {
	TotalSessionCost float64
	SessionCount     int
	ExpandTotal      int
	ExpandFound      int
	ExpandNotFound   int
	// Account balance (from compresr API)
	BalanceAvailable     bool
	Tier                 string
	CreditsRemainingUSD  float64
	CreditsUsedThisMonth float64
	MonthlyBudgetUSD     float64
	IsAdmin              bool
	DashboardURL         string
}

// FormatUnifiedReportFromReport formats a SavingsReport with extra unified data.
func FormatUnifiedReportFromReport(r SavingsReport, extra UnifiedReportData) string {
	var sb strings.Builder
	sb.WriteString("💰 Savings Report\n")
	sb.WriteString("═══════════════════════════════════════════════\n\n")

	// 2-column comparison table
	sb.WriteString("                  Original        After Compression\n")
	sb.WriteString("─────────────────────────────────────────────────\n")
	fmt.Fprintf(&sb, "  Tokens          %-16d%d\n", r.TotalOriginalTokens, r.CompressedTokens)
	fmt.Fprintf(&sb, "  USD             $%-15.4f$%.4f\n", r.OriginalCostUSD, r.CompressedCostUSD)
	sb.WriteString("─────────────────────────────────────────────────\n")

	// Savings summary
	// if r.TotalTokensSaved > 0 {
	// 	fmt.Fprintf(&sb, "  Saved           %d tokens (%.1f%%)  ·  $%.4f (%.1f%%)\n",
	// 		r.TotalTokensSaved, r.TotalSavedPct, r.CostSavedUSD, r.CostSavedPct)
	// }

	// Expand penalty
	if r.ExpandPenaltyTokens > 0 {
		fmt.Fprintf(&sb, "  Expand penalty  -%d tokens (-$%.4f)\n", r.ExpandPenaltyTokens, r.ExpandPenaltyCostUSD)
	}

	sb.WriteString("\n")

	// Account balance
	if extra.BalanceAvailable {
		if extra.IsAdmin {
			fmt.Fprintf(&sb, "  Plan: %s (unlimited)\n", formatTier(extra.Tier))
		} else if extra.MonthlyBudgetUSD > 0 {
			totalCredits := extra.CreditsRemainingUSD + extra.CreditsUsedThisMonth
			fmt.Fprintf(&sb, "  Plan: %s  ·  $%.2f / $%.2f remaining\n", formatTier(extra.Tier), extra.CreditsRemainingUSD, totalCredits)
		} else {
			fmt.Fprintf(&sb, "  Plan: %s  ·  $%.2f remaining\n", formatTier(extra.Tier), extra.CreditsRemainingUSD)
		}
	}

	// Requests
	fmt.Fprintf(&sb, "  Requests: %d compressed / %d total\n", r.CompressedRequests, r.TotalRequests)

	sb.WriteString("═══════════════════════════════════════════════\n")

	if extra.DashboardURL != "" {
		sb.WriteString("📊 Dashboard: " + extra.DashboardURL + "\n")
	}

	return sb.String()
}

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

// =============================================================================
// /savings command helpers
// =============================================================================

// IsSavingsRequest detects if a message is exactly the /savings command.
func IsSavingsRequest(content string) bool {
	return strings.ToLower(strings.TrimSpace(content)) == "/savings"
}

// BuildSavingsResponse creates a synthetic Anthropic API response with the savings report.
// When streaming is true, returns Anthropic SSE format.
func BuildSavingsResponse(report string, model string, streaming bool) []byte {
	msgID := fmt.Sprintf("msg_savings_%d", time.Now().UnixNano())
	outputTokens := len(report) / 4

	if !streaming {
		resp := map[string]interface{}{
			"id":            msgID,
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
				"output_tokens": outputTokens,
			},
		}
		data, _ := json.Marshal(resp)
		return data
	}

	var b strings.Builder

	msgStart, _ := json.Marshal(map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id": msgID, "type": "message", "role": "assistant", "model": model,
			"stop_reason": nil, "stop_sequence": nil, "content": []interface{}{},
			"usage": map[string]interface{}{"input_tokens": 0, "output_tokens": 0},
		},
	})
	b.WriteString("event: message_start\ndata: ")
	b.Write(msgStart)
	b.WriteString("\n\n")

	blockStart, _ := json.Marshal(map[string]interface{}{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]interface{}{"type": "text", "text": ""},
	})
	b.WriteString("event: content_block_start\ndata: ")
	b.Write(blockStart)
	b.WriteString("\n\n")

	delta, _ := json.Marshal(map[string]interface{}{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]interface{}{"type": "text_delta", "text": report},
	})
	b.WriteString("event: content_block_delta\ndata: ")
	b.Write(delta)
	b.WriteString("\n\n")

	blockStop, _ := json.Marshal(map[string]interface{}{
		"type": "content_block_stop", "index": 0,
	})
	b.WriteString("event: content_block_stop\ndata: ")
	b.Write(blockStop)
	b.WriteString("\n\n")

	msgDelta, _ := json.Marshal(map[string]interface{}{
		"type":  "message_delta",
		"delta": map[string]interface{}{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]interface{}{"output_tokens": outputTokens},
	})
	b.WriteString("event: message_delta\ndata: ")
	b.Write(msgDelta)
	b.WriteString("\n\n")

	msgStop, _ := json.Marshal(map[string]interface{}{"type": "message_stop"})
	b.WriteString("event: message_stop\ndata: ")
	b.Write(msgStop)
	b.WriteString("\n\n")

	return []byte(b.String())
}
