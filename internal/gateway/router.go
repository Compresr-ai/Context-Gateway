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
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

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

// UpdateConfig swaps the router's config reference (hot-reload).
// Pipe settings (enabled, strategy, thresholds) take effect on next request.
func (r *Router) UpdateConfig(cfg *config.Config) {
	r.config = cfg
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
		log.Debug().
			Int("tools_found", len(contents)).
			Bool("flag", result.ToolDiscovery).
			Int("body_len", len(ctx.OriginalRequest)).
			Msg("router: tool_discovery check")
	}

	return result
}

// ProcessAll processes the request through ALL applicable pipes.
// When both pipes are active, they run in PARALLEL since they modify
// non-overlapping JSON paths (tool_output: messages[], tool_discovery: tools[]).
// Results are merged via sjson: messages from tool_output + tools from tool_discovery.
func (r *Router) ProcessAll(ctx *PipelineContext) ([]byte, RouteResult, error) {
	flags := r.RouteFlags(ctx)
	body := ctx.OriginalRequest

	runTO := flags.ToolOutput && r.config.Pipes.ToolOutput.Strategy != config.StrategyPassthrough
	runTD := flags.ToolDiscovery && r.config.Pipes.ToolDiscovery.Strategy != config.StrategyPassthrough

	// Fast path: only one pipe active — no parallelization overhead
	if !runTO && !runTD {
		return body, flags, nil
	}
	if runTO && !runTD {
		return r.runPipe(r.toolOutputPool, ctx, body, "tool_output"), flags, nil
	}
	if !runTO && runTD {
		return r.runPipe(r.toolDiscoveryPool, ctx, body, "tool_discovery"), flags, nil
	}

	// Both pipes active — run in parallel.
	// They modify non-overlapping JSON paths (messages[] vs tools[])
	// and non-overlapping PipeContext fields.
	var (
		toBody, tdBody []byte
		toErr, tdErr   error
		wg             sync.WaitGroup
	)

	// Deep clone PipeContext for tool_discovery to prevent data races.
	// Nil out fields only used by the OTHER pipe to make the isolation explicit.
	tdCtx := *ctx.PipeContext
	tdCtx.OriginalRequest = body
	tdCtx.ShadowRefs = nil             // Only used by tool_output
	tdCtx.ToolOutputCompressions = nil // Only used by tool_output
	if ctx.ExpandedTools != nil {      // Copy map (read by both, written by neither in Process)
		expandedCopy := make(map[string]bool, len(ctx.ExpandedTools))
		for k, v := range ctx.ExpandedTools {
			expandedCopy[k] = v
		}
		tdCtx.ExpandedTools = expandedCopy
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		worker := r.toolOutputPool.acquire()
		defer r.toolOutputPool.release(worker) // Release even on panic
		defer func() {
			if r := recover(); r != nil {
				toErr = fmt.Errorf("tool_output panic: %v", r)
				log.Error().Interface("panic", r).Msg("tool_output pipe panicked")
			}
		}()
		ctx.OriginalRequest = body
		toBody, toErr = worker.Process(ctx.PipeContext)
	}()
	go func() {
		defer wg.Done()
		worker := r.toolDiscoveryPool.acquire()
		defer r.toolDiscoveryPool.release(worker) // Release even on panic
		defer func() {
			if r := recover(); r != nil {
				tdErr = fmt.Errorf("tool_discovery panic: %v", r)
				log.Error().Interface("panic", r).Msg("tool_discovery pipe panicked")
			}
		}()
		tdBody, tdErr = worker.Process(&tdCtx)
	}()
	wg.Wait()

	// Merge tool_discovery metrics back into main context
	ctx.ToolsFiltered = tdCtx.ToolsFiltered
	ctx.DeferredTools = tdCtx.DeferredTools
	ctx.OriginalToolCount = tdCtx.OriginalToolCount
	ctx.FilteredToolCount = tdCtx.FilteredToolCount
	ctx.ToolDiscoveryModel = tdCtx.ToolDiscoveryModel
	ctx.ToolDiscoverySkipReason = tdCtx.ToolDiscoverySkipReason

	// Merge body modifications
	body = mergeParallelResults(body, toBody, toErr, tdBody, tdErr)
	return body, flags, nil
}

// runPipe executes a single pipe (fast path, no parallelization overhead).
// Uses defer for worker release to prevent pool drain on panics.
func (r *Router) runPipe(pool *Pool, ctx *PipelineContext, body []byte, name string) (result []byte) {
	worker := pool.acquire()
	defer pool.release(worker) // Release even on panic
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Str("pipe", name).Msg("pipe panicked, using original body")
			result = body
		}
	}()
	ctx.OriginalRequest = body
	modifiedBody, err := worker.Process(ctx.PipeContext)
	if err != nil {
		log.Error().Err(err).Str("pipe", name).Msg("pipe failed, using original body")
		return body
	}
	return modifiedBody
}

// mergeParallelResults combines outputs from tool_output (messages[]) and tool_discovery (tools[]).
// They modify non-overlapping JSON paths, so we take messages from tool_output and tools from tool_discovery.
func mergeParallelResults(original, toBody []byte, toErr error, tdBody []byte, tdErr error) []byte {
	// Both failed → passthrough (log both errors)
	if toErr != nil && tdErr != nil {
		log.Warn().Err(toErr).Msg("parallel merge: tool_output failed")
		log.Warn().Err(tdErr).Msg("parallel merge: tool_discovery failed")
		return original
	}
	// One failed → use the other
	if toErr != nil {
		log.Warn().Err(toErr).Msg("parallel merge: tool_output failed, using tool_discovery only")
		return tdBody
	}
	if tdErr != nil {
		log.Warn().Err(tdErr).Msg("parallel merge: tool_discovery failed, using tool_output only")
		return toBody
	}

	// Both succeeded: take tool_output's body (has compressed messages[])
	// and overlay tool_discovery's tools[] onto it
	toolsValue := gjson.GetBytes(tdBody, "tools")
	if !toolsValue.Exists() {
		// tool_discovery removed all tools
		result, err := sjson.DeleteBytes(toBody, "tools")
		if err != nil {
			return toBody
		}
		return result
	}

	result, err := sjson.SetRawBytes(toBody, "tools", []byte(toolsValue.Raw))
	if err != nil {
		log.Warn().Err(err).Msg("parallel merge failed, using tool_output result")
		return toBody
	}
	return result
}
