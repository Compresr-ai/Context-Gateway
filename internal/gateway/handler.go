// HTTP request handling for the compression gateway.
//
// DESIGN: Main request flow:
//   - handleProxy():        Entry point for all LLM requests
//   - processCompressionPipeline(): Route to appropriate pipe
//   - handleStreaming():    SSE streaming with compressed request
//   - handleNonStreaming(): Standard request with expand loop
//
// Also includes health check, expand endpoint, and telemetry helpers.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/monitoring"
	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
	"github.com/compresr/context-gateway/internal/preemptive"
)

// maskKey masks an API key for logging (shows first 8 and last 4 chars).
func maskKey(key string) string {
	if key == "" {
		return "(empty)"
	}
	if len(key) < 16 {
		return "***"
	}
	return key[:8] + "..." + key[len(key)-4:]
}

// sanitizeModelName strips provider prefixes from model names in request body.
// Handles formats like "anthropic/claude-3" -> "claude-3", "openai/gpt-4" -> "gpt-4"
func sanitizeModelName(body []byte) []byte {
	// Quick check if body contains a provider prefix pattern
	if !bytes.Contains(body, []byte(`"model"`)) {
		return body
	}

	// Parse and modify
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return body // Return original if can't parse
	}

	if model, ok := req["model"].(string); ok {
		// Strip known provider prefixes
		for _, prefix := range []string{"anthropic/", "openai/", "google/", "meta/"} {
			if strings.HasPrefix(model, prefix) {
				req["model"] = strings.TrimPrefix(model, prefix)
				if sanitized, err := json.Marshal(req); err == nil {
					return sanitized
				}
				break
			}
		}
	}

	return body
}

// writeError writes a JSON error response.
func (g *Gateway) writeError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{"message": msg, "type": "gateway_error"},
	})
}

// handleHealth returns gateway health status.
func (g *Gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	health := map[string]interface{}{
		"status":  "ok",
		"time":    time.Now().Format(time.RFC3339),
		"version": "1.0.0",
	}

	if err := g.store.Set("_health_", "ok"); err != nil {
		health["status"] = "degraded"
	} else {
		g.store.Delete("_health_")
	}

	w.Header().Set("Content-Type", "application/json")
	if health["status"] != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(health)
}

// handleExpand retrieves raw data from shadow context.
func (g *Gateway) handleExpand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1024)

	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.ID) == 0 || len(req.ID) > 64 {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	data, ok := g.store.Get(req.ID)
	g.tracker.RecordExpand(&monitoring.ExpandEvent{
		Timestamp: time.Now(), ShadowRefID: req.ID, Found: ok, Success: ok,
	})

	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": req.ID, "content": data})
}

// handleProxy processes requests through the compression pipeline.
func (g *Gateway) handleProxy(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	requestID := g.getRequestID(r)

	// Validate request
	if r.Method != http.MethodPost {
		g.alerts.FlagInvalidRequest(requestID, "method not allowed", nil)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Non-LLM endpoints (telemetry, analytics, event_logging) forward to upstream unchanged
	// These SDK requests pass through transparently - client unaware of proxy
	if g.isNonLLMEndpoint(r.URL.Path) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodySize)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			g.writeError(w, "failed to read request", http.StatusBadRequest)
			return
		}

		// Forward to upstream unchanged
		resp, err := g.forwardPassthrough(r.Context(), r, body)
		if err != nil {
			log.Debug().Err(err).Str("path", r.URL.Path).Msg("passthrough failed")
			g.writeError(w, "upstream request failed", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		responseBody, _ := io.ReadAll(resp.Body)
		copyHeaders(w, resp.Header)
		w.WriteHeader(resp.StatusCode)
		w.Write(responseBody)
		return
	}

	// Read and validate body
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		g.alerts.FlagInvalidRequest(requestID, "failed to read body", nil)
		g.writeError(w, "failed to read request", http.StatusBadRequest)
		return
	}

	// Identify provider and get adapter - SINGLE entry point for provider detection
	provider, adapter := adapters.IdentifyAndGetAdapter(g.registry, r.URL.Path, r.Header)
	if adapter == nil {
		g.alerts.FlagInvalidRequest(requestID, "unsupported format", nil)
		g.writeError(w, "unsupported request format", http.StatusBadRequest)
		return
	}

	// Build pipeline context (no universal parsing needed)
	pipeCtx := NewPipelineContext(provider, adapter, body, r.URL.Path)
	pipeCtx.CompressionThreshold = config.ParseCompressionThreshold(r.Header.Get(HeaderCompressionThreshold))

	// Extract model for preemptive summarization
	model := adapter.ExtractModel(body)
	pipeCtx.Model = model

	// Process preemptive summarization (before compression pipeline)
	// This may modify the body if compaction is requested and ready
	// For SDK compaction with precomputed summary, may return synthetic response
	var preemptiveHeaders map[string]string
	var isCompaction bool
	var syntheticResponse []byte
	if g.preemptive != nil {
		var preemptiveBody []byte
		preemptiveBody, isCompaction, syntheticResponse, preemptiveHeaders, _ = g.preemptive.ProcessRequest(r.Header, body, model, adapter.Name())

		// If we have a synthetic response (SDK compaction with cached summary),
		// return it immediately without forwarding to Anthropic
		if len(syntheticResponse) > 0 {
			log.Info().
				Str("request_id", requestID).
				Int("response_size", len(syntheticResponse)).
				Msg("Returning synthetic compaction response (instant!)")

			// Add preemptive headers to response
			for k, v := range preemptiveHeaders {
				w.Header().Set(k, v)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Synthetic-Response", "true")
			w.WriteHeader(http.StatusOK)
			w.Write(syntheticResponse)

			// Log telemetry async to not block the response
			go g.recordRequestTelemetry(telemetryParams{
				requestID:        requestID,
				startTime:        startTime,
				method:           r.Method,
				path:             r.URL.Path,
				clientIP:         r.RemoteAddr,
				requestBodySize:  len(body),
				responseBodySize: len(syntheticResponse),
				provider:         adapter.Name(),
				pipeType:         PipeType("precomputed"),
				pipeStrategy:     "synthetic",
				originalTokens:   len(body) / 4,
				compressionUsed:  false,
				statusCode:       http.StatusOK,
				compressLatency:  0,
				forwardLatency:   0,
				pipeCtx:          pipeCtx,
				adapter:          adapter,
				requestBody:      body,
				responseBody:     syntheticResponse,
				forwardBody:      nil,
			})
			return
		}

		if isCompaction && preemptiveBody != nil && len(preemptiveBody) > 0 {
			// Merge compacted messages with original request (preserve model, tools, etc.)
			if merged, err := mergeCompactedWithOriginal(preemptiveBody, body); err == nil {
				body = merged
				// Update pipeCtx with new body
				pipeCtx.OriginalRequest = body
			}
		}
	}
	// Store preemptive headers in context for response
	pipeCtx.PreemptiveHeaders = preemptiveHeaders
	pipeCtx.IsCompaction = isCompaction

	// Process compression pipeline
	forwardBody, pipeType, pipeStrategy, compressionUsed, compressLatency := g.processCompressionPipeline(body, pipeCtx, requestID)

	// Estimate tokens from body size (~4 chars per token)
	originalTokens := len(body) / 4

	// Inject expand_context tool if needed (now compatible with streaming!)
	isStreaming := g.isStreamingRequest(body)
	expandEnabled := g.config.Pipes.ToolOutput.EnableExpandContext // Enabled for both streaming and non-streaming
	if expandEnabled && compressionUsed && len(pipeCtx.ShadowRefs) > 0 {
		if injected, err := tooloutput.InjectExpandContextTool(forwardBody, pipeCtx.ShadowRefs, string(provider)); err == nil {
			forwardBody = injected
		}
	}

	// Route to streaming or non-streaming handler
	if isStreaming {
		g.handleStreamingWithExpand(w, r, forwardBody, pipeCtx, requestID, startTime, adapter,
			pipeType, pipeStrategy, originalTokens, compressionUsed, compressLatency, body, expandEnabled)
	} else {
		g.handleNonStreaming(w, r, forwardBody, pipeCtx, requestID, startTime, adapter,
			pipeType, pipeStrategy, originalTokens, compressionUsed, compressLatency, body, expandEnabled)
	}
}

// handleNonStreaming handles non-streaming requests with expand loop support.
func (g *Gateway) handleNonStreaming(w http.ResponseWriter, r *http.Request, forwardBody []byte,
	pipeCtx *PipelineContext, requestID string, startTime time.Time, adapter adapters.Adapter,
	pipeType PipeType, pipeStrategy string, originalTokens int, compressionUsed bool,
	compressLatency time.Duration, originalBody []byte, expandEnabled bool) {

	provider := adapter.Name()

	forwardFunc := func(ctx context.Context, body []byte) (*http.Response, error) {
		return g.forwardPassthrough(ctx, r, body)
	}

	result, err := g.expander.RunExpandLoop(
		r.Context(), forwardFunc, forwardBody, requestID, provider, provider == "anthropic", expandEnabled,
	)
	if err != nil || result.Response == nil {
		g.recordRequestTelemetry(telemetryParams{
			requestID: requestID, startTime: startTime, method: r.Method, path: r.URL.Path,
			clientIP: r.RemoteAddr, requestBodySize: len(originalBody), responseBodySize: 0,
			provider: provider, pipeType: pipeType, pipeStrategy: pipeStrategy, originalTokens: originalTokens,
			compressionUsed: compressionUsed, statusCode: 502, errorMsg: "expand loop failed",
			compressLatency: compressLatency, forwardLatency: result.ForwardLatency, pipeCtx: pipeCtx,
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody,
		})
		g.writeError(w, "upstream request failed", http.StatusBadGateway)
		return
	}

	responseBody := result.ResponseBody
	if expandEnabled {
		if filtered, ok := g.expander.FilterExpandContextFromResponse(responseBody); ok {
			responseBody = filtered
		}
	}

	// Update pipeCtx with expand usage for logging
	pipeCtx.ExpandLoopCount = result.ExpandLoopCount

	// Log expand_context usage if LLM requested full content
	if result.ExpandLoopCount > 0 {
		// Collect shadow IDs from pipeCtx
		shadowIDs := make([]string, 0, len(pipeCtx.ToolOutputCompressions))
		for _, tc := range pipeCtx.ToolOutputCompressions {
			if tc.ShadowID != "" {
				shadowIDs = append(shadowIDs, tc.ShadowID)
			}
		}

		g.requestLogger.LogExpandContext(&monitoring.ExpandContextInfo{
			RequestID:     requestID,
			ShadowIDs:     shadowIDs,
			CallsFound:    result.ExpandCallsFound,
			CallsNotFound: result.ExpandCallsNotFound,
			TotalLoops:    result.ExpandLoopCount,
		})
	}

	// Record telemetry with usage extraction
	g.recordRequestTelemetry(telemetryParams{
		requestID: requestID, startTime: startTime, method: r.Method, path: r.URL.Path,
		clientIP: r.RemoteAddr, requestBodySize: len(originalBody), responseBodySize: len(responseBody),
		provider: provider, pipeType: pipeType, pipeStrategy: pipeStrategy, originalTokens: originalTokens,
		compressionUsed: compressionUsed, statusCode: result.Response.StatusCode,
		compressLatency: compressLatency, forwardLatency: result.ForwardLatency,
		expandLoops: result.ExpandLoopCount, expandCallsFound: result.ExpandCallsFound,
		expandCallsNotFound: result.ExpandCallsNotFound, pipeCtx: pipeCtx,
		adapter: adapter, requestBody: originalBody, responseBody: result.ResponseBody,
		forwardBody: forwardBody,
	})

	// Log provider errors and compression details
	if result.Response.StatusCode >= 400 {
		g.alerts.FlagProviderError(requestID, provider, result.Response.StatusCode,
			string(responseBody[:min(500, len(responseBody))]))
	}
	if compressionUsed {
		g.logCompressionDetails(pipeCtx, requestID, string(pipeType), originalBody, forwardBody)
	}

	// Write response
	copyHeaders(w, result.Response.Header)
	addPreemptiveHeaders(w, pipeCtx.PreemptiveHeaders)
	w.WriteHeader(result.Response.StatusCode)
	w.Write(responseBody)
}

// handleStreamingWithExpand handles streaming requests with expand_context support.
// When expand_context is enabled:
//  1. Buffer the streaming response (detect expand_context calls)
//  2. If expand_context detected → rewrite history, re-send to LLM
//  3. If not detected → flush buffer to client
//
// This implements "selective replace" design: only requested tools are expanded,
// keeping history clean and maximizing KV-cache prefix hits.
func (g *Gateway) handleStreamingWithExpand(w http.ResponseWriter, r *http.Request, forwardBody []byte,
	pipeCtx *PipelineContext, requestID string, startTime time.Time, adapter adapters.Adapter,
	pipeType PipeType, pipeStrategy string, originalTokens int, compressionUsed bool,
	compressLatency time.Duration, originalBody []byte, expandEnabled bool) {

	provider := adapter.Name()
	g.requestLogger.LogOutgoing(&monitoring.OutgoingRequestInfo{
		RequestID: requestID, Provider: provider, TargetURL: r.Header.Get(HeaderTargetURL),
		Method: "POST", BodySize: len(forwardBody), Compressed: compressionUsed,
	})

	forwardStart := time.Now()
	resp, err := g.forwardPassthrough(r.Context(), r, forwardBody)
	if err != nil {
		g.recordRequestTelemetry(telemetryParams{
			requestID: requestID, startTime: startTime, method: r.Method, path: r.URL.Path,
			clientIP: r.RemoteAddr, requestBodySize: len(originalBody), responseBodySize: 0,
			provider: provider, pipeType: pipeType, pipeStrategy: pipeStrategy + "_streaming", originalTokens: originalTokens,
			compressionUsed: compressionUsed, statusCode: 502, errorMsg: err.Error(),
			compressLatency: compressLatency, forwardLatency: time.Since(forwardStart), pipeCtx: pipeCtx,
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody,
		})
		g.writeError(w, "upstream request failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	// If expand not enabled, stream directly
	if !expandEnabled || !compressionUsed || len(pipeCtx.ShadowRefs) == 0 {
		defer resp.Body.Close()
		copyHeaders(w, resp.Header)
		addPreemptiveHeaders(w, pipeCtx.PreemptiveHeaders)
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(resp.StatusCode)
		g.streamResponse(w, resp.Body)

		g.recordRequestTelemetry(telemetryParams{
			requestID: requestID, startTime: startTime, method: r.Method, path: r.URL.Path,
			clientIP: r.RemoteAddr, requestBodySize: len(originalBody), responseBodySize: 0,
			provider: provider, pipeType: pipeType, pipeStrategy: pipeStrategy + "_streaming", originalTokens: originalTokens,
			compressionUsed: compressionUsed, statusCode: resp.StatusCode,
			compressLatency: compressLatency, forwardLatency: time.Since(forwardStart), pipeCtx: pipeCtx,
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody,
		})
		if compressionUsed {
			g.logCompressionDetails(pipeCtx, requestID, string(pipeType), originalBody, forwardBody)
		}
		return
	}

	// expand_context enabled: buffer response to detect expand calls
	streamBuffer := tooloutput.NewStreamBuffer()
	var bufferedChunks [][]byte

	// Read and buffer the entire stream
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			bufferedChunks = append(bufferedChunks, chunk)

			// Process for expand_context detection
			_, _ = streamBuffer.ProcessChunk(chunk)
		}
		if readErr != nil {
			break
		}
	}
	resp.Body.Close()

	// Check if expand_context was called
	expandCalls := streamBuffer.GetSuppressedCalls()

	if len(expandCalls) > 0 {
		// expand_context detected - rewrite history and re-send
		log.Info().
			Int("expand_calls", len(expandCalls)).
			Str("request_id", requestID).
			Msg("streaming: expand_context detected, rewriting history")

		// Rewrite history with expanded content
		rewrittenBody, expandedIDs, err := g.expander.RewriteHistoryWithExpansion(forwardBody, expandCalls)
		if err != nil {
			log.Error().Err(err).Msg("streaming: failed to rewrite history")
			// Fall back to flushing original response
			g.flushBufferedResponse(w, resp.Header, pipeCtx.PreemptiveHeaders, bufferedChunks)
			return
		}

		// Invalidate compressed mappings for expanded IDs
		g.expander.InvalidateExpandedMappings(expandedIDs)

		// Re-send with rewritten history
		retryResp, err := g.forwardPassthrough(r.Context(), r, rewrittenBody)
		if err != nil {
			log.Error().Err(err).Msg("streaming: failed to re-send after expansion")
			g.flushBufferedResponse(w, resp.Header, pipeCtx.PreemptiveHeaders, bufferedChunks)
			return
		}
		defer retryResp.Body.Close()

		// Stream the retry response (filter expand_context if present)
		copyHeaders(w, retryResp.Header)
		addPreemptiveHeaders(w, pipeCtx.PreemptiveHeaders)
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(retryResp.StatusCode)

		g.streamResponseWithFilter(w, retryResp.Body)

		g.recordRequestTelemetry(telemetryParams{
			requestID: requestID, startTime: startTime, method: r.Method, path: r.URL.Path,
			clientIP: r.RemoteAddr, requestBodySize: len(originalBody), responseBodySize: 0,
			provider: provider, pipeType: pipeType, pipeStrategy: pipeStrategy + "_streaming_expanded",
			originalTokens: originalTokens, compressionUsed: compressionUsed, statusCode: retryResp.StatusCode,
			compressLatency: compressLatency, forwardLatency: time.Since(forwardStart), pipeCtx: pipeCtx,
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody,
		})

		log.Info().
			Int("expanded_ids", len(expandedIDs)).
			Str("request_id", requestID).
			Msg("streaming: expansion complete")
	} else {
		// No expand_context detected - flush buffered response
		g.flushBufferedResponse(w, resp.Header, pipeCtx.PreemptiveHeaders, bufferedChunks)

		g.recordRequestTelemetry(telemetryParams{
			requestID: requestID, startTime: startTime, method: r.Method, path: r.URL.Path,
			clientIP: r.RemoteAddr, requestBodySize: len(originalBody), responseBodySize: 0,
			provider: provider, pipeType: pipeType, pipeStrategy: pipeStrategy + "_streaming", originalTokens: originalTokens,
			compressionUsed: compressionUsed, statusCode: resp.StatusCode,
			compressLatency: compressLatency, forwardLatency: time.Since(forwardStart), pipeCtx: pipeCtx,
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody,
		})
	}

	if compressionUsed {
		g.logCompressionDetails(pipeCtx, requestID, string(pipeType), originalBody, forwardBody)
	}
}

// flushBufferedResponse writes buffered chunks to the response writer.
func (g *Gateway) flushBufferedResponse(w http.ResponseWriter, headers http.Header, preemptiveHeaders map[string]string, chunks [][]byte) {
	copyHeaders(w, headers)
	addPreemptiveHeaders(w, preemptiveHeaders)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	for _, chunk := range chunks {
		w.Write(chunk)
		if ok {
			flusher.Flush()
		}
	}
}

// streamResponseWithFilter streams response while filtering expand_context calls.
func (g *Gateway) streamResponseWithFilter(w http.ResponseWriter, reader io.Reader) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Warn().Msg("streaming not supported, falling back to buffered")
		io.Copy(w, reader)
		return
	}

	streamBuffer := tooloutput.NewStreamBuffer()
	buf := make([]byte, 4096)

	for {
		n, err := reader.Read(buf)
		if n > 0 {
			// Filter expand_context from the stream
			filtered, _ := streamBuffer.ProcessChunk(buf[:n])
			if len(filtered) > 0 {
				w.Write(filtered)
				flusher.Flush()
			}
		}
		if err != nil {
			if err != io.EOF {
				log.Debug().Err(err).Msg("error reading stream")
			}
			break
		}
	}
}

// streamResponse streams data from reader to writer with flushing.
func (g *Gateway) streamResponse(w http.ResponseWriter, reader io.Reader) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Warn().Msg("streaming not supported, falling back to buffered")
		io.Copy(w, reader)
		return
	}

	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				log.Debug().Err(writeErr).Msg("client disconnected")
				break
			}
			flusher.Flush()
		}
		if err != nil {
			if err != io.EOF {
				log.Debug().Err(err).Msg("error reading stream")
			}
			break
		}
	}
}

// processCompressionPipeline routes and processes through compression pipes.
func (g *Gateway) processCompressionPipeline(body []byte, pipeCtx *PipelineContext, requestID string) ([]byte, PipeType, string, bool, time.Duration) {
	pipeType := g.router.Route(pipeCtx)
	if pipeType == PipeNone {
		return body, pipeType, config.StrategyPassthrough, false, 0
	}

	compressStart := time.Now()
	g.requestLogger.LogPipelineStage(&monitoring.PipelineStageInfo{
		RequestID: requestID, Stage: "process", Pipe: string(pipeType),
	})

	var pipeStrategy string
	var compressionUsed bool
	var forwardBody []byte = body

	switch pipeType {
	case PipeToolOutput:
		pipeStrategy = g.config.Pipes.ToolOutput.Strategy
		if pipeStrategy != config.StrategyPassthrough {
			if modifiedBody, err := g.router.Process(pipeCtx); err != nil {
				log.Warn().Err(err).Msg("tool_output pipe failed")
				g.alerts.FlagCompressionFailure(requestID, string(pipeType), pipeStrategy, err)
			} else {
				forwardBody = modifiedBody
				compressionUsed = pipeCtx.OutputCompressed
			}
		}
	case PipeHistory:
		pipeStrategy = g.config.Pipes.History.Strategy
		if pipeStrategy != config.StrategyPassthrough {
			if modifiedBody, err := g.router.ProcessHistory(pipeCtx); err != nil {
				log.Warn().Err(err).Msg("history pipe failed")
				g.alerts.FlagCompressionFailure(requestID, string(pipeType), pipeStrategy, err)
			} else {
				forwardBody = modifiedBody
				compressionUsed = pipeCtx.HistoryCompressed
			}
		}
	case PipeToolDiscovery:
		pipeStrategy = g.config.Pipes.ToolDiscovery.Strategy
		if pipeStrategy != config.StrategyPassthrough {
			if modifiedBody, err := g.router.Process(pipeCtx); err != nil {
				log.Warn().Err(err).Msg("tool_discovery pipe failed")
				g.alerts.FlagCompressionFailure(requestID, string(pipeType), pipeStrategy, err)
			} else {
				forwardBody = modifiedBody
				compressionUsed = pipeCtx.ToolsFiltered
			}
		}
	}

	compressLatency := time.Since(compressStart)

	// Record compression metrics
	for _, tc := range pipeCtx.ToolOutputCompressions {
		g.requestLogger.LogCompression(&monitoring.CompressionInfo{
			RequestID: requestID, ToolName: tc.ToolName, ToolCallID: tc.ToolCallID,
			ShadowID: tc.ShadowID, OriginalBytes: tc.OriginalBytes, CompressedBytes: tc.CompressedBytes,
			CompressionRatio: float64(tc.CompressedBytes) / float64(max(tc.OriginalBytes, 1)),
			CacheHit:         tc.CacheHit, IsLastTool: tc.IsLastTool, MappingStatus: tc.MappingStatus,
			Duration: compressLatency,
		})
		g.metrics.RecordCompression(tc.OriginalBytes, tc.CompressedBytes, true)
		if tc.CacheHit {
			g.metrics.RecordCacheHit()
		} else {
			g.metrics.RecordCacheMiss()
		}
	}

	return forwardBody, pipeType, pipeStrategy, compressionUsed, compressLatency
}

// forwardPassthrough forwards the request body unchanged to upstream.
func (g *Gateway) forwardPassthrough(ctx context.Context, r *http.Request, body []byte) (*http.Response, error) {
	targetURL := r.Header.Get(HeaderTargetURL)
	if targetURL != "" {
		// X-Target-URL provided - append request path if not already included
		if !strings.HasSuffix(targetURL, r.URL.Path) {
			targetURL = strings.TrimSuffix(targetURL, "/") + r.URL.Path
		}
	} else {
		targetURL = g.autoDetectTargetURL(r)
		if targetURL == "" {
			return nil, fmt.Errorf("missing %s header", HeaderTargetURL)
		}
	}

	// Sanitize model name (strip provider prefix like "anthropic/", "openai/")
	body = sanitizeModelName(body)

	log.Info().
		Str("targetURL", targetURL).
		Str("x-api-key", maskKey(r.Header.Get("x-api-key"))).
		Msg("forwarding request")

	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL: %w", err)
	}
	if !g.isAllowedHost(parsedURL.Host) {
		return nil, fmt.Errorf("target host not allowed: %s", parsedURL.Host)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	// Forward relevant headers
	for _, h := range []string{
		"Content-Type", "Authorization", "x-api-key", "x-goog-api-key",
		"api-key", "anthropic-version", "anthropic-beta",
	} {
		if v := r.Header.Get(h); v != "" {
			httpReq.Header.Set(h, v)
		}
	}

	resp, err := g.httpClient.Do(httpReq)
	if err != nil {
		log.Error().Err(err).Str("targetURL", targetURL).Msg("upstream request failed")
		return nil, err
	}

	// Log error responses for debugging
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		log.Error().
			Int("status", resp.StatusCode).
			Str("targetURL", targetURL).
			Str("response", string(bodyBytes[:min(500, len(bodyBytes))])).
			Msg("upstream error response")
		// Recreate body reader for caller
		resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	return resp, nil
}

// isStreamingRequest checks if the request has "stream": true.
func (g *Gateway) isStreamingRequest(body []byte) bool {
	if !bytes.Contains(body, []byte(`"stream"`)) {
		return false
	}
	var req struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &req)
	return req.Stream
}

// getRequestID gets or generates a request ID.
func (g *Gateway) getRequestID(r *http.Request) string {
	if id := r.Header.Get(HeaderRequestID); id != "" {
		return id
	}
	if id := monitoring.RequestIDFromContext(r.Context()); id != "" {
		return id
	}
	return uuid.New().String()
}

// copyHeaders copies HTTP headers from source to destination.
func copyHeaders(w http.ResponseWriter, src http.Header) {
	for k, v := range src {
		w.Header()[k] = v
	}
}

// =============================================================================
// TELEMETRY HELPERS
// =============================================================================

// telemetryParams holds all parameters needed for telemetry recording.
type telemetryParams struct {
	requestID           string
	startTime           time.Time
	method              string
	path                string
	clientIP            string
	requestBodySize     int
	responseBodySize    int
	provider            string
	pipeType            PipeType
	pipeStrategy        string
	originalTokens      int
	compressionUsed     bool
	statusCode          int
	errorMsg            string
	compressLatency     time.Duration
	forwardLatency      time.Duration
	expandLoops         int
	expandCallsFound    int
	expandCallsNotFound int
	pipeCtx             *PipelineContext
	// For usage extraction from API response
	adapter      adapters.Adapter
	requestBody  []byte // Original request from client
	responseBody []byte // Response from LLM
	forwardBody  []byte // Compressed request sent to LLM (for proxy interaction tracking)
}

// recordRequestTelemetry records a complete request event.
func (g *Gateway) recordRequestTelemetry(params telemetryParams) {
	m := g.calculateMetrics(params.pipeCtx, params.originalTokens)

	// Extract model and usage from request/response using adapter
	var model string
	var usage adapters.UsageInfo

	if params.adapter != nil {
		model = params.adapter.ExtractModel(params.requestBody)
		usage = params.adapter.ExtractUsage(params.responseBody)
	}

	g.tracker.RecordRequest(&monitoring.RequestEvent{
		RequestID:            params.requestID,
		Timestamp:            params.startTime,
		Method:               params.method,
		Path:                 params.path,
		ClientIP:             params.clientIP,
		Provider:             params.provider,
		Model:                model,
		RequestBodySize:      params.requestBodySize,
		ResponseBodySize:     params.responseBodySize,
		StatusCode:           params.statusCode,
		PipeType:             monitoring.PipeType(params.pipeType),
		PipeStrategy:         params.pipeStrategy,
		OriginalTokens:       m.originalTokens,
		CompressedTokens:     m.compressedTokens,
		TokensSaved:          m.tokensSaved,
		CompressionRatio:     m.compressionRatio,
		CompressionUsed:      params.compressionUsed,
		ShadowRefsCreated:    len(params.pipeCtx.ShadowRefs),
		ExpandLoops:          params.expandLoops,
		ExpandCallsFound:     params.expandCallsFound,
		ExpandCallsNotFound:  params.expandCallsNotFound,
		Success:              params.statusCode < 400,
		Error:                params.errorMsg,
		CompressionLatencyMs: params.compressLatency.Milliseconds(),
		ForwardLatencyMs:     params.forwardLatency.Milliseconds(),
		TotalLatencyMs:       time.Since(params.startTime).Milliseconds(),
		InputTokens:          usage.InputTokens,
		OutputTokens:         usage.OutputTokens,
		TotalTokens:          usage.TotalTokens,
	})

	// Record trajectory if enabled (ATIF format)
	g.recordTrajectory(params, model, usage)
}

// recordTrajectory records user messages and agent responses in ATIF format.
func (g *Gateway) recordTrajectory(params telemetryParams, model string, usage adapters.UsageInfo) {
	if g.trajectory == nil || !g.trajectory.Enabled() {
		return
	}

	// Only record successful requests
	if params.statusCode >= 400 {
		return
	}

	// Compute session ID from request body using the same logic as preemptive layer
	// This ensures trajectory files are grouped by the same session ID as compaction
	sessionID := preemptive.ComputeSessionID(params.requestBody)
	if sessionID == "" {
		// Fallback: check preemptive headers (may have computed it already)
		if params.pipeCtx != nil && params.pipeCtx.PreemptiveHeaders != nil {
			sessionID = params.pipeCtx.PreemptiveHeaders["X-Session-ID"]
		}
	}
	if sessionID == "" {
		// Final fallback: use "default" for requests without session ID
		sessionID = "default"
	}

	// Set model on first successful request
	if model != "" {
		g.trajectory.SetAgentModel(sessionID, model)
	}

	// Extract user message from request
	if params.adapter != nil && len(params.requestBody) > 0 {
		userQuery := params.adapter.ExtractUserQuery(params.requestBody)
		if userQuery != "" {
			g.trajectory.RecordUserMessage(sessionID, userQuery)
		}
	}

	// Extract agent response from response body (if available)
	var content string
	var toolCalls []monitoring.ToolCall
	if len(params.responseBody) > 0 {
		content, toolCalls = g.extractAgentResponse(params.responseBody)
	}

	// Always record agent step with proxy interaction for every LLM request
	// Even for streaming or when content extraction fails, we want to show proxy flow
	isStreaming := len(params.responseBody) == 0
	if isStreaming {
		content = "[streaming response]"
	}

	g.trajectory.RecordAgentResponse(sessionID, monitoring.AgentResponseData{
		Message:          content,
		Model:            model,
		ToolCalls:        toolCalls,
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
	})

	// Record proxy interaction (client→proxy→LLM→proxy→client flow)
	g.recordProxyInteraction(params, sessionID, usage)
}

// recordProxyInteraction records the full proxy flow for trajectory.
func (g *Gateway) recordProxyInteraction(params telemetryParams, sessionID string, usage adapters.UsageInfo) {
	if g.trajectory == nil || !g.trajectory.Enabled() {
		return
	}

	// Extract messages from original request (client → proxy)
	var clientMessages []any
	if len(params.requestBody) > 0 {
		var req map[string]any
		if err := json.Unmarshal(params.requestBody, &req); err == nil {
			if msgs, ok := req["messages"].([]any); ok {
				clientMessages = msgs
			}
		}
	}

	// Extract messages from forward body (proxy → LLM)
	var compressedMessages []any
	if len(params.forwardBody) > 0 {
		var req map[string]any
		if err := json.Unmarshal(params.forwardBody, &req); err == nil {
			if msgs, ok := req["messages"].([]any); ok {
				compressedMessages = msgs
			}
		}
	}

	// Extract messages from response (LLM → proxy)
	var responseMessages []any
	if len(params.responseBody) > 0 {
		var resp map[string]any
		if err := json.Unmarshal(params.responseBody, &resp); err == nil {
			if choices, ok := resp["choices"].([]any); ok {
				for _, c := range choices {
					if choice, ok := c.(map[string]any); ok {
						if msg, ok := choice["message"].(map[string]any); ok {
							responseMessages = append(responseMessages, msg)
						}
					}
				}
			}
		}
	}

	// Get compression info from pipeline context - convert to trajectory format
	var toolCompressions []monitoring.ToolCompressionEntry
	if params.pipeCtx != nil && len(params.pipeCtx.ToolOutputCompressions) > 0 {
		for _, tc := range params.pipeCtx.ToolOutputCompressions {
			ratio := float64(tc.CompressedBytes) / float64(max(tc.OriginalBytes, 1))
			// Determine status from MappingStatus
			status := tc.MappingStatus
			if status == "" {
				if tc.CacheHit {
					status = "cache_hit"
				} else if tc.CompressedBytes < tc.OriginalBytes {
					status = "compressed"
				} else {
					status = "passthrough"
				}
			}
			toolCompressions = append(toolCompressions, monitoring.ToolCompressionEntry{
				ToolName:          tc.ToolName,
				ToolCallID:        tc.ToolCallID,
				Status:            status,
				ShadowID:          tc.ShadowID,
				OriginalBytes:     tc.OriginalBytes,
				CompressedBytes:   tc.CompressedBytes,
				CompressionRatio:  ratio,
				OriginalContent:   tc.OriginalContent,
				CompressedContent: tc.CompressedContent,
				CacheHit:          tc.CacheHit,
			})
		}
	}

	// Estimate token counts (rough estimate: 4 chars per token)
	clientTokens := len(params.requestBody) / 4
	compressedTokens := len(params.forwardBody) / 4
	if params.originalTokens > 0 {
		clientTokens = params.originalTokens
	}

	g.trajectory.RecordProxyInteraction(sessionID, monitoring.ProxyInteractionData{
		PipeType:           string(params.pipeType),
		PipeStrategy:       params.pipeStrategy,
		ClientMessages:     clientMessages,
		CompressedMessages: compressedMessages,
		ClientTokens:       clientTokens,
		CompressedTokens:   compressedTokens,
		CompressionEnabled: params.compressionUsed,
		ToolCompressions:   toolCompressions,
		ResponseMessages:   responseMessages,
		ResponseTokens:     usage.OutputTokens,
	})
}

// extractAgentResponse extracts content and tool calls from an API response.
func (g *Gateway) extractAgentResponse(responseBody []byte) (string, []monitoring.ToolCall) {
	var resp map[string]any
	if err := json.Unmarshal(responseBody, &resp); err != nil {
		return "", nil
	}

	// Try OpenAI format: {"choices": [{"message": {"content": "...", "tool_calls": [...]}}]}
	if choices, ok := resp["choices"].([]any); ok && len(choices) > 0 {
		choice, ok := choices[0].(map[string]any)
		if !ok {
			return "", nil
		}
		msg, ok := choice["message"].(map[string]any)
		if !ok {
			return "", nil
		}

		content, _ := msg["content"].(string)
		var toolCalls []monitoring.ToolCall

		if tcs, ok := msg["tool_calls"].([]any); ok {
			for _, tc := range tcs {
				tcMap, ok := tc.(map[string]any)
				if !ok {
					continue
				}

				toolCall := monitoring.ToolCall{}
				if id, ok := tcMap["id"].(string); ok {
					toolCall.ToolCallID = id
				}

				if fn, ok := tcMap["function"].(map[string]any); ok {
					if name, ok := fn["name"].(string); ok {
						toolCall.FunctionName = name
					}
					if args, ok := fn["arguments"].(string); ok {
						var argsMap map[string]any
						if err := json.Unmarshal([]byte(args), &argsMap); err == nil {
							toolCall.Arguments = argsMap
						} else {
							toolCall.Arguments = args
						}
					}
				}

				if toolCall.ToolCallID != "" && toolCall.FunctionName != "" {
					toolCalls = append(toolCalls, toolCall)
				}
			}
		}

		return content, toolCalls
	}

	// Try Anthropic format: {"content": [{"type": "text", "text": "..."}], "stop_reason": "..."}
	if contentArr, ok := resp["content"].([]any); ok {
		var content string
		var toolCalls []monitoring.ToolCall

		for _, item := range contentArr {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}

			itemType, _ := itemMap["type"].(string)
			switch itemType {
			case "text":
				if text, ok := itemMap["text"].(string); ok {
					content += text
				}
			case "tool_use":
				toolCall := monitoring.ToolCall{}
				if id, ok := itemMap["id"].(string); ok {
					toolCall.ToolCallID = id
				}
				if name, ok := itemMap["name"].(string); ok {
					toolCall.FunctionName = name
				}
				if input, ok := itemMap["input"].(map[string]any); ok {
					toolCall.Arguments = input
				}
				if toolCall.ToolCallID != "" && toolCall.FunctionName != "" {
					toolCalls = append(toolCalls, toolCall)
				}
			}
		}

		return content, toolCalls
	}

	return "", nil
}

// requestMetrics holds calculated metrics for a request.
type requestMetrics struct {
	originalTokens, compressedTokens, tokensSaved int
	compressionRatio                              float64
}

// calculateMetrics computes compression metrics from pipeline context.
func (g *Gateway) calculateMetrics(pipeCtx *PipelineContext, originalTokens int) requestMetrics {
	m := requestMetrics{originalTokens: originalTokens, compressedTokens: originalTokens, compressionRatio: 1.0}

	var totalOriginal, totalCompressed int
	for _, tc := range pipeCtx.ToolOutputCompressions {
		totalOriginal += tc.OriginalBytes
		totalCompressed += tc.CompressedBytes
	}

	if saved := totalOriginal - totalCompressed; saved > 0 {
		// Estimate tokens saved: ~4 chars per token
		m.tokensSaved = saved / 4
		// Ensure compressedTokens doesn't go negative
		if m.tokensSaved > originalTokens {
			m.tokensSaved = originalTokens
		}
		m.compressedTokens = originalTokens - m.tokensSaved
	}
	if totalOriginal > 0 {
		m.compressionRatio = float64(totalCompressed) / float64(totalOriginal)
	}
	return m
}

// logCompressionDetails logs compression comparisons if enabled.
func (g *Gateway) logCompressionDetails(pipeCtx *PipelineContext, requestID, pipeType string, originalBody, compressedBody []byte) {
	if !g.tracker.CompressionLogEnabled() {
		return
	}

	for _, tc := range pipeCtx.ToolOutputCompressions {
		// Determine status from MappingStatus
		status := tc.MappingStatus
		if status == "" {
			if tc.CacheHit {
				status = "cache_hit"
			} else if tc.CompressedBytes < tc.OriginalBytes {
				status = "compressed"
			} else {
				status = "passthrough"
			}
		}

		g.tracker.LogCompressionComparison(monitoring.CompressionComparison{
			RequestID:         requestID,
			PipeType:          pipeType,
			ToolName:          tc.ToolName,
			ShadowID:          tc.ShadowID,
			OriginalBytes:     tc.OriginalBytes,
			CompressedBytes:   tc.CompressedBytes,
			CompressionRatio:  float64(tc.CompressedBytes) / float64(max(tc.OriginalBytes, 1)),
			OriginalContent:   tc.OriginalContent,
			CompressedContent: tc.CompressedContent,
			CacheHit:          tc.CacheHit,
			Status:            status,
			MinThreshold:      tc.MinThreshold,
			MaxThreshold:      tc.MaxThreshold,
		})
	}

	if len(pipeCtx.ToolOutputCompressions) == 0 {
		g.tracker.LogCompressionComparison(monitoring.CompressionComparison{
			RequestID:         requestID,
			PipeType:          pipeType,
			OriginalBytes:     len(originalBody),
			CompressedBytes:   len(compressedBody),
			CompressionRatio:  float64(len(compressedBody)) / float64(max(len(originalBody), 1)),
			OriginalContent:   string(originalBody),
			CompressedContent: string(compressedBody),
			Status:            "passthrough",
		})
	}
}

// =============================================================================
// PREEMPTIVE SUMMARIZATION HELPERS
// =============================================================================

// mergeCompactedWithOriginal merges compacted messages with original request fields.
// Preserves model, system, tools, and other fields from original.
func mergeCompactedWithOriginal(compactedMessages []byte, originalBody []byte) ([]byte, error) {
	var original map[string]interface{}
	if err := json.Unmarshal(originalBody, &original); err != nil {
		return nil, err
	}

	var compacted map[string]interface{}
	if err := json.Unmarshal(compactedMessages, &compacted); err != nil {
		return nil, err
	}

	// Replace messages with compacted version
	original["messages"] = compacted["messages"]

	return json.Marshal(original)
}

// addPreemptiveHeaders adds preemptive summarization headers to the response.
func addPreemptiveHeaders(w http.ResponseWriter, headers map[string]string) {
	if headers == nil {
		return
	}
	for k, v := range headers {
		w.Header().Set(k, v)
	}
}
