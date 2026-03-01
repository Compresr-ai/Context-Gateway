// Cost control configuration re-exports.
//
// DESIGN: Cost control config is defined in internal/costcontrol/types.go.
// This file re-exports the type for use by the main Config struct.
package config

import "github.com/compresr/context-gateway/internal/costcontrol"

// CostControlConfig is an alias for costcontrol.CostControlConfig.
type CostControlConfig = costcontrol.CostControlConfig
