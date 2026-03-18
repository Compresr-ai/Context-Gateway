// Package monitoring - metrics.go provides simple in-memory counters.
package monitoring

import (
	"sync/atomic"
	"time"
)

// MetricsCollector collects operational metrics.
type MetricsCollector struct {
	requests     atomic.Int64
	successes    atomic.Int64
	userTurns    atomic.Int64 // New user prompts (human typed, not tool loops/subagents)
	compressions atomic.Int64
	cacheHits    atomic.Int64
	cacheMisses  atomic.Int64
}

// NewMetricsCollector creates a new metrics collector.
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{}
}

// RecordRequest records a request.
func (mc *MetricsCollector) RecordRequest(success bool, _ time.Duration) {
	mc.requests.Add(1)
	if success {
		mc.successes.Add(1)
	}
}

// RecordUserTurn records a new human-initiated prompt (not a tool loop or subagent).
func (mc *MetricsCollector) RecordUserTurn() { mc.userTurns.Add(1) }

// RecordCompression records a compression operation.
func (mc *MetricsCollector) RecordCompression(_, _ int, _ bool) {
	mc.compressions.Add(1)
}

// RecordCacheHit records a cache hit.
func (mc *MetricsCollector) RecordCacheHit() { mc.cacheHits.Add(1) }

// RecordCacheMiss records a cache miss.
func (mc *MetricsCollector) RecordCacheMiss() { mc.cacheMisses.Add(1) }

// Stats returns current metrics.
func (mc *MetricsCollector) Stats() map[string]int64 {
	return map[string]int64{
		"requests":     mc.requests.Load(),
		"successes":    mc.successes.Load(),
		"user_turns":   mc.userTurns.Load(),
		"compressions": mc.compressions.Load(),
		"cache_hits":   mc.cacheHits.Load(),
		"cache_misses": mc.cacheMisses.Load(),
	}
}

// Reset zeros out all counters for a fresh session.
func (mc *MetricsCollector) Reset() {
	mc.requests.Store(0)
	mc.successes.Store(0)
	mc.userTurns.Store(0)
	mc.compressions.Store(0)
	mc.cacheHits.Store(0)
	mc.cacheMisses.Store(0)
}

// Stop is a no-op for compatibility.
func (mc *MetricsCollector) Stop() {}
