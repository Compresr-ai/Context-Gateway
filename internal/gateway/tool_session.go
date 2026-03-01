// Tool session management for hybrid tool discovery.
//
// DESIGN: Stores deferred tools (filtered out) and expanded tools (retrieved via search)
// per session. Used to enable the LLM to request tools that were filtered out.
//
// FLOW:
//  1. Tool discovery pipe filters tools, stores deferred tools in session
//  2. Gateway injects gateway_search_tools into the tools list
//  3. If LLM calls gateway_search_tools, gateway searches deferred tools
//  4. Matching tools are marked as "expanded" and included in subsequent requests
//
// Session ID is derived from the first user message hash (same as preemptive summarization).
package gateway

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/compresr/context-gateway/internal/adapters"
)

// ToolSession stores deferred and expanded tools for a single session.
type ToolSession struct {
	SessionID      string
	DeferredTools  []adapters.ExtractedContent // Tools filtered out by relevance scoring
	ExpandedTools  map[string]bool             // Tool names that were searched and found
	CreatedAt      time.Time
	LastAccessedAt time.Time
}

// ToolSessionStore manages tool sessions with automatic TTL cleanup.
type ToolSessionStore struct {
	sessions map[string]*ToolSession
	mu       sync.RWMutex
	ttl      time.Duration
}

// NewToolSessionStore creates a new tool session store.
func NewToolSessionStore(ttl time.Duration) *ToolSessionStore {
	if ttl == 0 {
		ttl = time.Hour // Default 1 hour TTL
	}
	store := &ToolSessionStore{
		sessions: make(map[string]*ToolSession),
		ttl:      ttl,
	}
	// Start background cleanup
	go store.cleanupLoop()
	return store
}

// Get retrieves a session by ID (returns nil if not found).
func (s *ToolSessionStore) Get(sessionID string) *ToolSession {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, ok := s.sessions[sessionID]
	if !ok {
		return nil
	}
	return session
}

// StoreDeferred stores deferred tools for a session.
func (s *ToolSessionStore) StoreDeferred(sessionID string, deferred []adapters.ExtractedContent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	if !ok {
		session = &ToolSession{
			SessionID:      sessionID,
			ExpandedTools:  make(map[string]bool),
			CreatedAt:      time.Now(),
			LastAccessedAt: time.Now(),
		}
		s.sessions[sessionID] = session
	}
	session.DeferredTools = deferred
	session.LastAccessedAt = time.Now()
}

// GetDeferred retrieves deferred tools for a session.
// Note: Does not update LastAccessedAt to avoid write under read lock.
// The session will be refreshed when StoreDeferred or MarkExpanded is called.
func (s *ToolSessionStore) GetDeferred(sessionID string) []adapters.ExtractedContent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, ok := s.sessions[sessionID]
	if !ok {
		return nil
	}
	// Return a copy to avoid races
	result := make([]adapters.ExtractedContent, len(session.DeferredTools))
	copy(result, session.DeferredTools)
	return result
}

// MarkExpanded marks tools as expanded (found via search).
func (s *ToolSessionStore) MarkExpanded(sessionID string, toolNames []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	if !ok {
		session = &ToolSession{
			SessionID:      sessionID,
			ExpandedTools:  make(map[string]bool),
			CreatedAt:      time.Now(),
			LastAccessedAt: time.Now(),
		}
		s.sessions[sessionID] = session
	}
	for _, name := range toolNames {
		session.ExpandedTools[name] = true
	}
	session.LastAccessedAt = time.Now()
}

// GetExpanded retrieves expanded tool names for a session.
func (s *ToolSessionStore) GetExpanded(sessionID string) map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, ok := s.sessions[sessionID]
	if !ok {
		return nil
	}
	// Return a copy to avoid races
	result := make(map[string]bool, len(session.ExpandedTools))
	for k, v := range session.ExpandedTools {
		result[k] = v
	}
	return result
}

// cleanupLoop periodically removes expired sessions.
func (s *ToolSessionStore) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		s.cleanup()
	}
}

// cleanup removes expired sessions.
func (s *ToolSessionStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-s.ttl)
	for id, session := range s.sessions {
		if session.LastAccessedAt.Before(cutoff) {
			delete(s.sessions, id)
		}
	}
}

// =============================================================================
// TOOL SEARCH
// =============================================================================

// SearchDeferredTools searches deferred tools by query.
// Returns top matches sorted by relevance score.
func SearchDeferredTools(deferred []adapters.ExtractedContent, query string, maxResults int) []adapters.ExtractedContent {
	if len(deferred) == 0 || query == "" {
		return nil
	}

	if maxResults <= 0 {
		maxResults = 5
	}

	queryLower := strings.ToLower(query)
	queryWords := tokenizeSearch(queryLower)

	type scored struct {
		tool  adapters.ExtractedContent
		score int
	}

	results := make([]scored, 0)
	for _, tool := range deferred {
		toolText := strings.ToLower(tool.ToolName + " " + tool.Content)
		score := 0

		// Exact name match (highest weight)
		toolNameLower := strings.ToLower(tool.ToolName)
		if strings.Contains(queryLower, toolNameLower) {
			score += 100
		}

		// Word overlap
		for _, word := range queryWords {
			if len(word) >= 3 && strings.Contains(toolText, word) {
				score += 10
			}
		}

		if score > 0 {
			results = append(results, scored{tool, score})
		}
	}

	// Sort by score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	// Return top N
	top := make([]adapters.ExtractedContent, 0, maxResults)
	for i := 0; i < len(results) && i < maxResults; i++ {
		top = append(top, results[i].tool)
	}
	return top
}

// tokenizeSearch splits text into words for search matching.
func tokenizeSearch(text string) []string {
	words := strings.FieldsFunc(text, func(r rune) bool {
		isLowerAlpha := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		if isLowerAlpha || isDigit || r == '_' || r == '-' {
			return false
		}
		return true
	})
	return words
}
