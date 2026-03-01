// Package costcontrol implements per-session cost tracking and budget enforcement.
//
// DESIGN: Tracks API costs per session and optionally enforces spending caps.
// Cost tracking is always active (for the dashboard). Enabled controls whether
// requests are blocked when configured caps are exceeded.
package costcontrol

import (
	"fmt"
	"time"
)

// CostControlConfig holds cost control settings.
type CostControlConfig struct {
	Enabled    bool    `yaml:"enabled"`     // Whether budget enforcement is active
	SessionCap float64 `yaml:"session_cap"` // USD per session. 0 = unlimited.
	GlobalCap  float64 `yaml:"global_cap"`  // USD across all sessions. 0 = unlimited.
}

// Validate checks cost control configuration.
func (c *CostControlConfig) Validate() error {
	if c.SessionCap < 0 {
		return fmt.Errorf("cost_control.session_cap must be >= 0, got %f", c.SessionCap)
	}
	if c.GlobalCap < 0 {
		return fmt.Errorf("cost_control.global_cap must be >= 0, got %f", c.GlobalCap)
	}
	return nil
}

// CostSession tracks accumulated cost for a single session.
type CostSession struct {
	ID           string
	Cost         float64
	RequestCount int
	Model        string
	CreatedAt    time.Time
	LastUpdated  time.Time
}

// BudgetCheckResult holds the result of a budget check.
type BudgetCheckResult struct {
	Allowed     bool
	CurrentCost float64 // Session cost
	GlobalCost  float64 // Total across all sessions
	Cap         float64 // Per-session cap
	GlobalCap   float64 // Global cap
}

// CostSessionSnapshot is a read-only copy of a session for the dashboard.
type CostSessionSnapshot struct {
	ID           string
	Cost         float64
	Cap          float64
	RequestCount int
	Model        string
	CreatedAt    time.Time
	LastUpdated  time.Time
}
