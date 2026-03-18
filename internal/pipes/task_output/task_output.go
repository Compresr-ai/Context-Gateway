// Package taskoutput handles task/subagent output in the compression pipeline.
// See types.go for design notes.
package taskoutput

import (
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/external"
	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
	"github.com/compresr/context-gateway/internal/tokenizer"
)

// Pipe processes task/subagent tool outputs before the tool_output pipe runs.
// It claims matching tool result IDs so the tool_output pipe skips them.
type Pipe struct {
	cfg    *config.Config
	logger *Logger // shared logger; may be nil (logging disabled)
}

// New creates a new task output pipe from config with a shared logger.
// Pass nil for logger to disable event logging.
func New(cfg *config.Config, logger *Logger) *Pipe {
	return &Pipe{
		cfg:    cfg,
		logger: logger,
	}
}

// Name returns the pipe identifier.
func (p *Pipe) Name() string { return PipeName }

// Strategy returns the configured strategy string.
func (p *Pipe) Strategy() string { return p.cfg.Pipes.TaskOutput.Strategy }

// Enabled reports whether the pipe is active.
func (p *Pipe) Enabled() bool { return p.cfg.Pipes.TaskOutput.Enabled }

// Process runs the task output pipe.
//
// Flow:
//  1. Detect client agent from PipeContext.ClientAgent.
//  2. Select the appropriate ClientSchema via SchemaForClient.
//  3. Extract all tool outputs from the request via the adapter.
//  4. Filter to items whose tool name matches the schema (IsTaskTool).
//  5. Claim matched IDs in ctx.TaskOutputHandledIDs (prevents double-processing).
//  6. Apply strategy: passthrough (no-op) or external_provider (parallel LLM calls).
//  7. Log per-provider events to separate JSONL files.
func (p *Pipe) Process(ctx *pipes.PipeContext) ([]byte, error) {
	if !p.Enabled() {
		return ctx.OriginalRequest, nil
	}

	// Select schema: config override takes precedence over auto-detected client.
	clientAgent := ClientAgent(ctx.ClientAgent)
	if override := p.cfg.Pipes.TaskOutput.ClientOverride; override != "" {
		clientAgent = ClientAgent(override)
	}
	extractor := NewExtractor(clientAgent)

	// GenericSchema matches nothing — skip early without allocating.
	if _, isGeneric := extractor.Schema().(*GenericSchema); isGeneric {
		return ctx.OriginalRequest, nil
	}

	// Extract all tool outputs using the adapter (provider-agnostic).
	allOutputs, err := ctx.Adapter.ExtractToolOutput(ctx.OriginalRequest)
	if err != nil || len(allOutputs) == 0 {
		return ctx.OriginalRequest, nil
	}

	// Filter to items matching the client's schema.
	taskOutputs := extractor.ExtractAll(allOutputs)
	if len(taskOutputs) == 0 {
		return ctx.OriginalRequest, nil
	}

	provider := string(ctx.Provider)
	cfg := p.cfg.Pipes.TaskOutput

	log.Debug().
		Str("client_agent", string(clientAgent)).
		Str("provider", provider).
		Int("task_items", len(taskOutputs)).
		Str("strategy", cfg.Strategy).
		Msg("task_output: processing task items")

	switch cfg.Strategy {
	case pipes.StrategyExternalProvider:
		// Claim IDs so tool_output pipe skips them — we are actually compressing them.
		if ctx.TaskOutputHandledIDs == nil {
			ctx.TaskOutputHandledIDs = make(map[string]struct{}, len(taskOutputs))
		}
		for _, to := range taskOutputs {
			if raw, ok := to.Source.(adapters.ExtractedContent); ok {
				ctx.TaskOutputHandledIDs[raw.ID] = struct{}{}
			}
		}
		return p.processExternalProvider(ctx, extractor, taskOutputs, provider)
	default: // passthrough — do not claim IDs; let tool_output compress them
		p.logEvents(ctx.RequestID, provider, string(clientAgent), taskOutputs, pipes.StrategyPassthrough)
		// Populate TaskOutputCompressions so the gateway can log these events
		// to the unified task_output_compression.jsonl (even in passthrough mode).
		for _, to := range taskOutputs {
			raw, _ := to.Source.(adapters.ExtractedContent)
			origTokens := tokenizer.CountTokens(to.PrimaryContent)
			ctx.TaskOutputCompressions = append(ctx.TaskOutputCompressions, pipes.ToolOutputCompression{
				ToolName:         raw.ToolName,
				ToolCallID:       raw.ID,
				OriginalTokens:   origTokens,
				CompressedTokens: origTokens,
				OriginalContent:  to.PrimaryContent,
				MappingStatus:    "passthrough",
			})
		}
		return ctx.OriginalRequest, nil
	}
}

// EXTERNAL PROVIDER STRATEGY

// taskResult holds the outcome of compressing one task item.
type taskResult struct {
	output     TaskOutput
	compressed string
	origTokens int
	compTokens int
	err        error
}

// processExternalProvider compresses all task items in parallel via an external LLM.
// On any item error it falls back to passthrough for that item.
func (p *Pipe) processExternalProvider(
	ctx *pipes.PipeContext,
	extractor *TaskOutputExtractor,
	taskOutputs []TaskOutput,
	provider string,
) ([]byte, error) {
	cfg := p.cfg.Pipes.TaskOutput
	minTokens := cfg.MinTokens
	if minTokens == 0 {
		minTokens = DefaultMinTokens
	}

	// Resolve external provider settings.
	ep, apiKey, model, epProvider, timeout := p.resolveExternalProvider()
	if ep == "" {
		log.Warn().Msg("task_output: external_provider strategy but no endpoint configured, falling back to passthrough")
		p.logEvents(ctx.RequestID, provider, ctx.ClientAgent, taskOutputs, pipes.StrategyPassthrough)
		for _, to := range taskOutputs {
			raw, _ := to.Source.(adapters.ExtractedContent)
			origTokens := tokenizer.CountTokens(to.PrimaryContent)
			ctx.TaskOutputCompressions = append(ctx.TaskOutputCompressions, pipes.ToolOutputCompression{
				ToolName:         raw.ToolName,
				ToolCallID:       raw.ID,
				OriginalTokens:   origTokens,
				CompressedTokens: origTokens,
				OriginalContent:  to.PrimaryContent,
				MappingStatus:    "passthrough_no_endpoint",
			})
		}
		return ctx.OriginalRequest, nil
	}

	sem := make(chan struct{}, maxConcurrentCompressions)
	results := make([]taskResult, len(taskOutputs))
	var wg sync.WaitGroup

	for i, to := range taskOutputs {
		wg.Add(1)
		go func(idx int, to TaskOutput) {
			defer wg.Done()
			origTokens := tokenizer.CountTokens(to.PrimaryContent)

			// Skip small items (below threshold).
			if origTokens <= minTokens {
				results[idx] = taskResult{
					output:     to,
					compressed: to.PrimaryContent,
					origTokens: origTokens,
					compTokens: origTokens,
				}
				return
			}

			sem <- struct{}{}
			defer func() { <-sem }()

			raw, _ := to.Source.(adapters.ExtractedContent)
			compressed, err := p.callLLM(ctx, raw, ep, apiKey, epProvider, model, timeout)
			if err != nil {
				log.Warn().
					Err(err).
					Str("tool", raw.ToolName).
					Msg("task_output: LLM compression failed, using original")
				results[idx] = taskResult{
					output:     to,
					compressed: to.PrimaryContent,
					origTokens: origTokens,
					compTokens: origTokens,
					err:        err,
				}
				return
			}
			results[idx] = taskResult{
				output:     to,
				compressed: compressed,
				origTokens: origTokens,
				compTokens: tokenizer.CountTokens(compressed),
			}
		}(i, to)
	}
	wg.Wait()

	// Build CompressedResult slice for the adapter.
	// Use Reconstruct to reassemble content with preserved metadata.
	compressedResults := make([]adapters.CompressedResult, 0, len(results))
	for _, r := range results {
		finalContent := extractor.Reconstruct(r.output, r.compressed)
		if raw, ok := r.output.Source.(adapters.ExtractedContent); ok {
			compressedResults = append(compressedResults, adapters.CompressedResult{
				ID:           raw.ID,
				Compressed:   finalContent,
				MessageIndex: raw.MessageIndex,
				BlockIndex:   raw.BlockIndex,
			})
		}
	}

	// Populate TaskOutputCompressions for unified monitoring (always, before Apply).
	for _, r := range results {
		raw, _ := r.output.Source.(adapters.ExtractedContent)
		status := "compressed"
		if r.err != nil {
			status = "error"
		} else if r.origTokens == r.compTokens {
			status = "passthrough_small"
		}
		ctx.TaskOutputCompressions = append(ctx.TaskOutputCompressions, pipes.ToolOutputCompression{
			ToolName:          raw.ToolName,
			ToolCallID:        raw.ID,
			OriginalTokens:    r.origTokens,
			CompressedTokens:  r.compTokens,
			OriginalContent:   r.output.PrimaryContent,
			CompressedContent: r.compressed,
			MappingStatus:     status,
		})
	}

	// Apply compressed content back via the adapter.
	modified, err := ctx.Adapter.ApplyToolOutput(ctx.OriginalRequest, compressedResults)
	if err != nil {
		log.Warn().Err(err).Msg("task_output: ApplyToolOutput failed, returning original body")
		p.logEvents(ctx.RequestID, provider, ctx.ClientAgent, taskOutputs, pipes.StrategyPassthrough)
		// Reset compressions to reflect that original content was actually returned.
		ctx.TaskOutputCompressions = ctx.TaskOutputCompressions[:0]
		for _, r := range results {
			raw, _ := r.output.Source.(adapters.ExtractedContent)
			origTokens := tokenizer.CountTokens(r.output.PrimaryContent)
			ctx.TaskOutputCompressions = append(ctx.TaskOutputCompressions, pipes.ToolOutputCompression{
				ToolName:         raw.ToolName,
				ToolCallID:       raw.ID,
				OriginalTokens:   origTokens,
				CompressedTokens: origTokens,
				OriginalContent:  r.output.PrimaryContent,
				MappingStatus:    "passthrough_apply_error",
			})
		}
		return ctx.OriginalRequest, nil
	}

	p.logCompressedEvents(ctx.RequestID, provider, ctx.ClientAgent, results)
	return modified, nil
}

// callLLM sends one task output to the external LLM for compression.
func (p *Pipe) callLLM(
	ctx *pipes.PipeContext,
	item adapters.ExtractedContent,
	endpoint, apiKey, llmProvider, model string,
	timeout time.Duration,
) (string, error) {
	// Use captured token when no static API key is configured.
	// CapturedAuth.IsXAPIKey distinguishes API key (x-api-key) from Bearer (OAuth/subscription).
	bearerToken := ""
	if apiKey == "" && ctx.CapturedAuth.HasAuth() {
		if ctx.CapturedAuth.IsXAPIKey {
			apiKey = ctx.CapturedAuth.Token
		} else {
			bearerToken = ctx.CapturedAuth.Token
		}
	}

	params := external.CallLLMParams{
		Provider:     llmProvider,
		Endpoint:     endpoint,
		ProviderKey:  apiKey,
		BearerAuth:   bearerToken,
		Model:        model,
		SystemPrompt: systemPrompt,
		UserPrompt:   fmt.Sprintf(userPromptFmt, item.ToolName, item.Content),
		MaxTokens:    maxResponseTokens,
		Timeout:      timeout,
	}

	result, err := external.CallLLM(ctx.RequestCtx, params)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

// resolveExternalProvider returns endpoint, apiKey, model, provider, and timeout
// from the task_output config, falling back to config.Providers if a Provider
// reference is set.
func (p *Pipe) resolveExternalProvider() (endpoint, apiKey, model, provider string, timeout time.Duration) {
	cfg := p.cfg.Pipes.TaskOutput

	// Named provider reference takes precedence.
	if cfg.Provider != "" {
		if resolved, err := p.cfg.ResolveProvider(cfg.Provider); err == nil {
			endpoint = resolved.Endpoint
			apiKey = resolved.ProviderAuth
			model = resolved.Model
			provider = resolved.Provider // capture provider type (e.g. "anthropic")
		} else {
			log.Warn().Err(err).Str("provider", cfg.Provider).
				Msg("task_output: failed to resolve provider reference, trying inline config")
		}
	}

	// Inline config overrides (or fills in when no named provider).
	ep := cfg.ExternalProvider
	if ep.Endpoint != "" {
		endpoint = ep.Endpoint
	}
	if ep.APIKey != "" {
		apiKey = ep.APIKey
	}
	if ep.Model != "" {
		model = ep.Model
	}
	if ep.Provider != "" {
		provider = ep.Provider
	}

	timeout = ep.Timeout
	if timeout == 0 {
		timeout = defaultExternalTimeout
	}
	return endpoint, apiKey, model, provider, timeout
}

// PER-PROVIDER JSONL LOGGING

// logEvents writes passthrough events for a list of TaskOutputs.
func (p *Pipe) logEvents(
	requestID string,
	provider string,
	clientAgent string,
	outputs []TaskOutput,
	strategy string,
) {
	if p.logger == nil {
		return
	}
	for _, to := range outputs {
		raw, _ := to.Source.(adapters.ExtractedContent)
		evt := TaskOutputEvent{
			RequestID:      requestID,
			Timestamp:      time.Now().UTC(),
			Provider:       provider,
			ClientAgent:    clientAgent,
			ToolName:       raw.ToolName,
			ToolCallID:     raw.ID,
			Strategy:       strategy,
			OriginalTokens: tokenizer.CountTokens(to.PrimaryContent),
			Status:         strategy,
		}
		p.logger.Write(provider, evt)
	}
}

// logCompressedEvents writes compression result events.
func (p *Pipe) logCompressedEvents(
	requestID string,
	provider string,
	clientAgent string,
	results []taskResult,
) {
	if p.logger == nil {
		return
	}
	for _, r := range results {
		status := "compressed"
		errMsg := ""
		if r.err != nil {
			status = "error"
			errMsg = r.err.Error()
		} else if r.origTokens == r.compTokens {
			status = "passthrough_small"
		}
		raw, _ := r.output.Source.(adapters.ExtractedContent)
		evt := TaskOutputEvent{
			RequestID:        requestID,
			Timestamp:        time.Now().UTC(),
			Provider:         provider,
			ClientAgent:      clientAgent,
			ToolName:         raw.ToolName,
			ToolCallID:       raw.ID,
			Strategy:         pipes.StrategyExternalProvider,
			OriginalTokens:   r.origTokens,
			CompressedTokens: r.compTokens,
			Status:           status,
			ErrorMsg:         errMsg,
		}
		p.logger.Write(provider, evt)
	}
}
