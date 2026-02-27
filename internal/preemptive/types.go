// Package preemptive provides preemptive summarization for context window management.
//
// DESIGN: Eliminate context window wait times by summarizing conversation history
// before it's needed. When context reaches a threshold (e.g., 80%), a background
// worker generates a summary. When compaction is requested, the summary is ready.
//
// ARCHITECTURE:
//   - Manager: Main entry point, orchestrates all components
//   - SessionManager: Tracks conversation sessions by hash
//   - Summarizer: Generates summaries via LLM API
//   - Detector: Identifies compaction requests from various agents
package preemptive

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/compresr/context-gateway/internal/adapters"
)

// CodexDetectorConfig for Codex detection.
type CodexDetectorConfig struct {
	Enabled        bool     `yaml:"enabled"`
	PromptPatterns []string `yaml:"prompt_patterns"`
}

// =============================================================================
// CONFIGURATION
// =============================================================================

// Config contains all configuration for preemptive summarization.
type Config struct {
	Enabled          bool    `yaml:"enabled"`
	TriggerThreshold float64 `yaml:"trigger_threshold"` // Start at this % (default: 80)

	// Timeouts
	PendingJobTimeout time.Duration `yaml:"pending_job_timeout,omitempty"` // Wait for pending job (default: 90s)
	SyncTimeout       time.Duration `yaml:"sync_timeout,omitempty"`        // Sync summarization timeout (default: 2m)

	// Token estimation
	TokenEstimateRatio int `yaml:"token_estimate_ratio,omitempty"` // Bytes per token (default: 4)

	// Testing override for context window size
	TestContextWindowOverride int `yaml:"test_context_window_override,omitempty"`

	// Logging
	LoggingEnabled    bool   `yaml:"logging_enabled,omitempty"` // Controls history_compaction.jsonl (follows telemetry_enabled)
	LogDir            string `yaml:"log_dir,omitempty"`
	CompactionLogPath string `yaml:"compaction_log_path,omitempty"`

	// Sub-configs
	Summarizer SummarizerConfig `yaml:"summarizer"`
	Session    SessionConfig    `yaml:"session"`
	Detectors  DetectorsConfig  `yaml:"detectors"`

	// Response headers
	AddResponseHeaders bool `yaml:"add_response_headers"`
}

// SummarizerConfig configures the summarization service.
type SummarizerConfig struct {
	// Strategy: "provider" (LLM) or "api" (Compresr API)
	Strategy string `yaml:"strategy"`

	// Provider reference (for strategy: "provider")
	// References a provider defined in the top-level "providers" section.
	// For Bedrock, set to "bedrock" — uses SigV4 signing instead of API key.
	Provider string `yaml:"provider,omitempty"`

	// Inline settings (used if Provider is not set, or for overrides)
	Model              string        `yaml:"model"`
	APISecret          string        `yaml:"api_key"`
	Endpoint           string        `yaml:"endpoint"`
	MaxTokens          int           `yaml:"max_tokens"`
	Timeout            time.Duration `yaml:"timeout"`
	KeepRecentTokens   int           `yaml:"keep_recent_tokens"`   // Fixed token count (override)
	KeepRecentCount    int           `yaml:"keep_recent"`          // Message-based (legacy fallback)
	TokenEstimateRatio int           `yaml:"token_estimate_ratio"` // Bytes per token for estimation
	SystemPrompt       string        `yaml:"system_prompt,omitempty"`

	// API config (for strategy: "api")
	API *APIConfig `yaml:"api,omitempty"`
}

// APIConfig for Compresr API compression.
type APIConfig struct {
	Endpoint string        `yaml:"endpoint"` // e.g., "/api/compress/history/"
	APIKey   string        `yaml:"api_key"`
	Model    string        `yaml:"model"` // e.g., "hcc_espresso_v1"
	Timeout  time.Duration `yaml:"timeout"`
}

// SessionConfig configures session management.
type SessionConfig struct {
	SummaryTTL           time.Duration `yaml:"summary_ttl"`
	HashMessageCount     int           `yaml:"hash_message_count"`
	DisableFuzzyMatching bool          `yaml:"disable_fuzzy_matching"` // Opt-out of fuzzy matching
}

// DetectorsConfig contains agent-specific compaction detectors.
type DetectorsConfig struct {
	ClaudeCode ClaudeCodeDetectorConfig `yaml:"claude_code"`
	Codex      CodexDetectorConfig      `yaml:"codex"`
	Generic    GenericDetectorConfig    `yaml:"generic"`
}

// ClaudeCodeDetectorConfig for Claude Code detection.
type ClaudeCodeDetectorConfig struct {
	Enabled        bool     `yaml:"enabled"`
	PromptPatterns []string `yaml:"prompt_patterns"`
}

// (CodexDetectorConfig removed)

// GenericDetectorConfig for generic detection.
type GenericDetectorConfig struct {
	Enabled        bool     `yaml:"enabled"`
	PromptPatterns []string `yaml:"prompt_patterns"`
	HeaderName     string   `yaml:"header_name"`
	HeaderValue    string   `yaml:"header_value"`
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.TriggerThreshold <= 0 || c.TriggerThreshold > 100 {
		return fmt.Errorf("trigger_threshold must be between 0 and 100")
	}

	// Validate strategy
	if c.Summarizer.Strategy == "" {
		c.Summarizer.Strategy = "provider" // default to provider (backward compat)
	}
	if c.Summarizer.Strategy != "provider" && c.Summarizer.Strategy != "api" {
		return fmt.Errorf("summarizer.strategy must be 'provider' or 'api'")
	}

	// Strategy-specific validation
	switch c.Summarizer.Strategy {
	case "provider":
		// Model is required unless using provider reference
		if c.Summarizer.Provider == "" && c.Summarizer.Model == "" {
			return fmt.Errorf("summarizer.model is required (or use provider reference)")
		}
		// API key is optional - can be captured from incoming requests (Max/Pro users)
		// Bedrock uses SigV4 signing (no API key needed)
		// Runtime error will occur in callAPI if no auth is available
		if c.Summarizer.MaxTokens <= 0 {
			return fmt.Errorf("summarizer.max_tokens must be positive")
		}
		if c.Summarizer.Timeout <= 0 {
			return fmt.Errorf("summarizer.timeout must be positive")
		}
	case "api":
		// API config validation
		if c.Summarizer.API == nil {
			return fmt.Errorf("summarizer.api is required when strategy is 'api'")
		}
		if c.Summarizer.API.Endpoint == "" {
			return fmt.Errorf("summarizer.api.endpoint is required")
		}
		if c.Summarizer.API.APIKey == "" {
			return fmt.Errorf("summarizer.api.api_key is required")
		}
		if c.Summarizer.API.Model == "" {
			return fmt.Errorf("summarizer.api.model is required")
		}
		if c.Summarizer.API.Timeout <= 0 {
			return fmt.Errorf("summarizer.api.timeout must be positive")
		}
	}

	if c.Session.SummaryTTL <= 0 {
		return fmt.Errorf("session.summary_ttl must be positive")
	}
	if c.Session.HashMessageCount <= 0 {
		return fmt.Errorf("session.hash_message_count must be positive")
	}
	return nil
}

// =============================================================================
// SESSION STATES
// =============================================================================

// SessionState represents the current state of a session.
type SessionState string

const (
	StateIdle    SessionState = "idle"    // No summarization in progress
	StatePending SessionState = "pending" // Summarization triggered
	StateReady   SessionState = "ready"   // Summary is ready
	StateUsed    SessionState = "used"    // Summary has been used
)

// SummaryGracePeriod is how long a summary remains available after first use.
const SummaryGracePeriod = 500 * time.Millisecond

// =============================================================================
// DETECTION RESULT
// =============================================================================

// DetectionResult contains the result of compaction detection.
type DetectionResult struct {
	IsCompactionRequest bool
	DetectedBy          string
	Confidence          float64
	Details             map[string]interface{}
}

// =============================================================================
// MODEL CONTEXT WINDOW
// =============================================================================

// ModelContextWindow defines context window for a model.
type ModelContextWindow struct {
	Model        string
	MaxTokens    int
	OutputMax    int
	EffectiveMax int
}

// =============================================================================
// TOKEN USAGE
// =============================================================================

// TokenUsage represents current token usage.
type TokenUsage struct {
	InputTokens  int
	MaxTokens    int
	UsagePercent float64
}

// =============================================================================
// INTERNAL REQUEST TYPES
// =============================================================================

// request represents a parsed incoming request.
type request struct {
	messages  []json.RawMessage
	model     string
	sessionID string
	provider  adapters.Provider
	detection DetectionResult

	// Per-request auth captured from headers
	authToken     string // Captured auth token from this request
	authIsXAPIKey bool   // true if from x-api-key header
	authEndpoint  string // Captured endpoint for this request
}

// summaryResult contains the result of a summarization.
type summaryResult struct {
	summary   string
	tokens    int
	lastIndex int
}
