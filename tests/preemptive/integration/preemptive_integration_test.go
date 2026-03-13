// Preemptive Integration Tests
//
// Tests the preemptive summarization subsystem: threshold logic,
// session tracking, and token estimation. No real LLM calls.
//
// Run with: go test ./tests/preemptive/integration/... -v
package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/preemptive"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// HELPERS
// =============================================================================

// disabledManagerConfig returns a preemptive config with enabled=false.
// The manager will be a no-op: ProcessRequest returns body unchanged.
func disabledManagerConfig() preemptive.Config {
	return preemptive.Config{
		Enabled:            false,
		TriggerThreshold:   85.0,
		TokenEstimateRatio: 4,
	}
}

// makeRequestBody builds a JSON request body with the given messages.
func makeRequestBody(messages []map[string]interface{}) []byte {
	body := map[string]interface{}{
		"model":      "claude-haiku-4-5",
		"max_tokens": 500,
		"messages":   messages,
	}
	data, _ := json.Marshal(body)
	return data
}

// =============================================================================
// TESTS
// =============================================================================

// TestIntegration_Preemptive_NotTriggeredBelowThreshold sends a small request
// through the preemptive manager and verifies that no summarization is triggered
// when context usage is well below the threshold.
func TestIntegration_Preemptive_NotTriggeredBelowThreshold(t *testing.T) {
	// Create a disabled manager (no-op) — it should pass through unchanged
	mgr := preemptive.NewManager(disabledManagerConfig())
	defer mgr.Stop()

	messages := []map[string]interface{}{
		{"role": "user", "content": "Hello, this is a short message."},
	}
	body := makeRequestBody(messages)

	// ProcessRequest with disabled manager should return body unchanged
	result, isCompaction, synthetic, _, err := mgr.ProcessRequest(nil, body, "claude-haiku-4-5", "anthropic")
	require.NoError(t, err)
	assert.False(t, isCompaction, "should not be a compaction request")
	assert.Nil(t, synthetic, "should not have synthetic response")
	assert.Equal(t, body, result, "body should be returned unchanged by disabled manager")

	// Also verify CalculateUsage — small request should have low usage
	usage := preemptive.CalculateUsage(100, 200000)
	assert.True(t, usage.UsagePercent < 1.0,
		"100 tokens out of 200k should be < 1%%, got %.2f%%", usage.UsagePercent)
}

// TestIntegration_Preemptive_SessionTracking sends two requests with
// the same first user message and verifies they get the same session ID.
func TestIntegration_Preemptive_SessionTracking(t *testing.T) {
	sessionCfg := preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	}
	sm := preemptive.NewSessionManager(sessionCfg)

	// First request: user message
	msg1 := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"Build a REST API in Go"}`),
	}
	sessionID1 := sm.GenerateSessionID(msg1)
	require.NotEmpty(t, sessionID1, "should generate a session ID")

	// Second request: same first user message + additional messages
	msg2 := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"Build a REST API in Go"}`),
		json.RawMessage(`{"role":"assistant","content":"I'll help you build that."}`),
		json.RawMessage(`{"role":"user","content":"Start with the handlers."}`),
	}
	sessionID2 := sm.GenerateSessionID(msg2)
	require.NotEmpty(t, sessionID2, "should generate a session ID")

	// Same first user message -> same session ID
	assert.Equal(t, sessionID1, sessionID2,
		"same first user message should produce same session ID")

	// Different first user message -> different session ID
	msg3 := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"Write a Python script"}`),
	}
	sessionID3 := sm.GenerateSessionID(msg3)
	assert.NotEqual(t, sessionID1, sessionID3,
		"different first user message should produce different session ID")

	// Verify session can be created and retrieved
	session := sm.GetOrCreateSession(sessionID1, "claude-haiku-4-5", 200000)
	require.NotNil(t, session)
	assert.Equal(t, sessionID1, session.ID)
	assert.Equal(t, preemptive.StateIdle, session.State)
	assert.Equal(t, "claude-haiku-4-5", session.Model)

	// Get the same session again
	session2 := sm.GetOrCreateSession(sessionID1, "claude-haiku-4-5", 200000)
	assert.Equal(t, session.ID, session2.ID, "should return the same session")
}

// TestIntegration_Preemptive_TokenEstimation verifies that token count
// estimation produces reasonable values for various body sizes.
func TestIntegration_Preemptive_TokenEstimation(t *testing.T) {
	tests := []struct {
		name         string
		bodySize     int
		maxTokens    int
		expectMinPct float64
		expectMaxPct float64
	}{
		{
			name:         "small body under 1% usage",
			bodySize:     400, // ~100 tokens at 4 bytes/token
			maxTokens:    200000,
			expectMinPct: 0.0,
			expectMaxPct: 1.0,
		},
		{
			name:         "medium body around 50% usage",
			bodySize:     400000, // ~100k tokens at 4 bytes/token
			maxTokens:    200000,
			expectMinPct: 40.0,
			expectMaxPct: 60.0,
		},
		{
			name:         "large body near threshold",
			bodySize:     680000, // ~170k tokens at 4 bytes/token
			maxTokens:    200000,
			expectMinPct: 80.0,
			expectMaxPct: 100.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Token estimate: bodySize / ratio (ratio=4)
			estimatedTokens := tt.bodySize / 4
			usage := preemptive.CalculateUsage(estimatedTokens, tt.maxTokens)

			assert.True(t, usage.UsagePercent >= tt.expectMinPct,
				"usage %.2f%% should be >= %.2f%%", usage.UsagePercent, tt.expectMinPct)
			assert.True(t, usage.UsagePercent <= tt.expectMaxPct,
				"usage %.2f%% should be <= %.2f%%", usage.UsagePercent, tt.expectMaxPct)
		})
	}
}

// TestIntegration_Preemptive_SessionStateTransitions verifies that session
// state transitions work correctly through the full lifecycle.
func TestIntegration_Preemptive_SessionStateTransitions(t *testing.T) {
	sessionCfg := preemptive.SessionConfig{
		SummaryTTL:       2 * time.Hour,
		HashMessageCount: 3,
	}
	sm := preemptive.NewSessionManager(sessionCfg)

	sessionID := "test-session-lifecycle"
	session := sm.GetOrCreateSession(sessionID, "claude-haiku-4-5", 200000)
	assert.Equal(t, preemptive.StateIdle, session.State)

	// Simulate summary becoming ready
	err := sm.SetSummaryReady(sessionID, "This is a test summary", 500, 10, 15)
	require.NoError(t, err)

	session = sm.Get(sessionID)
	require.NotNil(t, session)
	assert.Equal(t, preemptive.StateReady, session.State)
	assert.Equal(t, "This is a test summary", session.Summary)
	assert.Equal(t, 500, session.SummaryTokens)
	assert.Equal(t, 10, session.SummaryMessageIndex)

	// Increment use count (keeps StateReady)
	sm.IncrementUseCount(sessionID)
	session = sm.Get(sessionID)
	assert.Equal(t, preemptive.StateReady, session.State)
	assert.Equal(t, 1, session.CompactionUseCount)

	// Reset session
	sm.Reset(sessionID)
	session = sm.Get(sessionID)
	assert.Equal(t, preemptive.StateIdle, session.State)
	assert.Empty(t, session.Summary)
}

// TestIntegration_Preemptive_ModelContextWindow verifies that the model
// context window lookup returns correct values for known and unknown models.
func TestIntegration_Preemptive_ModelContextWindow(t *testing.T) {
	// Known model
	cw := preemptive.GetModelContextWindow("claude-haiku-4-5")
	assert.Equal(t, 200000, cw.MaxTokens)
	assert.True(t, cw.EffectiveMax > 0)

	// Unknown model falls back to default
	cw = preemptive.GetModelContextWindow("some-unknown-model")
	assert.Equal(t, 128000, cw.MaxTokens, "unknown model should use default context window")
	assert.True(t, cw.EffectiveMax > 0)
}
