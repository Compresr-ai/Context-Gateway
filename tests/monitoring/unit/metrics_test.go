package unit

import (
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/stretchr/testify/assert"
)

func TestNewMetricsCollector(t *testing.T) {
	mc := monitoring.NewMetricsCollector()
	assert.NotNil(t, mc)
}

func TestRecordRequest_Success(t *testing.T) {
	mc := monitoring.NewMetricsCollector()
	mc.RecordRequest(true, 100*time.Millisecond)
	mc.RecordRequest(true, 200*time.Millisecond)
	mc.RecordRequest(false, 50*time.Millisecond)
	// MetricsCollector uses atomic counters; we just verify no panics
}

func TestRecordCompression(t *testing.T) {
	mc := monitoring.NewMetricsCollector()
	mc.RecordCompression(1000, 500, true)
	mc.RecordCompression(2000, 800, false)
}

func TestRecordCacheHitMiss(t *testing.T) {
	mc := monitoring.NewMetricsCollector()
	mc.RecordCacheHit()
	mc.RecordCacheHit()
	mc.RecordCacheMiss()
}

func TestMetricsCollector_Stop(t *testing.T) {
	mc := monitoring.NewMetricsCollector()
	mc.RecordRequest(true, 10*time.Millisecond)
	mc.Stop() // Should be a no-op but shouldn't panic
}
