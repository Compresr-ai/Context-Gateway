package unit

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// SavingsTracker - basic operations
// ---------------------------------------------------------------------------

func newTracker(t *testing.T) *monitoring.SavingsTracker {
	t.Helper()
	st := monitoring.NewSavingsTracker()
	t.Cleanup(func() { st.Stop() })
	return st
}

func compressedEvent(orig, comp int) *monitoring.RequestEvent {
	return &monitoring.RequestEvent{
		CompressionUsed:  true,
		PipeType:         monitoring.PipeToolOutput,
		OriginalTokens:   orig,
		CompressedTokens: comp,
		Model:            "claude-sonnet-4-20250514",
	}
}

func TestNewSavingsTracker(t *testing.T) {
	st := newTracker(t)
	assert.NotNil(t, st)

	report := st.GetReport()
	assert.Equal(t, 0, report.TotalRequests)
	assert.Equal(t, 0, report.CompressedRequests)
}

func TestRecordRequest_NilEvent(t *testing.T) {
	st := newTracker(t)
	st.RecordRequest(nil, "") // should not panic
	assert.Equal(t, 0, st.GetReport().TotalRequests)
}

func TestRecordRequest_Global(t *testing.T) {
	st := newTracker(t)
	st.RecordRequest(compressedEvent(1000, 600), "")

	report := st.GetReport()
	assert.Equal(t, 1, report.TotalRequests)
	assert.Equal(t, 1, report.CompressedRequests)
	assert.Equal(t, 1000, report.OriginalTokens)
	assert.Equal(t, 600, report.CompressedTokens)
	assert.Equal(t, 400, report.TokensSaved)
}

func TestRecordRequest_NotCompressed(t *testing.T) {
	st := newTracker(t)

	event := &monitoring.RequestEvent{
		CompressionUsed: false,
		Model:           "claude-sonnet-4-20250514",
	}
	st.RecordRequest(event, "")

	report := st.GetReport()
	assert.Equal(t, 1, report.TotalRequests)
	assert.Equal(t, 0, report.CompressedRequests)
	assert.Equal(t, 1, report.PassthroughRequests)
}

func TestRecordRequestWithSession(t *testing.T) {
	st := newTracker(t)
	st.RecordRequestWithSession(compressedEvent(2000, 1000), "session-1")

	// Global report
	report := st.GetReport()
	assert.Equal(t, 1, report.TotalRequests)
	assert.Equal(t, 1, report.CompressedRequests)

	// Session report
	sessionReport := st.GetReportForSession("session-1")
	assert.Equal(t, 1, sessionReport.TotalRequests)
	assert.Equal(t, 1, sessionReport.CompressedRequests)
	assert.Equal(t, 2000, sessionReport.OriginalTokens)
	assert.Equal(t, 1000, sessionReport.CompressedTokens)
}

func TestGetReportForSession_NotFound(t *testing.T) {
	st := newTracker(t)
	report := st.GetReportForSession("nonexistent")
	assert.Equal(t, 0, report.TotalRequests)
}

func TestSessionIDs(t *testing.T) {
	st := newTracker(t)

	event := &monitoring.RequestEvent{Model: "claude-sonnet-4-20250514"}
	st.RecordRequestWithSession(event, "sess-a")
	st.RecordRequestWithSession(event, "sess-b")

	ids := st.SessionIDs()
	assert.Len(t, ids, 2)
	assert.Contains(t, ids, "sess-a")
	assert.Contains(t, ids, "sess-b")
}

func TestRecordToolDiscovery(t *testing.T) {
	st := newTracker(t)

	comparison := monitoring.CompressionComparison{
		AllTools:        []string{"tool1", "tool2", "tool3"},
		SelectedTools:   []string{"tool1"},
		OriginalBytes:   3000,
		CompressedBytes: 1000,
	}
	st.RecordToolDiscovery(comparison, "")

	report := st.GetReport()
	assert.Equal(t, 1, report.ToolDiscoveryRequests)
}

func TestRecordExpandPenalty(t *testing.T) {
	st := newTracker(t)
	st.RecordExpandPenalty(500, "session-1")

	report := st.GetReport()
	assert.Equal(t, 500, report.ExpandPenaltyTokens)

	sessionReport := st.GetReportForSession("session-1")
	assert.Equal(t, 500, sessionReport.ExpandPenaltyTokens)
}

func TestGetSavingsSummary(t *testing.T) {
	st := newTracker(t)
	st.RecordRequest(compressedEvent(1000, 400), "")

	tokensSaved, _, compressed, total := st.GetSavingsSummary()
	assert.Equal(t, 600, tokensSaved)
	assert.Equal(t, 1, compressed)
	assert.Equal(t, 1, total)
}

func TestGetCostBreakdown(t *testing.T) {
	st := newTracker(t)

	event := &monitoring.RequestEvent{
		CompressionUsed:  true,
		PipeType:         monitoring.PipeToolOutput,
		OriginalTokens:   100000,
		CompressedTokens: 50000,
		InputTokens:      100000,
		OutputTokens:     5000,
		Model:            "claude-sonnet-4-20250514",
	}
	st.RecordRequest(event, "")

	origCost, compCost, savedCost := st.GetCostBreakdown()
	// At minimum, costs should be non-negative
	assert.GreaterOrEqual(t, origCost, 0.0)
	assert.GreaterOrEqual(t, compCost, 0.0)
	assert.GreaterOrEqual(t, savedCost, 0.0)
}

func TestGetCompressionStats(t *testing.T) {
	st := newTracker(t)
	st.RecordRequest(compressedEvent(1000, 500), "")

	compressed, total, _, _, _ := st.GetCompressionStats()
	assert.Equal(t, 1, compressed)
	assert.Equal(t, 1, total)
}

func TestGetDetailedSummary(t *testing.T) {
	st := newTracker(t)
	st.RecordRequest(compressedEvent(1000, 500), "")

	details := st.GetDetailedSummary()
	assert.Equal(t, 1, details.TotalRequests)
	assert.Equal(t, 1, details.CompressedRequests)
	assert.Equal(t, 500, details.TokensSaved)
}

func TestFormatReport(t *testing.T) {
	st := newTracker(t)
	st.RecordRequest(compressedEvent(10000, 5000), "")

	report := st.FormatReport()
	assert.Contains(t, report, "Savings Report")
	assert.Contains(t, report, "10000")
}

func TestMultipleRequests_Accumulation(t *testing.T) {
	st := newTracker(t)

	for i := 0; i < 5; i++ {
		st.RecordRequest(compressedEvent(1000, 600), "")
	}

	report := st.GetReport()
	assert.Equal(t, 5, report.TotalRequests)
	assert.Equal(t, 5, report.CompressedRequests)
	assert.Equal(t, 5000, report.OriginalTokens)
	assert.Equal(t, 3000, report.CompressedTokens)
	assert.Equal(t, 2000, report.TokensSaved)
}

func TestStop_DoubleCall(t *testing.T) {
	st := monitoring.NewSavingsTracker()
	st.Stop()
	st.Stop() // second call should not panic
}

// ---------------------------------------------------------------------------
// IsSavingsRequest
// ---------------------------------------------------------------------------

func TestIsSavingsRequest(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "exact match", content: "/savings", want: true},
		{name: "with whitespace", content: "  /savings  ", want: true},
		{name: "uppercase", content: "/SAVINGS", want: true},
		{name: "mixed case", content: "/Savings", want: true},
		{name: "with prefix text", content: "show me /savings", want: false},
		{name: "empty", content: "", want: false},
		{name: "just slash", content: "/", want: false},
		{name: "similar command", content: "/saving", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := monitoring.IsSavingsRequest(tt.content)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// BuildSavingsResponse
// ---------------------------------------------------------------------------

func TestBuildSavingsResponse_NonStreaming(t *testing.T) {
	report := "Test savings report content"
	data := monitoring.BuildSavingsResponse(report, "claude-sonnet-4-20250514", false)

	var resp map[string]interface{}
	err := json.Unmarshal(data, &resp)
	require.NoError(t, err)

	assert.Equal(t, "message", resp["type"])
	assert.Equal(t, "assistant", resp["role"])
	assert.Equal(t, "claude-sonnet-4-20250514", resp["model"])
	assert.Equal(t, "end_turn", resp["stop_reason"])

	// Check content contains report
	content, ok := resp["content"].([]interface{})
	require.True(t, ok)
	require.Len(t, content, 1)
	block := content[0].(map[string]interface{})
	assert.Equal(t, "text", block["type"])
	assert.Equal(t, report, block["text"])

	// Check ID prefix
	id := resp["id"].(string)
	assert.True(t, strings.HasPrefix(id, "msg_savings_"))
}

func TestBuildSavingsResponse_Streaming(t *testing.T) {
	report := "Streaming test report"
	data := monitoring.BuildSavingsResponse(report, "claude-sonnet-4-20250514", true)

	result := string(data)
	assert.Contains(t, result, "event: message_start")
	assert.Contains(t, result, "event: content_block_start")
	assert.Contains(t, result, "event: content_block_delta")
	assert.Contains(t, result, "event: content_block_stop")
	assert.Contains(t, result, "event: message_delta")
	assert.Contains(t, result, "event: message_stop")
	assert.Contains(t, result, report)
}

// ---------------------------------------------------------------------------
// FormatUnifiedReport
// ---------------------------------------------------------------------------

func TestFormatUnifiedReport(t *testing.T) {
	st := newTracker(t)
	st.RecordRequest(compressedEvent(10000, 5000), "")

	extra := monitoring.UnifiedReportData{
		Tier:                "pro",
		CreditsRemainingUSD: 42.50,
		BalanceAvailable:    true,
	}
	report := st.FormatUnifiedReport(extra)
	assert.Contains(t, report, "Savings Report")
	assert.Contains(t, report, "Pro")
}

func TestFormatUnifiedReportForSession(t *testing.T) {
	st := newTracker(t)
	st.RecordRequestWithSession(compressedEvent(5000, 2000), "sess-test")

	extra := monitoring.UnifiedReportData{}
	report := st.FormatUnifiedReportForSession("sess-test", extra)
	assert.NotEmpty(t, report)
}

func TestFormatUnifiedReportFromReport(t *testing.T) {
	report := monitoring.SavingsReport{
		TotalRequests:      10,
		CompressedRequests: 8,
		OriginalTokens:    50000,
		CompressedTokens:  25000,
		TokensSaved:       25000,
	}
	extra := monitoring.UnifiedReportData{
		Tier:                "enterprise",
		CreditsRemainingUSD: 100.0,
		BalanceAvailable:    true,
	}
	result := monitoring.FormatUnifiedReportFromReport(report, extra)
	assert.Contains(t, result, "Savings Report")
	assert.Contains(t, result, "Enterprise")
}
