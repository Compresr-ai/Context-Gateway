package unit

import (
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// BASIC METRICS (backward compatibility)
// =============================================================================

func TestMetrics_Stats_Backward_Compatible(t *testing.T) {
	mc := monitoring.NewMetricsCollector()
	stats := mc.Stats()

	assert.Equal(t, int64(0), stats["requests"])
	assert.Equal(t, int64(0), stats["successes"])
	assert.Equal(t, int64(0), stats["compressions"])
	assert.Equal(t, int64(0), stats["cache_hits"])
	assert.Equal(t, int64(0), stats["cache_misses"])
}

func TestMetrics_RecordRequest(t *testing.T) {
	mc := monitoring.NewMetricsCollector()

	mc.RecordRequest(true, time.Second)
	mc.RecordRequest(true, time.Second)
	mc.RecordRequest(false, time.Second)

	stats := mc.Stats()
	assert.Equal(t, int64(3), stats["requests"])
	assert.Equal(t, int64(2), stats["successes"])
}

func TestMetrics_RecordCompression(t *testing.T) {
	mc := monitoring.NewMetricsCollector()

	mc.RecordCompression(1000, 500, true)
	mc.RecordCompression(2000, 800, true)

	stats := mc.Stats()
	assert.Equal(t, int64(2), stats["compressions"])
}

func TestMetrics_RecordCacheHitMiss(t *testing.T) {
	mc := monitoring.NewMetricsCollector()

	mc.RecordCacheHit()
	mc.RecordCacheHit()
	mc.RecordCacheMiss()

	stats := mc.Stats()
	assert.Equal(t, int64(2), stats["cache_hits"])
	assert.Equal(t, int64(1), stats["cache_misses"])
}

// =============================================================================
// TOKEN SAVINGS
// =============================================================================

func TestMetrics_RecordTokenSavings(t *testing.T) {
	mc := monitoring.NewMetricsCollector()

	mc.RecordTokenSavings(1000, 700, 300)
	mc.RecordTokenSavings(2000, 1500, 500)

	ts := mc.TokenStats()
	assert.Equal(t, int64(3000), ts.OriginalTokens)
	assert.Equal(t, int64(2200), ts.CompressedTokens)
	assert.Equal(t, int64(800), ts.TokensSaved)
}

func TestMetrics_TokenStats_SavingsPercent(t *testing.T) {
	mc := monitoring.NewMetricsCollector()

	mc.RecordTokenSavings(1000, 600, 400)

	ts := mc.TokenStats()
	assert.InDelta(t, 40.0, ts.SavingsPercent, 0.01)
}

func TestMetrics_TokenStats_ZeroOriginal(t *testing.T) {
	mc := monitoring.NewMetricsCollector()
	ts := mc.TokenStats()

	assert.Equal(t, int64(0), ts.OriginalTokens)
	assert.Equal(t, float64(0), ts.SavingsPercent)
}

func TestMetrics_RecordAPIUsage(t *testing.T) {
	mc := monitoring.NewMetricsCollector()

	mc.RecordAPIUsage(500, 200)
	mc.RecordAPIUsage(800, 300)

	ts := mc.TokenStats()
	assert.Equal(t, int64(1300), ts.InputTokens)
	assert.Equal(t, int64(500), ts.OutputTokens)
}

// =============================================================================
// TOOL DISCOVERY
// =============================================================================

func TestMetrics_RecordToolsFiltered(t *testing.T) {
	mc := monitoring.NewMetricsCollector()

	mc.RecordToolsFiltered(5, 3)  // Kept 5, removed 3
	mc.RecordToolsFiltered(10, 7) // Kept 10, removed 7

	stats := mc.FullStats()
	assert.Equal(t, int64(2), stats.ToolDiscovery.FilteredRequests)
	assert.Equal(t, int64(15), stats.ToolDiscovery.ToolsSent)
	assert.Equal(t, int64(10), stats.ToolDiscovery.ToolsRemoved)
}

// =============================================================================
// PREEMPTIVE SUMMARIZATION
// =============================================================================

func TestMetrics_PreemptiveCacheHit(t *testing.T) {
	mc := monitoring.NewMetricsCollector()

	mc.RecordPreemptiveCacheHit()
	mc.RecordPreemptiveCacheHit()
	mc.RecordPreemptiveJobDone()

	stats := mc.FullStats()
	assert.Equal(t, int64(2), stats.Preemptive.CacheHits)
	assert.Equal(t, int64(1), stats.Preemptive.JobsCompleted)
}

// =============================================================================
// FULL STATS RESPONSE
// =============================================================================

func TestMetrics_FullStats_Structure(t *testing.T) {
	mc := monitoring.NewMetricsCollector()

	// Simulate some activity
	mc.RecordRequest(true, time.Second)
	mc.RecordRequest(true, time.Second)
	mc.RecordRequest(false, time.Second)
	mc.RecordTokenSavings(5000, 3500, 1500)
	mc.RecordAPIUsage(4000, 1000)
	mc.RecordCompression(0, 0, true)
	mc.RecordCacheHit()
	mc.RecordCacheMiss()
	mc.RecordCacheMiss()
	mc.RecordToolsFiltered(8, 4)
	mc.RecordPreemptiveCacheHit()

	stats := mc.FullStats()

	// Requests
	assert.Equal(t, int64(3), stats.Requests.Total)
	assert.Equal(t, int64(2), stats.Requests.Successful)
	assert.Equal(t, int64(1), stats.Requests.Failed)

	// Tokens
	assert.Equal(t, int64(5000), stats.Tokens.OriginalTokens)
	assert.Equal(t, int64(3500), stats.Tokens.CompressedTokens)
	assert.Equal(t, int64(1500), stats.Tokens.TokensSaved)
	assert.InDelta(t, 30.0, stats.Tokens.SavingsPercent, 0.01)
	assert.Equal(t, int64(4000), stats.Tokens.InputTokens)
	assert.Equal(t, int64(1000), stats.Tokens.OutputTokens)

	// Compression
	assert.Equal(t, int64(1), stats.Compression.Operations)
	assert.Equal(t, int64(1), stats.Compression.CacheHits)
	assert.Equal(t, int64(2), stats.Compression.CacheMisses)
	assert.InDelta(t, 33.33, stats.Compression.CacheHitRate, 0.01)

	// Tool Discovery
	assert.Equal(t, int64(1), stats.ToolDiscovery.FilteredRequests)
	assert.Equal(t, int64(8), stats.ToolDiscovery.ToolsSent)
	assert.Equal(t, int64(4), stats.ToolDiscovery.ToolsRemoved)

	// Preemptive
	assert.Equal(t, int64(1), stats.Preemptive.CacheHits)
}

func TestMetrics_FullStats_Uptime(t *testing.T) {
	mc := monitoring.NewMetricsCollector()

	stats := mc.FullStats()

	require.NotEmpty(t, stats.Uptime)
	require.NotEmpty(t, stats.StartedAt)
	assert.GreaterOrEqual(t, stats.UptimeSeconds, int64(0))
}

func TestMetrics_FullStats_CacheHitRate_NoOps(t *testing.T) {
	mc := monitoring.NewMetricsCollector()

	stats := mc.FullStats()
	assert.Equal(t, float64(0), stats.Compression.CacheHitRate)
}

func TestMetrics_StartedAt(t *testing.T) {
	before := time.Now()
	mc := monitoring.NewMetricsCollector()
	after := time.Now()

	started := mc.StartedAt()
	assert.True(t, !started.Before(before))
	assert.True(t, !started.After(after))
}

// =============================================================================
// CONCURRENCY SAFETY
// =============================================================================

func TestMetrics_ConcurrentAccess(t *testing.T) {
	mc := monitoring.NewMetricsCollector()

	done := make(chan struct{})
	for i := 0; i < 100; i++ {
		go func() {
			mc.RecordRequest(true, time.Millisecond)
			mc.RecordTokenSavings(100, 70, 30)
			mc.RecordAPIUsage(50, 20)
			mc.RecordCompression(100, 50, true)
			mc.RecordCacheHit()
			mc.RecordToolsFiltered(5, 3)
			mc.RecordPreemptiveCacheHit()
			_ = mc.Stats()
			_ = mc.TokenStats()
			_ = mc.FullStats()
			done <- struct{}{}
		}()
	}

	for i := 0; i < 100; i++ {
		<-done
	}

	stats := mc.Stats()
	assert.Equal(t, int64(100), stats["requests"])

	ts := mc.TokenStats()
	assert.Equal(t, int64(10000), ts.OriginalTokens)
	assert.Equal(t, int64(3000), ts.TokensSaved)
}

// =============================================================================
// FORMAT DURATION
// =============================================================================

func TestMetrics_FullStats_UptimeFormat(t *testing.T) {
	mc := monitoring.NewMetricsCollector()

	// Just verify it doesn't panic and returns a string
	stats := mc.FullStats()
	require.NotEmpty(t, stats.Uptime)
	// Uptime should be "0m" or similar for a brand new collector
	assert.Contains(t, stats.Uptime, "m")
}
