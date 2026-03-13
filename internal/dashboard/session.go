// Package dashboard provides session tracking and a real-time monitoring dashboard.
//
// DESIGN: Tracks all active agent sessions flowing through the gateway.
// Each session represents a distinct agent connection (Claude Code, Cursor, etc.).
// Sessions are identified by a composite key derived from request characteristics.
//
// Session lifecycle:
//   - Created on first request from a new agent session
//   - Updated on every subsequent request/response
//   - Status transitions: active -> finished (based on activity timeout)
//   - "waiting_for_human" detected from response patterns (tool approval, questions)
//   - "waiting_for_human" persists until the human responds (no auto-finish)
//
// Thread-safe via sync.RWMutex. Hub notified on every state change.
package dashboard

import (
	"sync"
	"time"
)

// SessionStatus represents the current state of an agent session.
type SessionStatus string

const (
	StatusActive          SessionStatus = "active"
	StatusWaitingForHuman SessionStatus = "waiting_for_human"
	StatusFinished        SessionStatus = "finished"
)

// Session represents a single agent session flowing through the gateway.
type Session struct {
	ID        string        `json:"id"`
	AgentType string        `json:"agent_type"` // "claude_code", "cursor", "codex", etc.
	Provider  string        `json:"provider"`   // "anthropic", "openai", etc.
	Model     string        `json:"model"`
	Status    SessionStatus `json:"status"`

	// Activity tracking
	StartedAt      time.Time `json:"started_at"`
	LastActivityAt time.Time `json:"last_activity_at"`

	// Metrics
	RequestCount     int     `json:"request_count"`
	TokensIn         int     `json:"tokens_in"`
	TokensOut        int     `json:"tokens_out"`
	TokensSaved      int     `json:"tokens_saved"`
	CostUSD          float64 `json:"cost_usd"`
	CompressionCount int     `json:"compression_count"`

	// Context
	Summary       string `json:"summary"`         // Auto-generated summary of what the session is doing
	LastUserQuery string `json:"last_user_query"` // Last user message (for quick glance)
	LastToolUsed  string `json:"last_tool_used"`  // Last tool_use name
	WorkingDir    string `json:"working_dir"`     // Detected working directory (if available)

	// Instance identification (set by aggregation layer)
	GatewayPort int `json:"gateway_port,omitempty"`
}

// SessionUpdate is passed to SessionStore.Update to modify a session.
// Only non-zero fields are applied.
type SessionUpdate struct {
	Provider    string
	Model       string
	Status      SessionStatus
	TokensIn    int
	TokensOut   int
	TokensSaved int
	CostUSD     float64
	Compressed  bool
	UserQuery   string
	ToolUsed    string
	Summary     string
	WorkingDir  string
}

// SessionStore is a thread-safe store for active sessions.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	hub      *Hub // Notified on changes (may be nil)

	finishedTimeout time.Duration
	stopCh          chan struct{}
}

// NewSessionStore creates a session store with background status management.
func NewSessionStore(hub *Hub) *SessionStore {
	s := &SessionStore{
		sessions:        make(map[string]*Session),
		hub:             hub,
		finishedTimeout: 10 * time.Minute,
		stopCh:          make(chan struct{}),
	}
	go s.statusLoop()
	return s
}

// Track creates or updates a session on each request.
// Returns the session ID. agentType is detected from request headers.
func (s *SessionStore) Track(sessionID, agentType string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, exists := s.sessions[sessionID]
	if !exists {
		sess = &Session{
			ID:             sessionID,
			AgentType:      agentType,
			Status:         StatusActive,
			StartedAt:      time.Now(),
			LastActivityAt: time.Now(),
		}
		s.sessions[sessionID] = sess
		s.notifyUnlocked()
		return sess
	}

	// Reactivate if it was waiting for human or finished
	if sess.Status == StatusWaitingForHuman || sess.Status == StatusFinished {
		sess.Status = StatusActive
	}
	sess.RequestCount++
	sess.LastActivityAt = time.Now()
	if agentType != "" && sess.AgentType == "" {
		sess.AgentType = agentType
	}

	return sess
}

// Update applies an update to an existing session.
func (s *SessionStore) Update(sessionID string, u SessionUpdate) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[sessionID]
	if !ok {
		return
	}

	sess.LastActivityAt = time.Now()

	if u.Provider != "" {
		sess.Provider = u.Provider
	}
	if u.Model != "" {
		sess.Model = u.Model
	}
	if u.Status != "" {
		sess.Status = u.Status
	}
	if u.TokensIn > 0 {
		sess.TokensIn += u.TokensIn
	}
	if u.TokensOut > 0 {
		sess.TokensOut += u.TokensOut
	}
	// if u.TokensSaved > 0 {
	// 	sess.TokensSaved += u.TokensSaved
	// }
	if u.CostUSD > 0 {
		sess.CostUSD += u.CostUSD
	}
	if u.Compressed {
		sess.CompressionCount++
	}
	if u.UserQuery != "" {
		sess.LastUserQuery = truncate(u.UserQuery, 200)
	}
	if u.ToolUsed != "" {
		sess.LastToolUsed = u.ToolUsed
	}
	if u.Summary != "" {
		sess.Summary = u.Summary
	}
	if u.WorkingDir != "" {
		sess.WorkingDir = u.WorkingDir
	}

	s.notifyUnlocked()
}

// SetStatus explicitly sets a session's status (e.g., waiting_for_human).
func (s *SessionStore) SetStatus(sessionID string, status SessionStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sess, ok := s.sessions[sessionID]; ok {
		sess.Status = status
		s.notifyUnlocked()
	}
}

// All returns a snapshot of all sessions.
func (s *SessionStore) All() []Session {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		result = append(result, *sess)
	}
	return result
}

// Get returns a single session by ID.
func (s *SessionStore) Get(sessionID string) (Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sess, ok := s.sessions[sessionID]; ok {
		return *sess, true
	}
	return Session{}, false
}

// Remove deletes a session from the store.
func (s *SessionStore) Remove(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
	s.notifyUnlocked()
}

// Stop stops the background status loop.
func (s *SessionStore) Stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}

// statusLoop periodically transitions sessions to idle/finished based on inactivity.
func (s *SessionStore) statusLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.updateStatuses()
		}
	}
}

func (s *SessionStore) updateStatuses() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	changed := false

	for _, sess := range s.sessions {
		if sess.Status == StatusFinished || sess.Status == StatusWaitingForHuman {
			continue
		}

		idle := now.Sub(sess.LastActivityAt)

		// Only active sessions auto-finish after timeout.
		// WaitingForHuman sessions persist until the human responds.
		if sess.Status == StatusActive && idle > s.finishedTimeout {
			sess.Status = StatusFinished
			changed = true
		}
	}

	if changed {
		s.notifyUnlocked()
	}
}

// notifyUnlocked sends current state to the hub. Caller must hold at least a read lock.
func (s *SessionStore) notifyUnlocked() {
	if s.hub == nil {
		return
	}

	sessions := make([]Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, *sess)
	}
	s.hub.Broadcast(sessions)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
