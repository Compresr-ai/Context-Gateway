// Package tool_output compresses tool call results to reduce context size.
package tooloutput

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/external"
	"github.com/compresr/context-gateway/internal/adapters"
	authtypes "github.com/compresr/context-gateway/internal/auth/types"
	"github.com/compresr/context-gateway/internal/compresr"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
	"github.com/compresr/context-gateway/internal/tokenizer"
)

// Process compresses new tool outputs before sending to LLM.
// Only new (uncompressed) outputs are processed — outputs compressed on prior turns
// arrive with a [REF:] prefix and are skipped to preserve KV-cache.
// Returns the modified request body with compressed tool outputs.
func (p *Pipe) Process(ctx *pipes.PipeContext) ([]byte, error) {
	if !p.enabled {
		return ctx.OriginalRequest, nil
	}

	// Passthrough = do nothing
	if p.strategy == config.StrategyPassthrough {
		log.Debug().Msg("tool_output: passthrough mode, skipping")
		return ctx.OriginalRequest, nil
	}

	// Skip compression for cheap models (not economically viable)
	// This check is automatic - no configuration required
	// Can be bypassed with bypass_cost_check: true (useful for testing)
	if !p.bypassCostCheck && ShouldSkipCompressionForCost(ctx.TargetModel) {
		log.Info().
			Str("target_model", ctx.TargetModel).
			Str("cost_tier", GetModelCostTier(ctx.TargetModel)).
			Msg("tool_output: skipping compression for budget model")
		return ctx.OriginalRequest, nil
	}

	return p.compressAllTools(ctx)
}

// compressAllTools compresses new tool outputs in the request.
//
// Only compress new (uncompressed) outputs — prior turns are already compressed.
// Already-compressed outputs are detected by [REF:] prefix and skipped.
func (p *Pipe) compressAllTools(ctx *pipes.PipeContext) ([]byte, error) {
	// Adapter required for provider-agnostic extraction/application
	if ctx.Adapter == nil || len(ctx.OriginalRequest) == 0 {
		log.Warn().Msg("tool_output: no adapter or original request, skipping compression")
		return ctx.OriginalRequest, nil
	}

	// Get provider name for API source tracking
	provider := ctx.Adapter.Name()

	// ALWAYS delegate extraction to adapter - pipes don't implement extraction logic
	extracted, err := ctx.Adapter.ExtractToolOutput(ctx.OriginalRequest)
	if err != nil {
		log.Warn().Err(err).Msg("tool_output: adapter extraction failed, skipping compression")
		return ctx.OriginalRequest, nil
	}

	if len(extracted) == 0 {
		return ctx.OriginalRequest, nil
	}

	// Determine query for compression context:
	// - Query-agnostic models (LLM/cmprsr): don't need user query, use empty string
	// - Query-dependent models (reranker): need query for relevance scoring
	//
	// Query extraction strategy (in priority order):
	// 1. Assistant intent (best: captures WHY the LLM called the tool)
	// 2. Last user text message (good: captures the user's original request)
	// 3. Tool name + input summary (fallback: captures what was asked of the tool)
	// 4. Empty string for query-agnostic models (acceptable: model doesn't use it)
	var query string
	if p.IsQueryAgnostic() {
		query = ""
		log.Debug().
			Str("model", p.compresrModel).
			Bool("query_agnostic", true).
			Msg("tool_output: query-agnostic model, using empty query")
	} else {
		// Priority 1: Assistant's reasoning for calling the tool
		query = ctx.Adapter.ExtractAssistantIntent(ctx.OriginalRequest)
		if query == "" {
			// Priority 2: Last user text message (pre-computed, injected tags stripped)
			query = ctx.UserQuery
		}
		if query == "" {
			// Priority 3: Build query from tool names being compressed
			var toolNames []string
			for _, ext := range extracted {
				if ext.ToolName != "" {
					toolNames = append(toolNames, ext.ToolName)
				}
			}
			if len(toolNames) > 0 {
				query = "tool output from: " + strings.Join(toolNames, ", ")
			}
		}
		log.Debug().
			Str("model", p.compresrModel).
			Bool("query_agnostic", false).
			Int("query_len", len(query)).
			Msg("tool_output: using query for relevance scoring")
	}

	// Build compression tasks from extracted content
	tasks := make([]compressionTask, 0, len(extracted))
	var results []adapters.CompressedResult

	// Resolve skip_tools categories to provider-specific tool names
	skipSet := BuildSkipSet(p.skipCategories, ctx.Provider)

	for _, ext := range extracted {
		// Skip items already claimed by the task_output pipe.
		// task_output runs before tool_output and populates TaskOutputHandledIDs
		// so subagent results are not double-processed.
		if len(ctx.TaskOutputHandledIDs) > 0 {
			if _, claimed := ctx.TaskOutputHandledIDs[ext.ID]; claimed {
				log.Debug().
					Str("tool", ext.ToolName).
					Str("id", ext.ID).
					Msg("tool_output: skipped (claimed by task_output pipe)")
				continue
			}
		}

		// Skip empty tool outputs — nothing to compress.
		if ext.Content == "" {
			continue
		}

		// Skip already-compressed outputs from prior turns.
		// These arrive in conversation history with the [REF:] prefix
		// that was added when they were first compressed.
		if strings.HasPrefix(ext.Content, ShadowPrefixMarker) {
			log.Debug().
				Str("tool", ext.ToolName).
				Msg("tool_output: already compressed from prior turn, skipping")
			ctx.ToolOutputCompressions = append(ctx.ToolOutputCompressions, pipes.ToolOutputCompression{
				ToolName:         ext.ToolName,
				ToolCallID:       ext.ID,
				OriginalTokens:   tokenizer.CountTokens(ext.Content),
				CompressedTokens: tokenizer.CountTokens(ext.Content),
				MappingStatus:    "already_compressed",
				MinThreshold:     p.minTokens,
				MaxThreshold:     p.maxTokens,
				Model:            p.getEffectiveModel(),
			})
			continue
		}

		// Skip tools configured in skip_tools (resolved by provider)
		if skipSet[ext.ToolName] {
			log.Debug().
				Str("tool", ext.ToolName).
				Str("provider", string(ctx.Provider)).
				Msg("tool_output: skipped by skip_tools config")
			ctx.ToolOutputCompressions = append(ctx.ToolOutputCompressions, pipes.ToolOutputCompression{
				ToolName:         ext.ToolName,
				ToolCallID:       ext.ID,
				OriginalTokens:   tokenizer.CountTokens(ext.Content),
				CompressedTokens: tokenizer.CountTokens(ext.Content),
				MappingStatus:    "skipped_by_config",
				MinThreshold:     p.minTokens,
				MaxThreshold:     p.maxTokens,
				Model:            p.getEffectiveModel(),
			})
			continue
		}

		// Skip if content format is not in the effective compressible set.
		// Format is detected by the adapter during extraction (DetectContentFormat).
		// FormatUnknown (empty/unclassifiable content) always passthroughs.
		if !adapters.IsCompressible(ext.Format, p.effectiveFormats) {
			log.Debug().
				Str("tool", ext.ToolName).
				Str("format", string(ext.Format)).
				Msg("tool_output: content format not compressible, passthrough")
			ctx.ToolOutputCompressions = append(ctx.ToolOutputCompressions, pipes.ToolOutputCompression{
				ToolName:         ext.ToolName,
				ToolCallID:       ext.ID,
				OriginalTokens:   tokenizer.CountTokens(ext.Content),
				CompressedTokens: tokenizer.CountTokens(ext.Content),
				MappingStatus:    "passthrough_format",
				MinThreshold:     p.minTokens,
				MaxThreshold:     p.maxTokens,
				Model:            p.getEffectiveModel(),
			})
			continue
		}

		// Count tokens using tiktoken (accurate, model-aware)
		contentTokens := tokenizer.CountTokensForModel(ext.Content, ctx.TargetModel)

		// Skip if below min token threshold - but record for tracking
		if contentTokens <= p.minTokens {
			log.Debug().
				Int("tokens", contentTokens).
				Int("min_tokens", p.minTokens).
				Str("tool", ext.ToolName).
				Msg("tool_output: below min threshold, passthrough")
			// Record passthrough for trajectory tracking
			ctx.ToolOutputCompressions = append(ctx.ToolOutputCompressions, pipes.ToolOutputCompression{
				ToolName:         ext.ToolName,
				ToolCallID:       ext.ID,
				OriginalTokens:   contentTokens,
				CompressedTokens: contentTokens,
				OriginalContent:  ext.Content,
				MappingStatus:    "passthrough_small",
				MinThreshold:     p.minTokens,
				MaxThreshold:     p.maxTokens,
				Model:            p.getEffectiveModel(),
			})
			continue
		}
		if contentTokens > p.maxTokens {
			log.Debug().
				Int("tokens", contentTokens).
				Int("max_tokens", p.maxTokens).
				Str("tool", ext.ToolName).
				Msg("tool_output: above max threshold, passthrough")
			// Record passthrough for trajectory tracking
			ctx.ToolOutputCompressions = append(ctx.ToolOutputCompressions, pipes.ToolOutputCompression{
				ToolName:         ext.ToolName,
				ToolCallID:       ext.ID,
				OriginalTokens:   contentTokens,
				CompressedTokens: contentTokens,
				MappingStatus:    "passthrough_large",
				MinThreshold:     p.minTokens,
				MaxThreshold:     p.maxTokens,
				Model:            p.getEffectiveModel(),
			})
			continue
		}

		shadowID := p.contentHash(ext.Content)

		// Check compressed cache first (V2: C1 KV-cache preservation)
		if cachedCompressed, ok := p.store.GetCompressed(shadowID); ok {
			if tokenizer.CountTokens(cachedCompressed) < contentTokens {
				log.Info().
					Str("shadow_id", shadowID[:min(16, len(shadowID))]).
					Str("tool", ext.ToolName).
					Bool("expand_context_enabled", p.enableExpandContext).
					Msg("tool_output: cache HIT, using compressed")

				// Build content: prefixed with shadow ID if expand_context enabled, raw otherwise
				var cachedFinalContent string
				var cachedShadowRef string
				if p.enableExpandContext {
					// Full expand_context mode: prefix with shadow ID for retrieval
					if p.includeExpandHint {
						cachedFinalContent = fmt.Sprintf(PrefixFormatWithHint, shadowID, shadowID, cachedCompressed)
					} else {
						cachedFinalContent = fmt.Sprintf(PrefixFormat, shadowID, cachedCompressed)
					}
					p.touchOriginal(shadowID)
					ctx.ShadowRefs[shadowID] = ext.Content
					cachedShadowRef = shadowID
				} else {
					// No expand_context: use raw compressed content, no shadow tracking
					cachedFinalContent = cachedCompressed
					cachedShadowRef = ""
				}

				ctx.ToolOutputCompressions = append(ctx.ToolOutputCompressions, pipes.ToolOutputCompression{
					ToolName:          ext.ToolName,
					ToolCallID:        ext.ID,
					ShadowID:          cachedShadowRef,
					OriginalContent:   ext.Content,
					CompressedContent: cachedFinalContent,
					OriginalTokens:    contentTokens,
					CompressedTokens:  tokenizer.CountTokens(cachedFinalContent),
					CacheHit:          true,
					MappingStatus:     "cache_hit",
					MinThreshold:      p.minTokens,
					MaxThreshold:      p.maxTokens,
					Model:             p.getEffectiveModel(),
				})
				results = append(results, adapters.CompressedResult{
					ID:           ext.ID,
					Compressed:   cachedFinalContent,
					ShadowRef:    cachedShadowRef,
					MessageIndex: ext.MessageIndex,
					BlockIndex:   ext.BlockIndex,
				})
				p.recordCacheHit()
				ctx.OutputCompressed = true
				continue
			}
			_ = p.store.DeleteCompressed(shadowID)
		}

		p.recordCacheMiss()

		// Store content baseline if not already present.
		// If content was seen before but has no compressed cache entry, it means
		// compression failed or was rejected on a prior attempt. Retry compression
		// rather than permanently skipping — the failure may have been transient
		// (rate limit, API error), and the token savings from successful compression
		// outweigh the one-time KV-cache miss.
		// Successfully compressed content is handled above via the compressed cache hit path.
		if p.store != nil {
			if _, seen := p.store.Get(shadowID); !seen {
				_ = p.store.Set(shadowID, ext.Content)
			}
		}

		// Queue for compression — this is genuinely new content
		tasks = append(tasks, compressionTask{
			index:        ext.MessageIndex,
			msg:          message{Content: ext.Content, ToolCallID: ext.ID},
			toolName:     ext.ToolName,
			shadowID:     shadowID,
			original:     ext.Content,
			messageIndex: ext.MessageIndex,
			blockIndex:   ext.BlockIndex,
		})

		log.Debug().
			Int("tokens", contentTokens).
			Str("tool_name", ext.ToolName).
			Str("shadow_id", shadowID[:min(16, len(shadowID))]).
			Msg("tool_output: queued for compression (new content)")
	}

	if len(tasks) > 0 {
		// Process compressions with rate limiting (V2: C11)
		reqCtx := ctx.RequestCtx
		if reqCtx == nil {
			reqCtx = context.Background()
		}
		compResults := p.compressBatch(reqCtx, query, provider, ctx.CapturedAuth, tasks)

		// Apply results
		for result := range compResults {
			if !result.success {
				log.Warn().Err(result.err).Str("tool", result.toolName).Msg("tool_output: compression failed")
				p.recordCompressionFail()
				continue
			}

			if result.usedFallback {
				log.Info().
					Str("tool_name", result.toolName).
					Int("tokens", tokenizer.CountTokens(result.originalContent)).
					Msg("tool_output: using original content (fallback)")
				ctx.ToolOutputCompressions = append(ctx.ToolOutputCompressions, pipes.ToolOutputCompression{
					ToolName:          result.toolName,
					ToolCallID:        result.toolCallID,
					ShadowID:          "", // no shadow reference was created; original content was sent as-is
					OriginalContent:   result.originalContent,
					CompressedContent: result.compressedContent,
					OriginalTokens:    tokenizer.CountTokens(result.originalContent),
					CompressedTokens:  tokenizer.CountTokens(result.originalContent),
					CacheHit:          false,
					MappingStatus:     "passthrough",
					Model:             p.getEffectiveModel(),
				})
				continue
			}

			// Only use compression if token savings meet the minimum threshold.
			// compressionRatio = fraction of tokens removed (higher = more aggressive).
			// Reject when compressionRatio < p.refusalThreshold (configurable, default DefaultRefusalThreshold).
			// This also rejects cases where compression expanded the content (compressionRatio == 0 after clamping).
			origTokens := tokenizer.CountTokens(result.originalContent)
			compTokens := tokenizer.CountTokens(result.compressedContent)
			compressionRatio := tokenizer.CompressionRatio(origTokens, compTokens)
			if compressionRatio < p.refusalThreshold {
				log.Warn().
					Float64("compression_ratio", compressionRatio).
					Float64("min_ratio_required", p.refusalThreshold).
					Int("original_tokens", origTokens).
					Int("api_returned_tokens", compTokens).
					Str("tool", result.toolName).
					Msg("tool_output: insufficient token savings, using original")
				// Record origTokens for CompressedTokens because the original content is what
				// we actually send to the LLM — the API-returned content is discarded.
				// ShadowID is "" because no shadow was created (original is sent as-is).
				ctx.ToolOutputCompressions = append(ctx.ToolOutputCompressions, pipes.ToolOutputCompression{
					ToolName:          result.toolName,
					ToolCallID:        result.toolCallID,
					ShadowID:          "",
					OriginalContent:   result.originalContent,
					CompressedContent: result.originalContent,
					OriginalTokens:    origTokens,
					CompressedTokens:  origTokens,
					CacheHit:          false,
					MappingStatus:     "ratio_exceeded",
					MinThreshold:      p.minTokens,
					MaxThreshold:      p.maxTokens,
					Model:             p.getEffectiveModel(),
				})
				continue
			}

			// Cache compressed with long TTL
			if p.store != nil {
				if err := p.store.SetCompressed(result.shadowID, result.compressedContent); err != nil {
					log.Error().Err(err).Str("id", result.shadowID).Msg("tool_output: failed to cache")
				}
			}

			// Build content: prefixed with shadow ID if expand_context enabled, raw otherwise
			var finalContent string
			var shadowRef string
			if p.enableExpandContext {
				// Full expand_context mode: prefix with shadow ID for retrieval
				if p.includeExpandHint {
					finalContent = fmt.Sprintf(PrefixFormatWithHint, result.shadowID, result.shadowID, result.compressedContent)
				} else {
					finalContent = fmt.Sprintf(PrefixFormat, result.shadowID, result.compressedContent)
				}
				ctx.ShadowRefs[result.shadowID] = result.originalContent
				shadowRef = result.shadowID
			} else {
				// No expand_context: use raw compressed content, no shadow tracking
				finalContent = result.compressedContent
				shadowRef = ""
			}

			tokensSaved := origTokens - compTokens
			ctx.ToolOutputCompressions = append(ctx.ToolOutputCompressions, pipes.ToolOutputCompression{
				ToolName:          result.toolName,
				ToolCallID:        result.toolCallID,
				ShadowID:          shadowRef,
				OriginalContent:   result.originalContent,
				CompressedContent: finalContent,
				OriginalTokens:    origTokens,
				CompressedTokens:  compTokens,
				CacheHit:          false,
				MappingStatus:     "compressed",
				MinThreshold:      p.minTokens,
				MaxThreshold:      p.maxTokens,
				Model:             p.getEffectiveModel(),
			})

			results = append(results, adapters.CompressedResult{
				ID:           result.toolCallID,
				Compressed:   finalContent,
				ShadowRef:    shadowRef,
				MessageIndex: result.messageIndex,
				BlockIndex:   result.blockIndex,
			})

			p.recordCompressionOK(int64(tokensSaved))
			ctx.OutputCompressed = true

			log.Info().
				Str("strategy", p.strategy).
				Int("original_tokens", origTokens).
				Int("compressed_tokens", compTokens).
				Bool("expand_context_enabled", p.enableExpandContext).
				Str("shadow_id", shadowRef).
				Str("tool", result.toolName).
				Msg("tool_output: compressed successfully")
		}
	}

	// Annotate all compression records with the query used
	isQueryAgnostic := p.IsQueryAgnostic()
	for i := range ctx.ToolOutputCompressions {
		ctx.ToolOutputCompressions[i].Query = query
		ctx.ToolOutputCompressions[i].QueryAgnostic = isQueryAgnostic
	}

	// Apply all compressed results back to the request body
	if len(results) > 0 {
		modifiedBody, err := ctx.Adapter.ApplyToolOutput(ctx.OriginalRequest, results)
		if err != nil {
			log.Warn().Err(err).Msg("tool_output: failed to apply compressed results")
			return ctx.OriginalRequest, nil
		}
		return modifiedBody, nil
	}

	return ctx.OriginalRequest, nil
}

// compressBatch processes compression tasks with rate limiting (V2: C11).
func (p *Pipe) compressBatch(reqCtx context.Context, query, provider string, auth authtypes.CapturedAuth, tasks []compressionTask) <-chan compressionResult {
	results := make(chan compressionResult, len(tasks))

	go func() {
		var wg sync.WaitGroup

		for _, task := range tasks {
			// Cancel early if the request context is done (client disconnect, timeout).
			select {
			case <-reqCtx.Done():
				results <- compressionResult{
					index:           task.index,
					shadowID:        task.shadowID,
					toolName:        task.toolName,
					toolCallID:      task.msg.ToolCallID,
					originalContent: task.original,
					success:         false,
					err:             reqCtx.Err(),
					messageIndex:    task.messageIndex,
					blockIndex:      task.blockIndex,
				}
				continue
			default:
			}

			// V2: Rate limit (C11)
			if p.rateLimiter != nil {
				if !p.rateLimiter.Acquire() {
					p.recordRateLimited()
					log.Warn().Str("tool", task.toolName).Msg("tool_output: rate limited")
					results <- compressionResult{
						index:           task.index,
						shadowID:        task.shadowID,
						toolName:        task.toolName,
						toolCallID:      task.msg.ToolCallID,
						originalContent: task.original,
						success:         false,
						err:             fmt.Errorf("rate limited"),
						messageIndex:    task.messageIndex,
						blockIndex:      task.blockIndex,
					}
					continue
				}
			}

			// V2: Semaphore for concurrent limit (C11) — respect context cancellation.
			if p.semaphore != nil {
				select {
				case p.semaphore <- struct{}{}:
				case <-reqCtx.Done():
					results <- compressionResult{
						index:           task.index,
						shadowID:        task.shadowID,
						toolName:        task.toolName,
						toolCallID:      task.msg.ToolCallID,
						originalContent: task.original,
						success:         false,
						err:             reqCtx.Err(),
						messageIndex:    task.messageIndex,
						blockIndex:      task.blockIndex,
					}
					continue
				}
			}

			wg.Add(1)
			go func(t compressionTask) {
				defer wg.Done()
				defer func() {
					if p.semaphore != nil {
						<-p.semaphore
					}
				}()

				result := p.compressOne(reqCtx, query, provider, auth, t)
				results <- result
			}(task)
		}

		// Wait for all compression goroutines to complete before closing
		wg.Wait()
		close(results)
	}()

	return results
}

// compressOne compresses a single tool output.
func (p *Pipe) compressOne(reqCtx context.Context, query, provider string, auth authtypes.CapturedAuth, t compressionTask) compressionResult {
	var compressed string
	var err error

	switch p.strategy {
	case config.StrategyCompresr:
		compressed, err = p.compressViaCompresr(query, t.original, t.toolName, provider)
	case config.StrategyExternalProvider:
		compressed, err = p.compressViaExternalProvider(reqCtx, query, t.original, t.toolName, auth)
	case config.StrategySimple:
		// Simple first-words compression for testing expand_context
		compressed = p.CompressSimpleContent(t.original)
		err = nil
	case config.StrategyTrimming:
		// Tail-keep compression: discard head, keep only tail based on target_compression_ratio
		compressed = p.compressTrimming(t.original)
		err = nil
	default:
		return compressionResult{index: t.index, success: false, err: fmt.Errorf("unknown strategy: %s", p.strategy), messageIndex: t.messageIndex, blockIndex: t.blockIndex}
	}

	if err != nil {
		log.Warn().
			Err(err).
			Str("strategy", p.strategy).
			Str("fallback", p.fallbackStrategy).
			Str("tool", t.toolName).
			Msg("tool_output: compression failed, applying fallback")

		// Apply fallback strategy
		if p.fallbackStrategy == config.StrategyPassthrough {
			return compressionResult{
				index:             t.index,
				shadowID:          t.shadowID,
				toolName:          t.toolName,
				toolCallID:        t.msg.ToolCallID,
				originalContent:   t.original,
				compressedContent: t.original,
				success:           true,
				usedFallback:      true,
				messageIndex:      t.messageIndex,
				blockIndex:        t.blockIndex,
			}
		}

		if p.store != nil {
			_ = p.store.Delete(t.shadowID)
		}
		return compressionResult{index: t.index, success: false, err: err, messageIndex: t.messageIndex, blockIndex: t.blockIndex}
	}

	// V2: Don't add expand hint here - prefix is added at send-time
	return compressionResult{
		index:             t.index,
		shadowID:          t.shadowID,
		toolName:          t.toolName,
		toolCallID:        t.msg.ToolCallID,
		originalContent:   t.original,
		compressedContent: compressed,
		success:           true,
		messageIndex:      t.messageIndex,
		blockIndex:        t.blockIndex,
	}
}

// contentHash generates a deterministic shadow ID from content.
// V2: SHA256(normalize(original)) for consistency (E22)
func (p *Pipe) contentHash(content string) string {
	hash := sha256.Sum256([]byte(content))
	// Use first 16 bytes (32 hex chars) - still 128 bits of entropy
	return ShadowIDPrefix + hex.EncodeToString(hash[:16])
}

// touchOriginal extends the TTL of original content before LLM call (V2)
func (p *Pipe) touchOriginal(shadowID string) {
	if original, ok := p.store.Get(shadowID); ok {
		_ = p.store.Set(shadowID, original)
	}
}

// V2: Metrics recording helpers
func (p *Pipe) recordCacheHit() {
	p.mu.Lock()
	p.metrics.CacheHits++
	p.mu.Unlock()
}

func (p *Pipe) recordCacheMiss() {
	p.mu.Lock()
	p.metrics.CacheMisses++
	p.mu.Unlock()
}

func (p *Pipe) recordCompressionOK(tokensSaved int64) {
	p.mu.Lock()
	p.metrics.CompressionOK++
	p.metrics.TokensSaved += tokensSaved
	p.mu.Unlock()
}

func (p *Pipe) recordCompressionFail() {
	p.mu.Lock()
	p.metrics.CompressionFail++
	p.mu.Unlock()
}

func (p *Pipe) recordRateLimited() {
	p.mu.Lock()
	p.metrics.RateLimited++
	p.mu.Unlock()
}

// getEffectiveModel returns the compression model name with fallback to default.
func (p *Pipe) getEffectiveModel() string {
	if p.compresrModel != "" {
		return p.compresrModel
	}
	return compresr.DefaultToolOutputModel // toc_latte_v1
}

// COMPRESSION STRATEGIES

// compressViaCompresr calls the Compresr API via the centralized client.
// When the circuit breaker is open (repeated failures), returns the fallback error immediately
// without waiting for the full API timeout.
func (p *Pipe) compressViaCompresr(query, content, toolName, provider string) (string, error) {
	// Use the centralized Compresr client
	if p.compresrClient == nil {
		return "", fmt.Errorf("compresr client not initialized")
	}

	// Circuit breaker: skip the API call entirely when the circuit is open
	if !p.circuit.Allow() {
		return "", fmt.Errorf("compresr API circuit breaker open (repeated failures)")
	}

	// Use configured model, fallback to default if not set
	modelName := p.getEffectiveModel()

	// Build source string: gateway:anthropic or gateway:openai
	source := "gateway:" + provider

	params := compresr.CompressToolOutputParams{
		ToolOutput:             content,
		UserQuery:              query,
		ToolName:               toolName,
		ModelName:              modelName,
		Source:                 source,
		TargetCompressionRatio: p.targetCompressionRatio,
	}

	result, err := p.compresrClient.CompressToolOutput(params)
	if err != nil {
		p.circuit.RecordFailure()
		return "", fmt.Errorf("compresr API call failed: %w", err)
	}

	p.circuit.RecordSuccess()
	return result.CompressedOutput, nil
}

// compressViaExternalProvider calls an external LLM provider directly.
// Uses the api config (endpoint, api_key, model) from the config file.
// Provider is auto-detected from endpoint URL or can be set explicitly.
func (p *Pipe) compressViaExternalProvider(reqCtx context.Context, query, content, toolName string, auth authtypes.CapturedAuth) (string, error) {
	// Structured data prefix: detect format and extract verbatim prefix.
	// When content starts with JSON/YAML/XML, preserve the first minTokens worth verbatim
	// so the downstream model can parse the structure. Only the tail goes to LLM.
	// Note: content here is always > minTokens (smaller content is filtered earlier).
	// ExtractVerbatimPrefix handles the case where content <= prefixTokens*2 (passthrough).
	var verbatimPrefix, structuredFormat string
	format, _ := DetectStructuredFormat(content)
	if format != "" {
		// Use minTokens directly for prefix extraction (tiktoken-based)
		verbatim, rest := ExtractVerbatimPrefix(content, format, p.minTokens)
		if rest == "" {
			// Entire content fits in prefix — passthrough (triggers "ineffective" fallback)
			return content, nil
		}
		verbatimPrefix = verbatim
		structuredFormat = format
		content = rest // only compress the tail
	}

	var systemPrompt, userPrompt string
	if verbatimPrefix != "" {
		// Structured tail: specialized prompt
		systemPrompt = external.SystemPromptStructuredTail
		userPrompt = external.UserPromptStructuredTail(structuredFormat, toolName, content)
	} else if p.compresrQueryAgnostic || query == "" {
		systemPrompt = external.SystemPromptQueryAgnostic
		userPrompt = external.UserPromptQueryAgnostic(toolName, content)
	} else {
		systemPrompt = external.SystemPromptQuerySpecific
		userPrompt = external.UserPromptQuerySpecific(query, toolName, content)
	}

	// Auto-calculate max tokens: allow at most half the input token count as output
	maxTokens := tokenizer.CountTokens(content) / 2
	if maxTokens < 256 {
		maxTokens = 256
	}
	if maxTokens > 4096 {
		maxTokens = 4096
	}

	params := external.CallLLMParams{
		Endpoint:     p.compresrEndpoint,
		ProviderKey:  p.compresrKey,
		Model:        p.compresrModel,
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
		MaxTokens:    maxTokens,
		Timeout:      p.compresrTimeout,
	}

	// Auth fallback: reuse auth captured from the incoming request.
	// Handles both API key users (IsXAPIKey=true) and OAuth subscription users (IsXAPIKey=false).
	if params.ProviderKey == "" && auth.HasAuth() {
		if auth.IsXAPIKey {
			params.ProviderKey = auth.Token
		} else {
			params.BearerAuth = auth.Token
			if auth.BetaHeader != "" {
				params.ExtraHeaders = map[string]string{"anthropic-beta": auth.BetaHeader}
			}
		}
	}

	result, err := external.CallLLM(reqCtx, params)
	if err != nil {
		return "", err
	}

	compressed := result.Content

	// Validate compression reduced token count (compared to what was sent, not original)
	extOrigTokens := tokenizer.CountTokens(content)
	extCompTokens := tokenizer.CountTokens(compressed)
	if extCompTokens >= extOrigTokens {
		return "", fmt.Errorf("external_provider compression ineffective: output (%d tokens) >= input (%d tokens)",
			extCompTokens, extOrigTokens)
	}

	// Reassemble: verbatim prefix + separator + compressed tail
	if verbatimPrefix != "" {
		compressed = verbatimPrefix + "\n" + StructuredSeparator + "\n" + compressed
		log.Debug().
			Str("format", structuredFormat).
			Int("prefix_tokens", tokenizer.CountTokens(verbatimPrefix)).
			Int("tail_compressed_tokens", extCompTokens).
			Msg("tool_output: structured prefix preserved verbatim")
	}
	log.Debug().
		Str("provider", result.Provider).
		Str("model", p.compresrModel).
		Bool("query_agnostic", p.compresrQueryAgnostic).
		Int("original_tokens", extOrigTokens).
		Int("compressed_tokens", extCompTokens).
		Float64("ratio", tokenizer.CompressionRatio(extOrigTokens, extCompTokens)).
		Msg("tool_output: external_provider compression completed")

	return compressed, nil
}
