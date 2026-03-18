// Package monitoring - session_stats.go writes a live session_stats.json snapshot.
package monitoring

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// SessionStats is the structure written to session_stats.json.
// All counters are cumulative for the current session.
type SessionStats struct {
	SessionID    string `json:"session_id"`
	UpdatedAt    string `json:"updated_at"`
	SessionStart string `json:"session_start,omitempty"`

	Requests      SessionRequestStats       `json:"requests"`
	ToolOutput    SessionToolOutputStats    `json:"tool_output"`
	ToolDiscovery SessionToolDiscoveryStats `json:"tool_discovery"`
	ToolSearch    SessionToolSearchStats    `json:"tool_search"`
	ExpandContext SessionExpandContextStats `json:"expand_context"`
	Preemptive    SessionPreemptiveStats    `json:"preemptive_summarization"`
	Totals        SessionTotalStats         `json:"totals"`
}

// SessionRequestStats tracks request counts.
type SessionRequestStats struct {
	Total       int `json:"total"`
	MainAgent   int `json:"main_agent"`
	Subagent    int `json:"subagent"`
	Compressed  int `json:"compressed"`
	Passthrough int `json:"passthrough"`
}

// SessionToolOutputStats tracks tool output compression.
type SessionToolOutputStats struct {
	TotalOutputs     int     `json:"total_outputs"`     // every tool result seen by the pipe
	Compressed       int     `json:"compressed"`        // status="compressed"
	PassthroughSmall int     `json:"passthrough_small"` // below min threshold
	PassthroughLarge int     `json:"passthrough_large"` // above max threshold (too large to compress)
	CacheHits        int     `json:"cache_hits"`
	OriginalTokens   int     `json:"original_tokens"`
	CompressedTokens int     `json:"compressed_tokens"`
	TokensSaved      int     `json:"tokens_saved"`
	CompressionRatio float64 `json:"compression_ratio"` // removed fraction: 1 - comp/orig
}

// SessionToolDiscoveryStats tracks lazy-loading tool filtering (stubs stage).
type SessionToolDiscoveryStats struct {
	LazyLoadingEvents    int     `json:"lazy_loading_events"`    // # of filtering passes
	ToolsSeen            int     `json:"tools_seen"`             // sum of tool_count across all passes
	ToolsKept            int     `json:"tools_kept"`             // sum of (tool_count - stub_count)
	StubsCreated         int     `json:"stubs_created"`          // sum of stub_count — tools replaced with lightweight stubs
	PhantomToolsInjected int     `json:"phantom_tools_injected"` // sum of phantom_count
	CacheHits            int     `json:"cache_hits"`
	OriginalTokens       int     `json:"original_tokens"`       // tokens before stub replacement
	CompressedTokens     int     `json:"compressed_tokens"`     // tokens after stub replacement
	TokensSavedByStubs   int     `json:"tokens_saved_by_stubs"` // tokens saved by the stubs stage
	CompressionRatio     float64 `json:"compression_ratio"`
}

// SessionToolSearchStats tracks gateway_search_tools invocations.
type SessionToolSearchStats struct {
	Calls            int `json:"calls"`
	OriginalTokens   int `json:"original_tokens"`
	CompressedTokens int `json:"compressed_tokens"`
	TokensSaved      int `json:"tokens_saved"`
}

// SessionExpandContextStats tracks expand_context phantom tool usage.
type SessionExpandContextStats struct {
	ShadowRefsCreated   int `json:"shadow_refs_created"`
	ExpandCallsFound    int `json:"expand_calls_found"`
	ExpandCallsNotFound int `json:"expand_calls_not_found"`
	PenaltyTokens       int `json:"penalty_tokens"`
}

// SessionPreemptiveStats tracks preemptive summarization (history compaction).
type SessionPreemptiveStats struct {
	Triggers         int `json:"triggers"`
	OriginalTokens   int `json:"original_tokens"`
	SummarizedTokens int `json:"summarized_tokens"`
	TokensSaved      int `json:"tokens_saved"`
}

// SessionTotalStats holds combined savings across all sources.
type SessionTotalStats struct {
	TokensSaved int `json:"tokens_saved"` // all sources minus expand penalty
}

// sessionStatsCounters holds all mutable counters protected by mu.
type sessionStatsCounters struct {
	// requests
	requestsTotal      int
	requestsMainAgent  int
	requestsSubagent   int
	requestsCompressed int

	// tool output
	toolOutputTotal      int
	toolOutputCompressed int
	toolOutputSmall      int
	toolOutputLarge      int
	toolOutputCacheHits  int
	toolOutputOrigTokens int
	toolOutputCompTokens int

	// tool discovery (lazy loading / stubs stage)
	lazyLoadingEvents  int
	toolsSeen          int
	stubsCreated       int
	phantomInjected    int
	lazyLoadCacheHits  int
	lazyLoadOrigTokens int
	lazyLoadCompTokens int

	// tool search
	toolSearchCalls      int
	toolSearchOrigTokens int
	toolSearchCompTokens int

	// expand context
	shadowRefsCreated   int
	expandCallsFound    int
	expandCallsNotFound int
	expandPenaltyTokens int

	// preemptive summarization
	preemptiveTriggers         int
	preemptiveOrigTokens       int
	preemptiveSummarizedTokens int
}

// SessionStatsTracker accumulates per-session stats and flushes to session_stats.json
// every tickInterval. Designed to be embedded in Tracker and updated from Log* methods.
type SessionStatsTracker struct {
	path         string
	tickInterval time.Duration

	mu           sync.Mutex
	sessionID    string
	sessionStart string
	counters     sessionStatsCounters

	dirty atomic.Bool

	stopCh   chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once
}

// NewSessionStatsTracker creates a tracker that writes to path every tickInterval.
// Returns nil if path is empty (feature disabled).
func NewSessionStatsTracker(path string, tickInterval time.Duration) *SessionStatsTracker {
	if path == "" {
		return nil
	}
	if tickInterval <= 0 {
		tickInterval = 3 * time.Second
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		log.Error().Err(err).Str("path", path).Msg("session_stats: failed to create directory")
		return nil
	}
	return &SessionStatsTracker{
		path:         path,
		tickInterval: tickInterval,
		stopCh:       make(chan struct{}),
	}
}

// Start begins the background flush goroutine. Safe to call on nil.
func (t *SessionStatsTracker) Start() {
	if t == nil {
		return
	}
	t.wg.Add(1)
	go t.run()
}

// Stop flushes a final snapshot and stops the background goroutine. Safe to call on nil.
func (t *SessionStatsTracker) Stop() {
	if t == nil {
		return
	}
	t.stopOnce.Do(func() {
		close(t.stopCh)
	})
	t.wg.Wait()
}

// SetSession sets the session ID and records session start time.
// Subsequent calls with the same ID are no-ops. Safe to call on nil.
func (t *SessionStatsTracker) SetSession(sessionID string) {
	if t == nil || sessionID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sessionID == sessionID {
		return
	}
	t.sessionID = sessionID
	if t.sessionStart == "" {
		t.sessionStart = time.Now().UTC().Format(time.RFC3339)
	}
	t.dirty.Store(true)
}

// RecordRequest increments request and expand-context counters from a completed event.
// Safe to call on nil.
func (t *SessionStatsTracker) RecordRequest(event *RequestEvent) {
	if t == nil || event == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counters.requestsTotal++
	if event.IsMainAgent {
		t.counters.requestsMainAgent++
	} else {
		t.counters.requestsSubagent++
	}
	if event.CompressionUsed {
		t.counters.requestsCompressed++
	}
	t.counters.shadowRefsCreated += event.ShadowRefsCreated
	t.counters.expandCallsFound += event.ExpandCallsFound
	t.counters.expandCallsNotFound += event.ExpandCallsNotFound
	t.counters.expandPenaltyTokens += event.ExpandPenaltyTokens
	if event.HistoryCompactionTriggered {
		t.counters.preemptiveTriggers++
	}
	t.dirty.Store(true)
}

// RecordToolOutput increments tool output compression counters.
// status is one of: "compressed", "passthrough_small", "passthrough_large", "cache_hit".
// Safe to call on nil.
func (t *SessionStatsTracker) RecordToolOutput(status string, origTokens, compTokens int, cacheHit bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counters.toolOutputTotal++
	switch status {
	case "compressed":
		t.counters.toolOutputCompressed++
	case "passthrough_small":
		t.counters.toolOutputSmall++
	case "passthrough_large":
		t.counters.toolOutputLarge++
	}
	if cacheHit {
		t.counters.toolOutputCacheHits++
	}
	t.counters.toolOutputOrigTokens += origTokens
	t.counters.toolOutputCompTokens += compTokens
	t.dirty.Store(true)
}

// RecordLazyLoading increments tool discovery (stubs stage) counters.
// Safe to call on nil.
func (t *SessionStatsTracker) RecordLazyLoading(toolCount, stubCount, phantomCount, origTokens, compTokens int, cacheHit bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counters.lazyLoadingEvents++
	t.counters.toolsSeen += toolCount
	t.counters.stubsCreated += stubCount
	t.counters.phantomInjected += phantomCount
	if cacheHit {
		t.counters.lazyLoadCacheHits++
	}
	t.counters.lazyLoadOrigTokens += origTokens
	t.counters.lazyLoadCompTokens += compTokens
	t.dirty.Store(true)
}

// RecordToolSearch increments tool search result compression counters.
// origTokens and compTokens are the end-to-end totals for the search call.
// Safe to call on nil.
func (t *SessionStatsTracker) RecordToolSearch(origTokens, compTokens int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counters.toolSearchCalls++
	t.counters.toolSearchOrigTokens += origTokens
	t.counters.toolSearchCompTokens += compTokens
	t.dirty.Store(true)
}

// RecordPreemptive increments preemptive summarization token counters.
// Safe to call on nil.
func (t *SessionStatsTracker) RecordPreemptive(origTokens, summarizedTokens int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counters.preemptiveOrigTokens += origTokens
	t.counters.preemptiveSummarizedTokens += summarizedTokens
	t.dirty.Store(true)
}

func (t *SessionStatsTracker) run() {
	defer t.wg.Done()
	ticker := time.NewTicker(t.tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-t.stopCh:
			t.flush() // final flush on shutdown
			return
		case <-ticker.C:
			t.flush()
		}
	}
}

// flush writes session_stats.json when dirty. Uses atomic rename for safe concurrent reads.
func (t *SessionStatsTracker) flush() {
	if !t.dirty.CompareAndSwap(true, false) {
		return
	}
	t.mu.Lock()
	snapshot := t.buildSnapshot()
	t.mu.Unlock()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.dirty.Store(true)
		log.Error().Err(err).Msg("session_stats: marshal failed")
		return
	}
	tmpPath := t.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		t.dirty.Store(true)
		log.Error().Err(err).Str("path", tmpPath).Msg("session_stats: write failed")
		return
	}
	if err := os.Rename(tmpPath, t.path); err != nil {
		t.dirty.Store(true)
		log.Error().Err(err).Str("path", t.path).Msg("session_stats: rename failed")
	}
}

// buildSnapshot builds a SessionStats from current counters. Caller must hold t.mu.
func (t *SessionStatsTracker) buildSnapshot() SessionStats {
	c := &t.counters
	var s SessionStats

	s.SessionID = t.sessionID
	s.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	s.SessionStart = t.sessionStart

	// Requests
	s.Requests.Total = c.requestsTotal
	s.Requests.MainAgent = c.requestsMainAgent
	s.Requests.Subagent = c.requestsSubagent
	s.Requests.Compressed = c.requestsCompressed
	s.Requests.Passthrough = c.requestsTotal - c.requestsCompressed

	// Tool output
	s.ToolOutput.TotalOutputs = c.toolOutputTotal
	s.ToolOutput.Compressed = c.toolOutputCompressed
	s.ToolOutput.PassthroughSmall = c.toolOutputSmall
	s.ToolOutput.PassthroughLarge = c.toolOutputLarge
	s.ToolOutput.CacheHits = c.toolOutputCacheHits
	s.ToolOutput.OriginalTokens = c.toolOutputOrigTokens
	s.ToolOutput.CompressedTokens = c.toolOutputCompTokens
	s.ToolOutput.TokensSaved = c.toolOutputOrigTokens - c.toolOutputCompTokens
	if c.toolOutputOrigTokens > 0 {
		s.ToolOutput.CompressionRatio = 1.0 - float64(c.toolOutputCompTokens)/float64(c.toolOutputOrigTokens)
	}

	// Tool discovery (stubs stage)
	s.ToolDiscovery.LazyLoadingEvents = c.lazyLoadingEvents
	s.ToolDiscovery.ToolsSeen = c.toolsSeen
	s.ToolDiscovery.ToolsKept = c.toolsSeen - c.stubsCreated
	s.ToolDiscovery.StubsCreated = c.stubsCreated
	s.ToolDiscovery.PhantomToolsInjected = c.phantomInjected
	s.ToolDiscovery.CacheHits = c.lazyLoadCacheHits
	s.ToolDiscovery.OriginalTokens = c.lazyLoadOrigTokens
	s.ToolDiscovery.CompressedTokens = c.lazyLoadCompTokens
	s.ToolDiscovery.TokensSavedByStubs = c.lazyLoadOrigTokens - c.lazyLoadCompTokens
	if c.lazyLoadOrigTokens > 0 {
		s.ToolDiscovery.CompressionRatio = 1.0 - float64(c.lazyLoadCompTokens)/float64(c.lazyLoadOrigTokens)
	}

	// Tool search
	s.ToolSearch.Calls = c.toolSearchCalls
	s.ToolSearch.OriginalTokens = c.toolSearchOrigTokens
	s.ToolSearch.CompressedTokens = c.toolSearchCompTokens
	s.ToolSearch.TokensSaved = c.toolSearchOrigTokens - c.toolSearchCompTokens

	// Expand context
	s.ExpandContext.ShadowRefsCreated = c.shadowRefsCreated
	s.ExpandContext.ExpandCallsFound = c.expandCallsFound
	s.ExpandContext.ExpandCallsNotFound = c.expandCallsNotFound
	s.ExpandContext.PenaltyTokens = c.expandPenaltyTokens

	// Preemptive summarization
	s.Preemptive.Triggers = c.preemptiveTriggers
	s.Preemptive.OriginalTokens = c.preemptiveOrigTokens
	s.Preemptive.SummarizedTokens = c.preemptiveSummarizedTokens
	s.Preemptive.TokensSaved = c.preemptiveOrigTokens - c.preemptiveSummarizedTokens

	// Totals: sum all sources, subtract expand penalty
	toolOutputSaved := c.toolOutputOrigTokens - c.toolOutputCompTokens
	lazyLoadSaved := c.lazyLoadOrigTokens - c.lazyLoadCompTokens
	searchSaved := c.toolSearchOrigTokens - c.toolSearchCompTokens
	preemptiveSaved := c.preemptiveOrigTokens - c.preemptiveSummarizedTokens
	s.Totals.TokensSaved = max(0, toolOutputSaved+lazyLoadSaved+searchSaved+preemptiveSaved-c.expandPenaltyTokens)

	return s
}
