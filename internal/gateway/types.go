// Package gateway types - types for the context compression gateway.
package gateway

import (
	"time"

	"github.com/compresr/context-gateway/internal/adapters"
	authtypes "github.com/compresr/context-gateway/internal/auth/types"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
	"github.com/tidwall/gjson"
)

// CAPTURED AUTH - Centralized auth header capture for all subsystems

// CapturedAuth is an alias for the centralized auth type.
// All subsystems (pipes, preemptive, tool discovery) use authtypes.CapturedAuth directly.
type CapturedAuth = authtypes.CapturedAuth

// PIPELINE CONTEXT - Carries state through processing

// PipelineContext carries data through the processing pipeline.
// Created when a request arrives, passed to pipes for processing.
// Embeds pipes.PipeContext for shared pipe-related fields.
type PipelineContext struct {
	// Embedded PipeContext contains fields shared with pipes:
	// - Adapter, Provider, OriginalRequest
	// - ShadowRefs, ToolOutputCompressions
	// - CapturedAuth (unified auth from incoming request)
	// - OutputCompressed, ToolsFiltered
	// - ToolSessionID, ExpandedTools, DeferredTools
	*pipes.PipeContext

	// Gateway-specific fields (not used by pipes)
	OriginalPath string // Original request path (e.g., /v1/messages)
	Model        string // Model being used
	Stream       bool   // Is this a streaming request?
	ReceivedAt   time.Time

	// Expand context usage tracking
	ExpandLoopCount int  // How many times LLM called expand_context
	StreamTruncated bool // True if streaming response exceeded buffer limit

	// Cost control
	CostSessionID string // Session ID for cost tracking (hash-based, may vary between requests)

	// Stable conversation fingerprint — hash of clean first user message text (injected XML stripped).
	// Unlike CostSessionID, this is stable across all requests in the same conversation.
	// Used to distinguish the main conversation from subagent conversations for savings/prompt recording.
	StableFingerprint string

	// Preemptive summarization
	PreemptiveHeaders map[string]string // Headers to add to response
	IsCompaction      bool              // Whether this is a compaction request

	// Metrics
	OriginalTokenCount   int
	CompressedTokenCount int
	// Note: OriginalToolCount and FilteredToolCount are in embedded PipeContext

	// Session monitoring
	MonitorSessionID string // Session ID for the monitoring dashboard

	// Unified user message classification — single source of truth.
	// Computed once at the top of handleProxy, used by all downstream consumers.
	Classification MessageClassification
}

// NewPipelineContext creates a new pipeline context.
func NewPipelineContext(provider adapters.Provider, adapter adapters.Adapter, body []byte, path string) *PipelineContext {
	pipeCtx := pipes.NewPipeContext(adapter, body)
	pipeCtx.Provider = provider
	return &PipelineContext{
		PipeContext:    pipeCtx,
		OriginalPath:   path,
		ReceivedAt:     time.Now(),
		Classification: classifyUserMessage(body, adapter),
	}
}

// ToolOutputCompression is an alias for pipes.ToolOutputCompression.
// Kept for backward compatibility with existing gateway code.
type ToolOutputCompression = pipes.ToolOutputCompression

// isResponsesAPI detects OpenAI Responses API format: has "input" but no "messages".
func isResponsesAPI(body []byte) bool {
	return gjson.GetBytes(body, "input").Exists() && !gjson.GetBytes(body, "messages").Exists()
}

// searchToolName returns the configured search tool name or the default.
func (g *Gateway) searchToolName() string {
	name := g.cfg().Pipes.ToolDiscovery.SearchToolName
	if name == "" {
		return config.DefaultSearchToolName
	}
	return name
}
