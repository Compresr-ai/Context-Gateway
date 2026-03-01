// Package preemptive - config.go contains default configuration values.
//
// DESIGN: Centralized defaults for preemptive summarization configuration.
// These are used when config file doesn't specify values.
// This file contains ONLY data - no logic/helpers.
package preemptive

import "time"

// DefaultCodexPromptPatterns are phrases for OpenAI Codex compaction requests.
var DefaultCodexPromptPatterns = []string{
	"summarize the conversation",
	"create a summary of our conversation",
	"compress the context",
}

// =============================================================================
// DEFAULT PROMPT PATTERNS (per provider)
// =============================================================================

// DefaultClaudeCodePromptPatterns are phrases that indicate a Claude Code /compact request.
// These match the prompt that Claude Code sends when user runs /compact.
var DefaultClaudeCodePromptPatterns = []string{
	"your task is to create a detailed summary of the conversation so far",
	"do not use any tools. you must respond with only the <summary>...</summary> block",
	"important: do not use any tools",
}

// (Codex support removed)

// =============================================================================
// DEFAULT SYSTEM PROMPTS (per provider)
// =============================================================================

// DefaultClaudeSystemPrompt is the default summarization prompt for Anthropic Claude.
// Inspired by precompact-hook's "witness at the threshold" approach.
var DefaultClaudeSystemPrompt = `You are a partner working alongside the user, invested in their success. Context is about to reset - this summary carries forward the partnership.

Produce a recovery summary with these sections:

## Who We're Working With
The person in this conversation. Name, role, how they communicate. What do they care about?

## What We're Working On
The actual goal and inquiry driving the conversation. What's at stake?

## What Just Happened
Recent exchanges. What was discovered, decided, built? Be specific - file names, function names, error messages, code snippets.

## Interaction Pattern
Pace, direction, tone. What's working: tool effectiveness, approaches that succeeded or failed.

## Key Artifacts
Files, IDs, commands that worked. Technical details needed to continue.

## Continue With
Specific next actions when conversation resumes.

Be specific. Be thorough. Capture what matters, not just what happened.`

// (Codex support removed)

// =============================================================================
// MODEL CONTEXT WINDOWS
// =============================================================================

// DefaultModelContextWindows contains known model context windows.
// Key: model name, Value: context window configuration.
var DefaultModelContextWindows = map[string]ModelContextWindow{
	// Test models
	"test-model-small": {Model: "test-model-small", MaxTokens: 10000, OutputMax: 2000, EffectiveMax: 8000},
	"test-model-tiny":  {Model: "test-model-tiny", MaxTokens: 2000, OutputMax: 500, EffectiveMax: 1500},

	// Anthropic Claude models
	"claude-opus-4-6":            {Model: "claude-opus-4-6", MaxTokens: 200000, OutputMax: 128000, EffectiveMax: 72000},
	"claude-sonnet-4-5-20250929": {Model: "claude-sonnet-4-5-20250929", MaxTokens: 200000, OutputMax: 64000, EffectiveMax: 136000},
	"claude-sonnet-4-5":          {Model: "claude-sonnet-4-5", MaxTokens: 200000, OutputMax: 64000, EffectiveMax: 136000},
	"claude-haiku-4-5-20251001":  {Model: "claude-haiku-4-5-20251001", MaxTokens: 200000, OutputMax: 64000, EffectiveMax: 136000},
	"claude-haiku-4-5":           {Model: "claude-haiku-4-5", MaxTokens: 200000, OutputMax: 64000, EffectiveMax: 136000},

	// OpenAI models
	"gpt-4-turbo":         {Model: "gpt-4-turbo", MaxTokens: 128000, OutputMax: 4096, EffectiveMax: 123904},
	"gpt-4-turbo-preview": {Model: "gpt-4-turbo-preview", MaxTokens: 128000, OutputMax: 4096, EffectiveMax: 123904},
	"gpt-4o":              {Model: "gpt-4o", MaxTokens: 128000, OutputMax: 16384, EffectiveMax: 111616},
	"gpt-4o-mini":         {Model: "gpt-4o-mini", MaxTokens: 128000, OutputMax: 16384, EffectiveMax: 111616},

	// OpenAI Codex models (ChatGPT subscription)
	"gpt-5.3-codex": {Model: "gpt-5.3-codex", MaxTokens: 64000, OutputMax: 16384, EffectiveMax: 47616},
}

// DefaultUnknownModelContextWindow is the fallback for unknown models.
var DefaultUnknownModelContextWindow = ModelContextWindow{
	Model:        "unknown",
	MaxTokens:    128000,
	OutputMax:    4096,
	EffectiveMax: 123904,
}

// =============================================================================
// DEFAULT CONFIG
// =============================================================================

// DefaultConfig returns sensible defaults for preemptive summarization.
func DefaultConfig() Config {
	return Config{
		Enabled:            false,
		TriggerThreshold:   80.0,
		PendingJobTimeout:  90 * time.Second,
		SyncTimeout:        2 * time.Minute,
		TokenEstimateRatio: 4,
		LogDir:             "logs",
		Summarizer: SummarizerConfig{
			Model:              "claude-haiku-4-5",
			Endpoint:           "https://api.anthropic.com/v1/messages",
			MaxTokens:          4096,
			Timeout:            60 * time.Second,
			KeepRecentTokens:   20000, // Keep ~20K tokens of recent context (token-based)
			KeepRecentCount:    0,     // Disabled - using token-based instead
			TokenEstimateRatio: 4,     // Bytes per token for estimation
			SystemPrompt:       DefaultClaudeSystemPrompt,
		},
		Session: SessionConfig{
			SummaryTTL:       2 * time.Hour,
			HashMessageCount: 3,
		},
		Detectors: DetectorsConfig{
			ClaudeCode: ClaudeCodeDetectorConfig{
				Enabled:        true,
				PromptPatterns: DefaultClaudeCodePromptPatterns,
			},
			Codex: CodexDetectorConfig{
				Enabled:        true,
				PromptPatterns: DefaultCodexPromptPatterns,
			},
			Generic: GenericDetectorConfig{
				Enabled:     true,
				HeaderName:  "X-Request-Compaction",
				HeaderValue: "true",
			},
		},
		AddResponseHeaders: true,
	}
}
