// Package monitoring - expand_log.go tracks expand_context calls in memory.
package monitoring

import (
	"time"
)

const maxExpandLogEntries = 100

// ExpandLogEntry records a single expand_context call.
type ExpandLogEntry struct {
	Timestamp      time.Time `json:"timestamp"`
	SessionID      string    `json:"session_id,omitempty"`
	RequestID      string    `json:"request_id"`
	ShadowID       string    `json:"shadow_id"`
	Found          bool      `json:"found"`
	ContentPreview string    `json:"content_preview,omitempty"` // First 100 chars
	ContentLength  int       `json:"content_length"`            // Byte length for debugging
	ContentTokens  int       `json:"content_tokens,omitempty"`  // Tiktoken count
}

// ExpandLog keeps a ring buffer of recent expand_context events.
type ExpandLog struct {
	buf *RingBuffer[ExpandLogEntry]
}

// NewExpandLog creates a new expand log.
func NewExpandLog() *ExpandLog {
	return &ExpandLog{buf: NewRingBuffer[ExpandLogEntry](maxExpandLogEntries)}
}

// Reset clears all entries so the log starts fresh for a new session.
func (l *ExpandLog) Reset() { l.buf.Reset() }

// Record adds an expand_context event to the log.
func (l *ExpandLog) Record(entry ExpandLogEntry) { l.buf.Record(entry) }

// Recent returns the most recent N entries (newest first).
func (l *ExpandLog) Recent(n int) []ExpandLogEntry { return l.buf.Recent(n) }

// ExpandSummary is a brief summary of expand_context activity.
type ExpandSummary struct {
	Total    int `json:"total"`
	Found    int `json:"found"`
	NotFound int `json:"not_found"`
}

// Summary returns a brief summary for inline display.
func (l *ExpandLog) Summary() ExpandSummary {
	entries := l.buf.All()
	s := ExpandSummary{Total: len(entries)}
	for _, e := range entries {
		if e.Found {
			s.Found++
		} else {
			s.NotFound++
		}
	}
	return s
}

// RecentForSession returns the most recent N entries for a specific session (newest first).
func (l *ExpandLog) RecentForSession(sessionID string, n int) []ExpandLogEntry {
	return l.buf.RecentWhere(n, func(e ExpandLogEntry) bool { return e.SessionID == sessionID })
}

// SummaryForRequest returns a summary and total content tokens for a request.
func (l *ExpandLog) SummaryForRequest(requestID string) (ExpandSummary, int) {
	entries := l.buf.All()
	var s ExpandSummary
	var totalContentTokens int
	for _, e := range entries {
		if e.RequestID == requestID {
			s.Total++
			if e.Found {
				s.Found++
				totalContentTokens += e.ContentTokens
			} else {
				s.NotFound++
			}
		}
	}
	return s, totalContentTokens
}

// SummaryForSession returns a summary for a specific session.
func (l *ExpandLog) SummaryForSession(sessionID string) ExpandSummary {
	entries := l.buf.All()
	var s ExpandSummary
	for _, e := range entries {
		if e.SessionID == sessionID {
			s.Total++
			if e.Found {
				s.Found++
			} else {
				s.NotFound++
			}
		}
	}
	return s
}

// GetExpandSummary returns expand context stats for the TUI status bar.
// Implements tui.ExpandSource interface.
func (l *ExpandLog) GetExpandSummary() (total int, found int, notFound int) {
	s := l.Summary()
	return s.Total, s.Found, s.NotFound
}
