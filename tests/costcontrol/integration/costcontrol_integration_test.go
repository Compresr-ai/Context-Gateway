// Cost Control Integration Tests
//
// Tests verify end-to-end cost tracking and budget enforcement using
// real Tracker instances with realistic token counts and model pricing.
package integration

import (
	"testing"

	"github.com/compresr/context-gateway/internal/costcontrol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_CostControl_TrackingAccumulates verifies that costs
// accumulate correctly across multiple requests within a single session.
func TestIntegration_CostControl_TrackingAccumulates(t *testing.T) {
	cfg := costcontrol.CostControlConfig{
		Enabled:    false, // Tracking only, no enforcement
		SessionCap: 0,
		GlobalCap:  0,
	}
	tracker := costcontrol.NewTracker(cfg)
	defer tracker.Close()

	sessionID := "session-accumulate-001"
	model := "claude-haiku-4-5" // $1/MTok input, $5/MTok output

	// Record 3 requests with known token counts
	tracker.RecordUsage(sessionID, model, 1000, 500, 0, 0)
	tracker.RecordUsage(sessionID, model, 2000, 1000, 0, 0)
	tracker.RecordUsage(sessionID, model, 500, 250, 0, 0)

	// Expected costs per request (per-million-token pricing):
	//   Request 1: (1000/1M * $1) + (500/1M * $5)  = $0.001 + $0.0025 = $0.0035
	//   Request 2: (2000/1M * $1) + (1000/1M * $5) = $0.002 + $0.005  = $0.007
	//   Request 3: (500/1M * $1) + (250/1M * $5)   = $0.0005 + $0.00125 = $0.00175
	//   Total: $0.01225
	expectedTotal := 0.0035 + 0.007 + 0.00175

	sessionCost := tracker.GetSessionCost(sessionID)
	assert.InDelta(t, expectedTotal, sessionCost, 1e-9, "session cost should accumulate across requests")

	globalCost := tracker.GetGlobalCost()
	assert.InDelta(t, expectedTotal, globalCost, 1e-9, "global cost should match session cost for single session")

	// Verify session snapshots reflect accumulated data
	sessions := tracker.AllSessions()
	require.Len(t, sessions, 1, "should have exactly 1 session")
	assert.Equal(t, sessionID, sessions[0].ID)
	assert.Equal(t, 3, sessions[0].RequestCount, "request count should be 3")
	assert.InDelta(t, expectedTotal, sessions[0].Cost, 1e-9)
}

// TestIntegration_CostControl_BudgetEnforced verifies that once a budget
// cap is exceeded, subsequent CheckBudget calls reject the session.
func TestIntegration_CostControl_BudgetEnforced(t *testing.T) {
	// Set a tight global cap of $0.01
	cfg := costcontrol.CostControlConfig{
		Enabled:    true,
		SessionCap: 0,
		GlobalCap:  0.01,
	}
	tracker := costcontrol.NewTracker(cfg)
	defer tracker.Close()

	sessionID := "session-budget-001"
	model := "claude-haiku-4-5" // $1/MTok input, $5/MTok output

	// First check: no cost yet, should be allowed
	result := tracker.CheckBudget(sessionID)
	assert.True(t, result.Allowed, "should be allowed before any spend")

	// Record usage that stays under the cap
	// (1000/1M * $1) + (500/1M * $5) = $0.0035
	tracker.RecordUsage(sessionID, model, 1000, 500, 0, 0)

	result = tracker.CheckBudget(sessionID)
	assert.True(t, result.Allowed, "should be allowed when under cap")
	assert.InDelta(t, 0.0035, result.GlobalCost, 1e-6)

	// Record usage that pushes over the cap
	// Additional: (3000/1M * $1) + (1500/1M * $5) = $0.003 + $0.0075 = $0.0105
	// Total: $0.0035 + $0.0105 = $0.014 > $0.01 cap
	tracker.RecordUsage(sessionID, model, 3000, 1500, 0, 0)

	result = tracker.CheckBudget(sessionID)
	assert.False(t, result.Allowed, "should be rejected when over global cap")
	assert.InDelta(t, 0.01, result.GlobalCap, 1e-9)
	assert.Greater(t, result.GlobalCost, result.GlobalCap, "global cost should exceed cap")
}

// TestIntegration_CostControl_SessionIsolation verifies that two sessions
// with different budgets are tracked independently.
func TestIntegration_CostControl_SessionIsolation(t *testing.T) {
	// Use a per-session cap (when global_cap=0 and session_cap>0, effectiveCaps
	// treats session_cap as global_cap for backward compat, so set both explicitly)
	cfg := costcontrol.CostControlConfig{
		Enabled:    true,
		SessionCap: 0.005, // Per-session cap
		GlobalCap:  1.0,   // High global cap so it doesn't interfere
	}
	tracker := costcontrol.NewTracker(cfg)
	defer tracker.Close()

	sessionA := "session-A"
	sessionB := "session-B"
	model := "claude-haiku-4-5"

	// Session A: record modest usage, should stay under cap
	// (500/1M * $1) + (200/1M * $5) = $0.0005 + $0.001 = $0.0015
	tracker.RecordUsage(sessionA, model, 500, 200, 0, 0)

	// Session B: record heavier usage, should exceed per-session cap
	// (2000/1M * $1) + (1000/1M * $5) = $0.002 + $0.005 = $0.007 > $0.005
	tracker.RecordUsage(sessionB, model, 2000, 1000, 0, 0)

	// Verify independent tracking
	costA := tracker.GetSessionCost(sessionA)
	costB := tracker.GetSessionCost(sessionB)
	assert.InDelta(t, 0.0015, costA, 1e-9, "session A cost should reflect its own usage")
	assert.InDelta(t, 0.007, costB, 1e-9, "session B cost should reflect its own usage")
	assert.NotEqual(t, costA, costB, "sessions should have different costs")

	// Both sessions visible in AllSessions
	sessions := tracker.AllSessions()
	require.Len(t, sessions, 2, "should have 2 sessions")

	// Verify budget checks are independent
	resultA := tracker.CheckBudget(sessionA)
	resultB := tracker.CheckBudget(sessionB)
	assert.True(t, resultA.Allowed, "session A should be under cap")
	assert.False(t, resultB.Allowed, "session B should exceed per-session cap")

	// Global cost should be the sum of both sessions
	expectedGlobal := costA + costB
	assert.InDelta(t, expectedGlobal, tracker.GetGlobalCost(), 1e-6, "global cost should be sum of all sessions")
}
