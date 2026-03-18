// Package pipes defines the common Pipe interface for compression pipelines.
package pipes

import (
	"context"

	"github.com/compresr/context-gateway/internal/adapters"
	authtypes "github.com/compresr/context-gateway/internal/auth/types"
)

// PipeContext carries data through pipe processing.
// Pipes use this to access the adapter and store results.
type PipeContext struct {
	// Request context for cancellation and timeouts
	RequestCtx context.Context

	// RequestID is the unique identifier for this request (set by the gateway).
	// Used by pipes for per-event log correlation.
	RequestID string

	// Adapter for provider-agnostic extraction/application
	Adapter adapters.Adapter

	// Original request body
	OriginalRequest []byte

	// Target model for cost-based compression decisions
	TargetModel string

	// Results
	ShadowRefs             map[string]string // ID -> original content for expand_context
	ToolOutputCompressions []ToolOutputCompression
	TaskOutputCompressions []ToolOutputCompression // task/subagent outputs identified by task_output pipe

	// Captured auth from incoming request (for OAuth/Max/Pro users without API key)
	CapturedAuth authtypes.CapturedAuth

	// Provider of the incoming request (for provider-aware skip_tools)
	Provider adapters.Provider

	// UserQuery is the cleaned user prompt (injected tags stripped).
	// Set once by gateway classification; used by pipes for compression context.
	UserQuery string

	// Flags set by pipes
	OutputCompressed     bool
	ToolsFiltered        bool
	PhantomToolsInjected bool // true when phantom tools were injected into this request

	// Tool discovery session state (for hybrid search fallback)
	ToolSessionID string                      // Session ID for tool filtering
	ExpandedTools map[string]bool             // Tools previously found via search (force-keep)
	DeferredTools []adapters.ExtractedContent // Tools filtered out (stored for search)

	// Tool discovery model used for logging
	ToolDiscoveryModel string // Model used for tool discovery (e.g., "tdc_coldbrew_v1")

	// Tool discovery skip tracking
	ToolDiscoverySkipReason string // Reason for skipping tool discovery (e.g., "below_token_threshold", "no_tools")
	ToolDiscoveryToolCount  int    // Number of tools in request when skipped

	// Tool discovery counts for telemetry
	OriginalToolCount int // Tools before filtering
	KeptToolCount     int // Tools after filtering (kept)

	// CacheHit indicates if the tool discovery result was served from cache.
	// Used for lazy_loading telemetry to track cache effectiveness.
	CacheHit bool

	// SessionID for cache key (may be different from ToolSessionID for cost tracking)
	SessionID string

	// TaskOutputHandledIDs contains tool result IDs claimed by the task_output pipe.
	// The tool_output pipe skips these to avoid double-processing.
	// Populated sequentially before tool_output runs.
	TaskOutputHandledIDs map[string]struct{}

	// ClientAgent identifies which AI client is making this request.
	// Set by the gateway handler via detectClientAgent() before pipes run.
	// Used by the task_output pipe to select the appropriate ClientSchema.
	ClientAgent string
}

// ToolOutputCompression tracks individual tool output compression.
type ToolOutputCompression struct {
	ToolName          string `json:"tool_name"`
	ToolCallID        string `json:"tool_call_id"`
	ShadowID          string `json:"shadow_id"`
	OriginalTokens    int    `json:"original_tokens"`
	CompressedTokens  int    `json:"compressed_tokens"`
	CacheHit          bool   `json:"cache_hit"`
	IsLastTool        bool   `json:"is_last_tool"`
	MappingStatus     string `json:"mapping_status"` // "hit", "miss", "compressed", "passthrough_small", "passthrough_large"
	MinThreshold      int    `json:"min_threshold"`  // Min token threshold used
	MaxThreshold      int    `json:"max_threshold"`  // Max token threshold used
	Model             string `json:"model"`          // Compression model used (e.g., "toc_latte_v1")
	Query             string `json:"query"`          // User query used for compression context
	QueryAgnostic     bool   `json:"query_agnostic"` // Whether compression used empty query
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
