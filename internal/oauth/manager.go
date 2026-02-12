package oauth

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// TokenManager handles OAuth token lifecycle with thread-safe access
// and automatic background refresh.
type TokenManager struct {
	credentials *ClaudeCredentials
	mu          sync.RWMutex
	stopCh      chan struct{}
	running     bool
}

// Global singleton for the token manager.
var (
	globalManager *TokenManager
	globalOnce    sync.Once
)

// Global returns the global TokenManager instance.
// Creates one on first call.
func Global() *TokenManager {
	globalOnce.Do(func() {
		globalManager = NewTokenManager()
	})
	return globalManager
}

// NewTokenManager creates a new TokenManager.
func NewTokenManager() *TokenManager {
	return &TokenManager{
		stopCh: make(chan struct{}),
	}
}

// Initialize loads OAuth credentials and performs initial refresh if needed.
// Returns error if credentials cannot be loaded or refreshed.
// Returns nil with empty credentials if no OAuth credentials are available.
func (m *TokenManager) Initialize() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	creds, err := LoadClaudeCredentials()
	if err != nil {
		return fmt.Errorf("failed to load OAuth credentials: %w", err)
	}

	if creds == nil {
		// No credentials found - this is OK, just means OAuth is not available
		return nil
	}

	// Refresh if needed
	if creds.NeedsRefresh() {
		if creds.RefreshToken == "" {
			return fmt.Errorf("OAuth token expired and no refresh token available")
		}
		refreshed, err := RefreshAccessToken(creds.RefreshToken)
		if err != nil {
			return fmt.Errorf("failed to refresh OAuth token: %w", err)
		}
		creds = refreshed
	}

	m.credentials = creds
	return nil
}

// HasCredentials returns true if valid OAuth credentials are available.
func (m *TokenManager) HasCredentials() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.credentials != nil && m.credentials.AccessToken != ""
}

// GetAccessToken returns the current access token.
// Returns empty string if no credentials are available.
func (m *TokenManager) GetAccessToken() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.credentials == nil {
		return ""
	}
	return m.credentials.AccessToken
}

// GetCredentials returns a copy of the current credentials.
// Returns nil if no credentials are available.
func (m *TokenManager) GetCredentials() *ClaudeCredentials {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.credentials == nil {
		return nil
	}

	// Return a copy to prevent external modification
	creds := *m.credentials
	creds.Scopes = append([]string(nil), m.credentials.Scopes...)
	return &creds
}

// StartBackgroundRefresh starts a goroutine that periodically checks
// and refreshes the token before it expires.
func (m *TokenManager) StartBackgroundRefresh(ctx context.Context) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.stopCh = make(chan struct{})
	m.mu.Unlock()

	go m.backgroundRefreshLoop(ctx)
}

// StopBackgroundRefresh stops the background refresh goroutine.
func (m *TokenManager) StopBackgroundRefresh() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		close(m.stopCh)
		m.running = false
	}
}

// backgroundRefreshLoop periodically checks and refreshes the token.
func (m *TokenManager) backgroundRefreshLoop(ctx context.Context) {
	// Check every minute
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.refreshIfNeeded()
		}
	}
}

// refreshIfNeeded checks if the token needs refresh and refreshes it.
func (m *TokenManager) refreshIfNeeded() {
	m.mu.RLock()
	creds := m.credentials
	m.mu.RUnlock()

	if creds == nil || !creds.NeedsRefresh() {
		return
	}

	if creds.RefreshToken == "" {
		return
	}

	refreshed, err := RefreshAccessToken(creds.RefreshToken)
	if err != nil {
		// Log error but continue - we'll try again next tick
		fmt.Printf("[oauth] Background refresh failed: %v\n", err)
		return
	}

	m.mu.Lock()
	m.credentials = refreshed
	m.mu.Unlock()
}

// ForceRefresh forces a token refresh regardless of expiry status.
// Useful after receiving a 401 from the API.
func (m *TokenManager) ForceRefresh() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.credentials == nil || m.credentials.RefreshToken == "" {
		return fmt.Errorf("no refresh token available")
	}

	refreshed, err := RefreshAccessToken(m.credentials.RefreshToken)
	if err != nil {
		return err
	}

	m.credentials = refreshed
	return nil
}
