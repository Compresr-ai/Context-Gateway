// Package dashboard provides session tracking and a real-time monitoring dashboard.
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
)

// autoTransitionTimeout is how long a session must have no in-flight requests
// while remaining active before it is automatically transitioned to waiting_for_human.
// This handles agents that have finished responding but did not produce a clean
// turn-boundary signal (e.g. first initialisation request, or unknown stop reason).
const autoTransitionTimeout = 2 * time.Second

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
	RequestCount          int     `json:"request_count"`
	MainAgentRequestCount int     `json:"main_agent_request_count"`
	UserTurnCount         int     `json:"user_turn_count"` // Human-initiated prompts only
	TokensIn              int     `json:"tokens_in"`
	TokensOut             int     `json:"tokens_out"`
	TokensSaved           int     `json:"tokens_saved"`
	CostUSD               float64 `json:"cost_usd"`
	CompressionCount      int     `json:"compression_count"`

	// Context
	Summary       string `json:"summary"`         // Auto-generated summary of what the session is doing
	LastUserQuery string `json:"last_user_query"` // Last user message (for quick glance)
	LastToolUsed  string `json:"last_tool_used"`  // Last tool_use name
	WorkingDir    string `json:"working_dir"`     // Detected working directory (if available)

	// Instance identification (set by aggregation layer)
	GatewayPort int `json:"gateway_port,omitempty"`

	// InFlightRequests counts requests currently being processed.
	// Not exposed in JSON — internal bookkeeping only.
	InFlightRequests int `json:"-"`
}

// SessionUpdate is passed to SessionStore.Update to modify a session.
// Only non-zero fields are applied.
type SessionUpdate struct {
	Provider          string
	Model             string
	Status            SessionStatus
	TokensIn          int
	TokensOut         int
	TokensSaved       int
	CostUSD           float64
	Compressed        bool
	IsNewUserTurn     bool // True when a human initiated this request
	IsMainAgent       bool // True when request is from the main agent (not subagent)
	IsRequestComplete bool // True when request processing is done (decrements InFlightRequests)
	UserQuery         string
	ToolUsed          string
	Summary           string
	WorkingDir        string
}

// SessionStore is a thread-safe store for active sessions.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	hub      *Hub // Notified on changes (may be nil)

	idleTimeout time.Duration // inactivity window before a session is removed
	stopCh      chan struct{}
}

// NewSessionStore creates a session store with background status management.
//
//   - idleTimeout: how long a session can have no requests before being removed.
//     Pass 0 to use the default (10 minutes). Track() on a new request recreates it.
//     Gateway shutdown (Stop()) removes all sessions immediately.
func NewSessionStore(hub *Hub, idleTimeout time.Duration) *SessionStore {
	if idleTimeout <= 0 {
		idleTimeout = 10 * time.Minute
	}
	s := &SessionStore{
		sessions:    make(map[string]*Session),
		hub:         hub,
		idleTimeout: idleTimeout,
		stopCh:      make(chan struct{}),
	}
	go s.statusLoop()
	return s
}

// Track creates or updates a session on each request.
// Returns the session. agentType is detected from request headers.
func (s *SessionStore) Track(sessionID, agentType string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, exists := s.sessions[sessionID]
	if !exists {
		sess = &Session{
			ID:               sessionID,
			AgentType:        agentType,
			Status:           StatusActive,
			StartedAt:        time.Now(),
			LastActivityAt:   time.Now(),
			RequestCount:     1,
			InFlightRequests: 1,
		}
		s.sessions[sessionID] = sess
		s.notifyUnlocked()
		return sess
	}

	reactivated := sess.Status == StatusWaitingForHuman
	if reactivated {
		sess.Status = StatusActive
	}
	sess.RequestCount++
	sess.InFlightRequests++
	sess.LastActivityAt = time.Now()
	if agentType != "" && sess.AgentType == "" {
		sess.AgentType = agentType
	}
	if reactivated {
		s.notifyUnlocked()
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
	if u.TokensSaved > 0 {
		sess.TokensSaved += u.TokensSaved
	}
	if u.CostUSD > 0 {
		sess.CostUSD += u.CostUSD
	}
	if u.IsNewUserTurn {
		sess.UserTurnCount++
	}
	if u.IsMainAgent {
		sess.MainAgentRequestCount++
	}
	if u.IsRequestComplete && sess.InFlightRequests > 0 {
		sess.InFlightRequests--
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

// SetStatus explicitly sets a session's status.
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

// Stop shuts down the background status loop and removes all sessions immediately.
// WebSocket clients receive the empty state synchronously before the store goes inactive.
func (s *SessionStore) Stop() {
	select {
	case <-s.stopCh:
		return // already stopped
	default:
		close(s.stopCh)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) > 0 {
		s.sessions = make(map[string]*Session)
		s.notifyUnlocked()
	}
}

// statusLoop removes sessions that have been idle longer than idleTimeout.
func (s *SessionStore) statusLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.removeExpired()
		}
	}
}

func (s *SessionStore) removeExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	changed := false
	for id, sess := range s.sessions {
		age := now.Sub(sess.LastActivityAt)
		if age > s.idleTimeout {
			delete(s.sessions, id)
			changed = true
			continue
		}
		// Auto-transition: if a session is active with no in-flight requests and
		// has been quiet longer than autoTransitionTimeout, move it to waiting_for_human.
		// This catches cases where the last request completed without emitting a clean
		// turn-boundary signal (e.g. first initialisation request, unknown stop reason).
		if sess.Status == StatusActive && sess.InFlightRequests == 0 && age > autoTransitionTimeout {
			sess.Status = StatusWaitingForHuman
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
