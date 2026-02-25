package costcontrol

import (
	"sync"
	"sync/atomic"
	"time"
)

const sessionTTL = 24 * time.Hour

// Tracker tracks per-session API costs and enforces budget caps.
// Cost tracking is always active. Budget enforcement only applies
// when Enabled is true and at least one cap is configured.
type Tracker struct {
	config   CostControlConfig
	sessions map[string]*CostSession
	mu       sync.RWMutex

	// Atomic global cost accumulator for O(1) budget checks
	// Stored as cost * 1e9 (nano-dollars) to use atomic int64 ops
	globalCostNano int64
}

// NewTracker creates a new cost tracker. Starts a background cleanup goroutine.
func NewTracker(cfg CostControlConfig) *Tracker {
	t := &Tracker{
		config:   cfg,
		sessions: make(map[string]*CostSession),
	}
	go t.cleanup()
	return t
}

// CheckBudget checks whether a session can continue.
// Enforces both per-session cap and global cap when Enabled.
func (t *Tracker) CheckBudget(sessionID string) BudgetCheckResult {
	sessionCap, globalCap := t.effectiveCaps()

	t.mu.RLock()
	s := t.sessions[sessionID]
	sessionCost := 0.0
	if s != nil {
		sessionCost = s.Cost
	}
	t.mu.RUnlock()

	globalCost := float64(atomic.LoadInt64(&t.globalCostNano)) / 1e9

	// If not enforcing, always allow (still report costs)
	if !t.config.Enabled {
		return BudgetCheckResult{Allowed: true, CurrentCost: sessionCost, GlobalCost: globalCost, Cap: sessionCap, GlobalCap: globalCap}
	}

	// Check global cap first
	if globalCap > 0 && globalCost >= globalCap {
		return BudgetCheckResult{Allowed: false, CurrentCost: sessionCost, GlobalCost: globalCost, Cap: sessionCap, GlobalCap: globalCap}
	}

	// Check per-session cap
	if sessionCap > 0 && sessionCost >= sessionCap {
		return BudgetCheckResult{Allowed: false, CurrentCost: sessionCost, GlobalCost: globalCost, Cap: sessionCap, GlobalCap: globalCap}
	}

	return BudgetCheckResult{Allowed: true, CurrentCost: sessionCost, GlobalCost: globalCost, Cap: sessionCap, GlobalCap: globalCap}
}

// GetGlobalCost returns total accumulated cost across all sessions.
func (t *Tracker) GetGlobalCost() float64 {
	return float64(atomic.LoadInt64(&t.globalCostNano)) / 1e9
}

// RecordUsage records actual cost from token counts (non-streaming).
// cacheCreationTokens and cacheReadTokens are optional (Anthropic-specific).
func (t *Tracker) RecordUsage(sessionID, model string, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int) {
	pricing := GetModelPricing(model)
	var cost float64
	if cacheCreationTokens > 0 || cacheReadTokens > 0 {
		cost = CalculateCostWithCache(inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens, pricing)
	} else {
		cost = CalculateCost(inputTokens, outputTokens, pricing)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	s := t.getOrCreateLocked(sessionID, model)
	s.Cost += cost
	s.RequestCount++
	s.LastUpdated = time.Now()
	if model != "" {
		s.Model = model
	}

	costNano := int64(cost * 1e9)
	atomic.AddInt64(&t.globalCostNano, costNano)
}

// GetSessionCost returns accumulated cost for a session.
func (t *Tracker) GetSessionCost(sessionID string) float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if s, ok := t.sessions[sessionID]; ok {
		return s.Cost
	}
	return 0
}

// AllSessions returns a snapshot of all sessions for the dashboard.
func (t *Tracker) AllSessions() []CostSessionSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()

	sessionCap, _ := t.effectiveCaps()

	snapshots := make([]CostSessionSnapshot, 0, len(t.sessions))
	for _, s := range t.sessions {
		snapshots = append(snapshots, CostSessionSnapshot{
			ID:           s.ID,
			Cost:         s.Cost,
			Cap:          sessionCap,
			RequestCount: s.RequestCount,
			Model:        s.Model,
			CreatedAt:    s.CreatedAt,
			LastUpdated:  s.LastUpdated,
		})
	}
	return snapshots
}

// Config returns the tracker's config (for dashboard display).
func (t *Tracker) Config() CostControlConfig {
	cfg := t.config
	cfg.SessionCap, cfg.GlobalCap = t.effectiveCaps()
	return cfg
}

// effectiveCaps returns normalized session/global caps.
// Backward compatibility: historical wizard-generated configs stored "spend cap"
// in session_cap even though users expected an aggregate cap. If global_cap is
// unset and session_cap is set, treat it as a global cap.
func (t *Tracker) effectiveCaps() (sessionCap, globalCap float64) {
	sessionCap = t.config.SessionCap
	globalCap = t.config.GlobalCap
	if globalCap <= 0 && sessionCap > 0 {
		globalCap = sessionCap
		sessionCap = 0
	}
	return sessionCap, globalCap
}

func (t *Tracker) getOrCreateLocked(sessionID, model string) *CostSession {
	if s, ok := t.sessions[sessionID]; ok {
		return s
	}
	s := &CostSession{
		ID:          sessionID,
		Model:       model,
		CreatedAt:   time.Now(),
		LastUpdated: time.Now(),
	}
	t.sessions[sessionID] = s
	return s
}

func (t *Tracker) cleanup() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		t.mu.Lock()
		now := time.Now()
		for id, s := range t.sessions {
			if now.Sub(s.LastUpdated) > sessionTTL {
				costNano := int64(s.Cost * 1e9)
				atomic.AddInt64(&t.globalCostNano, -costNano)
				delete(t.sessions, id)
			}
		}
		t.mu.Unlock()
	}
}
