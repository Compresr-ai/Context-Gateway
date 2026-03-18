package unit

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/costcontrol"
	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeLogFile creates a session log file with the given content.
func writeLogFile(t *testing.T, logsDir, sessionID, filename, content string) {
	t.Helper()
	sessionDir := filepath.Join(logsDir, sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, filename), []byte(content), 0o644))
}

// startAggregatorAndWait creates, starts, and waits for one refresh cycle.
func startAggregatorAndWait(t *testing.T, logsDir string) *monitoring.LogAggregator {
	t.Helper()
	a := monitoring.NewLogAggregator(logsDir, 50*time.Millisecond)
	a.Start()
	// Wait for initial refresh + one tick to ensure parsing is done
	time.Sleep(150 * time.Millisecond)
	return a
}

func TestLogAggregator_UsesTelemetryCostUSDForCompressedSpend(t *testing.T) {
	logsDir := t.TempDir()
	sessionID := "session_agg_1"

	writeLogFile(t, logsDir, sessionID, "telemetry.jsonl", `{"request_id":"req_1","success":true,"model":"claude-opus-4-6","compression_used":true,"cost_usd":0.123456789,"input_tokens":1000,"output_tokens":10,"is_main_agent":true}
`)
	writeLogFile(t, logsDir, sessionID, "tool_output_compression.jsonl", `{"request_id":"req_1","model":"toc_latte_v1","original_tokens":100,"compressed_tokens":50,"status":"compressed"}
`)

	a := startAggregatorAndWait(t, logsDir)
	defer a.Stop()

	report := a.GetReportForSession(sessionID)
	assert.InDelta(t, 0.123456789, report.CompressedCostUSD, 1e-12)
	assert.Greater(t, report.CostSavedUSD, 0.0)
}

func TestLogAggregator_FallbackCostFromUsageWhenCostUSDMissing(t *testing.T) {
	logsDir := t.TempDir()
	sessionID := "session_agg_2"

	writeLogFile(t, logsDir, sessionID, "telemetry.jsonl", `{"request_id":"req_2","success":true,"model":"claude-sonnet-4-5","compression_used":false,"input_tokens":2000,"output_tokens":500,"cache_creation_input_tokens":1000,"cache_read_input_tokens":500,"is_main_agent":true}
`)

	a := startAggregatorAndWait(t, logsDir)
	defer a.Stop()

	report := a.GetReportForSession(sessionID)

	pricing := costcontrol.GetModelPricing("claude-sonnet-4-5")
	expected := costcontrol.CalculateCostWithCache(2000, 500, 1000, 500, pricing)
	assert.InDelta(t, expected, report.CompressedCostUSD, 1e-12)
	assert.Equal(t, 0.0, report.CostSavedUSD)
}

func TestLogAggregator_CacheAwareSavingsUsesEffectiveInputRate(t *testing.T) {
	logsDir := t.TempDir()
	sessionID := "session_agg_3"

	writeLogFile(t, logsDir, sessionID, "telemetry.jsonl", `{"request_id":"req_3","success":true,"model":"gpt-5.1-codex","compression_used":true,"input_tokens":0,"output_tokens":100,"cache_read_input_tokens":1000,"is_main_agent":true}
`)
	writeLogFile(t, logsDir, sessionID, "tool_output_compression.jsonl", `{"request_id":"req_3","model":"toc_latte_v1","original_tokens":100,"compressed_tokens":0,"status":"compressed"}
`)

	a := startAggregatorAndWait(t, logsDir)
	defer a.Stop()

	report := a.GetReportForSession(sessionID)

	pricing := costcontrol.GetModelPricing("gpt-5.1-codex")
	expectedEffective := pricing.InputPerMTok * pricing.CacheReadMultiplier
	expectedSavings := float64(100) / 1_000_000 * expectedEffective

	assert.InDelta(t, expectedSavings, report.CostSavedUSD, 1e-12)
}

func TestLogAggregator_ExpandPenaltyTokensFromTelemetry(t *testing.T) {
	logsDir := t.TempDir()
	sessionID := "session_expand_penalty_tokens"

	// Telemetry with expand_penalty_tokens set (new format)
	writeLogFile(t, logsDir, sessionID, "telemetry.jsonl", `{"request_id":"req_ept","success":true,"model":"claude-opus-4-6","compression_used":true,"input_tokens":5000,"output_tokens":100,"expand_calls_found":2,"expand_penalty_tokens":2000,"is_main_agent":true}
`)
	// Compression log with savings
	writeLogFile(t, logsDir, sessionID, "tool_output_compression.jsonl", `{"request_id":"req_ept","model":"toc_latte_v1","original_tokens":4000,"compressed_tokens":0,"status":"compressed"}
`)

	a := startAggregatorAndWait(t, logsDir)
	defer a.Stop()

	report := a.GetReportForSession(sessionID)
	// expand_penalty_tokens=2000 directly (no conversion needed)
	assert.Equal(t, 2000, report.ExpandPenaltyTokens)
	assert.Greater(t, report.ExpandPenaltyCostUSD, 0.0)
	// Token savings 4000 minus penalty 2000 = net 2000
	assert.Equal(t, 2000, report.TotalTokensSaved)
}

func TestLogAggregator_PreemptiveSummarizationFromTelemetry(t *testing.T) {
	logsDir := t.TempDir()
	sessionID := "session_preemptive"

	// Preemptive bytes fields have been removed — aggregator no longer tracks
	// preemptive summarization from telemetry. history_compaction_triggered is
	// still recorded for counting purposes.
	writeLogFile(t, logsDir, sessionID, "telemetry.jsonl", `{"request_id":"req_ps","success":true,"model":"claude-opus-4-6","compression_used":false,"input_tokens":10000,"output_tokens":200,"history_compaction_triggered":true,"is_main_agent":true}
`)

	a := startAggregatorAndWait(t, logsDir)
	defer a.Stop()

	report := a.GetReportForSession(sessionID)
	// Aggregator no longer extracts preemptive tokens from telemetry.
	assert.Equal(t, 0, report.PreemptiveSummarizationRequests)
	assert.Equal(t, 0, report.PreemptiveSummarizationTokens)
	assert.Equal(t, 0, report.TotalTokensSaved)
}

func TestLogAggregator_EmptyRequestIDFilteredInCompression(t *testing.T) {
	logsDir := t.TempDir()
	sessionID := "session_empty_rid"

	// One valid telemetry entry, one compression entry with empty request_id
	writeLogFile(t, logsDir, sessionID, "telemetry.jsonl", `{"request_id":"req_valid","success":true,"model":"claude-opus-4-6","compression_used":true,"input_tokens":1000,"output_tokens":10,"is_main_agent":true}
`)
	writeLogFile(t, logsDir, sessionID, "tool_output_compression.jsonl", `{"request_id":"","model":"toc_latte_v1","original_tokens":2000,"compressed_tokens":0,"status":"compressed"}
{"request_id":"req_valid","model":"toc_latte_v1","original_tokens":500,"compressed_tokens":0,"status":"compressed"}
`)

	a := startAggregatorAndWait(t, logsDir)
	defer a.Stop()

	report := a.GetReportForSession(sessionID)
	// Only req_valid compression counted: 500 original - 0 compressed = 500 tokens saved
	// The empty request_id entry should be filtered out
	assert.Equal(t, 500, report.TokensSaved)
}
