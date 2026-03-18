// Pipes configuration - compression pipeline settings.
package pipes

import (
	"fmt"
	"time"
)

// COMPRESSION RATIO CONSTANTS

// Compression ratio is the removed fraction: 1 - (compressedTokens / originalTokens).
// Range 0.0–1.0 where 0 = no tokens removed, 1 = all tokens removed.
// Higher = more aggressive compression. Matches the API's target_compression_ratio convention.
const (
	// DefaultTargetCompressionRatio is the default aggressiveness when not set in config.
	// 0.5 = remove ~50% of tokens (medium aggressiveness).
	DefaultTargetCompressionRatio = 0.5

	// MinTargetCompressionRatio is the least aggressive value accepted by the API.
	// 0.1 = remove at minimum 10% of tokens.
	MinTargetCompressionRatio = 0.1

	// MaxTargetCompressionRatio is the most aggressive value accepted by the API.
	// 0.9 = remove up to 90% of tokens.
	MaxTargetCompressionRatio = 0.9
)

// STRATEGY CONSTANTS

// Strategy constants for pipe execution.
const (
	StrategyPassthrough      = "passthrough"       // Do nothing, pass through unchanged
	StrategyExternalProvider = "external_provider" // Call external LLM provider (OpenAI/Anthropic) directly
	StrategyRelevance        = "relevance"         // Local relevance-based tool filtering (no external API)
	StrategyToolSearch       = "tool-search"       // Universal dispatcher: defers all tools, uses Compresr API for search

	// Tool output specific strategies (not used for tool discovery)
	StrategyAPI      = "api"      // Call Compresr API (tool output compression)
	StrategyCompresr = "compresr" // Alias for StrategyAPI (backward compat)
	StrategySimple   = "simple"   // Simple compression (first N words)
	StrategyTrimming = "trimming" // Tail-keep compression: discard head, keep only tail based on target_compression_ratio
)

// IsAPIStrategy returns true if the strategy is API-based (tool output only).
func IsAPIStrategy(strategy string) bool {
	return strategy == StrategyAPI || strategy == StrategyCompresr
}

// PIPES CONFIG - Root configuration for all pipes

// Config contains configuration for all compression pipes.
type Config struct {
	ToolOutput    ToolOutputConfig    `yaml:"tool_output"`    // Tool output compression
	ToolDiscovery ToolDiscoveryConfig `yaml:"tool_discovery"` // Tool filtering
	TaskOutput    TaskOutputConfig    `yaml:"task_output"`    // Task/subagent output handling
}

// Validate validates pipe configurations.
func (p *Config) Validate() error {
	if err := p.ToolOutput.Validate(); err != nil {
		return err
	}
	if err := p.ToolDiscovery.Validate(); err != nil {
		return err
	}
	if err := p.TaskOutput.Validate(); err != nil {
		return err
	}
	return nil
}

// TOOL OUTPUT PIPE CONFIG

// ToolOutputConfig configures tool result compression.
type ToolOutputConfig struct {
	Enabled          bool   `yaml:"enabled"`           // Enable this pipe
	Strategy         string `yaml:"strategy"`          // passthrough | compresr | external_provider
	FallbackStrategy string `yaml:"fallback_strategy"` // Fallback when primary fails

	// Provider reference (preferred over inline Compresr config)
	// References a provider defined in the top-level "providers" section.
	Provider string `yaml:"provider,omitempty"`

	// Compresr strategy config (for strategy=compresr or strategy=external_provider)
	// Can be overridden by Provider reference
	Compresr CompresrConfig `yaml:"compresr,omitempty"`

	// Compression thresholds (in tokens)
	MinTokens              int     `yaml:"min_tokens"`               // Below this token count, no compression (default: 512)
	MaxTokens              int     `yaml:"max_tokens"`               // Above this token count, skip compression (default: 50000)
	TargetCompressionRatio float64 `yaml:"target_compression_ratio"` // Sent to API: 0.1 = least aggressive, 0.9 = most aggressive. 0 = API default.
	RefusalThreshold       float64 `yaml:"refusal_threshold"`        // Reject compression if token savings < this ratio (default: 0.05 = must save at least 5%)

	// Expand context feature
	EnableExpandContext bool `yaml:"enable_expand_context"` // Inject expand_context tool
	IncludeExpandHint   bool `yaml:"include_expand_hint"`   // Add hint to compressed content

	// BypassCostCheck disables the automatic cost-based skip (useful for testing/benchmarking).
	// When false (default), cheap models (e.g. gpt-4o-mini) are skipped automatically.
	BypassCostCheck bool `yaml:"bypass_cost_check"`

	// Skip compression for specific tool categories (e.g., browser — real-time content)
	SkipTools SkipToolsConfig `yaml:"skip_tools,omitempty"`

	// ContentFormats controls which detected text formats are eligible for compression.
	// Default: all text-based formats (text, json, markdown) are compressed.
	ContentFormats ContentFormatsConfig `yaml:"content_formats,omitempty"`
}

// ContentFormatsConfig narrows which text formats are eligible for compression.
// allowed restricts to a subset; forbidden removes formats; forbidden takes precedence.
type ContentFormatsConfig struct {
	// Allowed is an explicit list of formats to compress.
	// If empty, all DefaultCompressibleFormats (text, json, markdown) are eligible.
	// Values not in DefaultCompressibleFormats are ignored with a startup warning.
	Allowed []string `yaml:"allowed,omitempty"`

	// Forbidden is a list of formats to never compress, even if in the allowed list.
	// Forbidden takes precedence over allowed.
	Forbidden []string `yaml:"forbidden,omitempty"`
}

// Validate validates tool output pipe config.
func (t *ToolOutputConfig) Validate() error {
	if !t.Enabled {
		return nil // Disabled pipes don't need strategy
	}
	// Validate target_compression_ratio when explicitly set (0 = API default, skip check)
	if t.TargetCompressionRatio != 0 && (t.TargetCompressionRatio < MinTargetCompressionRatio || t.TargetCompressionRatio > MaxTargetCompressionRatio) {
		return fmt.Errorf("tool_output: target_compression_ratio must be between %.1f (least aggressive) and %.1f (most aggressive), got %.2f",
			MinTargetCompressionRatio, MaxTargetCompressionRatio, t.TargetCompressionRatio)
	}
	if t.Strategy == "" || t.Strategy == StrategyPassthrough {
		return nil
	}
	if t.Strategy == StrategySimple || t.Strategy == StrategyTrimming {
		return nil
	}
	if IsAPIStrategy(t.Strategy) {
		// Provider or Compresr.Endpoint required
		if t.Provider == "" && t.Compresr.Endpoint == "" {
			return fmt.Errorf("tool_output: provider or compresr.endpoint required when strategy=%s", t.Strategy)
		}
		return nil
	}
	if t.Strategy == StrategyExternalProvider {
		// Provider or Compresr.Endpoint required
		if t.Provider == "" && t.Compresr.Endpoint == "" {
			return fmt.Errorf("tool_output: provider or compresr.endpoint required when strategy=external_provider")
		}
		return nil
	}
	return fmt.Errorf("tool_output: unknown strategy %q, must be 'passthrough', 'simple', 'trimming', 'compresr', or 'external_provider'", t.Strategy)
}

// TOOL DISCOVERY PIPE CONFIG

// ToolDiscoveryConfig configures tool filtering.
type ToolDiscoveryConfig struct {
	Enabled          bool   `yaml:"enabled"`           // Enable this pipe (enables lazy loading)
	Strategy         string `yaml:"strategy"`          // passthrough | relevance | compresr | tool-search
	FallbackStrategy string `yaml:"fallback_strategy"` // Fallback when primary fails

	// Provider reference (preferred over inline Compresr config)
	// References a provider defined in the top-level "providers" section.
	Provider string `yaml:"provider,omitempty"`

	// ═══════════════════════════════════════════════════════════════════
	// STAGE 1: Tool Discovery API (filter 70 → 5 tools)
	// Uses /api/compress/tool-discovery/ with tdc_coldbrew_v1
	// ═══════════════════════════════════════════════════════════════════
	Compresr CompresrConfig `yaml:"compresr,omitempty"`

	// Filtering settings
	AlwaysKeep     []string `yaml:"always_keep"`     // Tool names to never filter out
	TokenThreshold int      `yaml:"token_threshold"` // Trigger filtering when total tool definition tokens > this (default: 512)

	// Lazy loading settings (when enabled, tools become [deferred] stubs)
	EnableSearchFallback bool   `yaml:"enable_search_fallback"` // Inject gateway_search_tools (default: true)
	SearchToolName       string `yaml:"search_tool_name"`       // Name of the search tool (default: "gateway_search_tools")
	MaxSearchResults     int    `yaml:"max_search_results"`     // Max tools returned by search (default: 5)

	// ═══════════════════════════════════════════════════════════════════
	// STAGE 2: Schema Compression (compress each matched tool schema)
	// Uses /api/compress/tool-output/ with toc_latte_v1
	// Only applies when lazy loading is enabled
	// ═══════════════════════════════════════════════════════════════════
	SchemaCompression SchemaCompressionConfig `yaml:"schema_compression"`

	// DEPRECATED: Use schema_compression instead
	SearchResultCompression          SearchResultCompressionConfig `yaml:"search_result_compression"`
	EnableToolDescriptionCompression bool                          `yaml:"enable_tool_description_compression"`
}

// Validate validates tool discovery pipe config.
func (d *ToolDiscoveryConfig) Validate() error {
	if !d.Enabled {
		return nil
	}
	switch d.Strategy {
	case "", StrategyPassthrough:
		return nil
	case StrategyRelevance:
		return nil // Local keyword-based filtering, no external dependencies
	case StrategyCompresr:
		return nil // Compresr API-backed filtering, falls back to local relevance if unavailable
	case StrategyToolSearch:
		return nil // Universal dispatcher: defers all tools, uses Compresr API for search
	default:
		return fmt.Errorf("tool_discovery: unknown strategy %q, must be 'passthrough', 'relevance', 'compresr', or 'tool-search'", d.Strategy)
	}
}

// STRATEGY-SPECIFIC CONFIGS

// SearchResultCompressionConfig configures compression of gateway_search_tools results.
// This is separate from tool_output compression — it compresses the tool schemas
// returned to the LLM when they search for tools.
// Strategy is always "compresr" and token threshold is always 512.
type SearchResultCompressionConfig struct {
	Enabled bool `yaml:"enabled"` // Enable search result compression

	// Compresr/external_provider settings (optional, falls back to parent Compresr config)
	Endpoint string `yaml:"endpoint,omitempty"` // API endpoint URL
	APIKey   string `yaml:"api_key,omitempty"`  // API authentication key
}

// Validate validates search result compression config.
func (s *SearchResultCompressionConfig) Validate() error {
	return nil
}

// SchemaCompressionConfig configures per-tool schema compression (Stage 2).
// Compresses tool schemas returned by gateway_search_tools on-demand.
// Only the tools Claude requests get compressed.
//
// Uses a different endpoint than Stage 1:
// - Stage 1 (tool_discovery): /api/compress/tool-discovery/ with tdc_coldbrew_v1
// - Stage 2 (schema_compression): /api/compress/tool-output/ with toc_latte_v1
//
// Benefits over combined-blob compression:
// - Each tool compressed with query context (better relevance)
// - Parallel compression reduces latency
// - Better compression ratios on individual schemas
type SchemaCompressionConfig struct {
	// Enabled turns on per-tool schema compression (default: false)
	Enabled bool `yaml:"enabled"`

	// Compresr API settings for schema compression
	// Uses tool-output endpoint (different from tool-discovery)
	Endpoint string        `yaml:"endpoint,omitempty"` // Default: /api/compress/tool-output/
	APIKey   string        `yaml:"api_key,omitempty"`  // Falls back to parent compresr.api_key
	Model    string        `yaml:"model,omitempty"`    // Default: toc_latte_v1
	Timeout  time.Duration `yaml:"timeout,omitempty"`  // Default: 10s

	// TokenThreshold skips compression for tools below this token count (default: 200)
	// Small tools don't benefit from compression and add latency.
	TokenThreshold int `yaml:"token_threshold"`

	// Parallel enables parallel compression of multiple tools (default: true)
	// When false, tools are compressed sequentially.
	Parallel bool `yaml:"parallel"`

	// MaxConcurrent limits parallel compression workers (default: 5)
	// Prevents overwhelming the API with too many concurrent requests.
	MaxConcurrent int `yaml:"max_concurrent"`
}

// Validate validates schema compression config.
func (s *SchemaCompressionConfig) Validate() error {
	if s.TokenThreshold < 0 {
		return fmt.Errorf("schema_compression: token_threshold must be >= 0")
	}
	if s.MaxConcurrent < 0 {
		return fmt.Errorf("schema_compression: max_concurrent must be >= 0")
	}
	return nil
}

// SkipToolsConfig specifies tool categories to skip during compression.
type SkipToolsConfig struct {
	// Categories is a list of tool categories to skip (e.g., "browser").
	// Real-time content (browser) should not be compressed.
	Categories []string `yaml:"categories,omitempty"`
}

// CompresrConfig contains settings for calling the Compresr compression API.
// Not used in current release - tool output compression is disabled.
type CompresrConfig struct {
	Endpoint      string        `yaml:"endpoint"`       // Compresr API endpoint URL
	APIKey        string        `yaml:"api_key"`        // API authentication key
	Model         string        `yaml:"model"`          // Compression model to use
	Timeout       time.Duration `yaml:"timeout"`        // Request timeout
	QueryAgnostic bool          `yaml:"query_agnostic"` // If true, compression is context-agnostic
}

// TASK OUTPUT PIPE CONFIG

// TaskOutputConfig configures handling of task/subagent outputs.
//
// Task outputs are tool results produced by subagent calls (Claude Code Agent/Task tool,
// Codex wait_agent only, etc.). Detection is schema-driven:
// the gateway auto-detects the AI client from request headers and uses the matching
// ClientSchema to identify subagent tool names — no pattern configuration required.
//
// Modes:
//   - passthrough:       Route task outputs without modification (just claim IDs).
//   - external_provider: Compress each task output via an external LLM in parallel.
type TaskOutputConfig struct {
	Enabled  bool   `yaml:"enabled"`  // Enable this pipe
	Strategy string `yaml:"strategy"` // passthrough | external_provider

	// ClientOverride forces a specific client schema regardless of auto-detection.
	// Valid values: "claude_code", "codex", "generic".
	// Leave empty to use automatic detection from request headers (recommended).
	ClientOverride string `yaml:"client_override,omitempty"`

	// Provider reference for external_provider strategy.
	// References a named provider in the top-level "providers" section.
	Provider string `yaml:"provider,omitempty"`

	// ExternalProvider contains inline LLM settings for strategy=external_provider.
	// Used when Provider reference is not set.
	ExternalProvider TaskExternalProviderConfig `yaml:"external_provider,omitempty"`

	// MinTokens is the minimum token count below which task output is not compressed.
	// Only applies to external_provider strategy. Default: 256.
	MinTokens int `yaml:"min_tokens"`

	// LogFile is the base path for per-provider JSONL event logs.
	// Provider name is appended automatically: {log_file}_{provider}.jsonl
	// Example: "/var/log/gw/task_output" → "/var/log/gw/task_output_anthropic.jsonl"
	// If empty, task output events are not logged to a separate file.
	LogFile string `yaml:"log_file,omitempty"`
}

// TaskExternalProviderConfig contains LLM settings for task output compression.
type TaskExternalProviderConfig struct {
	// Provider explicitly identifies the LLM provider for auth/format handling.
	// One of: "anthropic", "openai", "gemini", "bedrock".
	// If empty, provider is auto-detected from Endpoint URL.
	Provider string        `yaml:"provider,omitempty"`
	Endpoint string        `yaml:"endpoint"` // LLM API endpoint URL
	APIKey   string        `yaml:"api_key"`  // API authentication key
	Model    string        `yaml:"model"`    // LLM model identifier
	Timeout  time.Duration `yaml:"timeout"`  // Request timeout (default: 30s)
}

// Validate validates the task output pipe config.
func (t *TaskOutputConfig) Validate() error {
	if !t.Enabled {
		return nil
	}
	switch t.Strategy {
	case "", StrategyPassthrough:
		return nil
	case StrategyExternalProvider:
		if t.Provider == "" && t.ExternalProvider.Endpoint == "" {
			return fmt.Errorf("task_output: provider or external_provider.endpoint required when strategy=external_provider")
		}
		if t.ExternalProvider.Endpoint != "" && t.ExternalProvider.Model == "" {
			return fmt.Errorf("task_output: external_provider.model required when external_provider.endpoint is set")
		}
		// TODO: When Provider is a named reference, model is validated at request time via resolveExternalProvider.
		// Consider injecting a resolver func here to catch misconfigurations at startup.
		return nil
	default:
		return fmt.Errorf("task_output: unknown strategy %q, must be 'passthrough' or 'external_provider'", t.Strategy)
	}
}
