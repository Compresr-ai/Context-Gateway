// Package monitoring - search_log.go tracks gateway_search_tools calls in memory.
package monitoring

import "time"

const maxSearchLogEntries = 100

// SearchLogEntry records a single gateway_search_tools call.
type SearchLogEntry struct {
	Timestamp     time.Time `json:"timestamp"`
	SessionID     string    `json:"session_id,omitempty"`
	RequestID     string    `json:"request_id"`
	Query         string    `json:"query"`
	DeferredCount int       `json:"deferred_count"` // Total deferred tools available
	ResultsCount  int       `json:"results_count"`  // Number of tools returned
	ToolsFound    []string  `json:"tools_found"`    // Names of tools found
	Strategy      string    `json:"strategy"`       // "api", "regex", etc.
}

// SearchLog keeps a ring buffer of recent gateway_search_tools events.
type SearchLog struct {
	buf *RingBuffer[SearchLogEntry]
}

// NewSearchLog creates a new search log.
func NewSearchLog() *SearchLog {
	return &SearchLog{buf: NewRingBuffer[SearchLogEntry](maxSearchLogEntries)}
}

// Reset clears all entries so the log starts fresh for a new session.
func (l *SearchLog) Reset() { l.buf.Reset() }

// Record adds a gateway_search_tools event to the log.
func (l *SearchLog) Record(entry SearchLogEntry) { l.buf.Record(entry) }

// Recent returns the most recent N entries (newest first).
func (l *SearchLog) Recent(n int) []SearchLogEntry { return l.buf.Recent(n) }

// Count returns the total number of logged searches.
func (l *SearchLog) Count() int { return l.buf.Count() }

// SearchSummary provides aggregate stats for search operations.
type SearchSummary struct {
	Total           int `json:"total"`            // Total search calls
	Queries         int `json:"queries"`          // Unique queries (same as total for now)
	ToolsDiscovered int `json:"tools_discovered"` // Total tools found across all searches
}

// Summary returns a brief summary for inline display.
func (l *SearchLog) Summary() SearchSummary {
	entries := l.buf.All()
	s := SearchSummary{Total: len(entries), Queries: len(entries)}
	for _, e := range entries {
		s.ToolsDiscovered += e.ResultsCount
	}
	return s
}

// RecentForSession returns the most recent N entries for a specific session (newest first).
func (l *SearchLog) RecentForSession(sessionID string, n int) []SearchLogEntry {
	return l.buf.RecentWhere(n, func(e SearchLogEntry) bool { return e.SessionID == sessionID })
}

// SummaryForSession returns a summary for a specific session.
func (l *SearchLog) SummaryForSession(sessionID string) SearchSummary {
	entries := l.buf.All()
	var s SearchSummary
	for _, e := range entries {
		if e.SessionID == sessionID {
			s.Total++
			s.Queries++
			s.ToolsDiscovered += e.ResultsCount
		}
	}
	return s
}
