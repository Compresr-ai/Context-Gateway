// Tool output compression - main compression logic.
//
// STATUS: Disabled in current release. Enable via config: pipes.tool_output.enabled: true
//
// FLOW:
//  1. Extract ALL tool outputs from request messages via adapter
//  2. For each output > minBytes: check cache → compress if miss
//  3. Store original with short TTL, compressed with long TTL (dual TTL)
//  4. Add <<<SHADOW:id>>> prefix at send-time (not storage-time)
//  5. Apply compressed content back via adapter
//
// DESIGN: Pipes are provider-agnostic. They use adapters for:
//   - ExtractToolOutput() to get tool results
//   - ApplyToolOutput() to patch compressed results back
package tooloutput

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/external"
	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/compresr"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
)

// Process compresses large tool outputs before sending to LLM.
// V2: Compresses ALL tool outputs for KV-cache preservation (C2).
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
	if ShouldSkipCompressionForCost(ctx.TargetModel) {
		log.Info().
			Str("target_model", ctx.TargetModel).
			Str("cost_tier", GetModelCostTier(ctx.TargetModel)).
			Msg("tool_output: skipping compression for budget model")
		return ctx.OriginalRequest, nil
	}

	return p.compressAllTools(ctx)
}

// compressAllTools compresses ALL tool outputs (V2: C2 Multi-Tool Batch).
//
// V2 Design:
//   - Compress ALL tools, not just last (fixes KV-cache miss on parallel tools)
//   - Cache lookup with TTL reset before compression
//   - Rate-limited parallel compression (C11)
//   - Prefix added at send-time (Refinement 2)
//
// DESIGN: Pipes ALWAYS delegate extraction to adapters. Pipes contain NO
// provider-specific logic - they only implement compression/filtering logic.
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

	// Determine query based on config:
	// - Query-agnostic models (LLM/cmprsr): don't need user query, use empty string
	// - Query-dependent models (reranker): need user query for relevance scoring
	var query string
	if p.IsQueryAgnostic() {
		// Query-agnostic models (cmprsr, LLM-based): no query needed
		query = ""
		log.Debug().
			Str("model", p.compresrModel).
			Bool("query_agnostic", true).
			Msg("tool_output: query-agnostic model, using empty query")
	} else {
		// Query-dependent models (reranker): extract last user message
		query = ctx.Adapter.ExtractUserQuery(ctx.OriginalRequest)
		if query == "" {
			// Fallback if no user query found
			query = "process this tool output"
		}
		log.Debug().
			Str("model", p.compresrModel).
			Bool("query_agnostic", false).
			Int("query_len", len(query)).
			Msg("tool_output: using user query for relevance scoring")
	}

	// Build compression tasks from extracted content
	tasks := make([]compressionTask, 0, len(extracted))
	var results []adapters.CompressedResult

	// Resolve skip_tools categories to provider-specific tool names
	skipSet := BuildSkipSet(p.skipCategories, ctx.Provider)

	for _, ext := range extracted {
		// Skip tools configured in skip_tools (resolved by provider)
		if skipSet[ext.ToolName] {
			log.Debug().
				Str("tool", ext.ToolName).
				Str("provider", string(ctx.Provider)).
				Msg("tool_output: skipped by skip_tools config")
			ctx.ToolOutputCompressions = append(ctx.ToolOutputCompressions, pipes.ToolOutputCompression{
				ToolName:        ext.ToolName,
				ToolCallID:      ext.ID,
				OriginalBytes:   len(ext.Content),
				CompressedBytes: len(ext.Content),
				MappingStatus:   "skipped_by_config",
				MinThreshold:    p.minBytes,
				MaxThreshold:    p.maxBytes,
				Model:           p.getEffectiveModel(),
			})
			continue
		}

		contentSize := len(ext.Content)

		// Skip if below min byte threshold - but record for tracking
		if contentSize <= p.minBytes {
			log.Debug().
				Int("size_bytes", contentSize).
				Int("min_bytes", p.minBytes).
				Str("tool", ext.ToolName).
				Msg("tool_output: below min threshold, passthrough")
			// Record passthrough for trajectory tracking
			ctx.ToolOutputCompressions = append(ctx.ToolOutputCompressions, pipes.ToolOutputCompression{
				ToolName:        ext.ToolName,
				ToolCallID:      ext.ID,
				OriginalBytes:   contentSize,
				CompressedBytes: contentSize,
				MappingStatus:   "passthrough_small",
				MinThreshold:    p.minBytes,
				MaxThreshold:    p.maxBytes,
				Model:           p.getEffectiveModel(),
			})
			continue
		}
		if contentSize > p.maxBytes {
			log.Debug().
				Int("size", contentSize).
				Int("max", p.maxBytes).
				Str("tool", ext.ToolName).
				Msg("tool_output: above max threshold, passthrough")
			// Record passthrough for trajectory tracking
			ctx.ToolOutputCompressions = append(ctx.ToolOutputCompressions, pipes.ToolOutputCompression{
				ToolName:        ext.ToolName,
				ToolCallID:      ext.ID,
				OriginalBytes:   contentSize,
				CompressedBytes: contentSize,
				MappingStatus:   "passthrough_large",
				MinThreshold:    p.minBytes,
				MaxThreshold:    p.maxBytes,
				Model:           p.getEffectiveModel(),
			})
			continue
		}

		shadowID := p.contentHash(ext.Content)

		// Check compressed cache first (V2: C1 KV-cache preservation)
		if cachedCompressed, ok := p.store.GetCompressed(shadowID); ok {
			if len(cachedCompressed) < contentSize {
				log.Info().
					Str("shadow_id", shadowID[:min(16, len(shadowID))]).
					Str("tool", ext.ToolName).
					Msg("tool_output: cache HIT, using compressed")

				prefixedContent := fmt.Sprintf(PrefixFormat, shadowID, cachedCompressed)
				p.touchOriginal(shadowID)

				ctx.ShadowRefs[shadowID] = ext.Content
				ctx.ToolOutputCompressions = append(ctx.ToolOutputCompressions, pipes.ToolOutputCompression{
					ToolName:          ext.ToolName,
					ToolCallID:        ext.ID,
					ShadowID:          shadowID,
					OriginalContent:   ext.Content,
					CompressedContent: prefixedContent,
					OriginalBytes:     contentSize,
					CompressedBytes:   len(prefixedContent),
					CacheHit:          true,
					MappingStatus:     "cache_hit",
					MinThreshold:      p.minBytes,
					MaxThreshold:      p.maxBytes,
					Model:             p.getEffectiveModel(),
				})
				results = append(results, adapters.CompressedResult{
					ID:         ext.ID,
					Compressed: prefixedContent,
					ShadowRef:  shadowID,
				})
				p.recordCacheHit()
				ctx.OutputCompressed = true
				continue
			}
			_ = p.store.DeleteCompressed(shadowID)
		}

		p.recordCacheMiss()

		if p.store != nil {
			_ = p.store.Set(shadowID, ext.Content)
		}

		// Queue for compression
		tasks = append(tasks, compressionTask{
			index:    ext.MessageIndex,
			msg:      message{Content: ext.Content, ToolCallID: ext.ID},
			toolName: ext.ToolName,
			shadowID: shadowID,
			original: ext.Content,
		})

		log.Debug().
			Int("size", contentSize).
			Str("tool_name", ext.ToolName).
			Str("shadow_id", shadowID[:min(16, len(shadowID))]).
			Msg("tool_output: queued for compression")
	}

	if len(tasks) > 0 {
		// Process compressions with rate limiting (V2: C11)
		reqCtx := ctx.RequestCtx
		if reqCtx == nil {
			reqCtx = context.Background()
		}
		compResults := p.compressBatch(reqCtx, query, provider, ctx.CapturedBearerToken, ctx.CapturedBetaHeader, tasks)

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
					Int("size", len(result.originalContent)).
					Msg("tool_output: using original content (fallback)")
				ctx.ToolOutputCompressions = append(ctx.ToolOutputCompressions, pipes.ToolOutputCompression{
					ToolName:          result.toolName,
					ToolCallID:        result.toolCallID,
					ShadowID:          result.shadowID,
					OriginalContent:   result.originalContent,
					CompressedContent: result.compressedContent,
					OriginalBytes:     len(result.originalContent),
					CompressedBytes:   len(result.originalContent),
					CacheHit:          false,
					MappingStatus:     "passthrough",
					Model:             p.getEffectiveModel(),
				})
				continue
			}

			// Only use compression if savings exceed threshold
			compressionRatio := float64(len(result.compressedContent)) / float64(len(result.originalContent))
			if compressionRatio > p.targetRatio {
				log.Warn().
					Float64("ratio", compressionRatio).
					Float64("max_ratio", p.targetRatio).
					Int("original", len(result.originalContent)).
					Int("compressed", len(result.compressedContent)).
					Str("tool", result.toolName).
					Msg("tool_output: compression ratio exceeds threshold, using original")
				ctx.ToolOutputCompressions = append(ctx.ToolOutputCompressions, pipes.ToolOutputCompression{
					ToolName:          result.toolName,
					ToolCallID:        result.toolCallID,
					ShadowID:          result.shadowID,
					OriginalContent:   result.originalContent,
					CompressedContent: result.compressedContent,
					OriginalBytes:     len(result.originalContent),
					CompressedBytes:   len(result.compressedContent),
					CacheHit:          false,
					MappingStatus:     "expansion_skipped",
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

			prefixedContent := fmt.Sprintf(PrefixFormat, result.shadowID, result.compressedContent)
			ctx.ShadowRefs[result.shadowID] = result.originalContent

			bytesSaved := len(result.originalContent) - len(prefixedContent)
			ctx.ToolOutputCompressions = append(ctx.ToolOutputCompressions, pipes.ToolOutputCompression{
				ToolName:          result.toolName,
				ToolCallID:        result.toolCallID,
				ShadowID:          result.shadowID,
				OriginalContent:   result.originalContent,
				CompressedContent: prefixedContent,
				OriginalBytes:     len(result.originalContent),
				CompressedBytes:   len(result.compressedContent),
				CacheHit:          false,
				MappingStatus:     "compressed",
				MinThreshold:      p.minBytes,
				MaxThreshold:      p.maxBytes,
				Model:             p.getEffectiveModel(),
			})

			results = append(results, adapters.CompressedResult{
				ID:         result.toolCallID,
				Compressed: prefixedContent,
				ShadowRef:  result.shadowID,
			})

			p.recordCompressionOK(int64(bytesSaved))
			ctx.OutputCompressed = true

			log.Info().
				Str("strategy", p.strategy).
				Int("original", len(result.originalContent)).
				Int("compressed", len(prefixedContent)).
				Str("shadow_id", result.shadowID[:min(16, len(result.shadowID))]).
				Str("tool", result.toolName).
				Msg("tool_output: compressed successfully")
		}
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
func (p *Pipe) compressBatch(reqCtx context.Context, query, provider, capturedBearerToken, capturedBetaHeader string, tasks []compressionTask) <-chan compressionResult {
	results := make(chan compressionResult, len(tasks))

	go func() {
		var wg sync.WaitGroup

		for _, task := range tasks {
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
					}
					continue
				}
			}

			// V2: Semaphore for concurrent limit (C11)
			if p.semaphore != nil {
				p.semaphore <- struct{}{}
			}

			wg.Add(1)
			go func(t compressionTask) {
				defer wg.Done()
				defer func() {
					if p.semaphore != nil {
						<-p.semaphore
					}
				}()

				result := p.compressOne(reqCtx, query, provider, capturedBearerToken, capturedBetaHeader, t)
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
func (p *Pipe) compressOne(reqCtx context.Context, query, provider, capturedBearerToken, capturedBetaHeader string, t compressionTask) compressionResult {
	var compressed string
	var err error

	switch p.strategy {
	case config.StrategyCompresr:
		compressed, err = p.compressViaCompresr(query, t.original, t.toolName, provider)
	case config.StrategyExternalProvider:
		compressed, err = p.compressViaExternalProvider(reqCtx, query, t.original, t.toolName, capturedBearerToken, capturedBetaHeader)
	case "simple":
		// TEMPORARY: Simple first-words compression for testing expand_context
		compressed = p.CompressSimpleContent(t.original)
		err = nil
	default:
		return compressionResult{index: t.index, success: false, err: fmt.Errorf("unknown strategy: %s", p.strategy)}
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
			}
		}

		if p.store != nil {
			_ = p.store.Delete(t.shadowID)
		}
		return compressionResult{index: t.index, success: false, err: err}
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
	}
}

// contentHash generates a deterministic shadow ID from content.
// V2: SHA256(normalize(original)) for consistency (E22)
func (p *Pipe) contentHash(content string) string {
	normalized := normalizeContent(content)
	hash := sha256.Sum256([]byte(normalized))
	// Use first 16 bytes (32 hex chars) - still 128 bits of entropy
	return ShadowIDPrefix + hex.EncodeToString(hash[:16])
}

// normalizeContent performs basic content normalization (V2: E22)
func normalizeContent(content string) string {
	return content
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

func (p *Pipe) recordCompressionOK(bytesSaved int64) {
	p.mu.Lock()
	p.metrics.CompressionOK++
	p.metrics.BytesSaved += bytesSaved
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

// ============================================================================
// COMPRESSION STRATEGIES
// ============================================================================

// compressViaCompresr calls the Compresr API via the centralized client.
func (p *Pipe) compressViaCompresr(query, content, toolName, provider string) (string, error) {
	// Use the centralized Compresr client
	if p.compresrClient == nil {
		return "", fmt.Errorf("compresr client not initialized")
	}

	// Use configured model, fallback to default if not set
	modelName := p.getEffectiveModel()

	// Build source string: gateway:anthropic or gateway:openai
	source := "gateway:" + provider

	params := compresr.CompressToolOutputParams{
		ToolOutput: content,
		UserQuery:  query,
		ToolName:   toolName,
		ModelName:  modelName,
		Source:     source,
	}

	result, err := p.compresrClient.CompressToolOutput(params)
	if err != nil {
		return "", fmt.Errorf("compresr API call failed: %w", err)
	}

	// Validate compression actually reduced size - if not, return error to trigger fallback
	if len(result.CompressedOutput) >= len(content) {
		return "", fmt.Errorf("compression ineffective: output (%d bytes) >= input (%d bytes)",
			len(result.CompressedOutput), len(content))
	}

	return result.CompressedOutput, nil
}

// compressViaExternalProvider calls an external LLM provider directly.
// Uses the api config (endpoint, api_key, model) from the config file.
// Provider is auto-detected from endpoint URL or can be set explicitly.
func (p *Pipe) compressViaExternalProvider(reqCtx context.Context, query, content, toolName, capturedBearerToken, capturedBetaHeader string) (string, error) {
	var systemPrompt, userPrompt string
	if p.compresrQueryAgnostic || query == "" {
		systemPrompt = external.SystemPromptQueryAgnostic
		userPrompt = external.UserPromptQueryAgnostic(toolName, content)
	} else {
		systemPrompt = external.SystemPromptQuerySpecific
		userPrompt = external.UserPromptQuerySpecific(query, toolName, content)
	}

	// Auto-calculate max tokens
	maxTokens := len(content) / 8
	if maxTokens < 256 {
		maxTokens = 256
	}
	if maxTokens > 4096 {
		maxTokens = 4096
	}

	params := external.CallLLMParams{
		Endpoint:     p.compresrEndpoint,
		APISecret:    p.compresrKey,
		Model:        p.compresrModel,
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
		MaxTokens:    maxTokens,
		Timeout:      p.compresrTimeout,
	}

	// OAuth fallback: reuse Bearer token captured from the incoming request.
	// Claude Code OAuth tokens (sk-ant-oat*) require Bearer auth + anthropic-beta header.
	if params.APISecret == "" && capturedBearerToken != "" {
		params.BearerAuth = capturedBearerToken
		if capturedBetaHeader != "" {
			params.ExtraHeaders = map[string]string{"anthropic-beta": capturedBetaHeader}
		}
	}

	result, err := external.CallLLM(reqCtx, params)
	if err != nil {
		return "", err
	}

	// Validate compression reduced size
	if len(result.Content) >= len(content) {
		return "", fmt.Errorf("external_provider compression ineffective: output (%d bytes) >= input (%d bytes)",
			len(result.Content), len(content))
	}

	log.Debug().
		Str("provider", result.Provider).
		Str("model", p.compresrModel).
		Bool("query_agnostic", p.compresrQueryAgnostic).
		Int("original_size", len(content)).
		Int("compressed_size", len(result.Content)).
		Float64("ratio", float64(len(result.Content))/float64(len(content))).
		Msg("tool_output: external_provider compression completed")

	return result.Content, nil
}
