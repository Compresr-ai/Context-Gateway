// Router routes requests to compression pipes based on content analysis.
//
// DESIGN: Content-based routing (no thresholds - intercept ALL):
//  1. Tool outputs (role: "tool") → ToolOutputPipe
//  2. Tools present             → ToolDiscoveryPipe (stub)
//
// Uses worker pools for concurrent pipe execution.
// Threshold logic (min bytes) is handled INSIDE each pipe.
package gateway

import (
	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/compresr/context-gateway/internal/pipes"
	tooldiscovery "github.com/compresr/context-gateway/internal/pipes/tool_discovery"
	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
	"github.com/compresr/context-gateway/internal/store"
)

// PipeType is an alias to monitoring.PipeType for convenience.
type PipeType = monitoring.PipeType

// Pipe type constants - re-exported from monitoring for convenience.
const (
	PipeNone          = monitoring.PipeNone
	PipeToolOutput    = monitoring.PipeToolOutput
	PipeToolDiscovery = monitoring.PipeToolDiscovery
)

// Router routes requests to the appropriate pipe based on content analysis.
type Router struct {
	config            *config.Config
	toolOutputPool    *Pool
	toolDiscoveryPool *Pool
}

// Pool manages workers for a pipe type.
type Pool struct {
	workers chan pipes.Pipe
	size    int
}

func newPool(size int, factory func() pipes.Pipe) *Pool {
	p := &Pool{workers: make(chan pipes.Pipe, size), size: size}
	for i := 0; i < size; i++ {
		p.workers <- factory()
	}
	return p
}

func (p *Pool) acquire() pipes.Pipe     { return <-p.workers }
func (p *Pool) release(pipe pipes.Pipe) { p.workers <- pipe }

// NewRouter creates a new router with worker pools.
func NewRouter(cfg *config.Config, st store.Store) *Router {
	poolSize := 10

	return &Router{
		config: cfg,
		toolOutputPool: newPool(poolSize, func() pipes.Pipe {
			return tooloutput.New(cfg, st)
		}),
		toolDiscoveryPool: newPool(poolSize, func() pipes.Pipe {
			return tooldiscovery.New(cfg)
		}),
	}
}

// Route analyzes request and determines which pipe should handle it.
// Routes based on content detection (priority order) - NO THRESHOLDS:
//  1. Any tool outputs (role: "tool" or Anthropic tool_result blocks) → ToolOutput pipe
//  2. Any tools present → ToolDiscovery pipe
//
// DESIGN: Router delegates ALL extraction logic to the adapter.
// The adapter is responsible for provider-specific parsing (OpenAI vs Anthropic format).
// Router only uses the extraction results to make routing decisions.
// Pipes will call adapter.Extract*() again for processing (adapters can cache if needed).
func (r *Router) Route(ctx *PipelineContext) PipeType {
	if ctx == nil || ctx.Adapter == nil || len(ctx.OriginalRequest) == 0 {
		return PipeNone
	}

	// Priority 1: ANY tool outputs - intercept ALL
	// Delegate extraction to adapter - handles both OpenAI and Anthropic formats
	if r.config.Pipes.ToolOutput.Enabled {
		contents, err := ctx.Adapter.ExtractToolOutput(ctx.OriginalRequest)
		if err == nil && len(contents) > 0 {
			return PipeToolOutput
		}
	}

	// Priority 2: ANY tools present - intercept ALL
	// Delegate extraction to adapter for provider-agnostic tool detection
	if r.config.Pipes.ToolDiscovery.Enabled {
		contents, err := ctx.Adapter.ExtractToolDiscovery(ctx.OriginalRequest, nil)
		if err == nil && len(contents) > 0 {
			return PipeToolDiscovery
		}
	}

	return PipeNone
}

// Process routes and processes the request through the appropriate pipe.
// Returns the modified request body (or original on error).
func (r *Router) Process(ctx *PipelineContext) ([]byte, error) {
	pipeType := r.Route(ctx)
	return r.processPipe(ctx, pipeType)
}

// ProcessResponse handles RESPONSE-SIDE compression for Tool Output and Tool Discovery.
// Called AFTER receiving response from LLM.
// On success: returns compressed response body (original cached for expand)
// On failure: returns original response unchanged
func (r *Router) ProcessResponse(ctx *PipelineContext, pipeType PipeType) ([]byte, error) {
	if pipeType != PipeToolOutput && pipeType != PipeToolDiscovery {
		return ctx.OriginalRequest, nil
	}

	var pool *Pool
	switch pipeType {
	case PipeToolOutput:
		pool = r.toolOutputPool
	case PipeToolDiscovery:
		pool = r.toolDiscoveryPool
	default:
		return ctx.OriginalRequest, nil
	}

	worker := pool.acquire()
	defer pool.release(worker)

	pipeCtx := r.toPipeContext(ctx)
	modifiedBody, err := worker.Process(pipeCtx)
	if err != nil {
		log.Error().Err(err).Str("pipe", worker.Name()).Msg("response compression failed")
		return ctx.OriginalRequest, err // Return original on failure
	}
	r.copyPipeResults(pipeCtx, ctx)
	return modifiedBody, nil
}

func (r *Router) processPipe(ctx *PipelineContext, pipeType PipeType) ([]byte, error) {
	if pipeType == PipeNone {
		return ctx.OriginalRequest, nil
	}

	var pool *Pool
	switch pipeType {
	case PipeToolOutput:
		pool = r.toolOutputPool
	case PipeToolDiscovery:
		pool = r.toolDiscoveryPool
	default:
		return ctx.OriginalRequest, nil
	}

	worker := pool.acquire()
	defer pool.release(worker)

	pipeCtx := r.toPipeContext(ctx)
	modifiedBody, err := worker.Process(pipeCtx)
	if err != nil {
		log.Error().Err(err).Str("pipe", worker.Name()).Msg("pipe failed")
		return ctx.OriginalRequest, err
	}
	r.copyPipeResults(pipeCtx, ctx)
	return modifiedBody, nil
}

// toPipeContext converts gateway PipelineContext to pipes.PipeContext.
// Pipes will delegate extraction to adapter - no pre-extracted content passed.
func (r *Router) toPipeContext(ctx *PipelineContext) *pipes.PipeContext {
	pipeCtx := pipes.NewPipeContext(ctx.Adapter, ctx.OriginalRequest)
	pipeCtx.CompressionThreshold = ctx.CompressionThreshold
	return pipeCtx
}

// copyPipeResults copies results from pipes.PipeContext back to PipelineContext.
func (r *Router) copyPipeResults(pipeCtx *pipes.PipeContext, ctx *PipelineContext) {
	ctx.OutputCompressed = pipeCtx.OutputCompressed
	ctx.ToolsFiltered = pipeCtx.ToolsFiltered

	// Merge shadow refs
	for k, v := range pipeCtx.ShadowRefs {
		ctx.ShadowRefs[k] = v
	}

	// Copy tool output compressions
	for _, toc := range pipeCtx.ToolOutputCompressions {
		ctx.ToolOutputCompressions = append(ctx.ToolOutputCompressions, ToolOutputCompression{
			ToolName:          toc.ToolName,
			ToolCallID:        toc.ToolCallID,
			ShadowID:          toc.ShadowID,
			OriginalContent:   toc.OriginalContent,
			CompressedContent: toc.CompressedContent,
			OriginalBytes:     toc.OriginalBytes,
			CompressedBytes:   toc.CompressedBytes,
			CacheHit:          toc.CacheHit,
			IsLastTool:        toc.IsLastTool,
			MappingStatus:     toc.MappingStatus,
			MinThreshold:      toc.MinThreshold,
			MaxThreshold:      toc.MaxThreshold,
		})
	}
}
