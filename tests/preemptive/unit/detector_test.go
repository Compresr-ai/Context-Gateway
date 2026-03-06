package preemptive_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/preemptive"
)

// =============================================================================
// COMPACTION DETECTOR TESTS
// =============================================================================

func TestCompactionDetector_NoPatterns(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		ClaudeCode: preemptive.ClaudeCodeDetectorConfig{
			Enabled:        false,
			PromptPatterns: []string{},
		},
	}

	detector := preemptive.GetDetector(adapters.ProviderAnthropic, cfg)
	result := detector.Detect([]byte(`{"messages": []}`))

	assert.False(t, result.IsCompactionRequest)
}

// =============================================================================
// CLAUDE CODE DETECTOR TESTS
// =============================================================================

func TestClaudeCodeDetector_PromptPattern_Summarize(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		ClaudeCode: preemptive.ClaudeCodeDetectorConfig{
			Enabled: true,
			PromptPatterns: []string{
				"summarize this conversation",
				"compact the context",
			},
		},
	}

	detector := preemptive.GetDetector(adapters.ProviderAnthropic, cfg)

	body := []byte(`{
		"messages": [
			{"role": "user", "content": "Please summarize this conversation for me"}
		]
	}`)

	result := detector.Detect(body)

	assert.True(t, result.IsCompactionRequest)
	assert.Equal(t, "claude_code_prompt", result.DetectedBy)
	assert.InDelta(t, 0.95, result.Confidence, 0.01)
}

func TestClaudeCodeDetector_PromptPattern_Compact(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		ClaudeCode: preemptive.ClaudeCodeDetectorConfig{
			Enabled: true,
			PromptPatterns: []string{
				"compact the context",
			},
		},
	}

	detector := preemptive.GetDetector(adapters.ProviderAnthropic, cfg)

	body := []byte(`{
		"messages": [
			{"role": "user", "content": "Let's compact the context now"}
		]
	}`)

	result := detector.Detect(body)

	assert.True(t, result.IsCompactionRequest)
}

func TestClaudeCodeDetector_PromptPattern_CaseInsensitive(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		ClaudeCode: preemptive.ClaudeCodeDetectorConfig{
			Enabled: true,
			PromptPatterns: []string{
				"summarize this conversation",
			},
		},
	}

	detector := preemptive.GetDetector(adapters.ProviderAnthropic, cfg)

	body := []byte(`{
		"messages": [
			{"role": "user", "content": "SUMMARIZE THIS CONVERSATION please"}
		]
	}`)

	result := detector.Detect(body)

	assert.True(t, result.IsCompactionRequest)
}

func TestClaudeCodeDetector_PromptPattern_NoMatch(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		ClaudeCode: preemptive.ClaudeCodeDetectorConfig{
			Enabled: true,
			PromptPatterns: []string{
				"summarize this conversation",
			},
		},
	}

	detector := preemptive.GetDetector(adapters.ProviderAnthropic, cfg)

	body := []byte(`{
		"messages": [
			{"role": "user", "content": "Please help me with this code"}
		]
	}`)

	result := detector.Detect(body)

	assert.False(t, result.IsCompactionRequest)
}

func TestClaudeCodeDetector_OnlyLastUserMessage(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		ClaudeCode: preemptive.ClaudeCodeDetectorConfig{
			Enabled: true,
			PromptPatterns: []string{
				"summarize this conversation",
			},
		},
	}

	detector := preemptive.GetDetector(adapters.ProviderAnthropic, cfg)

	// Pattern in earlier message, not in last
	body := []byte(`{
		" messages": [
			{"role": "user", "content": "summarize this conversation"},
			{"role": "assistant", "content": "Here is the summary..."},
			{"role": "user", "content": "Thanks! Now help me with code"}
		]
	}`)

	result := detector.Detect(body)

	// Should NOT detect - only checks last user message
	assert.False(t, result.IsCompactionRequest)
}

func TestClaudeCodeDetector_ContentBlockArray(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		ClaudeCode: preemptive.ClaudeCodeDetectorConfig{
			Enabled: true,
			PromptPatterns: []string{
				"summarize this conversation",
			},
		},
	}

	detector := preemptive.GetDetector(adapters.ProviderAnthropic, cfg)

	// Anthropic format with content blocks
	body := []byte(`{
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "Please summarize this conversation"}
			]}
		]
	}`)

	result := detector.Detect(body)

	assert.True(t, result.IsCompactionRequest)
}

// =============================================================================
// OPENAI DETECTOR TESTS
// =============================================================================

func TestOpenAIDetector_PromptPattern(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		Codex: preemptive.CodexDetectorConfig{
			Enabled:        true,
			PromptPatterns: []string{"compact history"},
		},
	}

	detector := preemptive.GetDetector(adapters.ProviderOpenAI, cfg)

	body := []byte(`{
		"messages": [
			{"role": "user", "content": "Please compact history now"}
		]
	}`)

	result := detector.Detect(body)

	assert.True(t, result.IsCompactionRequest)
	assert.Equal(t, "openai_prompt", result.DetectedBy)
}

// =============================================================================
// MALFORMED INPUT TESTS
// =============================================================================

func TestDetector_InvalidJSON(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		ClaudeCode: preemptive.ClaudeCodeDetectorConfig{
			Enabled: true,
		},
	}

	detector := preemptive.GetDetector(adapters.ProviderAnthropic, cfg)
	result := detector.Detect([]byte(`not valid json`))

	assert.False(t, result.IsCompactionRequest)
}

func TestDetector_EmptyMessages(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		ClaudeCode: preemptive.ClaudeCodeDetectorConfig{
			Enabled:        true,
			PromptPatterns: []string{"summarize"},
		},
	}

	detector := preemptive.GetDetector(adapters.ProviderAnthropic, cfg)
	result := detector.Detect([]byte(`{"messages": []}`))

	assert.False(t, result.IsCompactionRequest)
}

func TestDetector_NoUserMessages(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		ClaudeCode: preemptive.ClaudeCodeDetectorConfig{
			Enabled:        true,
			PromptPatterns: []string{"summarize"},
		},
	}

	detector := preemptive.GetDetector(adapters.ProviderAnthropic, cfg)

	body := []byte(`{
		"messages": [
			{"role": "assistant", "content": "summarize this please"}
		]
	}`)

	result := detector.Detect(body)

	// Should not match - only checks user messages for prompts
	assert.False(t, result.IsCompactionRequest)
}

// =============================================================================
// GENERIC DETECTOR TESTS (Header-based)
// =============================================================================

func TestGenericDetector_HeaderMatch(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		Generic: preemptive.GenericDetectorConfig{
			Enabled:     true,
			HeaderName:  "X-Request-Compaction",
			HeaderValue: "true",
		},
	}

	detector := preemptive.GetGenericDetector(cfg)
	assert.NotNil(t, detector)

	result := detector.DetectFromHeaders("true")

	assert.True(t, result.IsCompactionRequest)
	assert.Equal(t, "generic_header", result.DetectedBy)
	assert.Equal(t, 1.0, result.Confidence)
}

func TestGenericDetector_HeaderNoMatch(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		Generic: preemptive.GenericDetectorConfig{
			Enabled:     true,
			HeaderName:  "X-Request-Compaction",
			HeaderValue: "true",
		},
	}

	detector := preemptive.GetGenericDetector(cfg)
	assert.NotNil(t, detector)

	result := detector.DetectFromHeaders("false")

	assert.False(t, result.IsCompactionRequest)
}

func TestGenericDetector_Disabled(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		Generic: preemptive.GenericDetectorConfig{
			Enabled:     false,
			HeaderName:  "X-Request-Compaction",
			HeaderValue: "true",
		},
	}

	detector := preemptive.GetGenericDetector(cfg)
	assert.Nil(t, detector, "Generic detector should be nil when disabled")
}

func TestGenericDetector_EmptyHeader(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		Generic: preemptive.GenericDetectorConfig{
			Enabled:     true,
			HeaderName:  "X-Request-Compaction",
			HeaderValue: "true",
		},
	}

	detector := preemptive.GetGenericDetector(cfg)
	assert.NotNil(t, detector)

	result := detector.DetectFromHeaders("")

	assert.False(t, result.IsCompactionRequest)
}

func TestGenericDetector_HeaderName(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		Generic: preemptive.GenericDetectorConfig{
			Enabled:     true,
			HeaderName:  "X-Custom-Compaction",
			HeaderValue: "yes",
		},
	}

	detector := preemptive.GetGenericDetector(cfg)
	assert.NotNil(t, detector)

	assert.Equal(t, "X-Custom-Compaction", detector.HeaderName())
}

// =============================================================================
// OPENCLAW PATTERN DETECTION TESTS
// =============================================================================

func TestOpenClawPatternDetection_MergeSummaries(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		ClaudeCode: preemptive.ClaudeCodeDetectorConfig{
			Enabled: true,
			PromptPatterns: []string{
				"merge these partial summaries into a single cohesive summary",
			},
		},
	}

	detector := preemptive.GetDetector(adapters.ProviderAnthropic, cfg)

	body := []byte(`{
		"messages": [
			{"role": "user", "content": "Merge these partial summaries into a single cohesive summary. Preserve decisions, TODOs, open questions, and any constraints."}
		]
	}`)

	result := detector.Detect(body)

	assert.True(t, result.IsCompactionRequest)
	assert.Equal(t, "claude_code_prompt", result.DetectedBy)
}

func TestOpenClawPatternDetection_PreserveIdentifiers(t *testing.T) {
	cfg := preemptive.DetectorsConfig{
		Codex: preemptive.CodexDetectorConfig{
			Enabled: true,
			PromptPatterns: []string{
				"preserve all opaque identifiers exactly as written",
			},
		},
	}

	detector := preemptive.GetDetector(adapters.ProviderOpenAI, cfg)

	body := []byte(`{
		"messages": [
			{"role": "user", "content": "Preserve all opaque identifiers exactly as written (no shortening or reconstruction), including UUIDs, hashes, IDs, tokens."}
		]
	}`)

	result := detector.Detect(body)

	assert.True(t, result.IsCompactionRequest)
	assert.Equal(t, "openai_prompt", result.DetectedBy)
}
