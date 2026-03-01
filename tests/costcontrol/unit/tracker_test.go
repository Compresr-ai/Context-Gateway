package unit

import (
	"sync"
	"testing"

	"github.com/compresr/context-gateway/internal/costcontrol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTracker_DisabledAlwaysAllows(t *testing.T) {
	tracker := costcontrol.NewTracker(costcontrol.CostControlConfig{
		Enabled:    false,
		SessionCap: 1.0,
	})

	// Record some usage
	tracker.RecordUsage("session1", "claude-opus-4-6", 1_000_000, 100_000, 0, 0)

	// Budget check should always allow when disabled
	result := tracker.CheckBudget("session1")
	assert.True(t, result.Allowed)
	assert.Greater(t, result.CurrentCost, 0.0)
}

func TestTracker_ZeroCapAlwaysAllows(t *testing.T) {
	tracker := costcontrol.NewTracker(costcontrol.CostControlConfig{
		Enabled:    true,
		SessionCap: 0,
	})

	tracker.RecordUsage("session1", "claude-opus-4-6", 1_000_000, 100_000, 0, 0)

	result := tracker.CheckBudget("session1")
	assert.True(t, result.Allowed)
}

func TestTracker_BudgetCheckUnderCap(t *testing.T) {
	tracker := costcontrol.NewTracker(costcontrol.CostControlConfig{
		Enabled:    true,
		SessionCap: 100.0, // $100 cap
		GlobalCap:  1000.0,
	})

	// Small usage, well under cap
	tracker.RecordUsage("session1", "claude-haiku-4-5", 1000, 500, 0, 0)

	result := tracker.CheckBudget("session1")
	assert.True(t, result.Allowed)
	assert.Greater(t, result.CurrentCost, 0.0)
	assert.Equal(t, 100.0, result.Cap)
}

func TestTracker_BudgetCheckOverCap(t *testing.T) {
	tracker := costcontrol.NewTracker(costcontrol.CostControlConfig{
		Enabled:    true,
		SessionCap: 0.001, // Very small cap: $0.001
	})

	// Record large usage to exceed cap
	tracker.RecordUsage("session1", "claude-opus-4-6", 1_000_000, 100_000, 0, 0)

	result := tracker.CheckBudget("session1")
	assert.False(t, result.Allowed)
	assert.Greater(t, result.CurrentCost, 0.001)
}

func TestTracker_CostAccumulation(t *testing.T) {
	tracker := costcontrol.NewTracker(costcontrol.CostControlConfig{})

	tracker.RecordUsage("session1", "claude-sonnet-4-5", 1000, 500, 0, 0)
	cost1 := tracker.GetSessionCost("session1")

	tracker.RecordUsage("session1", "claude-sonnet-4-5", 1000, 500, 0, 0)
	cost2 := tracker.GetSessionCost("session1")

	assert.InDelta(t, cost1*2, cost2, 0.0001)
}

func TestTracker_AllSessions(t *testing.T) {
	tracker := costcontrol.NewTracker(costcontrol.CostControlConfig{
		SessionCap: 5.0,
		GlobalCap:  100.0,
	})

	tracker.RecordUsage("session1", "claude-sonnet-4-5", 1000, 500, 0, 0)
	tracker.RecordUsage("session2", "gpt-4o", 2000, 1000, 0, 0)

	sessions := tracker.AllSessions()
	require.Len(t, sessions, 2)

	// Check that both sessions are present
	ids := map[string]bool{}
	for _, s := range sessions {
		ids[s.ID] = true
		assert.Equal(t, 5.0, s.Cap)
		assert.Equal(t, 1, s.RequestCount)
		assert.Greater(t, s.Cost, 0.0)
	}
	assert.True(t, ids["session1"])
	assert.True(t, ids["session2"])
}

func TestTracker_SessionCapFallbackToGlobalCap(t *testing.T) {
	tracker := costcontrol.NewTracker(costcontrol.CostControlConfig{
		Enabled:    true,
		SessionCap: 0.005, // Legacy wizard behavior: interpreted as aggregate cap
		GlobalCap:  0,
	})

	// Each request costs ~$0.0035 on haiku.
	tracker.RecordUsage("s1", "claude-haiku-4-5", 1000, 500, 0, 0)
	result1 := tracker.CheckBudget("s1")
	assert.True(t, result1.Allowed)
	assert.Equal(t, 0.0, result1.Cap, "session cap should be normalized away")
	assert.InDelta(t, 0.005, result1.GlobalCap, 0.000001)

	tracker.RecordUsage("s2", "claude-haiku-4-5", 1000, 500, 0, 0)
	result2 := tracker.CheckBudget("s2")
	assert.False(t, result2.Allowed, "aggregate cap should block across sessions")
	assert.Greater(t, result2.GlobalCost, result2.GlobalCap)
}

func TestTracker_GlobalCapBlocks(t *testing.T) {
	tracker := costcontrol.NewTracker(costcontrol.CostControlConfig{
		Enabled:   true,
		GlobalCap: 0.001, // Very small global cap
	})

	// Record usage across two sessions
	tracker.RecordUsage("session1", "claude-opus-4-6", 500_000, 50_000, 0, 0)
	tracker.RecordUsage("session2", "claude-opus-4-6", 500_000, 50_000, 0, 0)

	// Both sessions should be blocked (global cap exceeded)
	result1 := tracker.CheckBudget("session1")
	assert.False(t, result1.Allowed)
	assert.Greater(t, result1.GlobalCost, 0.001)

	result2 := tracker.CheckBudget("session2")
	assert.False(t, result2.Allowed)

	// Even a new session should be blocked
	result3 := tracker.CheckBudget("session3")
	assert.False(t, result3.Allowed)
}

func TestTracker_GlobalCapAllows(t *testing.T) {
	tracker := costcontrol.NewTracker(costcontrol.CostControlConfig{
		Enabled:   true,
		GlobalCap: 1000.0, // Large global cap
	})

	tracker.RecordUsage("session1", "claude-haiku-4-5", 1000, 500, 0, 0)

	result := tracker.CheckBudget("session1")
	assert.True(t, result.Allowed)
	assert.Greater(t, result.GlobalCost, 0.0)
	assert.Equal(t, 1000.0, result.GlobalCap)
}

func TestTracker_GetGlobalCost(t *testing.T) {
	tracker := costcontrol.NewTracker(costcontrol.CostControlConfig{})

	tracker.RecordUsage("s1", "claude-sonnet-4-5", 1000, 500, 0, 0)
	tracker.RecordUsage("s2", "claude-sonnet-4-5", 1000, 500, 0, 0)

	globalCost := tracker.GetGlobalCost()
	s1Cost := tracker.GetSessionCost("s1")
	s2Cost := tracker.GetSessionCost("s2")
	assert.InDelta(t, s1Cost+s2Cost, globalCost, 0.0001)
}

func TestTracker_RecordUsageWithCacheTokens(t *testing.T) {
	tracker := costcontrol.NewTracker(costcontrol.CostControlConfig{})

	// Record without cache tokens: all 10000 input at full price
	tracker.RecordUsage("no-cache", "claude-sonnet-4-5", 10000, 1000, 0, 0)
	costWithout := tracker.GetSessionCost("no-cache")

	// Record with cache write: 5000 non-cached at 1x + 5000 at 1.25x = more expensive
	tracker.RecordUsage("with-cache-write", "claude-sonnet-4-5", 10000, 1000, 5000, 0)
	costWithWrite := tracker.GetSessionCost("with-cache-write")
	assert.Greater(t, costWithWrite, costWithout)

	// Record with cache read: 10000 non-cached at 1x + 5000 at 0.1x = more total input
	tracker.RecordUsage("with-cache-read", "claude-sonnet-4-5", 10000, 1000, 0, 5000)
	costWithRead := tracker.GetSessionCost("with-cache-read")
	assert.Greater(t, costWithRead, costWithout)

	// Cache write should be more expensive than cache read for same token count
	assert.Greater(t, costWithWrite, costWithRead)
}

func TestTracker_GetSessionCost_NotFound(t *testing.T) {
	tracker := costcontrol.NewTracker(costcontrol.CostControlConfig{})
	cost := tracker.GetSessionCost("nonexistent")
	assert.Equal(t, 0.0, cost)
}

func TestTracker_ConcurrentAccess(t *testing.T) {
	tracker := costcontrol.NewTracker(costcontrol.CostControlConfig{
		Enabled:    true,
		SessionCap: 1000.0,
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tracker.RecordUsage("concurrent-session", "claude-sonnet-4-5", 100, 50, 0, 0)
			tracker.CheckBudget("concurrent-session")
			tracker.GetSessionCost("concurrent-session")
		}()
	}
	wg.Wait()

	sessions := tracker.AllSessions()
	require.Len(t, sessions, 1)
	assert.Equal(t, 100, sessions[0].RequestCount)
}
