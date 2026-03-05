// Router routes requests to compression pipes based on content analysis.
//
// DESIGN: Content-based routing (no thresholds - intercept ALL):
//  1. Tool outputs (role: "tool") -> ToolOutputPipe
//  2. Tools present              -> ToolDiscoveryPipe
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

// RouteResult indicates which pipes should run on this request.
type RouteResult struct {
	ToolOutput    bool
	ToolDiscovery bool
}

// RouteFlags returns which pipes should run on this request.
func (r *Router) RouteFlags(ctx *PipelineContext) RouteResult {
	result := RouteResult{}
	if ctx == nil || ctx.Adapter == nil || len(ctx.OriginalRequest) == 0 {
		return result
	}

	// Check for tool outputs
	if r.config.Pipes.ToolOutput.Enabled {
		contents, err := ctx.Adapter.ExtractToolOutput(ctx.OriginalRequest)
		result.ToolOutput = err == nil && len(contents) > 0
	}

	// Check for tool discovery
	if r.config.Pipes.ToolDiscovery.Enabled {
		contents, err := ctx.Adapter.ExtractToolDiscovery(ctx.OriginalRequest, nil)
		if err == nil {
			ctx.ToolDiscoveryToolCount = len(contents)
			result.ToolDiscovery = len(contents) > 0
		}
	}

	return result
}

// ProcessAll processes the request through ALL applicable pipes.
// Order: tool_output first (modifies message content), then tool_discovery (filters tools array).
func (r *Router) ProcessAll(ctx *PipelineContext) ([]byte, RouteResult, error) {
	flags := r.RouteFlags(ctx)
	body := ctx.OriginalRequest

	// Process tool_output first (modifies tool result content in messages)
	if flags.ToolOutput && r.config.Pipes.ToolOutput.Strategy != config.StrategyPassthrough {
		worker := r.toolOutputPool.acquire()
		pipeCtx := ctx.PipeContext
		pipeCtx.OriginalRequest = body
		modifiedBody, err := worker.Process(pipeCtx)
		r.toolOutputPool.release(worker)
		if err != nil {
			log.Error().Err(err).Msg("tool_output pipe failed")
		} else {
			body = modifiedBody
		}
	}

	// Process tool_discovery second (filters tools array)
	if flags.ToolDiscovery && r.config.Pipes.ToolDiscovery.Strategy != config.StrategyPassthrough {
		worker := r.toolDiscoveryPool.acquire()
		pipeCtx := ctx.PipeContext
		pipeCtx.OriginalRequest = body // Use potentially modified body from tool_output
		modifiedBody, err := worker.Process(pipeCtx)
		r.toolDiscoveryPool.release(worker)
		if err != nil {
			log.Error().Err(err).Msg("tool_discovery pipe failed")
		} else {
			body = modifiedBody
		}
	}

	return body, flags, nil
}
