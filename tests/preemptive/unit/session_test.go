package preemptive_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/preemptive"
)

// =============================================================================
// SESSION MANAGER TESTS
// =============================================================================

func TestSessionManager_GenerateSessionID_DeterministicHashing(t *testing.T) {
	cfg := preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	}
	sm := preemptive.NewSessionManager(cfg)

	messages := []json.RawMessage{
		json.RawMessage(`{"role": "user", "content": "Hello"}`),
		json.RawMessage(`{"role": "assistant", "content": "Hi there"}`),
		json.RawMessage(`{"role": "user", "content": "How are you?"}`),
	}

	// Same messages should produce same ID
	id1 := sm.GenerateSessionID(messages)
	id2 := sm.GenerateSessionID(messages)

	assert.Equal(t, id1, id2, "same messages should produce same session ID")
	assert.Len(t, id1, 32, "session ID should be 32 characters")
}

func TestSessionManager_GenerateSessionID_DifferentMessages(t *testing.T) {
	cfg := preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	}
	sm := preemptive.NewSessionManager(cfg)

	messages1 := []json.RawMessage{
		json.RawMessage(`{"role": "user", "content": "Hello"}`),
	}
	messages2 := []json.RawMessage{
		json.RawMessage(`{"role": "user", "content": "Goodbye"}`),
	}

	id1 := sm.GenerateSessionID(messages1)
	id2 := sm.GenerateSessionID(messages2)

	assert.NotEqual(t, id1, id2, "different messages should produce different session IDs")
}

func TestSessionManager_GenerateSessionID_EmptyMessages(t *testing.T) {
	cfg := preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	}
	sm := preemptive.NewSessionManager(cfg)

	id := sm.GenerateSessionID([]json.RawMessage{})
	assert.Empty(t, id, "empty messages should return empty session ID")
}

func TestSessionManager_GenerateSessionID_FewerThanHashCount(t *testing.T) {
	cfg := preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 5, // Want 5 messages for hashing
	}
	sm := preemptive.NewSessionManager(cfg)

	// Only provide 2 messages
	messages := []json.RawMessage{
		json.RawMessage(`{"role": "user", "content": "Hello"}`),
		json.RawMessage(`{"role": "assistant", "content": "Hi"}`),
	}

	id := sm.GenerateSessionID(messages)
	assert.NotEmpty(t, id, "should generate ID with fewer messages than hash count")
	assert.Len(t, id, 32)
}

func TestSessionManager_GetOrCreateSession_New(t *testing.T) {
	sm := preemptive.NewSessionManager(preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	})

	session := sm.GetOrCreateSession("session-123", "claude-sonnet-4-5", 200000)

	require.NotNil(t, session)
	assert.Equal(t, "session-123", session.ID)
	assert.Equal(t, preemptive.StateIdle, session.State)
	assert.Equal(t, "claude-sonnet-4-5", session.Model)
	assert.Equal(t, 200000, session.MaxContextTokens)
}

func TestSessionManager_GetOrCreateSession_Existing(t *testing.T) {
	sm := preemptive.NewSessionManager(preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	})

	// Create session
	session1 := sm.GetOrCreateSession("session-123", "claude-sonnet-4-5", 200000)
	session1.LastKnownTokens = 50000 // Modify

	// Get same session
	session2 := sm.GetOrCreateSession("session-123", "claude-sonnet-4-5", 200000)

	assert.Equal(t, session1, session2, "should return same session instance")
	assert.Equal(t, 50000, session2.LastKnownTokens, "modifications should persist")
}

func TestSessionManager_GetSession_NotFound(t *testing.T) {
	sm := preemptive.NewSessionManager(preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	})

	session := sm.GetSession("nonexistent")
	assert.Nil(t, session)
}

func TestSessionManager_UpdateSession(t *testing.T) {
	sm := preemptive.NewSessionManager(preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	})

	// Create session
	sm.GetOrCreateSession("session-123", "model", 200000)

	// Update session
	err := sm.UpdateSession("session-123", func(s *preemptive.Session) {
		s.LastKnownTokens = 100000
		s.UsagePercent = 50.0
	})

	require.NoError(t, err)

	// Verify update
	session := sm.GetSession("session-123")
	assert.Equal(t, 100000, session.LastKnownTokens)
	assert.Equal(t, 50.0, session.UsagePercent)
}

func TestSessionManager_UpdateSession_NotFound(t *testing.T) {
	sm := preemptive.NewSessionManager(preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	})

	err := sm.UpdateSession("nonexistent", func(s *preemptive.Session) {})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSessionManager_SetSummaryReady(t *testing.T) {
	sm := preemptive.NewSessionManager(preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	})

	sm.GetOrCreateSession("session-123", "model", 200000)

	err := sm.SetSummaryReady("session-123", "This is the summary", 500, 10, 15) // 15 total messages
	require.NoError(t, err)

	session := sm.GetSession("session-123")
	assert.Equal(t, preemptive.StateReady, session.State)
	assert.Equal(t, "This is the summary", session.Summary)
	assert.Equal(t, 500, session.SummaryTokens)
	assert.Equal(t, 10, session.SummaryMessageIndex)
	assert.Equal(t, 15, session.SummaryMessageCount)
	assert.NotNil(t, session.SummaryCompletedAt)
}

func TestSessionManager_MarkSummaryUsed(t *testing.T) {
	sm := preemptive.NewSessionManager(preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	})

	sm.GetOrCreateSession("session-123", "model", 200000)
	sm.SetSummaryReady("session-123", "Summary", 100, 5, 10)

	err := sm.MarkSummaryUsed("session-123")
	require.NoError(t, err)

	session := sm.GetSession("session-123")
	assert.Equal(t, preemptive.StateUsed, session.State)
}

func TestSessionManager_ResetSession(t *testing.T) {
	sm := preemptive.NewSessionManager(preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	})

	sm.GetOrCreateSession("session-123", "model", 200000)
	sm.SetSummaryReady("session-123", "Summary", 100, 5, 10)

	sm.ResetSession("session-123")

	session := sm.GetSession("session-123")
	assert.Equal(t, preemptive.StateIdle, session.State)
	assert.Empty(t, session.Summary)
	assert.Zero(t, session.SummaryTokens)
	assert.Zero(t, session.SummaryMessageIndex)
	assert.Zero(t, session.SummaryMessageCount)
	assert.Zero(t, session.CompactionUseCount)
	assert.Nil(t, session.SummaryTriggeredAt)
	assert.Nil(t, session.SummaryCompletedAt)
}

func TestSessionManager_DeleteSession(t *testing.T) {
	sm := preemptive.NewSessionManager(preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	})

	sm.GetOrCreateSession("session-123", "model", 200000)
	assert.NotNil(t, sm.GetSession("session-123"))

	sm.DeleteSession("session-123")
	assert.Nil(t, sm.GetSession("session-123"))
}

func TestSessionManager_Stats(t *testing.T) {
	sm := preemptive.NewSessionManager(preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	})

	// Create sessions in different states
	sm.GetOrCreateSession("session-1", "model", 200000)
	sm.GetOrCreateSession("session-2", "model", 200000)
	sm.SetSummaryReady("session-2", "Summary", 100, 5, 10)
	sm.GetOrCreateSession("session-3", "model", 200000)

	stats := sm.Stats()

	assert.Equal(t, 3, stats["total_sessions"])
	byState := stats["by_state"].(map[preemptive.SessionState]int)
	assert.Equal(t, 2, byState[preemptive.StateIdle])
	assert.Equal(t, 1, byState[preemptive.StateReady])
}

// =============================================================================
// SESSION STATE TRANSITIONS
// =============================================================================

func TestSessionStateTransition_IdleToPending(t *testing.T) {
	sm := preemptive.NewSessionManager(preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	})

	session := sm.GetOrCreateSession("session-123", "model", 200000)
	assert.Equal(t, preemptive.StateIdle, session.State)

	err := sm.UpdateSession("session-123", func(s *preemptive.Session) {
		s.State = preemptive.StatePending
		now := time.Now()
		s.SummaryTriggeredAt = &now
	})
	require.NoError(t, err)

	session = sm.GetSession("session-123")
	assert.Equal(t, preemptive.StatePending, session.State)
	assert.NotNil(t, session.SummaryTriggeredAt)
}

func TestSessionStateTransition_PendingToReady(t *testing.T) {
	sm := preemptive.NewSessionManager(preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	})

	sm.GetOrCreateSession("session-123", "model", 200000)
	sm.UpdateSession("session-123", func(s *preemptive.Session) {
		s.State = preemptive.StatePending
	})

	err := sm.SetSummaryReady("session-123", "Generated summary", 500, 15, 20)
	require.NoError(t, err)

	session := sm.GetSession("session-123")
	assert.Equal(t, preemptive.StateReady, session.State)
	assert.Equal(t, "Generated summary", session.Summary)
}

func TestSessionStateTransition_ReadyToUsed(t *testing.T) {
	sm := preemptive.NewSessionManager(preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	})

	sm.GetOrCreateSession("session-123", "model", 200000)
	sm.SetSummaryReady("session-123", "Summary", 100, 5, 10)

	err := sm.MarkSummaryUsed("session-123")
	require.NoError(t, err)

	session := sm.GetSession("session-123")
	assert.Equal(t, preemptive.StateUsed, session.State)
	// Summary should still be preserved
	assert.Equal(t, "Summary", session.Summary)
	// CompactionUseCount should be incremented
	assert.Equal(t, 1, session.CompactionUseCount)
}

// =============================================================================
// SUMMARY VALIDITY AND INVALIDATION
// =============================================================================

func TestSessionManager_IsSummaryValidForMessages(t *testing.T) {
	sm := preemptive.NewSessionManager(preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	})

	sm.GetOrCreateSession("session-123", "model", 200000)
	sm.SetSummaryReady("session-123", "Summary", 100, 5, 10) // Created with 10 messages

	// Should be valid for same message count
	assert.True(t, sm.IsSummaryValidForMessages("session-123", 10))

	// Should be valid for fewer messages (shouldn't happen but safe)
	assert.True(t, sm.IsSummaryValidForMessages("session-123", 8))

	// Should be invalid for more messages (new messages arrived)
	assert.False(t, sm.IsSummaryValidForMessages("session-123", 12))

	// Should be invalid for non-existent session
	assert.False(t, sm.IsSummaryValidForMessages("non-existent", 10))
}

func TestSessionManager_IsSummaryValidForMessages_AfterUsed(t *testing.T) {
	sm := preemptive.NewSessionManager(preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	})

	sm.GetOrCreateSession("session-123", "model", 200000)
	sm.SetSummaryReady("session-123", "Summary", 100, 5, 10)

	// Mark as used
	sm.MarkSummaryUsed("session-123")

	// Should still be valid for same message count (key feature!)
	assert.True(t, sm.IsSummaryValidForMessages("session-123", 10))

	// Can use it multiple times
	sm.MarkSummaryUsed("session-123")
	assert.True(t, sm.IsSummaryValidForMessages("session-123", 10))
}

func TestSessionManager_InvalidateSummaryIfNewMessages(t *testing.T) {
	sm := preemptive.NewSessionManager(preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	})

	sm.GetOrCreateSession("session-123", "model", 200000)
	sm.SetSummaryReady("session-123", "Summary", 100, 5, 10)
	sm.MarkSummaryUsed("session-123")

	// Same message count - should NOT invalidate
	invalidated := sm.InvalidateSummaryIfNewMessages("session-123", 10)
	assert.False(t, invalidated)

	session := sm.GetSession("session-123")
	assert.Equal(t, preemptive.StateUsed, session.State)
	assert.Equal(t, "Summary", session.Summary)

	// More messages - should invalidate
	invalidated = sm.InvalidateSummaryIfNewMessages("session-123", 12)
	assert.True(t, invalidated)

	session = sm.GetSession("session-123")
	assert.Equal(t, preemptive.StateIdle, session.State)
	assert.Empty(t, session.Summary)
	assert.Zero(t, session.SummaryMessageCount)
	assert.Zero(t, session.CompactionUseCount)
}

func TestSessionManager_MultipleCompactionsWithoutNewMessages(t *testing.T) {
	sm := preemptive.NewSessionManager(preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	})

	sm.GetOrCreateSession("session-123", "model", 200000)
	sm.SetSummaryReady("session-123", "Summary", 100, 5, 10)

	// Simulate multiple /compact requests
	for i := 0; i < 5; i++ {
		// Check validity
		assert.True(t, sm.IsSummaryValidForMessages("session-123", 10))

		// Mark as used
		sm.MarkSummaryUsed("session-123")
	}

	session := sm.GetSession("session-123")
	assert.Equal(t, 5, session.CompactionUseCount)
	assert.Equal(t, "Summary", session.Summary) // Summary still available!
}
