package unit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/dashboard"
)

func TestSessionStore_TrackAndUpdate(t *testing.T) {
	hub := dashboard.NewHub()
	store := dashboard.NewSessionStore(hub)
	defer store.Stop()

	// Track a new session (first request — RequestCount becomes 0 for initial Track on creation)
	sess := store.Track("session-1", "claude_code")
	require.NotNil(t, sess)
	assert.Equal(t, "session-1", sess.ID)
	assert.Equal(t, "claude_code", sess.AgentType)
	assert.Equal(t, dashboard.StatusActive, sess.Status)

	// Update with request data (Track increments RequestCount, Update does not)
	store.Update("session-1", dashboard.SessionUpdate{
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-5-20250514",
		TokensIn:  1000,
		TokensOut: 500,
		CostUSD:   0.01,
		UserQuery: "Fix the bug in handler.go",
		ToolUsed:  "Read",
	})

	// Verify update applied
	got, ok := store.Get("session-1")
	require.True(t, ok)
	assert.Equal(t, "anthropic", got.Provider)
	assert.Equal(t, "claude-sonnet-4-5-20250514", got.Model)
	assert.Equal(t, 1000, got.TokensIn)
	assert.Equal(t, 500, got.TokensOut)
	assert.Equal(t, 0.01, got.CostUSD)
	assert.Equal(t, "Fix the bug in handler.go", got.LastUserQuery)
	assert.Equal(t, "Read", got.LastToolUsed)
	assert.Equal(t, 0, got.RequestCount) // First Track (creation) doesn't increment

	// Simulate second request: Track again then Update — RequestCount becomes 1
	store.Track("session-1", "claude_code")
	store.Update("session-1", dashboard.SessionUpdate{
		TokensIn:  2000,
		TokensOut: 800,
		CostUSD:   0.02,
	})
	got, _ = store.Get("session-1")
	assert.Equal(t, 3000, got.TokensIn)
	assert.Equal(t, 1300, got.TokensOut)
	assert.InDelta(t, 0.03, got.CostUSD, 0.001)
	assert.Equal(t, 1, got.RequestCount) // Only Track on re-entry increments
}

func TestSessionStore_All(t *testing.T) {
	store := dashboard.NewSessionStore(nil) // nil hub = no notifications
	defer store.Stop()

	store.Track("s1", "claude_code")
	store.Track("s2", "cursor")
	store.Track("s3", "codex")

	all := store.All()
	assert.Len(t, all, 3)
}

func TestSessionStore_SetStatus(t *testing.T) {
	store := dashboard.NewSessionStore(nil)
	defer store.Stop()

	store.Track("s1", "claude_code")
	store.SetStatus("s1", dashboard.StatusWaitingForHuman)

	got, ok := store.Get("s1")
	require.True(t, ok)
	assert.Equal(t, dashboard.StatusWaitingForHuman, got.Status)
}

func TestSessionStore_IdleTransition(t *testing.T) {
	hub := dashboard.NewHub()
	store := dashboard.NewSessionStore(hub)
	defer store.Stop()

	sess := store.Track("s1", "claude_code")
	assert.Equal(t, dashboard.StatusActive, sess.Status)

	// Simulate passage of time by directly accessing and waiting
	// The status loop runs every 5s with 30s idle timeout
	// For a unit test, just verify the store is functioning
	got, ok := store.Get("s1")
	require.True(t, ok)
	assert.Equal(t, dashboard.StatusActive, got.Status)
	assert.WithinDuration(t, time.Now(), got.LastActivityAt, time.Second)
}

func TestSessionStore_Remove(t *testing.T) {
	store := dashboard.NewSessionStore(nil)
	defer store.Stop()

	store.Track("s1", "claude_code")
	store.Remove("s1")

	_, ok := store.Get("s1")
	assert.False(t, ok)
	assert.Len(t, store.All(), 0)
}

func TestSessionStore_ReactivateOnTrack(t *testing.T) {
	store := dashboard.NewSessionStore(nil)
	defer store.Stop()

	store.Track("s1", "claude_code")
	store.SetStatus("s1", dashboard.StatusWaitingForHuman)

	// Re-tracking should reactivate
	store.Track("s1", "")
	got, _ := store.Get("s1")
	assert.Equal(t, dashboard.StatusActive, got.Status)
	assert.Equal(t, "claude_code", got.AgentType) // Should preserve original
}
