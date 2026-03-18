// Router routes requests to compression pipes based on content analysis.
package gateway

import (
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/compresr/context-gateway/internal/pipes"
	taskoutput "github.com/compresr/context-gateway/internal/pipes/task_output"
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
	PipeTaskOutput    = monitoring.PipeTaskOutput
)

// Router routes requests to the appropriate pipe based on content analysis.
type Router struct {
	mu                sync.RWMutex
	config            *config.Config
	taskOutputPool    *Pool // task output pipe (runs before tool_output)
	toolOutputPool    *Pool
	toolDiscoveryPool *Pool
	taskOutputLogger  *taskoutput.Logger // shared logger for all task_output pool workers
	store             store.Store        // kept for pool rebuild on config reload
	poolSize          int
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

	logger := taskoutput.NewLogger(cfg.Pipes.TaskOutput.LogFile)
	return &Router{
		config:           cfg,
		store:            st,
		poolSize:         poolSize,
		taskOutputLogger: logger,
		taskOutputPool: newPool(poolSize, func() pipes.Pipe {
			return taskoutput.New(cfg, logger)
		}),
		toolOutputPool: newPool(poolSize, func() pipes.Pipe {
			return tooloutput.New(cfg, st)
		}),
		toolDiscoveryPool: newPool(poolSize, func() pipes.Pipe {
			return tooldiscovery.New(cfg)
		}),
	}
}

// Close releases resources held by the router (log file descriptors, etc.).
func (r *Router) Close() error {
	r.mu.Lock()
	logger := r.taskOutputLogger
	r.mu.Unlock()
	if logger != nil {
		return logger.Close()
	}
	return nil
}

// UpdateConfig swaps the router's config and rebuilds pipe pools (hot-reload).
// New pools are built before acquiring the lock so in-flight requests on the old
// pools complete normally — old pool workers are released back to the old pool
// channel and garbage-collected once no references remain.
// The old logger is closed after the lock is released to avoid holding the lock
// during I/O.
func (r *Router) UpdateConfig(cfg *config.Config) {
	newLogger := taskoutput.NewLogger(cfg.Pipes.TaskOutput.LogFile)
	newTA := newPool(r.poolSize, func() pipes.Pipe {
		return taskoutput.New(cfg, newLogger)
	})
	newTO := newPool(r.poolSize, func() pipes.Pipe {
		return tooloutput.New(cfg, r.store)
	})
	newTD := newPool(r.poolSize, func() pipes.Pipe {
		return tooldiscovery.New(cfg)
	})

	r.mu.Lock()
	oldLogger := r.taskOutputLogger
	r.config = cfg
	r.taskOutputLogger = newLogger
	r.taskOutputPool = newTA
	r.toolOutputPool = newTO
	r.toolDiscoveryPool = newTD
	r.mu.Unlock()

	// Close old logger after releasing the lock to avoid holding the lock during I/O.
	if oldLogger != nil {
		if err := oldLogger.Close(); err != nil {
			log.Warn().Err(err).Msg("router: failed to close old task_output logger on config reload")
		}
	}
}

// snapshot returns a consistent read of config + pools under a short RLock.
// Callers use the returned values for the duration of one request so they
// see a coherent config snapshot even if UpdateConfig fires concurrently.
func (r *Router) snapshot() (*config.Config, *Pool, *Pool, *Pool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config, r.taskOutputPool, r.toolOutputPool, r.toolDiscoveryPool
}

// RouteResult indicates which pipes should run on this request.
type RouteResult struct {
	TaskOutput    bool // task output pipe (runs before tool_output)
	ToolOutput    bool
	ToolDiscovery bool
}

// RouteFlags returns which pipes should run on this request.
func (r *Router) RouteFlags(ctx *PipelineContext, cfg *config.Config) RouteResult {
	result := RouteResult{}
	if ctx == nil || ctx.Adapter == nil || len(ctx.OriginalRequest) == 0 {
		return result
	}

	// Extract tool outputs once for both task_output and tool_output checks.
	var toolOutputs []adapters.ExtractedContent
	if cfg.Pipes.TaskOutput.Enabled || cfg.Pipes.ToolOutput.Enabled {
		toolOutputs, _ = ctx.Adapter.ExtractToolOutput(ctx.OriginalRequest)
	}

	// Check for task outputs (enabled + tool results present).
	// Patterns are optional — with no patterns configured the pipe runs in passthrough
	// and claims nothing (tool_output still processes all outputs). The pipe itself
	// handles empty-patterns gracefully.
	result.TaskOutput = cfg.Pipes.TaskOutput.Enabled && len(toolOutputs) > 0

	// Check for tool outputs.
	result.ToolOutput = cfg.Pipes.ToolOutput.Enabled && len(toolOutputs) > 0

	// Check for tool discovery
	if cfg.Pipes.ToolDiscovery.Enabled {
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
//
// Execution order:
//  1. task_output (sequential) — claims subagent tool result IDs, optionally compresses them.
//  2. tool_output + tool_discovery (parallel) — skips IDs claimed by task_output.
//
// tool_output (messages[]) and tool_discovery (tools[]) modify non-overlapping JSON
// paths so they can run concurrently. Results are merged via sjson.
func (r *Router) ProcessAll(ctx *PipelineContext) ([]byte, RouteResult, error) {
	// Take a consistent snapshot so config changes mid-request don't produce torn reads.
	cfg, taPool, toPool, tdPool := r.snapshot()

	flags := r.RouteFlags(ctx, cfg)
	body := ctx.OriginalRequest

	// Phase 1: task_output runs first (sequential).
	// It populates ctx.TaskOutputHandledIDs so tool_output can skip claimed IDs.
	// Skip passthrough with no active client: GenericSchema matches nothing, so
	// running the pipe would be pure overhead.
	effectiveClient := ctx.ClientAgent
	if cfg.Pipes.TaskOutput.ClientOverride != "" {
		effectiveClient = cfg.Pipes.TaskOutput.ClientOverride
	}
	runTA := flags.TaskOutput &&
		(cfg.Pipes.TaskOutput.Strategy != config.StrategyPassthrough ||
			effectiveClient != "")
	if runTA {
		body = r.runPipe(taPool, ctx, body, "task_output")
	}

	runTO := flags.ToolOutput && cfg.Pipes.ToolOutput.Strategy != config.StrategyPassthrough
	runTD := flags.ToolDiscovery && cfg.Pipes.ToolDiscovery.Strategy != config.StrategyPassthrough

	// Fast path: only one pipe active — no parallelization overhead
	if !runTO && !runTD {
		return body, flags, nil
	}
	if runTO && !runTD {
		return r.runPipe(toPool, ctx, body, "tool_output"), flags, nil
	}
	if !runTO && runTD {
		return r.runPipe(tdPool, ctx, body, "tool_discovery"), flags, nil
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
		worker := toPool.acquire()
		defer toPool.release(worker) // Release even on panic
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
		worker := tdPool.acquire()
		defer tdPool.release(worker) // Release even on panic
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
	ctx.KeptToolCount = tdCtx.KeptToolCount
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
