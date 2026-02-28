// Pipes configuration - compression pipeline settings.
//
// STATUS: All compression pipes are disabled in current release.
// Only preemptive summarization is enabled.
//
// DESIGN: Two independent pipes process requests:
//   - ToolOutput:    Compress tool results, store originals for expand_context
//   - ToolDiscovery: Filter irrelevant tools
//
// Each pipe has a STRATEGY: "passthrough" (noop) or "compresr" (call compression service).
//
// NOTE: This file defines pipe-specific configuration types.
// The main Config struct in config/ imports and uses these types.
package pipes

import (
	"fmt"
	"time"
)

// =============================================================================
// STRATEGY CONSTANTS
// =============================================================================

// Strategy constants for pipe execution.
const (
	StrategyPassthrough      = "passthrough"       // Do nothing, pass through unchanged
	StrategyCompresr         = "compresr"          // Call compresr platform API
	StrategySimple           = "simple"            // Simple compression (first N words)
	StrategyExternalProvider = "external_provider" // Call external LLM provider (OpenAI/Anthropic) directly
	StrategyRelevance        = "relevance"         // Local relevance-based tool filtering (no external API)
	StrategyToolSearch       = "tool-search"       // Local regex-based tool search (no external API)
)

// =============================================================================
// COMPRESSION THRESHOLDS
// =============================================================================

// CompressionThreshold represents user-selectable compression trigger thresholds.
// Set via X-Compression-Threshold header.
type CompressionThreshold string

const (
	ThresholdOff  CompressionThreshold = "off"  // No compression ever
	Threshold256  CompressionThreshold = "256"  // Compress when > 256 tokens (default)
	Threshold1K   CompressionThreshold = "1k"   // Compress when > 1,000 tokens
	Threshold2K   CompressionThreshold = "2k"   // Compress when > 2,000 tokens
	Threshold4K   CompressionThreshold = "4k"   // Compress when > 4,000 tokens
	Threshold8K   CompressionThreshold = "8k"   // Compress when > 8,000 tokens
	Threshold16K  CompressionThreshold = "16k"  // Compress when > 16,000 tokens
	Threshold32K  CompressionThreshold = "32k"  // Compress when > 32,000 tokens
	Threshold64K  CompressionThreshold = "64k"  // Compress when > 64,000 tokens
	Threshold128K CompressionThreshold = "128k" // Compress when > 128,000 tokens
)

// ThresholdTokenCounts maps thresholds to token counts.
var ThresholdTokenCounts = map[CompressionThreshold]int{
	ThresholdOff: 0, Threshold256: 256, Threshold1K: 1000, Threshold2K: 2000, Threshold4K: 4000,
	Threshold8K: 8000, Threshold16K: 16000, Threshold32K: 32000, Threshold64K: 64000, Threshold128K: 128000,
}

// DefaultThreshold is the default compression threshold when none specified.
const DefaultThreshold = Threshold256

// ParseCompressionThreshold parses a threshold string from header, returns default if invalid.
func ParseCompressionThreshold(s string) CompressionThreshold {
	switch CompressionThreshold(s) {
	case ThresholdOff, Threshold256, Threshold1K, Threshold2K, Threshold4K, Threshold8K, Threshold16K, Threshold32K, Threshold64K, Threshold128K:
		return CompressionThreshold(s)
	default:
		return DefaultThreshold
	}
}

// TokenCount returns the token count for this threshold.
// Returns -1 for ThresholdOff (meaning compression disabled).
func (t CompressionThreshold) TokenCount() int {
	if t == ThresholdOff {
		return -1
	}
	if count, ok := ThresholdTokenCounts[t]; ok {
		return count
	}
	return ThresholdTokenCounts[DefaultThreshold]
}

// =============================================================================
// PIPES CONFIG - Root configuration for all pipes
// =============================================================================

// Config contains configuration for all compression pipes.
type Config struct {
	ToolOutput    ToolOutputConfig    `yaml:"tool_output"`    // Tool output compression
	ToolDiscovery ToolDiscoveryConfig `yaml:"tool_discovery"` // Tool filtering
}

// Validate validates pipe configurations.
func (p *Config) Validate() error {
	if err := p.ToolOutput.Validate(); err != nil {
		return err
	}
	if err := p.ToolDiscovery.Validate(); err != nil {
		return err
	}
	return nil
}

// =============================================================================
// TOOL OUTPUT PIPE CONFIG
// =============================================================================

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

	// Compression settings
	MinBytes    int     `yaml:"min_bytes"`    // Below this size, no compression (default: 2048)
	MaxBytes    int     `yaml:"max_bytes"`    // Above this size, skip compression (V2, default: 64KB)
	TargetRatio float64 `yaml:"target_ratio"` // Compress to this ratio (e.g., 0.5 = 50%)

	// Expand context feature
	EnableExpandContext bool `yaml:"enable_expand_context"` // Inject expand_context tool
	IncludeExpandHint   bool `yaml:"include_expand_hint"`   // Add hint to compressed content

	// Skip compression for specific tools (e.g., Read, Edit — needed for exact matching)
	SkipTools []string `yaml:"skip_tools,omitempty"`
}

// Validate validates tool output pipe config.
func (t *ToolOutputConfig) Validate() error {
	if !t.Enabled {
		return nil // Disabled pipes don't need strategy
	}
	if t.Strategy == "" || t.Strategy == StrategyPassthrough {
		return nil
	}
	if t.Strategy == StrategySimple {
		return nil
	}
	if t.Strategy == StrategyCompresr {
		// Provider or Compresr.Endpoint required
		if t.Provider == "" && t.Compresr.Endpoint == "" {
			return fmt.Errorf("tool_output: provider or compresr.endpoint required when strategy=compresr")
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
	return fmt.Errorf("tool_output: unknown strategy %q, must be 'passthrough', 'simple', 'compresr', or 'external_provider'", t.Strategy)
}

// =============================================================================
// TOOL DISCOVERY PIPE CONFIG
// =============================================================================

// ToolDiscoveryConfig configures tool filtering.
type ToolDiscoveryConfig struct {
	Enabled          bool   `yaml:"enabled"`           // Enable this pipe
	Strategy         string `yaml:"strategy"`          // passthrough | compresr | relevance
	FallbackStrategy string `yaml:"fallback_strategy"` // Fallback when primary fails

	// Provider reference (preferred over inline Compresr config)
	// References a provider defined in the top-level "providers" section.
	Provider string `yaml:"provider,omitempty"`

	// Compresr strategy config
	Compresr CompresrConfig `yaml:"compresr,omitempty"`

	// Filtering settings
	MinTools    int      `yaml:"min_tools"`    // Below this count, no filtering (default: 5)
	MaxTools    int      `yaml:"max_tools"`    // Keep at most this many tools (default: 25)
	TargetRatio float64  `yaml:"target_ratio"` // Keep this ratio of tools (e.g., 0.8 = 80%)
	AlwaysKeep  []string `yaml:"always_keep"`  // Tool names to never filter out

	// Hybrid search fallback (allows LLM to request filtered-out tools)
	EnableSearchFallback bool   `yaml:"enable_search_fallback"` // Inject gateway_search_tools (default: true when filtering)
	SearchToolName       string `yaml:"search_tool_name"`       // Name of the search tool (default: "gateway_search_tools")
	MaxSearchResults     int    `yaml:"max_search_results"`     // Max tools returned by search (default: 5)
}

// Validate validates tool discovery pipe config.
func (d *ToolDiscoveryConfig) Validate() error {
	if !d.Enabled {
		return nil // Disabled pipes don't need strategy
	}
	if d.Strategy == "" || d.Strategy == StrategyPassthrough {
		return nil
	}
	if d.Strategy == StrategyRelevance {
		return nil // No external dependencies needed
	}
	if d.Strategy == StrategyToolSearch {
		return nil // Local regex-based search, no external dependencies
	}
	if d.Strategy == StrategyCompresr {
		// Provider or Compresr.Endpoint required
		if d.Provider == "" && d.Compresr.Endpoint == "" {
			return fmt.Errorf("tool_discovery: provider or compresr.endpoint required when strategy=compresr")
		}
		return nil
	}
	return fmt.Errorf("tool_discovery: unknown strategy %q, must be 'passthrough', 'relevance', 'tool-search', or 'compresr'", d.Strategy)
}

// =============================================================================
// STRATEGY-SPECIFIC CONFIGS
// =============================================================================

// CompresrConfig contains settings for calling the Compresr compression API.
// Not used in current release - tool output compression is disabled.
type CompresrConfig struct {
	Endpoint      string        `yaml:"endpoint"`       // Compresr API endpoint URL
	APIKey        string        `yaml:"api_key"`        // API authentication key
	Model         string        `yaml:"model"`          // Compression model to use
	Timeout       time.Duration `yaml:"timeout"`        // Request timeout
	QueryAgnostic bool          `yaml:"query_agnostic"` // If true, compression is context-agnostic
}
