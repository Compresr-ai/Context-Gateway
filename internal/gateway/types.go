// Package gateway types - types for the context compression gateway.
//
// DESIGN: Types used by the gateway for:
//   - Pipeline processing context
//   - Request/response handling
//   - Provider configuration
//
// Types are defined here to avoid circular imports and provide clear contracts.
package gateway

import (
	"time"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/pipes"
)

// =============================================================================
// PIPELINE CONTEXT - Carries state through processing
// =============================================================================

// PipelineContext carries data through the processing pipeline.
// Created when a request arrives, passed to pipes for processing.
// Embeds pipes.PipeContext for shared pipe-related fields.
type PipelineContext struct {
	// Embedded PipeContext contains fields shared with pipes:
	// - Adapter, Provider, OriginalRequest
	// - CompressionThreshold, ShadowRefs, ToolOutputCompressions
	// - CapturedBearerToken, CapturedBetaHeader
	// - OutputCompressed, ToolsFiltered
	// - ToolSessionID, ExpandedTools, DeferredTools
	*pipes.PipeContext

	// Gateway-specific fields (not used by pipes)
	OriginalPath string // Original request path (e.g., /v1/messages)
	Model        string // Model being used
	Stream       bool   // Is this a streaming request?
	ReceivedAt   time.Time

	// Expand context usage tracking
	ExpandLoopCount int // How many times LLM called expand_context

	// Cost control
	CostSessionID string // Session ID for cost tracking

	// Preemptive summarization
	PreemptiveHeaders map[string]string // Headers to add to response
	IsCompaction      bool              // Whether this is a compaction request

	// Metrics
	OriginalTokenCount   int
	CompressedTokenCount int
	OriginalToolCount    int
	FilteredToolCount    int
}

// NewPipelineContext creates a new pipeline context.
func NewPipelineContext(provider adapters.Provider, adapter adapters.Adapter, body []byte, path string) *PipelineContext {
	pipeCtx := pipes.NewPipeContext(adapter, body)
	pipeCtx.Provider = provider
	return &PipelineContext{
		PipeContext:  pipeCtx,
		OriginalPath: path,
		ReceivedAt:   time.Now(),
	}
}

// ToolOutputCompression is an alias for pipes.ToolOutputCompression.
// Kept for backward compatibility with existing gateway code.
type ToolOutputCompression = pipes.ToolOutputCompression
