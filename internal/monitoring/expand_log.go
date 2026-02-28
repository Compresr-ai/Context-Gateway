// Package monitoring - expand_log.go tracks expand_context calls in memory.
//
// DESIGN: Ring buffer of recent expand_context events for dashboard display.
// Shows which tool outputs the model requested to see in full.
package monitoring

import (
	"sync"
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
	ContentLength  int       `json:"content_length"`
}

// ExpandLog keeps a ring buffer of recent expand_context events.
type ExpandLog struct {
	mu      sync.RWMutex
	entries []ExpandLogEntry
}

// NewExpandLog creates a new expand log.
func NewExpandLog() *ExpandLog {
	return &ExpandLog{
		entries: make([]ExpandLogEntry, 0, maxExpandLogEntries),
	}
}

// Record adds an expand_context event to the log.
func (l *ExpandLog) Record(entry ExpandLogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.entries) >= maxExpandLogEntries {
		// Shift: drop oldest
		copy(l.entries, l.entries[1:])
		l.entries[len(l.entries)-1] = entry
	} else {
		l.entries = append(l.entries, entry)
	}
}

// Recent returns the most recent N entries (newest first).
func (l *ExpandLog) Recent(n int) []ExpandLogEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if n <= 0 || len(l.entries) == 0 {
		return nil
	}
	if n > len(l.entries) {
		n = len(l.entries)
	}

	// Return newest first
	result := make([]ExpandLogEntry, n)
	for i := 0; i < n; i++ {
		result[i] = l.entries[len(l.entries)-1-i]
	}
	return result
}

// Count returns the total number of logged expansions.
func (l *ExpandLog) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.entries)
}

// Summary returns a brief summary for inline display.
func (l *ExpandLog) Summary() ExpandSummary {
	l.mu.RLock()
	defer l.mu.RUnlock()

	s := ExpandSummary{Total: len(l.entries)}
	for _, e := range l.entries {
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
	l.mu.RLock()
	defer l.mu.RUnlock()

	if n <= 0 || len(l.entries) == 0 {
		return nil
	}

	var result []ExpandLogEntry
	for i := len(l.entries) - 1; i >= 0 && len(result) < n; i-- {
		if l.entries[i].SessionID == sessionID {
			result = append(result, l.entries[i])
		}
	}
	return result
}

// SummaryForSession returns a summary for a specific session.
func (l *ExpandLog) SummaryForSession(sessionID string) ExpandSummary {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var s ExpandSummary
	for _, e := range l.entries {
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

// ExpandSummary is a brief summary of expand_context activity.
type ExpandSummary struct {
	Total    int `json:"total"`
	Found    int `json:"found"`
	NotFound int `json:"not_found"`
}

// GetExpandSummary returns expand context stats for the TUI status bar.
// Implements tui.ExpandSource interface.
func (l *ExpandLog) GetExpandSummary() (total int, found int, notFound int) {
	s := l.Summary()
	return s.Total, s.Found, s.NotFound
}
