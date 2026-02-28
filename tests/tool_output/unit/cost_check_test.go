package unit

import (
	"testing"

	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
	"github.com/stretchr/testify/assert"
)

func TestShouldSkipCompressionForCost_BudgetModels(t *testing.T) {
	// Budget models (<$0.5/MTok) should skip compression
	budgetModels := []string{
		// Anthropic
		"claude-3-haiku-20240307", // $0.25/MTok
		"claude-3-haiku",          // $0.25/MTok
		// OpenAI
		"gpt-4o-mini",            // $0.15/MTok
		"gpt-4o-mini-2024-07-18", // $0.15/MTok
		"gpt-4.1-nano",           // $0.10/MTok
		"gpt-5-nano",             // $0.05/MTok
		"gpt-5-mini",             // $0.25/MTok
		// Google
		"gemini-1.5-flash",      // $0.075/MTok
		"gemini-1.5-flash-8b",   // $0.0375/MTok
		"gemini-2.0-flash",      // $0.10/MTok
		"gemini-2.0-flash-lite", // $0.075/MTok
		"gemini-2.5-flash",      // $0.30/MTok
		"gemini-2.5-flash-lite", // $0.10/MTok
	}

	for _, model := range budgetModels {
		t.Run(model, func(t *testing.T) {
			assert.True(t, tooloutput.ShouldSkipCompressionForCost(model),
				"expected compression to be skipped for budget model %s", model)
		})
	}
}

func TestShouldSkipCompressionForCost_StandardModels(t *testing.T) {
	// Standard/Premium models (>$0.5/MTok) should NOT skip compression
	standardModels := []string{
		// Anthropic
		"claude-opus-4-6",            // $5/MTok
		"claude-opus-4-5",            // $5/MTok
		"claude-opus-4-1",            // $15/MTok
		"claude-sonnet-4-5",          // $3/MTok
		"claude-sonnet-4-6",          // $3/MTok
		"claude-haiku-4-5",           // $1/MTok
		"claude-haiku-3-5",           // $0.80/MTok (above $0.5 threshold)
		"claude-3-5-sonnet-20241022", // $3/MTok
		// OpenAI
		"gpt-5.2",     // $1.75/MTok
		"gpt-5.1",     // $1.25/MTok
		"gpt-5",       // $1.25/MTok
		"gpt-4.1",     // $2/MTok
		"gpt-4o",      // $2.5/MTok
		"gpt-4-turbo", // $10/MTok
		"o1",          // $15/MTok
		"o1-mini",     // $1.10/MTok
		"o3-mini",     // $1.10/MTok
		"o3",          // $2/MTok
		"o4-mini",     // $1.10/MTok
		// Google
		"gemini-1.5-pro",         // $1.25/MTok
		"gemini-2.0-pro",         // $1.25/MTok
		"gemini-2.5-pro",         // $1.25/MTok
		"gemini-3-flash-preview", // $0.50/MTok
		"gemini-3-pro-preview",   // $2/MTok
		"gemini-3.1-pro-preview", // $2/MTok
	}

	for _, model := range standardModels {
		t.Run(model, func(t *testing.T) {
			assert.False(t, tooloutput.ShouldSkipCompressionForCost(model),
				"expected compression NOT to be skipped for standard/premium model %s", model)
		})
	}
}

func TestShouldSkipCompressionForCost_EmptyModel(t *testing.T) {
	// Empty model should NOT skip (compress to be safe, defaults are expensive)
	assert.False(t, tooloutput.ShouldSkipCompressionForCost(""),
		"expected compression NOT to be skipped for empty model")
}

func TestShouldSkipCompressionForCost_UnknownModel(t *testing.T) {
	// Unknown models default to expensive pricing, should NOT skip
	assert.False(t, tooloutput.ShouldSkipCompressionForCost("some-unknown-model-xyz"),
		"expected compression NOT to be skipped for unknown model")
}

func TestGetModelCostTier(t *testing.T) {
	tests := []struct {
		model    string
		expected string
	}{
		// Premium ($10+/MTok)
		{"claude-opus-4-1", "premium"},
		{"o1", "premium"},
		{"gpt-5.2-pro", "premium"},

		// Standard ($2-10/MTok)
		{"claude-opus-4-6", "standard"},
		{"claude-sonnet-4-5", "standard"},
		{"gpt-4o", "standard"},
		{"gpt-5.2", "budget"}, // $1.75 is budget tier
		{"o3", "standard"},

		// Budget (<$2/MTok)
		{"claude-haiku-4-5", "budget"},
		{"gpt-4o-mini", "budget"},
		{"claude-3-haiku-20240307", "budget"},
		{"gemini-2.5-flash", "budget"},
		{"gpt-5-nano", "budget"},

		// Unknown
		{"", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			tier := tooloutput.GetModelCostTier(tt.model)
			assert.Equal(t, tt.expected, tier)
		})
	}
}

func TestMinViableInputCostPerMTok(t *testing.T) {
	// Verify the threshold constant is set correctly
	assert.Equal(t, 0.5, tooloutput.MinViableInputCostPerMTok,
		"threshold should be $0.5/MTok for economic viability")
}
