// Package monitoring - aggregator.go provides background log aggregation for /savings.
//
// DESIGN: LogAggregator is the single source of truth for savings data.
// Instead of tracking in-memory state during request handling, we parse
// the telemetry logs incrementally in the background.
//
// Benefits:
//   - Single source of truth (logs)
//   - No duplicate tracking code in handlers
//   - Persistent across gateway restarts
//   - Instant /savings responses (pre-computed cache)
//
// Flow:
//  1. Background goroutine ticks every 10s
//  2. Discovers session folders: logs/session_*/
//  3. For each session, reads telemetry.jsonl + tool_discovery.jsonl
//  4. Uses file offsets to only read new lines (incremental)
//  5. Aggregates into SavingsReport, stores atomically
//  6. /savings endpoint reads from atomic cache (instant)
package monitoring

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/compresr/context-gateway/internal/costcontrol"
	"github.com/rs/zerolog/log"
)

// LogAggregator parses session logs incrementally and caches savings reports.
type LogAggregator struct {
	logsDir  string
	interval time.Duration

	// Atomic cache for instant reads
	globalCache atomic.Pointer[SavingsReport]

	// Per-session caches
	sessionCachesMu sync.RWMutex
	sessionCaches   map[string]*SavingsReport

	// File offsets for incremental parsing
	offsetsMu sync.Mutex
	offsets   map[string]int64 // file path -> bytes read

	// Accumulated raw data (rebuilt from logs)
	dataMu      sync.Mutex
	globalData  *aggregatedData
	sessionData map[string]*aggregatedData

	// When set, only parse this session folder (ignore old sessions after reset).
	onlySessionMu sync.Mutex
	onlySession   string

	stopCh   chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once
}

// aggregatedData holds raw accumulated values before computing SavingsReport.
type aggregatedData struct {
	TotalRequests      int
	CompressedRequests int
	// Exact billed spend (nanodollars) from telemetry cost_usd when available.
	// Falls back to usage-based calculation for older telemetry lines.
	BilledCostNano int64

	// Tool output compression (from tool_output_compression.jsonl)
	OriginalTokens   int
	CompressedTokens int

	// Tool discovery (from tool_discovery.jsonl)
	ToolDiscoveryRequests int
	OriginalToolCount     int
	FilteredToolCount     int
	ToolDiscoveryBytes    int
	FilteredToolBytes     int

	// Expand penalty
	ExpandCallsFound    int
	ExpandCallsNotFound int
	ExpandPenaltyTokens int

	// Per-model usage for cost calculation
	ModelUsage map[string]ModelUsageStats

	// request_id -> LLM model mapping (populated from telemetry.jsonl)
	requestModels map[string]string
}

func newAggregatedData() *aggregatedData {
	return &aggregatedData{
		ModelUsage:    make(map[string]ModelUsageStats),
		requestModels: make(map[string]string),
	}
}

// NewLogAggregator creates an aggregator that parses logs from logsDir.
func NewLogAggregator(logsDir string, interval time.Duration) *LogAggregator {
	if interval == 0 {
		interval = 10 * time.Second
	}

	a := &LogAggregator{
		logsDir:       logsDir,
		interval:      interval,
		sessionCaches: make(map[string]*SavingsReport),
		offsets:       make(map[string]int64),
		globalData:    newAggregatedData(),
		sessionData:   make(map[string]*aggregatedData),
		stopCh:        make(chan struct{}),
	}

	empty := &SavingsReport{}
	a.globalCache.Store(empty)

	return a
}

// Start begins background aggregation.
func (a *LogAggregator) Start() {
	a.wg.Add(1)
	go a.run()
}

// ResetForNewSession clears all accumulated data and restricts future parsing
// to the given session folder only (ignoring old session logs on disk).
// Pass "" to parse all sessions (useful at startup).
func (a *LogAggregator) ResetForNewSession(currentSessionID string) {
	a.dataMu.Lock()
	a.globalData = newAggregatedData()
	a.sessionData = make(map[string]*aggregatedData)
	a.dataMu.Unlock()

	a.offsetsMu.Lock()
	a.offsets = make(map[string]int64)
	a.offsetsMu.Unlock()

	a.onlySessionMu.Lock()
	a.onlySession = currentSessionID
	a.onlySessionMu.Unlock()

	empty := &SavingsReport{}
	a.globalCache.Store(empty)

	a.sessionCachesMu.Lock()
	a.sessionCaches = make(map[string]*SavingsReport)
	a.sessionCachesMu.Unlock()
}

// Stop stops the background aggregation. Safe to call multiple times.
func (a *LogAggregator) Stop() {
	a.stopOnce.Do(func() {
		close(a.stopCh)
	})
	a.wg.Wait()
}

func (a *LogAggregator) run() {
	defer a.wg.Done()

	a.refresh()

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	for {
		select {
		case <-a.stopCh:
			return
		case <-ticker.C:
			a.refresh()
		}
	}
}

// refresh discovers sessions and parses new log entries.
func (a *LogAggregator) refresh() {
	a.onlySessionMu.Lock()
	only := a.onlySession
	a.onlySessionMu.Unlock()

	// If restricted to a specific session, only parse that one.
	if only != "" {
		sessionDir := filepath.Join(a.logsDir, only)
		if info, err := os.Stat(sessionDir); err == nil && info.IsDir() {
			a.parseSession(only, sessionDir)
		}
		a.rebuildGlobalReport()
		return
	}

	// Otherwise parse all sessions (startup, before first reset).
	pattern := filepath.Join(a.logsDir, "session_*")
	sessions, err := filepath.Glob(pattern)
	if err != nil {
		log.Debug().Err(err).Msg("LogAggregator: failed to glob sessions")
		return
	}

	for _, sessionDir := range sessions {
		sessionID := filepath.Base(sessionDir)
		a.parseSession(sessionID, sessionDir)
	}

	a.rebuildGlobalReport()
}

// parseSession parses all JSONL files incrementally for a single session.
func (a *LogAggregator) parseSession(sessionID, sessionDir string) {
	telemetryPath := filepath.Join(sessionDir, "telemetry.jsonl")
	compressionPath := filepath.Join(sessionDir, "tool_output_compression.jsonl")
	toolDiscoveryPath := filepath.Join(sessionDir, "tool_discovery.jsonl")

	a.dataMu.Lock()
	sd := a.sessionData[sessionID]
	if sd == nil {
		sd = newAggregatedData()
		a.sessionData[sessionID] = sd
	}
	a.dataMu.Unlock()

	a.parseFile(telemetryPath, func(line []byte) { a.processTelemetryLine(line, sd) })
	a.parseFile(compressionPath, func(line []byte) { a.processCompressionLine(line, sd) })
	a.parseFile(toolDiscoveryPath, func(line []byte) { a.processToolDiscoveryLine(line, sd) })

	report := a.buildReport(sd)
	a.sessionCachesMu.Lock()
	a.sessionCaches[sessionID] = report
	a.sessionCachesMu.Unlock()
}

// parseFile reads new lines from a file starting at the last known offset.
func (a *LogAggregator) parseFile(path string, handler func([]byte)) {
	a.offsetsMu.Lock()
	offset := a.offsets[path]
	a.offsetsMu.Unlock()

	f, err := os.Open(path) // #nosec G304 -- reading logs dir
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	if offset > 0 {
		if _, seekErr := f.Seek(offset, 0); seekErr != nil {
			return
		}
	}

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		handler(line)
	}

	newOffset, err := f.Seek(0, 1)
	if err == nil {
		a.offsetsMu.Lock()
		a.offsets[path] = newOffset
		a.offsetsMu.Unlock()
	}
}

// processTelemetryLine parses a single telemetry JSON line and accumulates cost data.
func (a *LogAggregator) processTelemetryLine(line []byte, sd *aggregatedData) {
	var event struct {
		RequestID           string  `json:"request_id"`
		PipeType            string  `json:"pipe_type"`
		CompressionUsed     bool    `json:"compression_used"`
		Model               string  `json:"model"`
		InputTokens         int     `json:"input_tokens"`
		OutputTokens        int     `json:"output_tokens"`
		CacheCreationTokens int     `json:"cache_creation_input_tokens"`
		CacheReadTokens     int     `json:"cache_read_input_tokens"`
		ExpandCallsFound    int     `json:"expand_calls_found"`
		ExpandCallsNotFound int     `json:"expand_calls_not_found"`
		Success             bool    `json:"success"`
		CostUSD             float64 `json:"cost_usd"`
	}

	if err := json.Unmarshal(line, &event); err != nil {
		return
	}
	if !event.Success {
		return
	}

	a.dataMu.Lock()
	defer a.dataMu.Unlock()

	model := event.Model
	if model == "" {
		model = "unknown"
	}
	if event.RequestID != "" && model != "unknown" {
		sd.requestModels[event.RequestID] = model
		a.globalData.requestModels[event.RequestID] = model
	}

	// Session data
	sd.TotalRequests++
	if event.CompressionUsed {
		sd.CompressedRequests++
	}
	sd.ExpandCallsFound += event.ExpandCallsFound
	sd.ExpandCallsNotFound += event.ExpandCallsNotFound

	usage := sd.ModelUsage[model]
	usage.InputTokens += event.InputTokens
	usage.OutputTokens += event.OutputTokens
	usage.CacheCreationTokens += event.CacheCreationTokens
	usage.CacheReadTokens += event.CacheReadTokens
	usage.RequestCount++
	sd.ModelUsage[model] = usage

	// Global data
	a.globalData.TotalRequests++
	if event.CompressionUsed {
		a.globalData.CompressedRequests++
	}
	a.globalData.ExpandCallsFound += event.ExpandCallsFound
	a.globalData.ExpandCallsNotFound += event.ExpandCallsNotFound

	gUsage := a.globalData.ModelUsage[model]
	gUsage.InputTokens += event.InputTokens
	gUsage.OutputTokens += event.OutputTokens
	gUsage.CacheCreationTokens += event.CacheCreationTokens
	gUsage.CacheReadTokens += event.CacheReadTokens
	gUsage.RequestCount++
	a.globalData.ModelUsage[model] = gUsage

	// Billed cost: prefer telemetry cost_usd, fall back to usage-based calculation.
	costNano := int64(0)
	if event.CostUSD > 0 {
		costNano = int64(math.Round(event.CostUSD * 1e9))
	} else if event.InputTokens > 0 || event.OutputTokens > 0 || event.CacheCreationTokens > 0 || event.CacheReadTokens > 0 {
		pricing := costcontrol.GetModelPricing(model)
		cost := costcontrol.CalculateCostWithCache(
			event.InputTokens, event.OutputTokens,
			event.CacheCreationTokens, event.CacheReadTokens, pricing)
		costNano = int64(math.Round(cost * 1e9))
	}
	sd.BilledCostNano += costNano
	a.globalData.BilledCostNano += costNano
}

// resolveModel resolves the LLM model for a compression entry.
// Compression JSONL may contain the compression model name (e.g., "toc_latte_v1")
// instead of the LLM model, so we cross-reference with telemetry data.
func (a *LogAggregator) resolveModel(jsonModel, requestID string, sd *aggregatedData) string {
	if jsonModel != "" && !isCompressionModel(jsonModel) {
		return jsonModel
	}
	if requestID != "" {
		if m, ok := sd.requestModels[requestID]; ok {
			return m
		}
		if m, ok := a.globalData.requestModels[requestID]; ok {
			return m
		}
	}
	return "unknown"
}

func isCompressionModel(model string) bool {
	return len(model) > 3 && (model[:4] == "toc_" || model[:4] == "tdc_")
}

// processCompressionLine parses a tool_output_compression.jsonl line.
func (a *LogAggregator) processCompressionLine(line []byte, sd *aggregatedData) {
	var entry struct {
		RequestID       string `json:"request_id"`
		Model           string `json:"model"`
		OriginalBytes   int    `json:"original_bytes"`
		CompressedBytes int    `json:"compressed_bytes"`
		Status          string `json:"status"`
	}

	if err := json.Unmarshal(line, &entry); err != nil {
		return
	}

	bytesSaved := entry.OriginalBytes - entry.CompressedBytes
	if bytesSaved <= 0 {
		return
	}

	tokensSaved := bytesSaved / 4

	a.dataMu.Lock()
	defer a.dataMu.Unlock()

	sd.OriginalTokens += entry.OriginalBytes / 4
	sd.CompressedTokens += entry.CompressedBytes / 4

	model := a.resolveModel(entry.Model, entry.RequestID, sd)
	usage := sd.ModelUsage[model]
	usage.TokensSaved += tokensSaved
	sd.ModelUsage[model] = usage

	a.globalData.OriginalTokens += entry.OriginalBytes / 4
	a.globalData.CompressedTokens += entry.CompressedBytes / 4

	gUsage := a.globalData.ModelUsage[model]
	gUsage.TokensSaved += tokensSaved
	a.globalData.ModelUsage[model] = gUsage
}

// processToolDiscoveryLine parses a tool_discovery.jsonl line.
func (a *LogAggregator) processToolDiscoveryLine(line []byte, sd *aggregatedData) {
	var entry struct {
		RequestID       string   `json:"request_id"`
		Model           string   `json:"model"`
		OriginalBytes   int      `json:"original_bytes"`
		CompressedBytes int      `json:"compressed_bytes"`
		Status          string   `json:"status"`
		AllTools        []string `json:"all_tools"`
		SelectedTools   []string `json:"selected_tools"`
	}

	if err := json.Unmarshal(line, &entry); err != nil {
		return
	}

	a.dataMu.Lock()
	defer a.dataMu.Unlock()

	sd.ToolDiscoveryRequests++
	sd.OriginalToolCount += len(entry.AllTools)
	sd.FilteredToolCount += len(entry.SelectedTools)
	sd.ToolDiscoveryBytes += entry.OriginalBytes
	sd.FilteredToolBytes += entry.CompressedBytes

	bytesSaved := entry.OriginalBytes - entry.CompressedBytes
	if bytesSaved > 0 {
		tokensSaved := bytesSaved / 4
		model := a.resolveModel(entry.Model, entry.RequestID, sd)
		usage := sd.ModelUsage[model]
		usage.ToolDiscoveryTokens += tokensSaved
		sd.ModelUsage[model] = usage

		gUsage := a.globalData.ModelUsage[model]
		gUsage.ToolDiscoveryTokens += tokensSaved
		a.globalData.ModelUsage[model] = gUsage
	}

	a.globalData.ToolDiscoveryRequests++
	a.globalData.OriginalToolCount += len(entry.AllTools)
	a.globalData.FilteredToolCount += len(entry.SelectedTools)
	a.globalData.ToolDiscoveryBytes += entry.OriginalBytes
	a.globalData.FilteredToolBytes += entry.CompressedBytes
}

// rebuildGlobalReport builds and caches the global report.
func (a *LogAggregator) rebuildGlobalReport() {
	a.dataMu.Lock()
	report := a.buildReport(a.globalData)
	a.dataMu.Unlock()

	a.globalCache.Store(report)
}

// buildReport generates a SavingsReport from aggregated data.
// Uses the shared computeReport for all math (now cache-aware), then overrides
// CompressedCostUSD with exact billed spend from telemetry logs when available.
func (a *LogAggregator) buildReport(data *aggregatedData) *SavingsReport {
	if data == nil {
		return &SavingsReport{}
	}

	sd := &savingsData{
		TotalRequests:         data.TotalRequests,
		CompressedRequests:    data.CompressedRequests,
		OriginalTokens:        data.OriginalTokens,
		CompressedTokens:      data.CompressedTokens,
		ToolDiscoveryRequests: data.ToolDiscoveryRequests,
		OriginalToolCount:     data.OriginalToolCount,
		FilteredToolCount:     data.FilteredToolCount,
		ToolDiscoveryBytes:    data.ToolDiscoveryBytes,
		FilteredToolBytes:     data.FilteredToolBytes,
		ExpandPenaltyTokens:   data.ExpandPenaltyTokens,
		ModelUsage:            data.ModelUsage,
	}
	report := computeReport(sd)

	// Override CompressedCostUSD with exact billed spend from telemetry logs
	// (more accurate than token-based estimate when cost_usd is recorded).
	if data.BilledCostNano > 0 {
		report.CompressedCostUSD = float64(data.BilledCostNano) / 1e9
		report.OriginalCostUSD = report.CompressedCostUSD + report.CostSavedUSD
		if report.OriginalCostUSD > 0 {
			report.CostSavedPct = report.CostSavedUSD / report.OriginalCostUSD * 100
		}
	}

	return &report
}

// GetReport returns the cached global savings report (instant).
func (a *LogAggregator) GetReport() SavingsReport {
	if r := a.globalCache.Load(); r != nil {
		return *r
	}
	return SavingsReport{}
}

// GetReportForSession returns the cached report for a specific session.
func (a *LogAggregator) GetReportForSession(sessionID string) SavingsReport {
	a.sessionCachesMu.RLock()
	defer a.sessionCachesMu.RUnlock()

	if r := a.sessionCaches[sessionID]; r != nil {
		return *r
	}
	return SavingsReport{}
}

// GetSavingsSummary returns a quick summary for CLI display.
func (a *LogAggregator) GetSavingsSummary() (int, float64, int, int) {
	r := a.GetReport()
	return r.TotalTokensSaved, r.CostSavedUSD, r.CompressedRequests, r.TotalRequests
}

// GetCostBreakdown returns original, compressed, and saved cost in USD.
func (a *LogAggregator) GetCostBreakdown() (float64, float64, float64) {
	r := a.GetReport()
	return r.OriginalCostUSD, r.CompressedCostUSD, r.CostSavedUSD
}

// GetCompressionStats returns compression and tool discovery statistics.
func (a *LogAggregator) GetCompressionStats() (int, int, int, int, int) {
	r := a.GetReport()
	return r.CompressedRequests, r.TotalRequests, r.ToolDiscoveryRequests, r.OriginalToolCount, r.FilteredToolCount
}
