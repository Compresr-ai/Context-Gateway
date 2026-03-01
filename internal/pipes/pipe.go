// Package pipes defines the common Pipe interface for compression pipelines.
//
// DESIGN: Two independent pipe packages, each implementing this interface:
//   - tool_discovery/: Filter irrelevant tools
//   - tool_output/:    Compress tool results, enable expand_context
//
// FLOW:
//  1. Pipe receives adapter from gateway via PipeContext
//  2. Pipe calls adapter.Extract*() to get content for processing
//  3. Pipe processes content (compress/filter) - no provider-specific logic
//  4. Pipe calls adapter.Apply*() to patch results back
//
// The Router in gateway/ decides which pipe to use based on request content.
//
// NOTE: Pipe configuration types are defined in config.go in this package.
package pipes

import (
	"context"

	"github.com/compresr/context-gateway/internal/adapters"
)

// PipeContext carries data through pipe processing.
// Pipes use this to access the adapter and store results.
type PipeContext struct {
	// Request context for cancellation and timeouts
	RequestCtx context.Context

	// Adapter for provider-agnostic extraction/application
	Adapter adapters.Adapter

	// Original request body
	OriginalRequest []byte

	// Target model for cost-based compression decisions
	TargetModel string

	// Compression threshold (from user header)
	CompressionThreshold CompressionThreshold

	// Results
	ShadowRefs             map[string]string // ID -> original content for expand_context
	ToolOutputCompressions []ToolOutputCompression

	// Captured auth from incoming request (for OAuth/Max/Pro users without API key)
	CapturedBearerToken string
	CapturedBetaHeader  string // anthropic-beta header value from incoming request

	// Provider of the incoming request (for provider-aware skip_tools)
	Provider adapters.Provider

	// Flags set by pipes
	OutputCompressed bool
	ToolsFiltered    bool

	// Tool discovery session state (for hybrid search fallback)
	ToolSessionID string                      // Session ID for tool filtering
	ExpandedTools map[string]bool             // Tools previously found via search (force-keep)
	DeferredTools []adapters.ExtractedContent // Tools filtered out (stored for search)

	// Tool discovery model used for logging
	ToolDiscoveryModel string // Model used for tool discovery (e.g., "tdc_coldbrew_v1")
}

// ToolOutputCompression tracks individual tool output compression.
type ToolOutputCompression struct {
	ToolName          string `json:"tool_name"`
	ToolCallID        string `json:"tool_call_id"`
	ShadowID          string `json:"shadow_id"`
	OriginalBytes     int    `json:"original_bytes"`
	CompressedBytes   int    `json:"compressed_bytes"`
	CacheHit          bool   `json:"cache_hit"`
	IsLastTool        bool   `json:"is_last_tool"`
	MappingStatus     string `json:"mapping_status"` // "hit", "miss", "compressed", "passthrough_small", "passthrough_large"
	MinThreshold      int    `json:"min_threshold"`  // Min byte threshold used
	MaxThreshold      int    `json:"max_threshold"`  // Max byte threshold used
	Model             string `json:"model"`          // Compression model used (e.g., "toc_latte_v1")
	OriginalContent   string `json:"original_content"`
	CompressedContent string `json:"compressed_content"`
}

// NewPipeContext creates a new pipe context.
func NewPipeContext(adapter adapters.Adapter, body []byte) *PipeContext {
	return &PipeContext{
		Adapter:         adapter,
		OriginalRequest: body,
		ShadowRefs:      make(map[string]string),
	}
}

// Pipe defines the interface for a processing pipe.
// All pipes are independent and can run in parallel.
// Pipes must NOT contain provider-specific logic - they use adapters for that.
type Pipe interface {
	// Name returns the pipe identifier.
	Name() string

	// Strategy returns the processing strategy:
	// "passthrough" = do nothing, "compresr" = call Compresr API
	Strategy() string

	// Enabled returns whether this pipe is active.
	Enabled() bool

	// Process applies transformation using the adapter.
	// 1. Calls adapter.Extract*() to get content
	// 2. Processes content (compress/filter)
	// 3. Calls adapter.Apply*() to patch results back
	// Returns modified request body or error.
	Process(ctx *PipeContext) ([]byte, error)
}
