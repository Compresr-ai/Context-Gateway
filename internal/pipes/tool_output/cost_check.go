// Cost-based compression skip logic.
//
// PURPOSE: Skip compression for cheap models where the cost of the compression
// API call would exceed the token savings. Based on production telemetry:
//
//	Target model        Requests    Tokens saved    Value saved
//	opus-4-6 ($15/MTok)    2,562        18.2M         $273.05
//	sonnet-4-5 ($3/MTok)      56        208.1K         $0.62
//	haiku-4-5 ($0.25/MTok) 1,917        171.6K         $0.14
//
// Haiku accounts for 42% of requests but only 0.05% of savings. The compression
// API calls for those requests likely cost more than the $0.14 saved.
//
// DESIGN: Uses a simple price threshold. If the target model's input cost is
// below $1.50/MTok, compression is not economically viable. This threshold
// accounts for compression API costs (~$1/MTok with Haiku) plus overhead.
package tooloutput

import (
	"github.com/compresr/context-gateway/internal/costcontrol"
	"github.com/rs/zerolog/log"
)

const (
	// MinViableInputCostPerMTok is the minimum target model input cost (USD/MTok)
	// below which compression is not economically viable.
	//
	// Rationale:
	//   - Compression uses Haiku (~$0.25/MTok input for claude-3-haiku)
	//   - Adding ~100% margin for API overhead and variability
	//   - $0.50/MTok threshold: skip only the very cheapest models
	//
	// Models BELOW threshold (skip compression):
	//   - claude-3-haiku ($0.25), gpt-4o-mini ($0.15), gpt-3.5-turbo ($0.5)
	//   - gemini-flash ($0.075-0.1), deepseek ($0.14), ministral ($0.04)
	//
	// Models ABOVE threshold (compress):
	//   - claude-haiku-4-5 ($1), claude-sonnet ($3), claude-opus ($5-15)
	//   - gpt-4o ($2.5), gpt-4-turbo ($10), o1 ($15)
	//   - gemini-pro ($1.25), mistral-large ($2), llama-3-70b ($0.8)
	MinViableInputCostPerMTok = 0.5
)

// ShouldSkipCompressionForCost checks if compression should be skipped based on
// the target model's pricing. Returns true if compression is not economically viable.
//
// This check is automatic and requires zero configuration. It uses the existing
// pricing table from costcontrol package.
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
