package costcontrol

import "strings"

// ModelPricing holds per-million-token pricing for a model.
type ModelPricing struct {
	InputPerMTok  float64 // USD per million input tokens
	OutputPerMTok float64 // USD per million output tokens
}

// modelPricingTable maps model names to their pricing.
var modelPricingTable = map[string]ModelPricing{
	// Claude 4.x (dated)
	"claude-opus-4-6":            {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-0-20250514":   {InputPerMTok: 15, OutputPerMTok: 75},
	"claude-sonnet-4-5-20250929": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-sonnet-4-0-20250514": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-haiku-4-5-20251001":  {InputPerMTok: 1, OutputPerMTok: 5},

	// Claude short aliases
	"claude-sonnet-4-5": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-sonnet-4-0": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-haiku-4-5":  {InputPerMTok: 1, OutputPerMTok: 5},

	// Claude 3.x
	"claude-3-5-sonnet-20241022": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-3-5-haiku-20241022":  {InputPerMTok: 1, OutputPerMTok: 5},
	"claude-3-haiku-20240307":    {InputPerMTok: 0.25, OutputPerMTok: 1.25},

	// OpenAI
	"gpt-4o":                 {InputPerMTok: 2.5, OutputPerMTok: 10},
	"gpt-4o-2024-11-20":      {InputPerMTok: 2.5, OutputPerMTok: 10},
	"gpt-4o-mini":            {InputPerMTok: 0.15, OutputPerMTok: 0.60},
	"gpt-4o-mini-2024-07-18": {InputPerMTok: 0.15, OutputPerMTok: 0.60},
}

// defaultPricing is used for unknown models (conservative to prevent silent overspend).
var defaultPricing = ModelPricing{InputPerMTok: 15, OutputPerMTok: 75}

// modelFamilyPricing maps model family prefixes to pricing.
// Ordered longest-prefix-first in lookup to avoid e.g. "claude-opus" ($15)
// matching when "claude-opus-4-6" ($5) is the correct match.
var modelFamilyPricing = map[string]ModelPricing{
	// Version-specific families (must win over broad families)
	"claude-opus-4-6":   {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-0":   {InputPerMTok: 15, OutputPerMTok: 75},
	"claude-sonnet-4-5": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-sonnet-4-0": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-haiku-4-5":  {InputPerMTok: 1, OutputPerMTok: 5},
	"claude-3-5-sonnet": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-3-5-haiku":  {InputPerMTok: 1, OutputPerMTok: 5},
	"claude-3-haiku":    {InputPerMTok: 0.25, OutputPerMTok: 1.25},

	// Broad families (fallback)
	"claude-opus":   {InputPerMTok: 15, OutputPerMTok: 75},
	"claude-sonnet": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-haiku":  {InputPerMTok: 1, OutputPerMTok: 5},
	"gpt-4o-mini":   {InputPerMTok: 0.15, OutputPerMTok: 0.60},
	"gpt-4o":        {InputPerMTok: 2.5, OutputPerMTok: 10},
	"gpt-4":         {InputPerMTok: 10, OutputPerMTok: 30},
}

// GetModelPricing returns pricing for a model.
// Tries exact match, then prefix/family match (longest prefix wins), then default.
func GetModelPricing(model string) ModelPricing {
	// Exact match
	if p, ok := modelPricingTable[model]; ok {
		return p
	}

	// Family/prefix match (longest prefix wins)
	bestPrefix := ""
	var bestPricing ModelPricing
	for prefix, p := range modelFamilyPricing {
		if strings.HasPrefix(model, prefix) && len(prefix) > len(bestPrefix) {
			bestPrefix = prefix
			bestPricing = p
		}
	}
	if bestPrefix != "" {
		return bestPricing
	}

	return defaultPricing
}

// CalculateCost computes the cost in USD from token counts.
func CalculateCost(inputTokens, outputTokens int, pricing ModelPricing) float64 {
	inputCost := float64(inputTokens) / 1_000_000 * pricing.InputPerMTok
	outputCost := float64(outputTokens) / 1_000_000 * pricing.OutputPerMTok
	return inputCost + outputCost
}

// CalculateCostWithCache computes cost accounting for Anthropic cache pricing.
// Anthropic's input_tokens is the non-cached input count; cache tokens are separate.
// Billing: non-cached at full price, cache creation at 1.25x, cache read at 0.1x.
func CalculateCostWithCache(inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int, pricing ModelPricing) float64 {
	inputCost := float64(inputTokens) / 1_000_000 * pricing.InputPerMTok
	outputCost := float64(outputTokens) / 1_000_000 * pricing.OutputPerMTok
	cacheWriteCost := float64(cacheCreationTokens) / 1_000_000 * pricing.InputPerMTok * 1.25
	cacheReadCost := float64(cacheReadTokens) / 1_000_000 * pricing.InputPerMTok * 0.1
	return inputCost + outputCost + cacheWriteCost + cacheReadCost
}
