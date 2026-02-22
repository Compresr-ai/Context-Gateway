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
	"github.com/compresr/context-gateway/internal/costcontrol"
	"github.com/compresr/context-gateway/internal/monitoring"
	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
	"github.com/compresr/context-gateway/internal/preemptive"
	"github.com/compresr/context-gateway/internal/utils"
)

type forwardAuthMeta struct {
	InitialMode   string
	EffectiveMode string
	FallbackUsed  bool
}

func mergeForwardAuthMeta(dst *forwardAuthMeta, src forwardAuthMeta) {
	if dst == nil {
		return
	}
	if src.InitialMode != "" {
		dst.InitialMode = src.InitialMode
	}
	if src.EffectiveMode != "" {
		dst.EffectiveMode = src.EffectiveMode
	}
	if src.FallbackUsed {
		dst.FallbackUsed = true
	}
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
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
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
		_ = g.store.Delete("_health_")
	}

	w.Header().Set("Content-Type", "application/json")
	if health["status"] != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(health)
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
	_ = json.NewEncoder(w).Encode(map[string]string{"id": req.ID, "content": data})
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
		resp, _, err := g.forwardPassthrough(r.Context(), r, body)
		if err != nil {
			log.Debug().Err(err).Str("path", r.URL.Path).Msg("passthrough failed")
			g.writeError(w, "upstream request failed", http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()

		responseBody, _ := io.ReadAll(resp.Body)
		copyHeaders(w, resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(responseBody)
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

	// Initialize tool session for hybrid tool discovery
	// Use canonical session ID from preemptive package (hash of first user message)
	if g.toolSessions != nil && g.config.Pipes.ToolDiscovery.Enabled {
		sessionID := preemptive.ComputeSessionID(body)
		if sessionID != "" {
			pipeCtx.ToolSessionID = sessionID
			// Load expanded tools from session (tools found via previous searches)
			pipeCtx.ExpandedTools = g.toolSessions.GetExpanded(sessionID)
		}
	}

	// Capture auth headers from incoming request for compression pipe (Max/Pro OAuth users)
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		pipeCtx.CapturedBearerToken = strings.TrimPrefix(auth, "Bearer ")
	}
	if beta := r.Header.Get("anthropic-beta"); beta != "" {
		pipeCtx.CapturedBetaHeader = beta
	}

	// Extract model for preemptive summarization
	model := adapter.ExtractModel(body)
	pipeCtx.Model = model

	// Cost control: budget check (before forwarding)
	if g.costTracker != nil {
		costSessionID := preemptive.ComputeSessionID(body)
		if costSessionID == "" {
			costSessionID = "default"
		}
		pipeCtx.CostSessionID = costSessionID
		budget := g.costTracker.CheckBudget(costSessionID)
		if !budget.Allowed {
			g.returnBudgetExceededResponse(w, adapter.Name(), budget)
			return
		}
	}

	// Process preemptive summarization (before compression pipeline)
	// This may modify the body if compaction is requested and ready
	// For SDK compaction with precomputed summary, may return synthetic response
	var preemptiveHeaders map[string]string
	var isCompaction bool
	var syntheticResponse []byte
	if g.preemptive != nil {
		// Capture auth token for summarizer (allows Max/Pro users without explicit API key)
		if auth := r.Header.Get("x-api-key"); auth != "" {
			log.Debug().Str("auth_type", "x-api-key").Str("auth", utils.MaskKey(auth)).Msg("Captured auth for summarizer")
			g.preemptive.SetAuthToken(auth, true) // from x-api-key header
		} else if auth := r.Header.Get("Authorization"); auth != "" {
			log.Debug().Str("auth_type", "Authorization").Str("auth", utils.MaskKey(auth)).Msg("Captured auth for summarizer")
			g.preemptive.SetAuthToken(strings.TrimPrefix(auth, "Bearer "), false) // from Authorization header
		}
		// Capture upstream endpoint URL for summarizer (same logic as forwardPassthrough)
		// Priority: X-Target-URL header > autoDetect
		xTargetURL := r.Header.Get(HeaderTargetURL)
		targetURL := xTargetURL
		if targetURL == "" {
			targetURL = g.autoDetectTargetURL(r)
		}
		if targetURL != "" {
			log.Info().
				Str("X-Target-URL_header", xTargetURL).
				Str("auto_detected", g.autoDetectTargetURL(r)).
				Str("final_endpoint", targetURL).
				Msg("Captured endpoint for summarizer")
			g.preemptive.SetEndpoint(targetURL)
		}

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
			_, _ = w.Write(syntheticResponse)

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

	// Store deferred tools in session for hybrid search fallback
	if g.toolSessions != nil && pipeCtx.ToolSessionID != "" && len(pipeCtx.DeferredTools) > 0 {
		g.toolSessions.StoreDeferred(pipeCtx.ToolSessionID, pipeCtx.DeferredTools)
	}

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

// handleNonStreaming handles non-streaming requests with phantom tool loop support.
// Phantom tools (expand_context, gateway_search_tools) are handled internally.
func (g *Gateway) handleNonStreaming(w http.ResponseWriter, r *http.Request, forwardBody []byte,
	pipeCtx *PipelineContext, requestID string, startTime time.Time, adapter adapters.Adapter,
	pipeType PipeType, pipeStrategy string, originalTokens int, compressionUsed bool,
	compressLatency time.Duration, originalBody []byte, expandEnabled bool) {

	providerName := adapter.Name()
	provider := adapter.Provider()
	authMeta := forwardAuthMeta{}

	forwardFunc := func(ctx context.Context, body []byte) (*http.Response, error) {
		resp, meta, err := g.forwardPassthrough(ctx, r, body)
		if err == nil {
			mergeForwardAuthMeta(&authMeta, meta)
		}
		return resp, err
	}

	// Build request-scoped phantom handlers to avoid cross-request state leakage.
	searchFallbackEnabled := g.config.Pipes.ToolDiscovery.Enabled &&
		g.config.Pipes.ToolDiscovery.Strategy == config.StrategyAPI
	var requestPhantomLoop *PhantomLoop
	var searchHandler *SearchToolHandler
	if expandEnabled || searchFallbackEnabled {
		var handlers []PhantomToolHandler

		if searchFallbackEnabled {
			searchToolName := g.config.Pipes.ToolDiscovery.SearchToolName
			if searchToolName == "" {
				searchToolName = "gateway_search_tools"
			}
			maxSearchResults := g.config.Pipes.ToolDiscovery.MaxSearchResults
			if maxSearchResults <= 0 {
				maxSearchResults = 5
			}
			apiEndpoint := g.config.Pipes.ToolDiscovery.API.Endpoint
			if g.config.Pipes.ToolDiscovery.Strategy == config.StrategyAPI && apiEndpoint == "" {
				base := strings.TrimRight(g.config.URLs.Compresr, "/")
				if base != "" {
					// Default API discovery endpoint used by strategy=api.
					apiEndpoint = base + "/v1/tool-discovery/search"
				}
			}
			searchHandler = NewSearchToolHandler(searchToolName, maxSearchResults, g.toolSessions, SearchToolHandlerOptions{
				Strategy:    g.config.Pipes.ToolDiscovery.Strategy,
				APIEndpoint: apiEndpoint,
				APIKey:      g.config.Pipes.ToolDiscovery.API.APISecret,
				APITimeout:  g.config.Pipes.ToolDiscovery.API.Timeout,
				AlwaysKeep:  g.config.Pipes.ToolDiscovery.AlwaysKeep,
			})

			// Combine deferred tools from session (previous requests) AND current request.
			// This ensures tools filtered in this request are searchable in the same turn.
			if pipeCtx.ToolSessionID != "" {
				var combinedDeferred []adapters.ExtractedContent
				if session := g.toolSessions.Get(pipeCtx.ToolSessionID); session != nil {
					combinedDeferred = append(combinedDeferred, session.DeferredTools...)
				}
				if len(pipeCtx.DeferredTools) > 0 {
					combinedDeferred = append(combinedDeferred, pipeCtx.DeferredTools...)
				}
				searchHandler.SetRequestContext(pipeCtx.ToolSessionID, combinedDeferred)
			}
			handlers = append(handlers, searchHandler)
		}

		if expandEnabled {
			handlers = append(handlers, NewExpandContextHandler(g.store))
		}

		if len(handlers) > 0 {
			requestPhantomLoop = NewPhantomLoop(handlers...)
		}
	}

	// Run phantom tool loop (handles both expand_context and gateway_search_tools)
	var result *PhantomLoopResult
	var err error
	if requestPhantomLoop != nil {
		result, err = requestPhantomLoop.Run(r.Context(), forwardFunc, forwardBody, provider)
	} else {
		// Fallback: simple forward without phantom tool handling
		resp, fwdErr := forwardFunc(r.Context(), forwardBody)
		if fwdErr != nil {
			err = fwdErr
		} else {
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			result = &PhantomLoopResult{
				ResponseBody: respBody,
				Response:     resp,
			}
		}
	}

	if err != nil || result == nil || result.Response == nil {
		g.logToolDiscoveryAPIFallbacks(requestID, searchHandler)
		var forwardLatency time.Duration
		if result != nil {
			forwardLatency = result.ForwardLatency
		}
		g.recordRequestTelemetry(telemetryParams{
			requestID: requestID, startTime: startTime, method: r.Method, path: r.URL.Path,
			clientIP: r.RemoteAddr, requestBodySize: len(originalBody), responseBodySize: 0,
			provider: providerName, pipeType: pipeType, pipeStrategy: pipeStrategy, originalTokens: originalTokens,
			compressionUsed: compressionUsed, statusCode: 502, errorMsg: "phantom loop failed",
			compressLatency: compressLatency, forwardLatency: forwardLatency, pipeCtx: pipeCtx,
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody,
			authModeInitial: authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
		})
		g.writeError(w, "upstream request failed", http.StatusBadGateway)
		return
	}

	responseBody := result.ResponseBody
	g.logToolDiscoveryAPIFallbacks(requestID, searchHandler)

	// Update pipeCtx with loop usage for logging
	pipeCtx.ExpandLoopCount = result.LoopCount

	// Log phantom tool usage
	if result.LoopCount > 0 {
		log.Info().
			Int("loops", result.LoopCount).
			Interface("handled", result.HandledCalls).
			Msg("phantom_loop: completed")
	}

	// Record telemetry with usage extraction
	g.recordRequestTelemetry(telemetryParams{
		requestID: requestID, startTime: startTime, method: r.Method, path: r.URL.Path,
		clientIP: r.RemoteAddr, requestBodySize: len(originalBody), responseBodySize: len(responseBody),
		provider: providerName, pipeType: pipeType, pipeStrategy: pipeStrategy, originalTokens: originalTokens,
		compressionUsed: compressionUsed, statusCode: result.Response.StatusCode,
		compressLatency: compressLatency, forwardLatency: result.ForwardLatency,
		expandLoops: result.LoopCount, pipeCtx: pipeCtx,
		adapter: adapter, requestBody: originalBody, responseBody: result.ResponseBody,
		forwardBody:     forwardBody,
		authModeInitial: authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
	})

	// Log provider errors and compression details
	if result.Response.StatusCode >= 400 {
		g.alerts.FlagProviderError(requestID, providerName, result.Response.StatusCode,
			string(responseBody[:min(500, len(responseBody))]))
	}
	if compressionUsed {
		g.logCompressionDetails(pipeCtx, requestID, string(pipeType), originalBody, forwardBody)
	}

	// Write response
	copyHeaders(w, result.Response.Header)
	addPreemptiveHeaders(w, pipeCtx.PreemptiveHeaders)
	w.WriteHeader(result.Response.StatusCode)
	_, _ = w.Write(responseBody)
}

func (g *Gateway) logToolDiscoveryAPIFallbacks(requestID string, searchHandler *SearchToolHandler) {
	if searchHandler == nil || !g.tracker.ToolDiscoveryLogEnabled() {
		return
	}

	events := searchHandler.ConsumeAPIFallbackEvents()
	for _, evt := range events {
		status := "api_fallback"
		if evt.Reason != "" {
			status = status + "_" + evt.Reason
		}

		g.tracker.LogToolDiscoveryComparison(monitoring.CompressionComparison{
			RequestID:         requestID,
			PipeType:          string(PipeToolDiscovery),
			ToolName:          searchHandler.Name(),
			OriginalBytes:     evt.DeferredCount,
			CompressedBytes:   evt.ReturnedCount,
			CompressionRatio:  float64(max(evt.ReturnedCount, 1)) / float64(max(evt.DeferredCount, 1)),
			OriginalContent:   evt.Query,
			CompressedContent: truncateLogValue(evt.Detail, 500),
			Status:            status,
		})
	}
}

func truncateLogValue(value string, maxLen int) string {
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen] + "..."
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
	resp, authMeta, err := g.forwardPassthrough(r.Context(), r, forwardBody)
	if err != nil {
		g.recordRequestTelemetry(telemetryParams{
			requestID: requestID, startTime: startTime, method: r.Method, path: r.URL.Path,
			clientIP: r.RemoteAddr, requestBodySize: len(originalBody), responseBodySize: 0,
			provider: provider, pipeType: pipeType, pipeStrategy: pipeStrategy + "_streaming", originalTokens: originalTokens,
			compressionUsed: compressionUsed, statusCode: 502, errorMsg: err.Error(),
			compressLatency: compressLatency, forwardLatency: time.Since(forwardStart), pipeCtx: pipeCtx,
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody,
			authModeInitial: authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
		})
		g.writeError(w, "upstream request failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	// If expand not enabled, stream directly
	if !expandEnabled || !compressionUsed || len(pipeCtx.ShadowRefs) == 0 {
		defer func() { _ = resp.Body.Close() }()
		copyHeaders(w, resp.Header)
		addPreemptiveHeaders(w, pipeCtx.PreemptiveHeaders)
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(resp.StatusCode)
		sseUsage := g.streamResponse(w, resp.Body)

		g.recordRequestTelemetry(telemetryParams{
			requestID: requestID, startTime: startTime, method: r.Method, path: r.URL.Path,
			clientIP: r.RemoteAddr, requestBodySize: len(originalBody), responseBodySize: 0,
			provider: provider, pipeType: pipeType, pipeStrategy: pipeStrategy + "_streaming", originalTokens: originalTokens,
			compressionUsed: compressionUsed, statusCode: resp.StatusCode,
			compressLatency: compressLatency, forwardLatency: time.Since(forwardStart), pipeCtx: pipeCtx,
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody, streamUsage: &sseUsage,
			authModeInitial: authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
		})
		if compressionUsed {
			g.logCompressionDetails(pipeCtx, requestID, string(pipeType), originalBody, forwardBody)
		}
		return
	}

	// expand_context enabled: buffer response to detect expand calls
	streamBuffer := tooloutput.NewStreamBuffer()
	usageParser := newSSEUsageParser()
	var bufferedChunks [][]byte

	// Read and buffer the entire stream
	buf := make([]byte, DefaultBufferSize)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			bufferedChunks = append(bufferedChunks, chunk)
			usageParser.Feed(chunk)

			// Process for expand_context detection
			_, _ = streamBuffer.ProcessChunk(chunk)
		}
		if readErr != nil {
			break
		}
	}
	_ = resp.Body.Close()

	// Extract usage from buffered SSE chunks
	bufferedUsage := usageParser.Usage()

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
		retryResp, retryMeta, err := g.forwardPassthrough(r.Context(), r, rewrittenBody)
		if err != nil {
			log.Error().Err(err).Msg("streaming: failed to re-send after expansion")
			g.flushBufferedResponse(w, resp.Header, pipeCtx.PreemptiveHeaders, bufferedChunks)
			return
		}
		mergeForwardAuthMeta(&authMeta, retryMeta)
		defer func() { _ = retryResp.Body.Close() }()

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
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody, streamUsage: &bufferedUsage,
			authModeInitial: authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
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
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody, streamUsage: &bufferedUsage,
			authModeInitial: authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
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
		_, _ = w.Write(chunk)
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
		_, _ = io.Copy(w, reader)
		return
	}

	streamBuffer := tooloutput.NewStreamBuffer()
	buf := make([]byte, DefaultBufferSize)

	for {
		n, err := reader.Read(buf)
		if n > 0 {
			// Filter expand_context from the stream
			filtered, _ := streamBuffer.ProcessChunk(buf[:n])
			if len(filtered) > 0 {
				_, _ = w.Write(filtered)
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
// Returns usage extracted from SSE events (Anthropic message_start/message_delta).
func (g *Gateway) streamResponse(w http.ResponseWriter, reader io.Reader) adapters.UsageInfo {
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Warn().Msg("streaming not supported, falling back to buffered")
		_, _ = io.Copy(w, reader)
		return adapters.UsageInfo{}
	}

	usageParser := newSSEUsageParser()

	buf := make([]byte, DefaultBufferSize)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			usageParser.Feed(chunk)

			if _, writeErr := w.Write(chunk); writeErr != nil {
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
	return usageParser.Usage()
}

type anthropicSSEUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type anthropicSSEPayload struct {
	Usage   anthropicSSEUsage `json:"usage"`
	Message struct {
		Usage anthropicSSEUsage `json:"usage"`
	} `json:"message"`
}

// sseUsageParser incrementally parses Anthropic SSE events and extracts usage.
// It only reads structured "data: {json}" events to avoid false positives from
// arbitrary text that might contain token-like key names.
type sseUsageParser struct {
	buffer []byte
	usage  adapters.UsageInfo
}

func newSSEUsageParser() *sseUsageParser {
	return &sseUsageParser{
		buffer: make([]byte, 0, DefaultBufferSize),
	}
}

func (p *sseUsageParser) Feed(chunk []byte) {
	p.buffer = append(p.buffer, chunk...)
	p.parse(false)
}

func (p *sseUsageParser) Usage() adapters.UsageInfo {
	p.parse(true)
	return p.usage
}

func (p *sseUsageParser) parse(flush bool) {
	for {
		event, rest, ok := nextSSEEvent(p.buffer, flush)
		if !ok {
			return
		}
		p.buffer = rest
		p.parseEvent(event)
	}
}

func nextSSEEvent(buf []byte, flush bool) ([]byte, []byte, bool) {
	if idx := bytes.Index(buf, []byte("\r\n\r\n")); idx >= 0 {
		return buf[:idx], buf[idx+4:], true
	}
	if idx := bytes.Index(buf, []byte("\n\n")); idx >= 0 {
		return buf[:idx], buf[idx+2:], true
	}
	if flush {
		trimmed := bytes.TrimSpace(buf)
		if len(trimmed) > 0 {
			return trimmed, nil, true
		}
	}
	return nil, nil, false
}

func (p *sseUsageParser) parseEvent(event []byte) {
	lines := bytes.Split(event, []byte("\n"))
	dataLines := make([][]byte, 0, 2)

	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}

		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		dataLines = append(dataLines, payload)
	}

	if len(dataLines) == 0 {
		return
	}

	data := bytes.Join(dataLines, []byte("\n"))
	var payload anthropicSSEPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return
	}

	p.applyUsage(payload.Message.Usage)
	p.applyUsage(payload.Usage)
}

func (p *sseUsageParser) applyUsage(u anthropicSSEUsage) {
	if u.InputTokens > 0 {
		p.usage.InputTokens = u.InputTokens
	}
	if u.OutputTokens > p.usage.OutputTokens {
		p.usage.OutputTokens = u.OutputTokens
	}
	if u.CacheCreationInputTokens > 0 {
		p.usage.CacheCreationInputTokens = u.CacheCreationInputTokens
	}
	if u.CacheReadInputTokens > 0 {
		p.usage.CacheReadInputTokens = u.CacheReadInputTokens
	}

	p.usage.TotalTokens = p.usage.InputTokens + p.usage.OutputTokens +
		p.usage.CacheCreationInputTokens + p.usage.CacheReadInputTokens
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
	forwardBody := body

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
func (g *Gateway) forwardPassthrough(ctx context.Context, r *http.Request, body []byte) (*http.Response, forwardAuthMeta, error) {
	authMeta := forwardAuthMeta{InitialMode: "unknown", EffectiveMode: "unknown"}
	targetURL := r.Header.Get(HeaderTargetURL)
	if targetURL != "" {
		// X-Target-URL provided - append request path if not already included
		if !strings.HasSuffix(targetURL, r.URL.Path) {
			targetURL = strings.TrimSuffix(targetURL, "/") + r.URL.Path
		}
	} else {
		targetURL = g.autoDetectTargetURL(r)
		if targetURL == "" {
			return nil, authMeta, fmt.Errorf("missing %s header", HeaderTargetURL)
		}
	}

	// Detect if this is a Bedrock request
	isBedrock := g.isBedrockRequest(r.URL.Path)

	// Sanitize model name (strip provider prefix like "anthropic/", "openai/")
	// Skip for Bedrock since model ID format is different (e.g., "anthropic.claude-3-5-sonnet")
	if !isBedrock {
		body = sanitizeModelName(body)
	}

	log.Info().
		Str("targetURL", targetURL).
		Bool("bedrock", isBedrock).
		Str("x-api-key", utils.MaskKey(r.Header.Get("x-api-key"))).
		Str("authorization", utils.MaskKey(r.Header.Get("Authorization"))).
		Msg("forwarding request")

	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		return nil, authMeta, fmt.Errorf("invalid target URL: %w", err)
	}
	if !g.isAllowedHost(parsedURL.Host) {
		return nil, authMeta, fmt.Errorf("target host not allowed: %s", parsedURL.Host)
	}

	// Auth fallback context: provider-scoped subscription -> API key.
	provider, _ := adapters.IdentifyAndGetAdapter(g.registry, r.URL.Path, r.Header)
	// In this forwarding path, anthropic-version is definitive.
	if r.Header.Get("anthropic-version") != "" {
		provider = adapters.ProviderAnthropic
	} else if provider == adapters.ProviderUnknown && strings.HasPrefix(strings.TrimSpace(r.Header.Get("x-api-key")), "sk-ant-") {
		provider = adapters.ProviderAnthropic
	}
	initialMode, isSubscriptionAuth := detectAuthMode(provider, r.Header)
	authMeta.InitialMode = initialMode

	fallbackAPIKey := resolveFallbackAPIKey(provider, g.config.Providers)
	canFallbackToAPIKey := isSubscriptionAuth && fallbackAPIKey != ""
	sessionID := preemptive.ComputeSessionID(body)
	useAPIKeyForSession := canFallbackToAPIKey && g.authMode != nil && g.authMode.ShouldUseAPIKeyMode(sessionID)

	sendUpstream := func(useAPIKeyMode bool) (*http.Response, []byte, error) {
		httpReq, reqErr := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewReader(body))
		if reqErr != nil {
			return nil, nil, reqErr
		}

		if isBedrock && g.bedrockSigner != nil && g.bedrockSigner.IsConfigured() {
			// Bedrock: use AWS SigV4 signing instead of forwarding API key headers
			httpReq.Header.Set("Content-Type", "application/json")
			if signErr := g.bedrockSigner.SignRequest(ctx, httpReq, body); signErr != nil {
				return nil, nil, fmt.Errorf("failed to sign Bedrock request: %w", signErr)
			}
		} else {
			// Non-Bedrock: forward relevant headers
			for _, h := range []string{
				"Content-Type", "Authorization", "x-api-key", "x-goog-api-key",
				"api-key", "anthropic-version", "anthropic-beta",
			} {
				if v := r.Header.Get(h); v != "" {
					httpReq.Header.Set(h, v)
				}
			}

			// Sticky fallback mode: override subscription auth with API key.
			if useAPIKeyMode {
				httpReq.Header.Del("Authorization")
				httpReq.Header.Set("x-api-key", fallbackAPIKey)
			}
		}
		if useAPIKeyMode {
			authMeta.EffectiveMode = "api_key"
		} else {
			authMeta.EffectiveMode = authMeta.InitialMode
		}
		resp, doErr := g.httpClient.Do(httpReq)
		if doErr != nil {
			log.Error().Err(doErr).Str("targetURL", targetURL).Msg("upstream request failed")
			return nil, nil, doErr
		}

		// Read body for upstream errors so we can inspect and preserve it.
		if resp.StatusCode >= 400 {
			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			log.Error().
				Int("status", resp.StatusCode).
				Str("targetURL", targetURL).
				Bool("api_key_mode", useAPIKeyMode).
				Str("response", string(bodyBytes[:min(500, len(bodyBytes))])).
				Msg("upstream error response")
			return resp, bodyBytes, nil
		}
		return resp, nil, nil
	}

	// First attempt: sticky mode may already force API key for this session.
	resp, respBody, err := sendUpstream(useAPIKeyForSession)
	if err != nil {
		return nil, authMeta, err
	}

	// One-shot fallback: if subscription appears exhausted, retry with API key and stick to it.
	if canFallbackToAPIKey && !useAPIKeyForSession && resp != nil && isLikelySubscriptionExhausted(provider, resp.StatusCode, respBody) {
		if g.authMode != nil {
			g.authMode.MarkAPIKeyMode(sessionID)
		}
		authMeta.FallbackUsed = true
		_ = resp.Body.Close()
		log.Info().
			Str("session_id", sessionID).
			Int("status", resp.StatusCode).
			Msg("auth_fallback: switching session to api-key mode")
		retryResp, _, retryErr := sendUpstream(true)
		return retryResp, authMeta, retryErr
	}

	return resp, authMeta, nil
}

// isBedrockRequest checks if the request path matches Bedrock URL patterns.
// Returns false if Bedrock support is not explicitly enabled in config.
func (g *Gateway) isBedrockRequest(path string) bool {
	if !g.config.Bedrock.Enabled {
		return false
	}
	return strings.Contains(path, "/model/") &&
		(strings.HasSuffix(path, "/invoke") ||
			strings.HasSuffix(path, "/invoke-with-response-stream") ||
			strings.HasSuffix(path, "/converse") ||
			strings.HasSuffix(path, "/converse-stream"))
}

// isStreamingRequest checks if the request has "stream": true.
func (g *Gateway) isStreamingRequest(body []byte) bool {
	if !bytes.Contains(body, []byte(`"stream"`)) {
		return false
	}
	var req struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)
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
	adapter           adapters.Adapter
	requestBody       []byte              // Original request from client
	responseBody      []byte              // Response from LLM
	streamUsage       *adapters.UsageInfo // Pre-extracted usage from SSE stream (streaming only)
	forwardBody       []byte              // Compressed request sent to LLM (for proxy interaction tracking)
	authModeInitial   string
	authModeEffective string
	authFallbackUsed  bool
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

		// For streaming, use pre-extracted SSE usage if body-based extraction returned nothing
		if usage.TotalTokens == 0 && params.streamUsage != nil && params.streamUsage.TotalTokens > 0 {
			usage = *params.streamUsage
		}
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
		AuthModeInitial:      params.authModeInitial,
		AuthModeEffective:    params.authModeEffective,
		AuthFallbackUsed:     params.authFallbackUsed,
		InputTokens:          usage.InputTokens,
		OutputTokens:         usage.OutputTokens,
		TotalTokens:          usage.TotalTokens,
	})

	// Record cost tracking (only when we have actual token counts from the API response).
	// Streaming responses have empty bodies so ExtractUsage returns zeros — skip rather
	// than estimate, since estimation ignores caching and overestimates by 10x+.
	if g.costTracker != nil && params.pipeCtx != nil && params.pipeCtx.CostSessionID != "" && usage.TotalTokens > 0 {
		g.costTracker.RecordUsage(params.pipeCtx.CostSessionID, model,
			usage.InputTokens, usage.OutputTokens,
			usage.CacheCreationInputTokens, usage.CacheReadInputTokens)
	}

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
	if pipeType == string(PipeToolDiscovery) {
		if !g.tracker.ToolDiscoveryLogEnabled() {
			return
		}
		status := "passthrough"
		if !bytes.Equal(originalBody, compressedBody) {
			status = "filtered"
		}
		allTools := extractToolNamesFromRequest(originalBody)
		selectedTools := extractToolNamesFromRequest(compressedBody)
		g.tracker.LogToolDiscoveryComparison(monitoring.CompressionComparison{
			RequestID:       requestID,
			PipeType:        pipeType,
			OriginalBytes:   len(originalBody),
			CompressedBytes: len(compressedBody),
			CompressionRatio: float64(len(compressedBody)) /
				float64(max(len(originalBody), 1)),
			AllTools:      allTools,
			SelectedTools: selectedTools,
			Status:        status,
		})
		return
	}

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

func extractToolNamesFromRequest(body []byte) []string {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}

	tools, ok := req["tools"].([]any)
	if !ok || len(tools) == 0 {
		return nil
	}

	names := make([]string, 0, len(tools))
	seen := make(map[string]bool, len(tools))
	for _, toolAny := range tools {
		tool, ok := toolAny.(map[string]any)
		if !ok {
			continue
		}

		name, _ := tool["name"].(string)
		if name == "" {
			if fn, ok := tool["function"].(map[string]any); ok {
				name, _ = fn["name"].(string)
			}
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}

	return names
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

// =============================================================================
// COST CONTROL
// =============================================================================

// handleCostDashboard serves the cost dashboard.
func (g *Gateway) handleCostDashboard(w http.ResponseWriter, r *http.Request) {
	if g.costTracker != nil {
		g.costTracker.HandleDashboard(w, r)
	} else {
		http.Error(w, "cost tracking not initialized", http.StatusInternalServerError)
	}
}

// returnBudgetExceededResponse writes a synthetic response when budget is exceeded.
// Returns HTTP 200 so agent clients display the message rather than retry.
func (g *Gateway) returnBudgetExceededResponse(w http.ResponseWriter, provider string, budget costcontrol.BudgetCheckResult) {
	var msg string
	if budget.GlobalCap > 0 && budget.GlobalCost >= budget.GlobalCap {
		msg = fmt.Sprintf("Global budget exceeded. Total spend: $%.4f, limit: $%.2f. "+
			"Increase the global cap in your gateway config (cost_control.global_cap).",
			budget.GlobalCost, budget.GlobalCap)
	} else {
		msg = fmt.Sprintf("Session budget exceeded. Current spend: $%.4f, limit: $%.2f. "+
			"Please start a new session or increase the budget cap in your gateway config (cost_control.session_cap).",
			budget.CurrentCost, budget.Cap)
	}

	var resp []byte
	if provider == "anthropic" {
		resp, _ = json.Marshal(map[string]interface{}{
			"id":            "msg_budget_exceeded",
			"type":          "message",
			"role":          "assistant",
			"model":         "budget-control",
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"content":       []map[string]interface{}{{"type": "text", "text": msg}},
			"usage":         map[string]interface{}{"input_tokens": 0, "output_tokens": 0},
		})
	} else {
		resp, _ = json.Marshal(map[string]interface{}{
			"id":      "budget_exceeded",
			"object":  "chat.completion",
			"model":   "budget-control",
			"choices": []map[string]interface{}{{"index": 0, "message": map[string]interface{}{"role": "assistant", "content": msg}, "finish_reason": "stop"}},
			"usage":   map[string]interface{}{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Budget-Exceeded", "true")
	w.Header().Set("X-Session-Cost", fmt.Sprintf("%.4f", budget.CurrentCost))
	w.Header().Set("X-Session-Cap", fmt.Sprintf("%.4f", budget.Cap))
	w.Header().Set("X-Global-Cost", fmt.Sprintf("%.4f", budget.GlobalCost))
	w.Header().Set("X-Global-Cap", fmt.Sprintf("%.4f", budget.GlobalCap))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp)
}
