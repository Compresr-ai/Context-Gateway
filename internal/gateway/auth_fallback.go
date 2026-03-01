package gateway

import (
	"sync"
	"time"
)

const (
	defaultAuthFallbackTTL = time.Hour
)

// authFallbackStore keeps per-session auth mode for subscription->api key fallback.
type authFallbackStore struct {
	mu       sync.RWMutex
	sessions map[string]time.Time // session_id -> last fallback time
	ttl      time.Duration
	stopCh   chan struct{}
}

func newAuthFallbackStore(ttl time.Duration) *authFallbackStore {
	if ttl <= 0 {
		ttl = defaultAuthFallbackTTL
	}
	s := &authFallbackStore{
		sessions: make(map[string]time.Time),
		ttl:      ttl,
		stopCh:   make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

func (s *authFallbackStore) MarkAPIKeyMode(sessionID string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = time.Now()
}

func (s *authFallbackStore) ShouldUseAPIKeyMode(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	s.mu.RLock()
	t, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Since(t) > s.ttl {
		s.mu.Lock()
		delete(s.sessions, sessionID)
		s.mu.Unlock()
		return false
	}
	return true
}

func (s *authFallbackStore) cleanupLoop() {
	ticker := time.NewTicker(DefaultCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.cleanup()
		}
	}
}

// Stop stops the cleanup goroutine.
func (s *authFallbackStore) Stop() {
	close(s.stopCh)
}

func (s *authFallbackStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, t := range s.sessions {
		if now.Sub(t) > s.ttl {
			delete(s.sessions, id)
		}
	}
}
