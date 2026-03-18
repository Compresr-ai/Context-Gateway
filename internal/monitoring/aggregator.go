// aggregator.go parses session telemetry logs incrementally and caches savings reports.
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

	globalCache atomic.Pointer[SavingsReport]

	sessionCachesMu sync.RWMutex
	sessionCaches   map[string]*SavingsReport

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
	ToolDiscoveryRequests   int
	OriginalToolCount       int
	KeptToolCount           int
	OrigToolDiscoveryTokens int
	CompToolDiscoveryTokens int

	// Expand penalty
	ExpandCallsFound    int
	ExpandCallsNotFound int
	ExpandPenaltyTokens int

	// Preemptive summarization
	PreemptiveSummarizationRequests int
	PreemptiveSummarizationTokens   int // Original tokens before summarization
	PreemptiveSummarizedTokens      int // Tokens after summarization

	// Per-model usage for cost calculation
	ModelUsage map[string]ModelUsageStats

	// request_id -> LLM model mapping (populated from telemetry.jsonl)
	requestModels map[string]string

	// request_ids from main agent requests (for filtering compression/discovery entries)
	mainAgentRequestIDs map[string]bool

	// All requests (including sub-agents) — for session card display
	AllRequestsCount    int
	AllRequestsCostNano int64
	AllModelUsage       map[string]int

	// Session activity counters
	UserTurns          int
	CompactionTriggers int
	ToolSearchCalls    int

	// Timestamps for session metadata
	firstTimestamp time.Time
	lastTimestamp  time.Time
}

func newAggregatedData() *aggregatedData {
	return &aggregatedData{
		ModelUsage:          make(map[string]ModelUsageStats),
		requestModels:       make(map[string]string),
		mainAgentRequestIDs: make(map[string]bool),
		AllModelUsage:       make(map[string]int),
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
	a.discoverAndParseSessions()

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
// Only main agent (user) requests are counted — subagent requests are excluded
// so that dashboard metrics reflect the user's actual requests and savings.
func (a *LogAggregator) processTelemetryLine(line []byte, sd *aggregatedData) {
	var event struct {
		RequestID                  string  `json:"request_id"`
		PipeType                   string  `json:"pipe_type"`
		CompressionUsed            bool    `json:"compression_used"`
		Model                      string  `json:"model"`
		InputTokens                int     `json:"input_tokens"`
		OutputTokens               int     `json:"output_tokens"`
		CacheCreationTokens        int     `json:"cache_creation_input_tokens"`
		CacheReadTokens            int     `json:"cache_read_input_tokens"`
		ExpandCallsFound           int     `json:"expand_calls_found"`
		ExpandCallsNotFound        int     `json:"expand_calls_not_found"`
		ExpandPenaltyTokens        int     `json:"expand_penalty_tokens"`
		HistoryCompactionTriggered bool    `json:"history_compaction_triggered"`
		Success                    bool    `json:"success"`
		CostUSD                    float64 `json:"cost_usd"`
		IsMainAgent                bool    `json:"is_main_agent"`
	}

	if err := json.Unmarshal(line, &event); err != nil {
		return
	}
	if !event.Success {
		return
	}
	// Skip entries with empty RequestID — these are not real requests
	if event.RequestID == "" {
		return
	}

	a.dataMu.Lock()
	defer a.dataMu.Unlock()

	model := event.Model
	if model == "" {
		model = "unknown"
	}
	if model != "unknown" {
		sd.requestModels[event.RequestID] = model
		a.globalData.requestModels[event.RequestID] = model
	}

	// Track main agent request IDs for filtering compression/discovery entries.
	if event.IsMainAgent {
		sd.mainAgentRequestIDs[event.RequestID] = true
		a.globalData.mainAgentRequestIDs[event.RequestID] = true
		sd.UserTurns++
		a.globalData.UserTurns++
	}

	// Track ALL requests for session card display.
	sd.AllRequestsCount++
	a.globalData.AllRequestsCount++
	if event.CostUSD > 0 {
		allCostNano := int64(math.Round(event.CostUSD * 1e9))
		sd.AllRequestsCostNano += allCostNano
		a.globalData.AllRequestsCostNano += allCostNano
	}

	// Track model usage across ALL requests for session display
	sd.AllModelUsage[model]++
	a.globalData.AllModelUsage[model]++

	// Only count main agent (user) requests in savings metrics.
	if !event.IsMainAgent {
		return
	}

	// Session data
	sd.TotalRequests++
	if event.CompressionUsed {
		sd.CompressedRequests++
	}
	sd.ExpandCallsFound += event.ExpandCallsFound
	sd.ExpandCallsNotFound += event.ExpandCallsNotFound
	if event.ExpandPenaltyTokens > 0 {
		sd.ExpandPenaltyTokens += event.ExpandPenaltyTokens
		usage := sd.ModelUsage[model]
		usage.ExpandPenaltyTokens += event.ExpandPenaltyTokens
		sd.ModelUsage[model] = usage
	}

	if event.HistoryCompactionTriggered {
		sd.CompactionTriggers++
		a.globalData.CompactionTriggers++
	}

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
	if event.ExpandPenaltyTokens > 0 {
		a.globalData.ExpandPenaltyTokens += event.ExpandPenaltyTokens
		gUsagePenalty := a.globalData.ModelUsage[model]
		gUsagePenalty.ExpandPenaltyTokens += event.ExpandPenaltyTokens
		a.globalData.ModelUsage[model] = gUsagePenalty
	}

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
// Counts all entries regardless of agent type (no main-agent filter applied).
func (a *LogAggregator) processCompressionLine(line []byte, sd *aggregatedData) {
	var entry struct {
		RequestID        string `json:"request_id"`
		Model            string `json:"model"`
		OriginalTokens   int    `json:"original_tokens"`
		CompressedTokens int    `json:"compressed_tokens"`
		Status           string `json:"status"`
	}

	if err := json.Unmarshal(line, &entry); err != nil {
		return
	}

	origTokens := entry.OriginalTokens
	compTokens := entry.CompressedTokens

	tokensSaved := origTokens - compTokens
	if tokensSaved <= 0 {
		return
	}

	a.dataMu.Lock()
	defer a.dataMu.Unlock()

	// Skip entries with empty RequestID — they can't be attributed.
	if entry.RequestID == "" {
		return
	}

	sd.OriginalTokens += origTokens
	sd.CompressedTokens += compTokens

	model := a.resolveModel(entry.Model, entry.RequestID, sd)
	usage := sd.ModelUsage[model]
	usage.TokensSaved += tokensSaved
	sd.ModelUsage[model] = usage

	a.globalData.OriginalTokens += origTokens
	a.globalData.CompressedTokens += compTokens

	gUsage := a.globalData.ModelUsage[model]
	gUsage.TokensSaved += tokensSaved
	a.globalData.ModelUsage[model] = gUsage
}

// processToolDiscoveryLine parses a tool_discovery.jsonl line.
// Only counts entries linked to main agent requests (via request_id).
func (a *LogAggregator) processToolDiscoveryLine(line []byte, sd *aggregatedData) {
	var entry struct {
		EventType        string `json:"event_type"`
		RequestID        string `json:"request_id"`
		Model            string `json:"model"`
		Status           string `json:"status"`
		ToolCount        int    `json:"tool_count"`
		StubCount        int    `json:"stub_count"`
		OriginalTokens   int    `json:"original_tokens"`
		CompressedTokens int    `json:"compressed_tokens"`
		Strategy         string `json:"strategy"`
	}

	if err := json.Unmarshal(line, &entry); err != nil {
		return
	}

	a.dataMu.Lock()
	defer a.dataMu.Unlock()

	// Skip entries with empty RequestID — they can't be attributed.
	if entry.RequestID == "" {
		return
	}

	// Skip entries with unknown/empty EventType — they can't be reliably attributed.
	// New entries always have EventType set; old entries without it are ambiguous.
	if entry.EventType == "" {
		return
	}

	// tool_output events: tool name signal — savings already counted via tool_output_compression.jsonl.
	if entry.EventType == EventTypeToolOutput {
		return
	}

	// tool_search_result events: count search calls and track compression savings if present.
	if entry.EventType == EventTypeToolSearchResult {
		sd.ToolSearchCalls++
		a.globalData.ToolSearchCalls++

		if entry.OriginalTokens > 0 && entry.CompressedTokens > 0 {
			tokensSaved := entry.OriginalTokens - entry.CompressedTokens
			if tokensSaved > 0 {
				model := a.resolveModel(entry.Model, entry.RequestID, sd)
				usage := sd.ModelUsage[model]
				usage.ToolDiscoveryTokens += tokensSaved
				sd.ModelUsage[model] = usage

				gUsage := a.globalData.ModelUsage[model]
				gUsage.ToolDiscoveryTokens += tokensSaved
				a.globalData.ModelUsage[model] = gUsage

				sd.OrigToolDiscoveryTokens += entry.OriginalTokens
				sd.CompToolDiscoveryTokens += entry.CompressedTokens
				a.globalData.OrigToolDiscoveryTokens += entry.OriginalTokens
				a.globalData.CompToolDiscoveryTokens += entry.CompressedTokens

				log.Debug().
					Int("original_tokens", entry.OriginalTokens).
					Int("compressed_tokens", entry.CompressedTokens).
					Int("tokens_saved", tokensSaved).
					Str("strategy", entry.Strategy).
					Msg("aggregator: tool_search_result compression recorded")
			}
		}
		return
	}

	selectedCount := entry.ToolCount - entry.StubCount

	sd.ToolDiscoveryRequests++
	sd.OriginalToolCount += entry.ToolCount
	sd.KeptToolCount += selectedCount

	origTokens := entry.OriginalTokens
	compTokens := entry.CompressedTokens
	sd.OrigToolDiscoveryTokens += origTokens
	sd.CompToolDiscoveryTokens += compTokens

	tokensSaved := origTokens - compTokens
	if tokensSaved > 0 {
		model := a.resolveModel(entry.Model, entry.RequestID, sd)
		usage := sd.ModelUsage[model]
		usage.ToolDiscoveryTokens += tokensSaved
		sd.ModelUsage[model] = usage

		gUsage := a.globalData.ModelUsage[model]
		gUsage.ToolDiscoveryTokens += tokensSaved
		a.globalData.ModelUsage[model] = gUsage
	}

	a.globalData.ToolDiscoveryRequests++
	a.globalData.OriginalToolCount += entry.ToolCount
	a.globalData.KeptToolCount += selectedCount
	a.globalData.OrigToolDiscoveryTokens += origTokens
	a.globalData.CompToolDiscoveryTokens += compTokens
}

// discoverAndParseSessions finds all subdirectories in logsDir that contain
// a telemetry.jsonl file and parses them. This replaces the old session_* glob
// to support user-named session directories (e.g., "test", "after", "kg").
func (a *LogAggregator) discoverAndParseSessions() {
	entries, err := os.ReadDir(a.logsDir)
	if err != nil {
		log.Debug().Err(err).Msg("LogAggregator: failed to read logs dir")
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionDir := filepath.Join(a.logsDir, entry.Name())
		// Only parse directories that contain telemetry data.
		telemetryFile := filepath.Join(sessionDir, "telemetry.jsonl")
		if _, statErr := os.Stat(telemetryFile); statErr != nil {
			continue
		}
		a.parseSession(entry.Name(), sessionDir)
	}

	a.rebuildGlobalReport()
}

// SessionMeta holds metadata extracted from telemetry for a session.
type SessionMeta struct {
	Models             []string  // All models used in this session (deduplicated, excluding "unknown")
	CreatedAt          time.Time // Timestamp of first telemetry entry
	LastTimestamp      time.Time // Timestamp of last telemetry entry
	AllRequestsCount   int       // Total requests (all agents, for session card display)
	AllRequestsCostUSD float64   // Total billed cost (all agents, for session card display)
}

// GetAllSessionsReport parses ALL session directories on disk and returns
// an aggregated report across all of them. This bypasses the onlySession
// restriction so the dashboard can show historical totals.
// It also returns per-session reports and metadata keyed by session directory name.
func (a *LogAggregator) GetAllSessionsReport() (SavingsReport, map[string]*SavingsReport, map[string]*SessionMeta) {
	entries, err := os.ReadDir(a.logsDir)
	if err != nil {
		return SavingsReport{}, nil, nil
	}

	allData := newAggregatedData()
	sessionReports := make(map[string]*SavingsReport)
	sessionMetas := make(map[string]*SessionMeta)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionDir := filepath.Join(a.logsDir, entry.Name())
		telemetryFile := filepath.Join(sessionDir, "telemetry.jsonl")
		if _, statErr := os.Stat(telemetryFile); statErr != nil {
			continue
		}

		sd := newAggregatedData()
		meta := &SessionMeta{}

		// Parse telemetry — timestamps and model are tracked inside processTelemetryLineInto.
		a.parseFileOnce(telemetryFile, func(line []byte) {
			a.processTelemetryLineInto(line, sd)
		})

		// Extract metadata from aggregated data.
		meta.CreatedAt = sd.firstTimestamp
		meta.LastTimestamp = sd.lastTimestamp
		meta.AllRequestsCount = sd.AllRequestsCount
		if sd.AllRequestsCostNano > 0 {
			meta.AllRequestsCostUSD = float64(sd.AllRequestsCostNano) / 1e9
		}

		// Collect all unique models used across ALL requests (not just main agent).
		for m := range sd.AllModelUsage {
			if m != "unknown" && m != "" {
				meta.Models = append(meta.Models, m)
			}
		}

		// Fallback: use dir modification time if no timestamp in telemetry
		if meta.CreatedAt.IsZero() {
			if info, statErr := os.Stat(telemetryFile); statErr == nil {
				meta.CreatedAt = info.ModTime()
			}
		}

		// Parse compression
		a.parseFileOnce(filepath.Join(sessionDir, "tool_output_compression.jsonl"),
			func(line []byte) { a.processCompressionLineInto(line, sd) })
		// Parse tool discovery
		a.parseFileOnce(filepath.Join(sessionDir, "tool_discovery.jsonl"),
			func(line []byte) { a.processToolDiscoveryLineInto(line, sd) })

		report := a.buildReport(sd)
		sessionReports[entry.Name()] = report
		sessionMetas[entry.Name()] = meta

		// Merge into allData
		mergeAggregatedData(allData, sd)
	}

	globalReport := a.buildReport(allData)
	return *globalReport, sessionReports, sessionMetas
}

// parseFileOnce reads a file from the beginning (no offset tracking) for one-shot aggregation.
func (a *LogAggregator) parseFileOnce(path string, handler func([]byte)) {
	f, err := os.Open(path) // #nosec G304 -- reading logs dir
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

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
}

// processTelemetryLineInto parses a telemetry line and writes only to the given sd (no globalData).
//
// DESIGN NOTE — why two variants exist:
//   - processTelemetryLine:     incremental live parsing; writes to both sd (session) and a.globalData;
//     acquires a.dataMu; no timestamp tracking (not needed for live data).
//   - processTelemetryLineInto: one-shot historical parsing; writes only to the caller-supplied sd;
//     no lock (caller owns sd exclusively); tracks timestamps for session metadata.
//
// The model-resolution strategy also differs: the live variant falls back to a.globalData.requestModels
// when sd lacks an entry; the historical variant only looks in sd (which is fully populated in-order).
func (a *LogAggregator) processTelemetryLineInto(line []byte, sd *aggregatedData) {
	var event struct {
		RequestID                  string    `json:"request_id"`
		PipeType                   string    `json:"pipe_type"`
		CompressionUsed            bool      `json:"compression_used"`
		Model                      string    `json:"model"`
		Timestamp                  time.Time `json:"timestamp"`
		InputTokens                int       `json:"input_tokens"`
		OutputTokens               int       `json:"output_tokens"`
		CacheCreationTokens        int       `json:"cache_creation_input_tokens"`
		CacheReadTokens            int       `json:"cache_read_input_tokens"`
		ExpandCallsFound           int       `json:"expand_calls_found"`
		ExpandCallsNotFound        int       `json:"expand_calls_not_found"`
		ExpandPenaltyTokens        int       `json:"expand_penalty_tokens"`
		HistoryCompactionTriggered bool      `json:"history_compaction_triggered"`
		Success                    bool      `json:"success"`
		CostUSD                    float64   `json:"cost_usd"`
		IsMainAgent                bool      `json:"is_main_agent"`
	}

	if err := json.Unmarshal(line, &event); err != nil {
		return
	}
	if !event.Success || event.RequestID == "" {
		return
	}

	model := event.Model
	if model == "" {
		model = "unknown"
	}
	if model != "unknown" {
		sd.requestModels[event.RequestID] = model
	}
	if event.IsMainAgent {
		sd.mainAgentRequestIDs[event.RequestID] = true
		sd.UserTurns++
	}

	// Track timestamps for session metadata (all entries, not just main agent).
	if !event.Timestamp.IsZero() {
		if sd.firstTimestamp.IsZero() || event.Timestamp.Before(sd.firstTimestamp) {
			sd.firstTimestamp = event.Timestamp
		}
		if event.Timestamp.After(sd.lastTimestamp) {
			sd.lastTimestamp = event.Timestamp
		}
	}

	// Track ALL requests and their costs for session card display.
	sd.AllRequestsCount++
	if event.CostUSD > 0 {
		sd.AllRequestsCostNano += int64(math.Round(event.CostUSD * 1e9))
	}

	// Track model usage across ALL requests for session display
	sd.AllModelUsage[model]++

	if !event.IsMainAgent {
		return
	}

	sd.TotalRequests++
	if event.CompressionUsed {
		sd.CompressedRequests++
	}
	sd.ExpandCallsFound += event.ExpandCallsFound
	sd.ExpandCallsNotFound += event.ExpandCallsNotFound
	if event.ExpandPenaltyTokens > 0 {
		sd.ExpandPenaltyTokens += event.ExpandPenaltyTokens
		usage := sd.ModelUsage[model]
		usage.ExpandPenaltyTokens += event.ExpandPenaltyTokens
		sd.ModelUsage[model] = usage
	}

	if event.HistoryCompactionTriggered {
		sd.CompactionTriggers++
	}

	usage := sd.ModelUsage[model]
	usage.InputTokens += event.InputTokens
	usage.OutputTokens += event.OutputTokens
	usage.CacheCreationTokens += event.CacheCreationTokens
	usage.CacheReadTokens += event.CacheReadTokens
	usage.RequestCount++
	sd.ModelUsage[model] = usage

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
}

// processCompressionLineInto parses a compression line and writes only to the given sd.
// See processTelemetryLineInto design note above — same live-vs-historical split applies here.
func (a *LogAggregator) processCompressionLineInto(line []byte, sd *aggregatedData) {
	var entry struct {
		RequestID        string `json:"request_id"`
		Model            string `json:"model"`
		OriginalTokens   int    `json:"original_tokens"`
		CompressedTokens int    `json:"compressed_tokens"`
		Status           string `json:"status"`
	}

	if err := json.Unmarshal(line, &entry); err != nil {
		return
	}

	if entry.RequestID == "" {
		return
	}

	origTokens := entry.OriginalTokens
	compTokens := entry.CompressedTokens

	tokensSaved := origTokens - compTokens
	if tokensSaved <= 0 {
		return
	}

	sd.OriginalTokens += origTokens
	sd.CompressedTokens += compTokens

	model := entry.Model
	if model == "" || isCompressionModel(model) {
		if m, ok := sd.requestModels[entry.RequestID]; ok {
			model = m
		} else {
			model = "unknown"
		}
	}
	usage := sd.ModelUsage[model]
	usage.TokensSaved += tokensSaved
	sd.ModelUsage[model] = usage
}

// processToolDiscoveryLineInto parses a tool-discovery line and writes only to the given sd.
// See processTelemetryLineInto design note above — same live-vs-historical split applies here.
func (a *LogAggregator) processToolDiscoveryLineInto(line []byte, sd *aggregatedData) {
	var entry struct {
		EventType        string `json:"event_type"`
		RequestID        string `json:"request_id"`
		Model            string `json:"model"`
		ToolCount        int    `json:"tool_count"`
		StubCount        int    `json:"stub_count"`
		OriginalTokens   int    `json:"original_tokens"`
		CompressedTokens int    `json:"compressed_tokens"`
		Strategy         string `json:"strategy"`
	}

	if err := json.Unmarshal(line, &entry); err != nil {
		return
	}
	if entry.RequestID == "" {
		return
	}

	// Skip entries with unknown/empty EventType — they can't be reliably attributed.
	if entry.EventType == "" {
		return
	}

	// tool_output events: tool name signal — savings already counted via tool_output_compression.jsonl.
	if entry.EventType == EventTypeToolOutput {
		return
	}

	// tool_search_result events: count search calls and track compression savings if present.
	if entry.EventType == EventTypeToolSearchResult {
		sd.ToolSearchCalls++

		if entry.OriginalTokens > 0 && entry.CompressedTokens > 0 {
			tokensSaved := entry.OriginalTokens - entry.CompressedTokens
			if tokensSaved > 0 {
				model := entry.Model
				if model == "" || isCompressionModel(model) {
					if m, ok := sd.requestModels[entry.RequestID]; ok {
						model = m
					} else {
						model = "unknown"
					}
				}
				usage := sd.ModelUsage[model]
				usage.ToolDiscoveryTokens += tokensSaved
				sd.ModelUsage[model] = usage

				sd.OrigToolDiscoveryTokens += entry.OriginalTokens
				sd.CompToolDiscoveryTokens += entry.CompressedTokens
			}
		}
		return
	}

	selectedCount := entry.ToolCount - entry.StubCount

	sd.ToolDiscoveryRequests++
	sd.OriginalToolCount += entry.ToolCount
	sd.KeptToolCount += selectedCount

	origTokens := entry.OriginalTokens
	compTokens := entry.CompressedTokens
	sd.OrigToolDiscoveryTokens += origTokens
	sd.CompToolDiscoveryTokens += compTokens

	tokensSaved := origTokens - compTokens
	if tokensSaved > 0 {
		model := entry.Model
		if model == "" || isCompressionModel(model) {
			if m, ok := sd.requestModels[entry.RequestID]; ok {
				model = m
			} else {
				model = "unknown"
			}
		}
		usage := sd.ModelUsage[model]
		usage.ToolDiscoveryTokens += tokensSaved
		sd.ModelUsage[model] = usage
	}
}

// mergeAggregatedData merges src into dst.
func mergeAggregatedData(dst, src *aggregatedData) {
	dst.TotalRequests += src.TotalRequests
	dst.CompressedRequests += src.CompressedRequests
	dst.BilledCostNano += src.BilledCostNano
	dst.AllRequestsCount += src.AllRequestsCount
	dst.AllRequestsCostNano += src.AllRequestsCostNano
	for model, count := range src.AllModelUsage {
		dst.AllModelUsage[model] += count
	}
	dst.OriginalTokens += src.OriginalTokens
	dst.CompressedTokens += src.CompressedTokens
	dst.ToolDiscoveryRequests += src.ToolDiscoveryRequests
	dst.OriginalToolCount += src.OriginalToolCount
	dst.KeptToolCount += src.KeptToolCount
	dst.OrigToolDiscoveryTokens += src.OrigToolDiscoveryTokens
	dst.CompToolDiscoveryTokens += src.CompToolDiscoveryTokens
	dst.ExpandCallsFound += src.ExpandCallsFound
	dst.ExpandCallsNotFound += src.ExpandCallsNotFound
	dst.ExpandPenaltyTokens += src.ExpandPenaltyTokens
	dst.PreemptiveSummarizationRequests += src.PreemptiveSummarizationRequests
	dst.PreemptiveSummarizationTokens += src.PreemptiveSummarizationTokens
	dst.PreemptiveSummarizedTokens += src.PreemptiveSummarizedTokens
	dst.UserTurns += src.UserTurns
	dst.CompactionTriggers += src.CompactionTriggers
	dst.ToolSearchCalls += src.ToolSearchCalls

	for model, usage := range src.ModelUsage {
		existing := dst.ModelUsage[model]
		existing.InputTokens += usage.InputTokens
		existing.OutputTokens += usage.OutputTokens
		existing.CacheCreationTokens += usage.CacheCreationTokens
		existing.CacheReadTokens += usage.CacheReadTokens
		existing.RequestCount += usage.RequestCount
		existing.TokensSaved += usage.TokensSaved
		existing.ToolDiscoveryTokens += usage.ToolDiscoveryTokens
		existing.ExpandPenaltyTokens += usage.ExpandPenaltyTokens
		existing.PreemptiveSummarizationSaved += usage.PreemptiveSummarizationSaved
		dst.ModelUsage[model] = existing
	}
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
		TotalRequests:                   data.TotalRequests,
		CompressedRequests:              data.CompressedRequests,
		OriginalTokens:                  data.OriginalTokens,
		CompressedTokens:                data.CompressedTokens,
		ToolDiscoveryRequests:           data.ToolDiscoveryRequests,
		OriginalToolCount:               data.OriginalToolCount,
		KeptToolCount:                   data.KeptToolCount,
		OrigToolDiscoveryTokens:         data.OrigToolDiscoveryTokens,
		CompToolDiscoveryTokens:         data.CompToolDiscoveryTokens,
		ExpandPenaltyTokens:             data.ExpandPenaltyTokens,
		PreemptiveSummarizationRequests: data.PreemptiveSummarizationRequests,
		PreemptiveSummarizationTokens:   data.PreemptiveSummarizationTokens,
		PreemptiveSummarizedTokens:      data.PreemptiveSummarizedTokens,
		ModelUsage:                      data.ModelUsage,
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

	// Populate session activity counters.
	report.UserTurns = data.UserTurns
	report.CompactionTriggers = data.CompactionTriggers
	report.ToolSearchCalls = data.ToolSearchCalls

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
	return r.CompressedRequests, r.TotalRequests, r.ToolDiscoveryRequests, r.OriginalToolCount, r.KeptToolCount
}

// LogsDir returns the directory where session logs are stored.
func (a *LogAggregator) LogsDir() string {
	return a.logsDir
}

// GetSessionDir returns the full path to a session's log directory.
func (a *LogAggregator) GetSessionDir(sessionID string) string {
	return filepath.Join(a.logsDir, sessionID)
}

// InvalidateSession removes a deleted session from all in-memory caches and
// rebuilds the global aggregated data so totals remain accurate immediately.
// Call this after deleting a session directory from disk.
func (a *LogAggregator) InvalidateSession(sessionID string) {
	// Drop per-session data and rebuild global from remaining sessions.
	a.dataMu.Lock()
	delete(a.sessionData, sessionID)
	// Rebuild globalData as the sum of all remaining session data.
	newGlobal := newAggregatedData()
	for _, sd := range a.sessionData {
		mergeAggregatedData(newGlobal, sd)
	}
	a.globalData = newGlobal
	a.dataMu.Unlock()

	// Drop per-session report cache.
	a.sessionCachesMu.Lock()
	delete(a.sessionCaches, sessionID)
	a.sessionCachesMu.Unlock()

	// Drop file offsets for this session so a future parse starts clean.
	sessionDir := filepath.Join(a.logsDir, sessionID)
	a.offsetsMu.Lock()
	for _, filename := range []string{"telemetry.jsonl", "tool_output_compression.jsonl", "tool_discovery.jsonl"} {
		delete(a.offsets, filepath.Join(sessionDir, filename))
	}
	a.offsetsMu.Unlock()

	// Rebuild and publish the global report from the new globalData.
	a.rebuildGlobalReport()
}
