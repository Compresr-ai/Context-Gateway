// cost_check.go skips compression for models where the API cost exceeds token savings.
package tooloutput

import (
	"github.com/compresr/context-gateway/internal/costcontrol"
	"github.com/rs/zerolog/log"
)

const (
	// MinViableInputCostPerMTok is the minimum target model cost (USD/MTok) to justify compression.
	// Models below this threshold (e.g., Haiku, gpt-4o-mini) save too little to cover API costs.
	MinViableInputCostPerMTok = 0.5
)

// ShouldSkipCompressionForCost returns true if compression is not economically viable for the target model.
func ShouldSkipCompressionForCost(targetModel string) bool {
	if targetModel == "" {
		// Unknown model: compress to be safe (defaults are expensive)
		return false
	}

	pricing := costcontrol.GetModelPricing(targetModel)

	// Check if target model is below the economic viability threshold
	shouldSkip := pricing.InputPerMTok < MinViableInputCostPerMTok

	if shouldSkip {
		log.Debug().
			Str("target_model", targetModel).
			Float64("input_cost_per_mtok", pricing.InputPerMTok).
			Float64("threshold", MinViableInputCostPerMTok).
			Msg("tool_output: skipping compression for cheap model (not economically viable)")
	}

	return shouldSkip
}

// GetModelCostTier returns a human-readable tier for the target model.
// Used for logging and metrics.
func GetModelCostTier(targetModel string) string {
	if targetModel == "" {
		return "unknown"
	}

	pricing := costcontrol.GetModelPricing(targetModel)

	switch {
	case pricing.InputPerMTok >= 10:
		return "premium" // Opus ($15), GPT-4 ($10)
	case pricing.InputPerMTok >= 2:
		return "standard" // Sonnet ($3), GPT-4o ($2.5)
	default:
		return "budget" // Haiku (<$1.5), GPT-4o-mini ($0.15)
	}
}
