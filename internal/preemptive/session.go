// Session management for preemptive summarization.
//
// Sessions are identified by hashing conversation content to create a stable ID.
// This allows the stateless proxy to recognize the same conversation across requests.
package preemptive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Session represents a conversation session.
type Session struct {
	ID          string       `json:"id"`
	State       SessionState `json:"state"`
	CreatedAt   time.Time    `json:"created_at"`
	LastUpdated time.Time    `json:"last_updated"`
	Model       string       `json:"model"`

	// Token tracking
	LastKnownTokens  int     `json:"last_known_tokens"`
	MaxContextTokens int     `json:"max_context_tokens"`
	UsagePercent     float64 `json:"usage_percent"`

	// Summary data
	Summary             string     `json:"summary,omitempty"`
	SummaryTokens       int        `json:"summary_tokens"`
	SummaryMessageIndex int        `json:"summary_message_index"` // Messages 0..N summarized
	SummaryMessageCount int        `json:"summary_message_count"` // Total when summary created
	SummaryTriggeredAt  *time.Time `json:"summary_triggered_at,omitempty"`
	SummaryCompletedAt  *time.Time `json:"summary_completed_at,omitempty"`
	SummaryUsedAt       *time.Time `json:"summary_used_at,omitempty"`
	CompactionUseCount  int        `json:"compaction_use_count"`
}

// SessionManager manages conversation sessions.
type SessionManager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
	config   SessionConfig
}

// NewSessionManager creates a new session manager.
func NewSessionManager(cfg SessionConfig) *SessionManager {
	sm := &SessionManager{
		sessions: make(map[string]*Session),
		config:   cfg,
	}
	go sm.cleanup()
	return sm
}

// GenerateSessionID creates a stable session ID from conversation messages.
//
// HIERARCHICAL APPROACH:
// 1. Primary: Hash the FIRST USER MESSAGE only (stable - the original task never changes)
// 2. This avoids issues with system prompts changing (Claude Code modifies them dynamically)
//
// For subagents or edge cases without user messages, returns "" and caller should use fuzzy matching.
func (sm *SessionManager) GenerateSessionID(messages []json.RawMessage) string {
	if len(messages) == 0 {
		return ""
	}

	// Find the FIRST user message - this is the task identifier that never changes
	for _, msg := range messages {
		var parsed map[string]interface{}
		if err := json.Unmarshal(msg, &parsed); err != nil {
			continue
		}

		role, ok := parsed["role"].(string)
		if !ok {
			continue
		}

		if role == "user" {
			// Hash the entire first user message (content + any metadata)
			h := sha256.New()
			canonical, _ := json.Marshal(parsed)
			h.Write(canonical)
			return hex.EncodeToString(h.Sum(nil))[:32]
		}
	}

	// No user message found (likely a subagent) - return empty for fuzzy matching fallback
	return ""
}

// GenerateSessionIDLegacy creates a session ID using the old N-message hash approach.
// Kept for backward compatibility and as a secondary fallback.
func (sm *SessionManager) GenerateSessionIDLegacy(messages []json.RawMessage) string {
	if len(messages) == 0 {
		return ""
	}

	count := sm.config.HashMessageCount
	if count > len(messages) {
		count = len(messages)
	}

	h := sha256.New()
	for i := 0; i < count; i++ {
		var msg map[string]interface{}
		if err := json.Unmarshal(messages[i], &msg); err != nil {
			h.Write(messages[i])
		} else {
			canonical, _ := json.Marshal(msg)
			h.Write(canonical)
		}
		h.Write([]byte("|"))
	}

	return hex.EncodeToString(h.Sum(nil))[:32]
}

// GetOrCreateSession retrieves an existing session or creates a new one.
func (sm *SessionManager) GetOrCreateSession(sessionID, model string, maxTokens int) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if s, ok := sm.sessions[sessionID]; ok {
		s.LastUpdated = time.Now()
		return s
	}

	s := &Session{
		ID:               sessionID,
		State:            StateIdle,
		CreatedAt:        time.Now(),
		LastUpdated:      time.Now(),
		MaxContextTokens: maxTokens,
		Model:            model,
	}
	sm.sessions[sessionID] = s
	return s
}

// Get retrieves a session by ID.
func (sm *SessionManager) Get(sessionID string) *Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.sessions[sessionID]
}

// Update updates a session with a function.
func (sm *SessionManager) Update(sessionID string, fn func(*Session)) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	fn(s)
	s.LastUpdated = time.Now()
	return nil
}

// SetSummaryReady marks a session's summary as ready.
func (sm *SessionManager) SetSummaryReady(sessionID, summary string, tokens, lastIndex, messageCount int) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.sessions[sessionID]
	if !ok {
		return nil // Not an error - session may have been cleaned up
	}

	now := time.Now()
	s.State = StateReady
	s.Summary = summary
	s.SummaryTokens = tokens
	s.SummaryCompletedAt = &now
	s.SummaryMessageIndex = lastIndex
	s.SummaryMessageCount = messageCount
	s.CompactionUseCount = 0
	s.LastUpdated = now
	return nil
}

// MarkUsed marks the session's summary as used (deprecated).
// Use IncrementUseCount instead to keep summary available.
func (sm *SessionManager) MarkUsed(sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.sessions[sessionID]
	if !ok {
		return
	}

	s.CompactionUseCount++
	if s.SummaryUsedAt == nil {
		now := time.Now()
		s.SummaryUsedAt = &now
	}
	s.State = StateUsed
	s.LastUpdated = time.Now()
}

// IncrementUseCount increments the compaction use counter without changing state.
// This keeps the summary in StateReady, allowing multiple compaction requests
// to reuse the same precomputed summary.
func (sm *SessionManager) IncrementUseCount(sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.sessions[sessionID]
	if !ok {
		return
	}

	s.CompactionUseCount++
	if s.SummaryUsedAt == nil {
		now := time.Now()
		s.SummaryUsedAt = &now
	}
	// Keep State as StateReady - summary remains available
	s.LastUpdated = time.Now()
}

// Reset resets a session to idle state.
func (sm *SessionManager) Reset(sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if s, ok := sm.sessions[sessionID]; ok {
		sm.resetSessionLocked(s)
	}
}

func (sm *SessionManager) resetSessionLocked(s *Session) {
	s.State = StateIdle
	s.Summary = ""
	s.SummaryTokens = 0
	s.SummaryTriggeredAt = nil
	s.SummaryCompletedAt = nil
	s.SummaryMessageIndex = 0
	s.SummaryMessageCount = 0
	s.SummaryUsedAt = nil
	s.CompactionUseCount = 0
	s.LastUpdated = time.Now()
}

// Stats returns session statistics.
func (sm *SessionManager) Stats() map[string]interface{} {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	states := make(map[SessionState]int)
	for _, s := range sm.sessions {
		states[s.State]++
	}

	return map[string]interface{}{
		"total_sessions": len(sm.sessions),
		"by_state":       states,
	}
}

// =============================================================================
// FUZZY MATCHING - For subagents and edge cases
// =============================================================================

// FuzzyMatchResult contains the result of a fuzzy session match.
type FuzzyMatchResult struct {
	Session    *Session
	MatchType  string  // "exact", "message_count", "recent", "none"
	Confidence float64 // 0.0 to 1.0
}

// FindBestMatchingSession finds a session that likely matches the current conversation.
// Used as a fallback when hash-based matching fails (e.g., subagents, system prompt changes).
//
// MATCHING STRATEGY (in priority order):
// 1. Exact ID match (if sessionID provided)
// 2. Recent session with ready summary + similar message count + same model
// 3. Most recent session with ready summary for the same model
//
// Returns nil if no suitable match is found.
func (sm *SessionManager) FindBestMatchingSession(messageCount int, model string, excludeSessionID string) *FuzzyMatchResult {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var bestMatch *Session
	var bestScore float64
	var matchType string

	now := time.Now()

	for _, s := range sm.sessions {
		// Skip if this is the session we're explicitly excluding
		if s.ID == excludeSessionID {
			continue
		}

		// Only consider sessions with ready or pending summaries
		if s.State != StateReady && s.State != StatePending {
			continue
		}

		// Must be same model family (ignore version differences)
		if !isSameModelFamily(s.Model, model) {
			continue
		}

		// Must be recent (within 30 minutes)
		age := now.Sub(s.LastUpdated)
		if age > 30*time.Minute {
			continue
		}

		// Calculate match score
		score := sm.calculateMatchScore(s, messageCount, age)

		if score > bestScore {
			bestScore = score
			bestMatch = s
			matchType = sm.determineMatchType(s, messageCount, age)
		}
	}

	// Only return if confidence is high enough
	if bestScore >= 0.5 && bestMatch != nil {
		return &FuzzyMatchResult{
			Session:    bestMatch,
			MatchType:  matchType,
			Confidence: bestScore,
		}
	}

	return nil
}

// calculateMatchScore computes a similarity score for a potential session match.
func (sm *SessionManager) calculateMatchScore(s *Session, messageCount int, age time.Duration) float64 {
	// Recency score: 1.0 at 0 min, 0.0 at 30 min
	recencyScore := 1.0 - (age.Minutes() / 30.0)
	if recencyScore < 0 {
		recencyScore = 0
	}

	// Message count similarity
	// The compaction request typically has MORE messages than when summary was triggered
	// (user continued working after trigger)
	countDiff := messageCount - s.SummaryMessageCount

	var countScore float64
	if countDiff >= 0 && countDiff <= 30 {
		// Reasonable growth: good match
		countScore = 1.0 - (float64(countDiff) / 50.0)
	} else if countDiff > 30 && countDiff <= 100 {
		// Large growth but possible
		countScore = 0.5 - (float64(countDiff-30) / 140.0)
	} else if countDiff < 0 && countDiff >= -10 {
		// Slight decrease (messages pruned) - acceptable
		countScore = 0.7
	} else {
		// Too different
		countScore = 0.0
	}

	if countScore < 0 {
		countScore = 0
	}

	// State bonus: prefer ready over in-progress
	stateBonus := 0.0
	if s.State == StateReady {
		stateBonus = 0.2
	}

	// Combined score (recency is most important)
	return recencyScore*0.5 + countScore*0.3 + stateBonus
}

// determineMatchType categorizes how the match was made.
func (sm *SessionManager) determineMatchType(s *Session, messageCount int, age time.Duration) string {
	countDiff := absInt(messageCount - s.SummaryMessageCount)

	if countDiff <= 5 && age < 5*time.Minute {
		return "strong_match"
	} else if countDiff <= 20 && age < 15*time.Minute {
		return "good_match"
	} else if age < 10*time.Minute {
		return "recent_match"
	} else {
		return "weak_match"
	}
}

// isSameModelFamily checks if two model names refer to the same model family.
// e.g., "claude-opus-4-6" and "claude-opus-4-20250514" are the same family.
func isSameModelFamily(model1, model2 string) bool {
	// Quick exact match
	if model1 == model2 {
		return true
	}

	// Extract family name (e.g., "claude-opus" from "claude-opus-4-6")
	family1 := extractModelFamily(model1)
	family2 := extractModelFamily(model2)

	return family1 == family2 && family1 != ""
}

// extractModelFamily extracts the base family from a model name.
func extractModelFamily(model string) string {
	// Common patterns: claude-opus-4-6, claude-sonnet-4-5-20250929, claude-haiku-4-5-20251001
	families := []string{"claude-opus", "claude-sonnet", "claude-haiku", "gpt-4", "gpt-3.5"}
	for _, family := range families {
		if len(model) >= len(family) && model[:len(family)] == family {
			return family
		}
	}
	return model // Return full name if no family detected
}

// absInt returns absolute value of an integer.
func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// =============================================================================
// ADDITIONAL API METHODS (aliases and extensions)
// =============================================================================

// GetSession is an alias for Get.
func (sm *SessionManager) GetSession(sessionID string) *Session {
	return sm.Get(sessionID)
}

// UpdateSession is an alias for Update.
func (sm *SessionManager) UpdateSession(sessionID string, fn func(*Session)) error {
	return sm.Update(sessionID, fn)
}

// MarkSummaryUsed marks the session's summary as used and returns an error if not found.
func (sm *SessionManager) MarkSummaryUsed(sessionID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	s.CompactionUseCount++
	if s.SummaryUsedAt == nil {
		now := time.Now()
		s.SummaryUsedAt = &now
	}
	s.State = StateUsed
	s.LastUpdated = time.Now()
	return nil
}

// ResetSession resets a session to idle state.
func (sm *SessionManager) ResetSession(sessionID string) {
	sm.Reset(sessionID)
}

// DeleteSession removes a session completely.
func (sm *SessionManager) DeleteSession(sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.sessions, sessionID)
}

// IsSummaryValidForMessages checks if the summary is valid for the given message count.
// Returns true if the session exists, has a ready/used state, and message count hasn't increased.
func (sm *SessionManager) IsSummaryValidForMessages(sessionID string, messageCount int) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	s, ok := sm.sessions[sessionID]
	if !ok {
		return false
	}

	// Summary must be ready or used
	if s.State != StateReady && s.State != StateUsed {
		return false
	}

	// Valid if current message count is same or fewer than when summary was created
	return messageCount <= s.SummaryMessageCount
}

// InvalidateSummaryIfNewMessages invalidates the summary if new messages have arrived.
// Returns true if the summary was invalidated.
func (sm *SessionManager) InvalidateSummaryIfNewMessages(sessionID string, messageCount int) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	s, ok := sm.sessions[sessionID]
	if !ok {
		return false
	}

	// Only invalidate if new messages arrived
	if messageCount > s.SummaryMessageCount {
		sm.resetSessionLocked(s)
		return true
	}

	return false
}

// cleanup periodically removes expired sessions.
func (sm *SessionManager) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		sm.mu.Lock()
		now := time.Now()
		for id, s := range sm.sessions {
			if now.Sub(s.LastUpdated) > sm.config.SummaryTTL {
				delete(sm.sessions, id)
			}
		}
		sm.mu.Unlock()
	}
}
