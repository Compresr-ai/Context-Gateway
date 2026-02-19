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
	"github.com/compresr/context-gateway/internal/adapters"
)

// PipeContext carries data through pipe processing.
// Pipes use this to access the adapter and store results.
type PipeContext struct {
	// Adapter for provider-agnostic extraction/application
	Adapter adapters.Adapter

	// Original request body
	OriginalRequest []byte

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
	ToolsSent        int // Tools kept after filtering
	ToolsRemoved     int // Tools removed by filtering
}

// ToolOutputCompression tracks individual tool output compression.
type ToolOutputCompression struct {
	ToolName          string
	ToolCallID        string
	ShadowID          string
	OriginalContent   string
	CompressedContent string
	OriginalBytes     int
	CompressedBytes   int
	CacheHit          bool
	IsLastTool        bool
	MappingStatus     string // "hit", "miss", "compressed", "passthrough_small", "passthrough_large"
	MinThreshold      int    // Min byte threshold used
	MaxThreshold      int    // Max byte threshold used
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
	// "passthrough" = do nothing, "api" = call Compresr API
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
