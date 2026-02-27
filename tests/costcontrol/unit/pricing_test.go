package unit

import (
	"testing"

	"github.com/compresr/context-gateway/internal/costcontrol"
	"github.com/stretchr/testify/assert"
)

func TestGetModelPricing_KnownModels(t *testing.T) {
	tests := []struct {
		model      string
		wantInput  float64
		wantOutput float64
	}{
		{"claude-opus-4-6", 5, 25},
		{"claude-sonnet-4-5", 3, 15},
		{"claude-haiku-4-5", 1, 5},
		{"gpt-4o", 2.5, 10},
		{"gpt-4o-mini", 0.15, 0.60},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			p := costcontrol.GetModelPricing(tt.model)
			assert.Equal(t, tt.wantInput, p.InputPerMTok)
			assert.Equal(t, tt.wantOutput, p.OutputPerMTok)
		})
	}
}

func TestGetModelPricing_UnknownModel(t *testing.T) {
	p := costcontrol.GetModelPricing("some-unknown-model-xyz")
	// Should return conservative defaults
	assert.Equal(t, 15.0, p.InputPerMTok)
	assert.Equal(t, 75.0, p.OutputPerMTok)
}

func TestGetModelPricing_FamilyMatch(t *testing.T) {
	// A dated model should match via family prefix
	p := costcontrol.GetModelPricing("claude-sonnet-4-5-20260101")
	assert.Equal(t, 3.0, p.InputPerMTok)
	assert.Equal(t, 15.0, p.OutputPerMTok)
}

func TestGetModelPricing_VersionedFamilyMatch(t *testing.T) {
	// claude-opus-4-6 dated variant should match "claude-opus-4-6" prefix ($5/$25)
	// NOT the broad "claude-opus" prefix ($15/$75)
	p := costcontrol.GetModelPricing("claude-opus-4-6-20260101")
	assert.Equal(t, 5.0, p.InputPerMTok)
	assert.Equal(t, 25.0, p.OutputPerMTok)

	// claude-haiku-4-5 dated variant
	p2 := costcontrol.GetModelPricing("claude-haiku-4-5-20260601")
	assert.Equal(t, 1.0, p2.InputPerMTok)
	assert.Equal(t, 5.0, p2.OutputPerMTok)
}

func TestCalculateCost(t *testing.T) {
	pricing := costcontrol.ModelPricing{InputPerMTok: 3, OutputPerMTok: 15}

	// 1000 input tokens + 500 output tokens
	cost := costcontrol.CalculateCost(1000, 500, pricing)
	expected := (1000.0/1_000_000)*3 + (500.0/1_000_000)*15
	assert.InDelta(t, expected, cost, 0.0001)
}

func TestCalculateCost_Zero(t *testing.T) {
	pricing := costcontrol.ModelPricing{InputPerMTok: 3, OutputPerMTok: 15}
	cost := costcontrol.CalculateCost(0, 0, pricing)
	assert.Equal(t, 0.0, cost)
}

func TestCalculateCostWithCache(t *testing.T) {
	pricing := costcontrol.ModelPricing{InputPerMTok: 3, OutputPerMTok: 15}

	// Anthropic: input_tokens=2000 (non-cached), 500 output, 2000 cache write, 5000 cache read
	// input_tokens excludes cached tokens — cache tokens are separate and additional
	cost := costcontrol.CalculateCostWithCache(2000, 500, 2000, 5000, pricing)

	inputCost := 2000.0 / 1_000_000 * 3             // non-cached at full price
	outputCost := 500.0 / 1_000_000 * 15            // output at full price
	cacheWriteCost := 2000.0 / 1_000_000 * 3 * 1.25 // cache creation at 1.25x
	cacheReadCost := 5000.0 / 1_000_000 * 3 * 0.1   // cache read at 0.1x
	expected := inputCost + outputCost + cacheWriteCost + cacheReadCost
	assert.InDelta(t, expected, cost, 0.0000001)
}

func TestCalculateCostWithCache_ZeroCacheTokens(t *testing.T) {
	pricing := costcontrol.ModelPricing{InputPerMTok: 3, OutputPerMTok: 15}

	// No cache tokens — should equal CalculateCost
	withCache := costcontrol.CalculateCostWithCache(1000, 500, 0, 0, pricing)
	without := costcontrol.CalculateCost(1000, 500, pricing)
	assert.Equal(t, without, withCache)
}

func TestCalculateCostWithCache_OnlyCacheRead(t *testing.T) {
	pricing := costcontrol.ModelPricing{InputPerMTok: 5, OutputPerMTok: 25}

	// 1000 non-cached input, 500 output, 0 cache write, 40000 cache read
	// Cache reads at 0.1x should be much cheaper than full price
	cost := costcontrol.CalculateCostWithCache(1000, 500, 0, 40000, pricing)

	inputCost := 1000.0 / 1_000_000 * 5
	outputCost := 500.0 / 1_000_000 * 25
	cacheReadCost := 40000.0 / 1_000_000 * 5 * 0.1
	expected := inputCost + outputCost + cacheReadCost
	assert.InDelta(t, expected, cost, 0.0000001)

	// Total input (non-cached + cached) at full price would be more expensive
	fullPrice := costcontrol.CalculateCost(1000+40000, 500, pricing)
	assert.Less(t, cost, fullPrice)
}

func TestCalculateCostWithCache_OnlyCacheWrite(t *testing.T) {
	pricing := costcontrol.ModelPricing{InputPerMTok: 5, OutputPerMTok: 25}

	// 1000 non-cached input, 500 output, 3000 cache write, 0 cache read
	cost := costcontrol.CalculateCostWithCache(1000, 500, 3000, 0, pricing)

	inputCost := 1000.0 / 1_000_000 * 5
	outputCost := 500.0 / 1_000_000 * 25
	cacheWriteCost := 3000.0 / 1_000_000 * 5 * 1.25
	expected := inputCost + outputCost + cacheWriteCost
	assert.InDelta(t, expected, cost, 0.0000001)
}
