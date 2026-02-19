// Package monitoring - metrics.go provides simple counters.
//
// DESIGN: Lightweight in-memory counters for operational metrics:
//   - requests/successes: Total and successful request counts
//   - compressions:       Number of compression operations
//   - cache_hits/misses:  Shadow context cache performance
//   - tokens:             Original, compressed, and saved token counts
//   - tools:              Tool discovery filtering stats
//
// For production, export these to Prometheus or similar.
package monitoring

import (
	"fmt"
	"sync/atomic"
	"time"
)

// MetricsCollector collects operational metrics.
type MetricsCollector struct {
	startedAt time.Time

	// Request counters
	requests     atomic.Int64
	successes    atomic.Int64
	compressions atomic.Int64
	cacheHits    atomic.Int64
	cacheMisses  atomic.Int64

	// Token savings counters
	totalOriginalTokens   atomic.Int64
	totalCompressedTokens atomic.Int64
	totalTokensSaved      atomic.Int64
	totalInputTokens      atomic.Int64 // From API response (actual billed input)
	totalOutputTokens     atomic.Int64 // From API response (actual billed output)

	// Tool discovery counters
	totalToolsFiltered atomic.Int64 // Requests where tools were filtered
	totalToolsSent     atomic.Int64 // Total tools sent after filtering
	totalToolsRemoved  atomic.Int64 // Total tools removed by filtering

	// Preemptive summarization counters
	preemptiveCacheHits atomic.Int64 // Compaction served from cache (instant)
	preemptiveJobsDone  atomic.Int64 // Background summarization jobs completed
}

// NewMetricsCollector creates a new metrics collector.
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		startedAt: time.Now(),
	}
}

// RecordRequest records a request.
func (mc *MetricsCollector) RecordRequest(success bool, _ time.Duration) {
	mc.requests.Add(1)
	if success {
		mc.successes.Add(1)
	}
}

// RecordCompression records a compression operation.
func (mc *MetricsCollector) RecordCompression(_, _ int, _ bool) {
	mc.compressions.Add(1)
}

// RecordCacheHit records a cache hit.
func (mc *MetricsCollector) RecordCacheHit() { mc.cacheHits.Add(1) }

// RecordCacheMiss records a cache miss.
func (mc *MetricsCollector) RecordCacheMiss() { mc.cacheMisses.Add(1) }

// RecordTokenSavings records token counts for a single request.
func (mc *MetricsCollector) RecordTokenSavings(original, compressed, saved int) {
	mc.totalOriginalTokens.Add(int64(original))
	mc.totalCompressedTokens.Add(int64(compressed))
	mc.totalTokensSaved.Add(int64(saved))
}

// RecordAPIUsage records actual token usage from the API response.
func (mc *MetricsCollector) RecordAPIUsage(inputTokens, outputTokens int) {
	mc.totalInputTokens.Add(int64(inputTokens))
	mc.totalOutputTokens.Add(int64(outputTokens))
}

// RecordToolsFiltered records a tool discovery filtering event.
func (mc *MetricsCollector) RecordToolsFiltered(toolsSent, toolsRemoved int) {
	mc.totalToolsFiltered.Add(1)
	mc.totalToolsSent.Add(int64(toolsSent))
	mc.totalToolsRemoved.Add(int64(toolsRemoved))
}

// RecordPreemptiveCacheHit records a preemptive summarization cache hit.
func (mc *MetricsCollector) RecordPreemptiveCacheHit() { mc.preemptiveCacheHits.Add(1) }

// RecordPreemptiveJobDone records a completed background summarization job.
func (mc *MetricsCollector) RecordPreemptiveJobDone() { mc.preemptiveJobsDone.Add(1) }

// StartedAt returns when the metrics collector was created.
func (mc *MetricsCollector) StartedAt() time.Time { return mc.startedAt }

// Stats returns current metrics as a flat map (backward compatible).
func (mc *MetricsCollector) Stats() map[string]int64 {
	return map[string]int64{
		"requests":     mc.requests.Load(),
		"successes":    mc.successes.Load(),
		"compressions": mc.compressions.Load(),
		"cache_hits":   mc.cacheHits.Load(),
		"cache_misses": mc.cacheMisses.Load(),
	}
}

// TokenStats returns token savings metrics.
func (mc *MetricsCollector) TokenStats() TokenStatsData {
	original := mc.totalOriginalTokens.Load()
	compressed := mc.totalCompressedTokens.Load()
	saved := mc.totalTokensSaved.Load()

	var savingsPercent float64
	if original > 0 {
		savingsPercent = float64(saved) / float64(original) * 100
	}

	return TokenStatsData{
		OriginalTokens:   original,
		CompressedTokens: compressed,
		TokensSaved:      saved,
		SavingsPercent:   savingsPercent,
		InputTokens:      mc.totalInputTokens.Load(),
		OutputTokens:     mc.totalOutputTokens.Load(),
	}
}

// FullStats returns all metrics in a structured format for the /stats endpoint.
func (mc *MetricsCollector) FullStats() StatsResponse {
	uptime := time.Since(mc.startedAt)
	requests := mc.requests.Load()
	successes := mc.successes.Load()
	hits := mc.cacheHits.Load()
	misses := mc.cacheMisses.Load()

	var cacheHitRate float64
	if total := hits + misses; total > 0 {
		cacheHitRate = float64(hits) / float64(total) * 100
	}

	return StatsResponse{
		Uptime:        formatDuration(uptime),
		UptimeSeconds: int64(uptime.Seconds()),
		StartedAt:     mc.startedAt.Format(time.RFC3339),
		Requests: RequestStats{
			Total:      requests,
			Successful: successes,
			Failed:     requests - successes,
		},
		Tokens: mc.TokenStats(),
		Compression: CompressionStats{
			Operations:   mc.compressions.Load(),
			CacheHits:    hits,
			CacheMisses:  misses,
			CacheHitRate: cacheHitRate,
		},
		ToolDiscovery: ToolDiscoveryStats{
			FilteredRequests: mc.totalToolsFiltered.Load(),
			ToolsSent:        mc.totalToolsSent.Load(),
			ToolsRemoved:     mc.totalToolsRemoved.Load(),
		},
		Preemptive: PreemptiveStats{
			CacheHits:     mc.preemptiveCacheHits.Load(),
			JobsCompleted: mc.preemptiveJobsDone.Load(),
		},
	}
}

// StatsResponse is the structured response for the /stats endpoint.
type StatsResponse struct {
	Uptime        string             `json:"uptime"`
	UptimeSeconds int64              `json:"uptime_seconds"`
	StartedAt     string             `json:"started_at"`
	Requests      RequestStats       `json:"requests"`
	Tokens        TokenStatsData     `json:"tokens"`
	Compression   CompressionStats   `json:"compression"`
	ToolDiscovery ToolDiscoveryStats `json:"tool_discovery"`
	Preemptive    PreemptiveStats    `json:"preemptive"`
}

// RequestStats holds request count metrics.
type RequestStats struct {
	Total      int64 `json:"total"`
	Successful int64 `json:"successful"`
	Failed     int64 `json:"failed"`
}

// TokenStatsData holds token savings metrics.
type TokenStatsData struct {
	OriginalTokens   int64   `json:"original_tokens"`
	CompressedTokens int64   `json:"compressed_tokens"`
	TokensSaved      int64   `json:"tokens_saved"`
	SavingsPercent   float64 `json:"savings_percent"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
}

// CompressionStats holds compression pipeline metrics.
type CompressionStats struct {
	Operations   int64   `json:"operations"`
	CacheHits    int64   `json:"cache_hits"`
	CacheMisses  int64   `json:"cache_misses"`
	CacheHitRate float64 `json:"cache_hit_rate"`
}

// ToolDiscoveryStats holds tool filtering metrics.
type ToolDiscoveryStats struct {
	FilteredRequests int64 `json:"filtered_requests"`
	ToolsSent        int64 `json:"tools_sent"`
	ToolsRemoved     int64 `json:"tools_removed"`
}

// PreemptiveStats holds preemptive summarization metrics.
type PreemptiveStats struct {
	CacheHits      int64 `json:"cache_hits"`
	JobsCompleted  int64 `json:"jobs_completed"`
	ActiveSessions int64 `json:"active_sessions"`
}

// formatDuration formats a duration as a human-readable string.
func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

// Stop is a no-op for compatibility.
func (mc *MetricsCollector) Stop() {}
