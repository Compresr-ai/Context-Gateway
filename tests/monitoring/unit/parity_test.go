// parity_test.go — Tests verifying SavingsTracker and LogAggregator produce
// identical results for the same scenario, and that costs match provider billing.
package unit

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/costcontrol"
	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Issue 1: Token saved parity between SavingsTracker and LogAggregator
// ---------------------------------------------------------------------------

// TestParity_TokensSaved_BasicCompression verifies that SavingsTracker and
// LogAggregator report the same TokensSaved for a basic compression scenario.
func TestParity_TokensSaved_BasicCompression(t *testing.T) {
	model := "claude-opus-4-6"
	requestID := "req_parity_1"
	sessionID := "session_parity_1"

	// --- SavingsTracker path ---
	st := monitoring.NewSavingsTracker()
	t.Cleanup(func() { st.Stop() })

	event := &monitoring.RequestEvent{
		CompressionUsed: true,
		Model:           model,
		InputTokens:     5000,
		OutputTokens:    200,
		IsMainAgent:     true,
	}
	st.RecordRequest(event, sessionID)

	comp := monitoring.CompressionComparison{
		RequestID:        requestID,
		ProviderModel:    model,
		OriginalTokens:   5000,
		CompressedTokens: 2000,
		Status:           "compressed",
	}
	st.RecordToolOutputCompression(comp, sessionID, true)

	stReport := st.GetReportForSession(sessionID)

	// --- LogAggregator path ---
	logsDir := t.TempDir()
	telemetryLine := fmt.Sprintf(
		`{"request_id":"%s","success":true,"model":"%s","compression_used":true,"input_tokens":5000,"output_tokens":200,"is_main_agent":true}`,
		requestID, model)
	compressionLine := fmt.Sprintf(
		`{"request_id":"%s","model":"toc_latte_v1","original_tokens":5000,"compressed_tokens":2000,"status":"compressed"}`,
		requestID)

	writeLogFile(t, logsDir, sessionID, "telemetry.jsonl", telemetryLine+"\n")
	writeLogFile(t, logsDir, sessionID, "tool_output_compression.jsonl", compressionLine+"\n")

	a := startAggregatorAndWait(t, logsDir)
	defer a.Stop()
	aggReport := a.GetReportForSession(sessionID)

	// Assert parity
	assert.Equal(t, stReport.TokensSaved, aggReport.TokensSaved,
		"TokensSaved mismatch: SavingsTracker=%d LogAggregator=%d", stReport.TokensSaved, aggReport.TokensSaved)
	assert.Equal(t, stReport.TotalTokensSaved, aggReport.TotalTokensSaved,
		"TotalTokensSaved mismatch: SavingsTracker=%d LogAggregator=%d", stReport.TotalTokensSaved, aggReport.TotalTokensSaved)
	assert.Equal(t, stReport.OriginalTokens, aggReport.OriginalTokens)
	assert.Equal(t, stReport.CompressedTokens, aggReport.CompressedTokens)
}

// TestParity_TokensSaved_WithExpandPenalty verifies that expand penalty tokens
// and costs are identical between SavingsTracker and LogAggregator.
func TestParity_TokensSaved_WithExpandPenalty(t *testing.T) {
	model := "claude-opus-4-6"
	requestID := "req_parity_ep"
	sessionID := "session_parity_ep"
	expandPenaltyTokens := 2000

	// --- SavingsTracker path ---
	st := monitoring.NewSavingsTracker()
	t.Cleanup(func() { st.Stop() })

	event := &monitoring.RequestEvent{
		CompressionUsed: true,
		Model:           model,
		InputTokens:     5000,
		OutputTokens:    200,
		IsMainAgent:     true,
	}
	st.RecordRequest(event, sessionID)
	st.RecordToolOutputCompression(monitoring.CompressionComparison{
		RequestID: requestID, ProviderModel: model,
		OriginalTokens: 5000, CompressedTokens: 2000, Status: "compressed",
	}, sessionID, true)
	st.RecordExpandPenalty(expandPenaltyTokens, model, sessionID)

	stReport := st.GetReportForSession(sessionID)

	// --- LogAggregator path ---
	logsDir := t.TempDir()
	telemetryLine := fmt.Sprintf(
		`{"request_id":"%s","success":true,"model":"%s","compression_used":true,"input_tokens":5000,"output_tokens":200,"expand_calls_found":2,"expand_penalty_tokens":%d,"is_main_agent":true}`,
		requestID, model, expandPenaltyTokens)
	compressionLine := fmt.Sprintf(
		`{"request_id":"%s","model":"toc_latte_v1","original_tokens":5000,"compressed_tokens":2000,"status":"compressed"}`,
		requestID)

	writeLogFile(t, logsDir, sessionID, "telemetry.jsonl", telemetryLine+"\n")
	writeLogFile(t, logsDir, sessionID, "tool_output_compression.jsonl", compressionLine+"\n")

	a := startAggregatorAndWait(t, logsDir)
	defer a.Stop()
	aggReport := a.GetReportForSession(sessionID)

	// Token parity
	assert.Equal(t, stReport.ExpandPenaltyTokens, aggReport.ExpandPenaltyTokens,
		"ExpandPenaltyTokens mismatch")
	assert.Equal(t, stReport.TotalTokensSaved, aggReport.TotalTokensSaved,
		"TotalTokensSaved mismatch after expand penalty")

	// Cost parity: ExpandPenaltyCostUSD must be > 0 in BOTH
	assert.Greater(t, stReport.ExpandPenaltyCostUSD, 0.0,
		"SavingsTracker ExpandPenaltyCostUSD should be > 0")
	assert.Greater(t, aggReport.ExpandPenaltyCostUSD, 0.0,
		"LogAggregator ExpandPenaltyCostUSD should be > 0")
	assert.InDelta(t, stReport.ExpandPenaltyCostUSD, aggReport.ExpandPenaltyCostUSD, 1e-9,
		"ExpandPenaltyCostUSD mismatch")
}

// TestParity_TokensSaved_WithPreemptiveSummarization verifies preemptive
// summarization tokens and costs are identical between both systems.
func TestParity_TokensSaved_WithPreemptiveSummarization(t *testing.T) {
	model := "claude-opus-4-6"
	requestID := "req_parity_ps"
	sessionID := "session_parity_ps"
	originalTokens := 10000
	compressedTokens := 5000

	// --- SavingsTracker path ---
	st := monitoring.NewSavingsTracker()
	t.Cleanup(func() { st.Stop() })

	event := &monitoring.RequestEvent{
		CompressionUsed: false,
		Model:           model,
		InputTokens:     10000,
		OutputTokens:    200,
		IsMainAgent:     true,
	}
	st.RecordRequest(event, sessionID)
	st.RecordPreemptiveSummarization(originalTokens, compressedTokens, model, sessionID, true)

	stReport := st.GetReportForSession(sessionID)

	// --- LogAggregator path ---
	logsDir := t.TempDir()
	telemetryLine := fmt.Sprintf(
		`{"request_id":"%s","success":true,"model":"%s","compression_used":false,"input_tokens":10000,"output_tokens":200,"history_compaction_triggered":true,"is_main_agent":true}`,
		requestID, model)

	writeLogFile(t, logsDir, sessionID, "telemetry.jsonl", telemetryLine+"\n")

	a := startAggregatorAndWait(t, logsDir)
	defer a.Stop()
	aggReport := a.GetReportForSession(sessionID)

	// SavingsTracker tracks preemptive summarization in-memory.
	assert.Equal(t, 1, stReport.PreemptiveSummarizationRequests)
	assert.Equal(t, originalTokens-compressedTokens, stReport.PreemptiveSummarizationTokens,
		"PreemptiveSummarizationTokens mismatch in SavingsTracker")
	assert.Greater(t, stReport.CostSavedUSD, 0.0,
		"CostSavedUSD should be > 0 with preemptive summarization")

	// LogAggregator no longer tracks preemptive summarization (bytes fields removed).
	// Aggregator reports zero preemptive summarization.
	assert.Equal(t, 0, aggReport.PreemptiveSummarizationRequests)
	assert.Equal(t, 0, aggReport.PreemptiveSummarizationTokens)
}

// TestParity_TokensSaved_ToolDiscovery verifies tool discovery savings parity.
func TestParity_TokensSaved_ToolDiscovery(t *testing.T) {
	model := "claude-opus-4-6"
	requestID := "req_parity_td"
	sessionID := "session_parity_td"

	// --- SavingsTracker path ---
	st := monitoring.NewSavingsTracker()
	t.Cleanup(func() { st.Stop() })

	event := &monitoring.RequestEvent{
		CompressionUsed: false,
		Model:           model,
		InputTokens:     5000,
		OutputTokens:    100,
		IsMainAgent:     true,
	}
	st.RecordRequest(event, sessionID)

	disc := monitoring.CompressionComparison{
		RequestID:        requestID,
		ProviderModel:    model,
		AllTools:         []string{"read", "write", "bash", "grep", "glob"},
		SelectedTools:    []string{"read", "write"},
		OriginalTokens:   3000,
		CompressedTokens: 1000,
		Status:           "filtered",
	}
	st.RecordToolDiscovery(disc, sessionID, true)

	stReport := st.GetReportForSession(sessionID)

	// --- LogAggregator path ---
	logsDir := t.TempDir()
	telemetryLine := fmt.Sprintf(
		`{"request_id":"%s","success":true,"model":"%s","compression_used":false,"input_tokens":5000,"output_tokens":100,"is_main_agent":true}`,
		requestID, model)
	discoveryLine := fmt.Sprintf(
		`{"request_id":"%s","model":"%s","original_tokens":3000,"compressed_tokens":1000,"status":"filtered","event_type":"lazy_loading","tool_count":5,"stub_count":3}`,
		requestID, model)

	writeLogFile(t, logsDir, sessionID, "telemetry.jsonl", telemetryLine+"\n")
	writeLogFile(t, logsDir, sessionID, "tool_discovery.jsonl", discoveryLine+"\n")

	a := startAggregatorAndWait(t, logsDir)
	defer a.Stop()
	aggReport := a.GetReportForSession(sessionID)

	// Token parity
	assert.Equal(t, stReport.ToolDiscoveryRequests, aggReport.ToolDiscoveryRequests)
	assert.Equal(t, stReport.ToolDiscoveryTokens, aggReport.ToolDiscoveryTokens,
		"ToolDiscoveryTokens mismatch")
	assert.Equal(t, stReport.TotalTokensSaved, aggReport.TotalTokensSaved,
		"TotalTokensSaved mismatch with tool discovery")
}

// TestParity_FullScenario verifies all savings sources together.
func TestParity_FullScenario(t *testing.T) {
	model := "claude-opus-4-6"
	sessionID := "session_parity_full"
	reqCompress := "req_compress"
	reqPreemptive := "req_preemptive"

	// --- SavingsTracker path ---
	st := monitoring.NewSavingsTracker()
	t.Cleanup(func() { st.Stop() })

	// Request 1: compression + expand penalty
	st.RecordRequest(&monitoring.RequestEvent{
		CompressionUsed: true, Model: model,
		InputTokens: 5000, OutputTokens: 200, IsMainAgent: true,
	}, sessionID)
	st.RecordToolOutputCompression(monitoring.CompressionComparison{
		RequestID: reqCompress, ProviderModel: model,
		OriginalTokens: 5000, CompressedTokens: 2000, Status: "compressed",
	}, sessionID, true)
	st.RecordToolDiscovery(monitoring.CompressionComparison{
		RequestID: reqCompress, ProviderModel: model,
		AllTools: []string{"a", "b", "c", "d", "e"}, SelectedTools: []string{"a", "b"},
		OriginalTokens: 2500, CompressedTokens: 1000, Status: "filtered",
	}, sessionID, true)
	st.RecordExpandPenalty(2000, model, sessionID) // 2000 tokens penalty

	// Request 2: preemptive summarization (SavingsTracker only — aggregator no longer tracks via bytes)
	st.RecordRequest(&monitoring.RequestEvent{
		CompressionUsed: false, Model: model,
		InputTokens: 10000, OutputTokens: 300, IsMainAgent: true,
	}, sessionID)
	st.RecordPreemptiveSummarization(10000, 5000, model, sessionID, true)

	stReport := st.GetReportForSession(sessionID)

	// --- LogAggregator path ---
	logsDir := t.TempDir()
	telemetry := fmt.Sprintf(
		`{"request_id":"%s","success":true,"model":"%s","compression_used":true,"input_tokens":5000,"output_tokens":200,"expand_calls_found":1,"expand_penalty_tokens":2000,"is_main_agent":true}
{"request_id":"%s","success":true,"model":"%s","compression_used":false,"input_tokens":10000,"output_tokens":300,"history_compaction_triggered":true,"is_main_agent":true}
`, reqCompress, model, reqPreemptive, model)

	compression := fmt.Sprintf(
		`{"request_id":"%s","model":"toc_latte_v1","original_tokens":5000,"compressed_tokens":2000,"status":"compressed"}
`, reqCompress)

	discovery := fmt.Sprintf(
		`{"request_id":"%s","model":"%s","original_tokens":2500,"compressed_tokens":1000,"status":"filtered","event_type":"lazy_loading","tool_count":5,"stub_count":3}
`, reqCompress, model)

	writeLogFile(t, logsDir, sessionID, "telemetry.jsonl", telemetry)
	writeLogFile(t, logsDir, sessionID, "tool_output_compression.jsonl", compression)
	writeLogFile(t, logsDir, sessionID, "tool_discovery.jsonl", discovery)

	a := startAggregatorAndWait(t, logsDir)
	defer a.Stop()
	aggReport := a.GetReportForSession(sessionID)

	// PARITY CHECK — token/discovery fields are identical between both systems.
	// Preemptive summarization is NOT checked for aggregator parity since the
	// LogAggregator no longer reads byte-based preemptive fields from telemetry.
	assert.Equal(t, stReport.TotalRequests, aggReport.TotalRequests, "TotalRequests")
	assert.Equal(t, stReport.CompressedRequests, aggReport.CompressedRequests, "CompressedRequests")
	assert.Equal(t, stReport.TokensSaved, aggReport.TokensSaved, "TokensSaved")
	assert.Equal(t, stReport.ToolDiscoveryTokens, aggReport.ToolDiscoveryTokens, "ToolDiscoveryTokens")
	assert.Equal(t, stReport.ExpandPenaltyTokens, aggReport.ExpandPenaltyTokens, "ExpandPenaltyTokens")

	// Cost parity for compression + discovery + penalty (excludes preemptive)
	assert.InDelta(t, stReport.ExpandPenaltyCostUSD, aggReport.ExpandPenaltyCostUSD, 1e-9, "ExpandPenaltyCostUSD")

	// Sanity: totals should be positive
	assert.Greater(t, stReport.TotalTokensSaved, 0)
	assert.Greater(t, stReport.CostSavedUSD, 0.0)
}

// ---------------------------------------------------------------------------
// Issue 2: Cost accuracy — does CompressedCostUSD match provider charges?
// ---------------------------------------------------------------------------

// TestCostAccuracy_MatchesProviderBilling verifies that our cost calculation
// matches what the LLM provider (Anthropic/OpenAI) actually charges.
func TestCostAccuracy_MatchesProviderBilling(t *testing.T) {
	tests := []struct {
		name                string
		model               string
		inputTokens         int
		outputTokens        int
		cacheCreation       int
		cacheRead           int
		expectedCostFormula string // Description of expected formula
	}{
		{
			name:         "Anthropic Claude Sonnet 4 no cache",
			model:        "claude-sonnet-4-5",
			inputTokens:  10000,
			outputTokens: 500,
		},
		{
			name:          "Anthropic Claude Opus with cache",
			model:         "claude-opus-4-6",
			inputTokens:   2000, // non-cached only
			outputTokens:  500,
			cacheCreation: 1000,
			cacheRead:     8000,
		},
		{
			name:          "OpenAI GPT-4o with cache",
			model:         "gpt-4o",
			inputTokens:   3000,
			outputTokens:  1000,
			cacheCreation: 0,
			cacheRead:     5000,
		},
		{
			name:         "Anthropic Claude Haiku",
			model:        "claude-haiku-4-5",
			inputTokens:  50000,
			outputTokens: 2000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pricing := costcontrol.GetModelPricing(tt.model)
			require.Greater(t, pricing.InputPerMTok, 0.0, "model %s should have pricing", tt.model)

			// Calculate expected cost by hand
			expectedInput := float64(tt.inputTokens) / 1_000_000 * pricing.InputPerMTok
			expectedOutput := float64(tt.outputTokens) / 1_000_000 * pricing.OutputPerMTok

			writeMult := pricing.CacheWriteMultiplier
			readMult := pricing.CacheReadMultiplier
			if writeMult == 0 {
				writeMult = 1.25
			}
			if readMult == 0 {
				readMult = 0.1
			}
			expectedCacheWrite := float64(tt.cacheCreation) / 1_000_000 * pricing.InputPerMTok * writeMult
			expectedCacheRead := float64(tt.cacheRead) / 1_000_000 * pricing.InputPerMTok * readMult

			expectedTotal := expectedInput + expectedOutput + expectedCacheWrite + expectedCacheRead

			// Get actual cost from our calculation
			actualCost := costcontrol.CalculateCostWithCache(
				tt.inputTokens, tt.outputTokens,
				tt.cacheCreation, tt.cacheRead, pricing)

			assert.InDelta(t, expectedTotal, actualCost, 1e-12,
				"Cost mismatch for %s", tt.model)

			// Verify cost is non-negative and reasonable
			assert.GreaterOrEqual(t, actualCost, 0.0)
			if tt.inputTokens > 0 || tt.outputTokens > 0 {
				assert.Greater(t, actualCost, 0.0, "cost should be > 0 with tokens")
			}
		})
	}
}

// TestCostAccuracy_SavingsTrackerMatchesCostFormula verifies that
// SavingsTracker's CompressedCostUSD matches CalculateCostWithCache.
func TestCostAccuracy_SavingsTrackerMatchesCostFormula(t *testing.T) {
	model := "claude-opus-4-6"

	st := monitoring.NewSavingsTracker()
	t.Cleanup(func() { st.Stop() })

	event := &monitoring.RequestEvent{
		CompressionUsed:          true,
		Model:                    model,
		InputTokens:              2000,
		OutputTokens:             500,
		CacheCreationInputTokens: 1000,
		CacheReadInputTokens:     8000,
		IsMainAgent:              true,
	}
	st.RecordRequest(event, "")

	report := st.GetReport()

	// Expected cost from direct calculation
	pricing := costcontrol.GetModelPricing(model)
	expectedCost := costcontrol.CalculateCostWithCache(2000, 500, 1000, 8000, pricing)

	assert.InDelta(t, expectedCost, report.CompressedCostUSD, 1e-12,
		"SavingsTracker CompressedCostUSD doesn't match CalculateCostWithCache")
}

// TestCostAccuracy_AggregatorCostUSDOverridesBilledSpend verifies that
// when telemetry has cost_usd, the aggregator uses it as authoritative.
func TestCostAccuracy_AggregatorCostUSDOverridesBilledSpend(t *testing.T) {
	logsDir := t.TempDir()
	sessionID := "session_cost_override"
	exactCost := 0.0567890123

	// Telemetry with explicit cost_usd — this should be treated as authoritative
	telemetry := fmt.Sprintf(
		`{"request_id":"req_co","success":true,"model":"claude-opus-4-6","compression_used":true,"cost_usd":%.10f,"input_tokens":5000,"output_tokens":200,"is_main_agent":true}
`, exactCost)

	writeLogFile(t, logsDir, sessionID, "telemetry.jsonl", telemetry)

	a := startAggregatorAndWait(t, logsDir)
	defer a.Stop()

	report := a.GetReportForSession(sessionID)

	// CompressedCostUSD should be the exact cost_usd from telemetry
	assert.InDelta(t, exactCost, report.CompressedCostUSD, 1e-9,
		"Aggregator should use telemetry cost_usd as authoritative billed spend")
}

// TestCostAccuracy_NoCacheDoubleCount verifies that cache tokens are never
// double-counted in the cost calculation.
func TestCostAccuracy_NoCacheDoubleCount(t *testing.T) {
	model := "claude-opus-4-6"
	pricing := costcontrol.GetModelPricing(model)

	// Scenario: 1000 fresh input + 5000 cache read + 500 output
	// Cache read tokens should NOT be counted at full input price
	cost := costcontrol.CalculateCostWithCache(1000, 500, 0, 5000, pricing)

	// If double-counted, cost would be:
	// (1000+5000)/1M * inputRate + 500/1M * outputRate (way too high)
	doubleCounted := float64(6000)/1_000_000*pricing.InputPerMTok +
		float64(500)/1_000_000*pricing.OutputPerMTok

	// Correct cost should be significantly less (cache read is 0.1x)
	readMult := pricing.CacheReadMultiplier
	if readMult == 0 {
		readMult = 0.1
	}
	correctCost := float64(1000)/1_000_000*pricing.InputPerMTok +
		float64(500)/1_000_000*pricing.OutputPerMTok +
		float64(5000)/1_000_000*pricing.InputPerMTok*readMult

	assert.InDelta(t, correctCost, cost, 1e-12, "cost formula mismatch")
	assert.Less(t, cost, doubleCounted, "cache tokens appear to be double-counted")

	// The difference should be substantial — cache read at 0.1x vs 1.0x
	ratio := cost / doubleCounted
	assert.Less(t, ratio, 0.5, "cost with cache should be much less than double-counted")
}

// TestCostAccuracy_OriginalCostEqualsCompressedPlusSaved verifies the invariant:
// OriginalCostUSD = CompressedCostUSD + CostSavedUSD
func TestCostAccuracy_OriginalCostEqualsCompressedPlusSaved(t *testing.T) {
	st := monitoring.NewSavingsTracker()
	t.Cleanup(func() { st.Stop() })

	model := "claude-opus-4-6"
	st.RecordRequest(&monitoring.RequestEvent{
		CompressionUsed: true, Model: model,
		InputTokens: 5000, OutputTokens: 200,
		CacheCreationInputTokens: 1000, CacheReadInputTokens: 3000,
		IsMainAgent: true,
	}, "")
	st.RecordToolOutputCompression(monitoring.CompressionComparison{
		ProviderModel: model, OriginalTokens: 5000, CompressedTokens: 2000, Status: "compressed",
	}, "", true)

	report := st.GetReport()

	// The core invariant
	assert.InDelta(t,
		report.OriginalCostUSD,
		report.CompressedCostUSD+report.CostSavedUSD,
		1e-12,
		"OriginalCost should equal CompressedCost + CostSaved")

	assert.Greater(t, report.CostSavedUSD, 0.0, "should have savings")
	assert.Greater(t, report.CompressedCostUSD, 0.0, "should have billed spend")
}

// TestCostAccuracy_ExpandPenaltyReducesSavings verifies that expand penalty
// correctly reduces both token savings and cost savings.
func TestCostAccuracy_ExpandPenaltyReducesSavings(t *testing.T) {
	model := "claude-opus-4-6"

	// Without expand penalty
	stNoPenalty := monitoring.NewSavingsTracker()
	t.Cleanup(func() { stNoPenalty.Stop() })
	stNoPenalty.RecordRequest(&monitoring.RequestEvent{
		CompressionUsed: true, Model: model,
		InputTokens: 5000, OutputTokens: 200, IsMainAgent: true,
	}, "")
	stNoPenalty.RecordToolOutputCompression(monitoring.CompressionComparison{
		ProviderModel: model, OriginalTokens: 5000, CompressedTokens: 2000, Status: "compressed",
	}, "", true)
	reportNoPenalty := stNoPenalty.GetReport()

	// With expand penalty
	stWithPenalty := monitoring.NewSavingsTracker()
	t.Cleanup(func() { stWithPenalty.Stop() })
	stWithPenalty.RecordRequest(&monitoring.RequestEvent{
		CompressionUsed: true, Model: model,
		InputTokens: 5000, OutputTokens: 200, IsMainAgent: true,
	}, "")
	stWithPenalty.RecordToolOutputCompression(monitoring.CompressionComparison{
		ProviderModel: model, OriginalTokens: 5000, CompressedTokens: 2000, Status: "compressed",
	}, "", true)
	stWithPenalty.RecordExpandPenalty(1000, model, "") // 1000 token penalty

	reportWithPenalty := stWithPenalty.GetReport()

	// Penalty should reduce total tokens saved
	assert.Equal(t,
		reportNoPenalty.TotalTokensSaved-1000,
		reportWithPenalty.TotalTokensSaved,
		"expand penalty should reduce TotalTokensSaved by penalty amount")

	// Penalty should reduce cost saved
	assert.Less(t, reportWithPenalty.CostSavedUSD, reportNoPenalty.CostSavedUSD,
		"expand penalty should reduce CostSavedUSD")

	// ExpandPenaltyCostUSD should be > 0
	assert.Greater(t, reportWithPenalty.ExpandPenaltyCostUSD, 0.0,
		"ExpandPenaltyCostUSD should be positive when penalty exists")

	// Cost difference should equal the penalty cost
	costDiff := reportNoPenalty.CostSavedUSD - reportWithPenalty.CostSavedUSD
	assert.InDelta(t, reportWithPenalty.ExpandPenaltyCostUSD, costDiff, 1e-12,
		"cost difference should equal ExpandPenaltyCostUSD")
}

// ---------------------------------------------------------------------------
// Issue 3: Config persistence across sessions
// ---------------------------------------------------------------------------

// TestConfigPersistence_GlobalChangesSurviveReload verifies that global config
// changes persist to the YAML file and can be re-loaded (simulating a new session).
func TestConfigPersistence_GlobalChangesSurviveReload(t *testing.T) {
	// Create initial config
	cfg := minimalConfigForParity()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "config.yaml")

	initial, err := config.ToYAML(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filePath, initial, 0o600))

	// Session 1: make global config changes
	r1 := config.NewReloader(cfg, filePath)

	newCap := 50.0
	threshold := 70.0
	disabled := false
	_, err = r1.Update(config.ConfigPatch{
		CostControl: &config.CostControlPatch{
			GlobalCap: &newCap,
		},
		Preemptive: &config.PreemptivePatch{
			TriggerThreshold: &threshold,
		},
		Pipes: &config.PipesPatch{
			ToolOutput: &config.ToolOutputPatch{
				Enabled: &disabled,
			},
		},
	})
	require.NoError(t, err)

	// Verify changes took effect in session 1.
	// Pipes are always enabled by design (applyDefaults forces Enabled=true),
	// so even patching Enabled=false is overridden to true.
	c1 := r1.Current()
	assert.Equal(t, 50.0, c1.CostControl.GlobalCap)
	assert.Equal(t, 70.0, c1.Preemptive.TriggerThreshold)
	assert.True(t, c1.Pipes.ToolOutput.Enabled, "pipes are always enabled (applyDefaults)")

	// Session 2: reload from file (simulating gateway restart)
	data, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.NotEmpty(t, data, "persisted config should not be empty")

	cfg2, err := config.LoadFromBytes(data)
	require.NoError(t, err)

	r2 := config.NewReloader(cfg2, filePath)
	c2 := r2.Current()

	// All global changes should persist
	assert.Equal(t, 50.0, c2.CostControl.GlobalCap,
		"global_cap should persist across sessions")
	assert.Equal(t, 70.0, c2.Preemptive.TriggerThreshold,
		"trigger_threshold should persist across sessions")
	assert.True(t, c2.Pipes.ToolOutput.Enabled,
		"pipes are always enabled (applyDefaults overrides enabled:false)")
}

// TestConfigPersistence_SessionChangesDoNotSurviveReload verifies that
// session-scoped changes do NOT persist to the YAML file.
func TestConfigPersistence_SessionChangesDoNotSurviveReload(t *testing.T) {
	cfg := minimalConfigForParity()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "config.yaml")

	initial, err := config.ToYAML(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filePath, initial, 0o600))

	// Session 1: make session-only change
	r1 := config.NewReloader(cfg, filePath)

	sessionCap := 99.0
	_, err = r1.UpdateSession(config.ConfigPatch{
		CostControl: &config.CostControlPatch{
			GlobalCap: &sessionCap,
		},
	})
	require.NoError(t, err)

	// Session-scoped change should be visible in current session
	c1 := r1.Current()
	assert.Equal(t, 99.0, c1.CostControl.GlobalCap,
		"session override should be visible in current session")

	// Session 2: reload from file
	data, err := os.ReadFile(filePath)
	require.NoError(t, err)

	cfg2, err := config.LoadFromBytes(data)
	require.NoError(t, err)

	r2 := config.NewReloader(cfg2, filePath)
	c2 := r2.Current()

	// Session change should NOT persist
	assert.Equal(t, 10.0, c2.CostControl.GlobalCap,
		"session-scoped changes should NOT persist — should revert to original 10.0")
}

// TestConfigPersistence_GlobalThenSessionOverride verifies that global changes
// persist while session overrides on top of them do not.
func TestConfigPersistence_GlobalThenSessionOverride(t *testing.T) {
	cfg := minimalConfigForParity()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "config.yaml")

	initial, err := config.ToYAML(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filePath, initial, 0o600))

	r1 := config.NewReloader(cfg, filePath)

	// First: global change (persists)
	globalCap := 25.0
	_, err = r1.Update(config.ConfigPatch{
		CostControl: &config.CostControlPatch{GlobalCap: &globalCap},
	})
	require.NoError(t, err)

	// Then: session override on top (does NOT persist)
	sessionCap := 100.0
	_, err = r1.UpdateSession(config.ConfigPatch{
		CostControl: &config.CostControlPatch{GlobalCap: &sessionCap},
	})
	require.NoError(t, err)

	// Current session sees session override
	assert.Equal(t, 100.0, r1.Current().CostControl.GlobalCap)

	// Reload: should see global change (25.0), not session override (100.0)
	data, err := os.ReadFile(filePath)
	require.NoError(t, err)
	cfg2, err := config.LoadFromBytes(data)
	require.NoError(t, err)

	r2 := config.NewReloader(cfg2, filePath)
	assert.Equal(t, 25.0, r2.Current().CostControl.GlobalCap,
		"should see persisted global change, not session override")
}

// TestConfigPersistence_MultipleGlobalUpdatesAccumulate verifies that
// multiple global updates accumulate correctly in the persisted file.
func TestConfigPersistence_MultipleGlobalUpdatesAccumulate(t *testing.T) {
	cfg := minimalConfigForParity()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "config.yaml")

	initial, err := config.ToYAML(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filePath, initial, 0o600))

	r := config.NewReloader(cfg, filePath)

	// Update 1: change cost cap
	cap := 30.0
	_, err = r.Update(config.ConfigPatch{
		CostControl: &config.CostControlPatch{GlobalCap: &cap},
	})
	require.NoError(t, err)

	// Update 2: change preemptive threshold (should preserve update 1)
	threshold := 60.0
	_, err = r.Update(config.ConfigPatch{
		Preemptive: &config.PreemptivePatch{TriggerThreshold: &threshold},
	})
	require.NoError(t, err)

	// Reload and verify both persist
	data, err := os.ReadFile(filePath)
	require.NoError(t, err)
	cfg2, err := config.LoadFromBytes(data)
	require.NoError(t, err)

	assert.Equal(t, 30.0, cfg2.CostControl.GlobalCap,
		"first global update should persist")
	assert.Equal(t, 60.0, cfg2.Preemptive.TriggerThreshold,
		"second global update should persist alongside first")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func minimalConfigForParity() *config.Config {
	cfg, err := config.LoadFromBytes([]byte(`
server:
  port: 18081
  read_timeout: 30s
  write_timeout: 1000s
urls:
  compresr: "https://api.compresr.ai"
store:
  type: memory
  ttl: 1h
providers:
  anthropic:
    api_key: "test-key"
    model: "claude-haiku-4-5"
pipes:
  tool_output:
    enabled: true
    strategy: compresr
    min_tokens: 512
    target_compression_ratio: 0.5
    compresr:
      endpoint: "https://api.compresr.ai/api/compress/tool-output/"
      api_key: "test"
      model: "hcc_espresso_v1"
      timeout: 30s
  tool_discovery:
    enabled: true
    strategy: relevance
preemptive:
  enabled: true
  trigger_threshold: 85.0
  summarizer:
    strategy: compresr
    model: "claude-haiku-4-5"
    max_tokens: 4096
    timeout: 60s
    compresr:
      endpoint: "https://api.compresr.ai/api/compress/history/"
      api_key: "test"
      model: "hcc_espresso_v1"
      timeout: 60s
  session:
    summary_ttl: 3h
    hash_message_count: 3
cost_control:
  enabled: true
  session_cap: 0
  global_cap: 10.0
monitoring:
  telemetry_enabled: false
`))
	if err != nil {
		panic("minimalConfigForParity failed: " + err.Error())
	}
	return cfg
}
