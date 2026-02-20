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
	"github.com/compresr/context-gateway/internal/config"
)

// =============================================================================
// PIPELINE CONTEXT - Carries state through processing
// =============================================================================

// PipelineContext carries data through the processing pipeline.
// Created when a request arrives, passed to pipes for processing.
type PipelineContext struct {
	// Provider info
	Provider adapters.Provider
	Adapter  adapters.Adapter

	// Request data
	OriginalRequest []byte // Raw original request for forwarding
	OriginalPath    string // Original request path (e.g., /v1/messages)
	Model           string // Model being used
	Stream          bool   // Is this a streaming request?
	ReceivedAt      time.Time

	// User-selected compression threshold (from X-Compression-Threshold header)
	// Values: off, 256, 1k, 2k, 4k, 8k, 16k, 32k, 64k, 128k
	CompressionThreshold config.CompressionThreshold

	// Pipe processing results
	OutputCompressed bool
	ToolsFiltered    bool

	// Shadow context references (for expand_context)
	ShadowRefs map[string]string // ID -> stored content

	// Expand context usage tracking
	ExpandLoopCount int // How many times LLM called expand_context

	// Individual tool output compressions for detailed logging
	ToolOutputCompressions []ToolOutputCompression

	// Captured auth from incoming request (for compression with OAuth tokens)
	CapturedBearerToken string
	CapturedBetaHeader  string

	// Cost control
	CostSessionID string // Session ID for cost tracking

	// Preemptive summarization
	PreemptiveHeaders map[string]string // Headers to add to response
	IsCompaction      bool              // Whether this is a compaction request

	// Hybrid tool discovery
	ToolSessionID string                      // Session ID for tool filtering
	ExpandedTools map[string]bool             // Tools found via search (force-keep on subsequent requests)
	DeferredTools []adapters.ExtractedContent // Tools filtered out (available for search)

	// Metrics
	OriginalTokenCount   int
	CompressedTokenCount int
	OriginalToolCount    int
	FilteredToolCount    int
}

// NewPipelineContext creates a new pipeline context.
func NewPipelineContext(provider adapters.Provider, adapter adapters.Adapter, body []byte, path string) *PipelineContext {
	return &PipelineContext{
		Provider:        provider,
		Adapter:         adapter,
		OriginalRequest: body,
		OriginalPath:    path,
		ReceivedAt:      time.Now(),
		ShadowRefs:      make(map[string]string),
	}
}

// ToolOutputCompression tracks individual tool output compression for logging.
type ToolOutputCompression struct {
	ToolName          string `json:"tool_name"`
	ToolCallID        string `json:"tool_call_id"`
	ShadowID          string `json:"shadow_id"`
	OriginalContent   string `json:"original_content"`
	CompressedContent string `json:"compressed_content"`
	OriginalBytes     int    `json:"original_bytes"`
	CompressedBytes   int    `json:"compressed_bytes"`
	CacheHit          bool   `json:"cache_hit"`
	IsLastTool        bool   `json:"is_last_tool"`
	MappingStatus     string `json:"mapping_status"` // "hit", "compressed", "passthrough_small", "passthrough_large"
	MinThreshold      int    `json:"min_threshold"`  // Min byte threshold used
	MaxThreshold      int    `json:"max_threshold"`  // Max byte threshold used
}
