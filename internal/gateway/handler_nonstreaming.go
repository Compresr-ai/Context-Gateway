// Non-streaming request handling with phantom tool loop support.
package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/monitoring"
)

// handleNonStreaming handles non-streaming requests with phantom tool loop support.
// Phantom tools (expand_context, gateway_search_tools) are handled internally.
func (g *Gateway) handleNonStreaming(w http.ResponseWriter, r *http.Request, forwardBody []byte,
	pipeCtx *PipelineContext, requestID string, startTime time.Time, adapter adapters.Adapter,
	pipeType PipeType, pipeStrategy string, originalBodySize int, compressionUsed bool,
	compressLatency time.Duration, originalBody []byte, expandEnabled bool, initialUsage *adapters.UsageInfo,
	compressedBodySize int) {

	providerName := adapter.Name()
	authMeta := forwardAuthMeta{}

	forwardFunc := func(ctx context.Context, body []byte) (*http.Response, error) {
		resp, meta, err := g.forwardPassthrough(ctx, r, body)
		if err == nil {
			mergeForwardAuthMeta(&authMeta, meta)
		}
		return resp, err
	}

	// Build request-scoped phantom handlers to avoid cross-request state leakage.
	// Always enabled — phantom loop handles gateway_search_tools unconditionally (MCP-server pattern).
	// InjectAll in handler.go already injects gateway_search_tools into every request; the loop must always handle it.
	searchFallbackEnabled := true // Always enabled — phantom loop handles gateway_search_tools unconditionally (MCP-server pattern)
	var requestPhantomLoop *PhantomLoop
	var searchHandler *SearchToolHandler
	var combinedDeferred []adapters.ExtractedContent
	if expandEnabled || searchFallbackEnabled {
		var handlers []PhantomToolHandler

		if searchFallbackEnabled {
			searchToolName := g.searchToolName()
			maxSearchResults := g.cfg().Pipes.ToolDiscovery.MaxSearchResults
			if maxSearchResults <= 0 {
				maxSearchResults = 5
			}

			// Configure SearchToolHandler with Compresr API endpoint for search
			opts := SearchToolHandlerOptions{
				Strategy:   g.cfg().Pipes.ToolDiscovery.Strategy,
				AlwaysKeep: g.cfg().Pipes.ToolDiscovery.AlwaysKeep,
			}

			// Configure Stage 1: Tool Discovery API endpoint
			apiEndpoint := g.cfg().Pipes.ToolDiscovery.Compresr.Endpoint
			if apiEndpoint == "" && g.cfg().URLs.Compresr != "" {
				// No endpoint configured, use default path with base URL
				apiEndpoint = strings.TrimRight(g.cfg().URLs.Compresr, "/") + "/api/compress/tool-discovery/"
			} else if strings.HasPrefix(apiEndpoint, "/") && g.cfg().URLs.Compresr != "" {
				// Relative path configured — join with base URL
				apiEndpoint = strings.TrimRight(g.cfg().URLs.Compresr, "/") + apiEndpoint
			}
			opts.APIEndpoint = apiEndpoint
			opts.ProviderAuth = g.cfg().Pipes.ToolDiscovery.Compresr.APIKey
			opts.APIModel = g.cfg().Pipes.ToolDiscovery.Compresr.Model
			opts.APITimeout = g.cfg().Pipes.ToolDiscovery.Compresr.Timeout

			// Configure Stage 2: Schema Compression (per-tool compression)
			schemaCfg := g.cfg().Pipes.ToolDiscovery.SchemaCompression
			schemaEndpoint := schemaCfg.Endpoint
			if schemaEndpoint == "" && g.cfg().URLs.Compresr != "" {
				schemaEndpoint = strings.TrimRight(g.cfg().URLs.Compresr, "/") + "/api/compress/tool-output/"
			} else if strings.HasPrefix(schemaEndpoint, "/") && g.cfg().URLs.Compresr != "" {
				schemaEndpoint = strings.TrimRight(g.cfg().URLs.Compresr, "/") + schemaEndpoint
			}
			schemaAPIKey := schemaCfg.APIKey
			if schemaAPIKey == "" {
				schemaAPIKey = g.cfg().Pipes.ToolDiscovery.Compresr.APIKey // Fall back to Stage 1 key
			}
			opts.SchemaCompression = SchemaCompressionOpts{
				Enabled:        schemaCfg.Enabled,
				Endpoint:       schemaEndpoint,
				APIKey:         schemaAPIKey,
				Model:          schemaCfg.Model,
				Timeout:        schemaCfg.Timeout,
				TokenThreshold: schemaCfg.TokenThreshold,
				Parallel:       schemaCfg.Parallel,
				MaxConcurrent:  schemaCfg.MaxConcurrent,
			}
			if opts.SchemaCompression.Enabled && g.compresrClient != nil {
				opts.SchemaCompression.CompresrClient = g.compresrClient
			}

			searchHandler = NewSearchToolHandler(searchToolName, maxSearchResults, g.toolSessions, opts)
			if g.searchLog != nil {
				searchHandler.WithSearchLog(g.searchLog, requestID, pipeCtx.CostSessionID)
			}
			if g.tracker != nil {
				searchHandler.WithTracker(g.tracker)
			}
			// Set isMainAgent for tool search logging
			searchHandler.WithIsMainAgent(pipeCtx.Classification.IsMainAgent)

			// Combine deferred tools from session (previous requests) AND current request.
			// This ensures tools filtered in this request are searchable in the same turn.
			// Current-request tools take precedence; session tools fill in the rest (dedup by name).
			if pipeCtx.ToolSessionID != "" {
				seen := make(map[string]bool)
				// Current request tools first (latest definition wins).
				for _, t := range pipeCtx.DeferredTools {
					if !seen[t.ToolName] {
						seen[t.ToolName] = true
						combinedDeferred = append(combinedDeferred, t)
					}
				}
				// Session tools second (accumulated from previous requests, skip duplicates).
				if session := g.toolSessions.Get(pipeCtx.ToolSessionID); session != nil {
					for _, t := range session.DeferredTools {
						if !seen[t.ToolName] {
							seen[t.ToolName] = true
							combinedDeferred = append(combinedDeferred, t)
						}
					}
				}
				searchHandler.SetRequestContext(r.Context(), pipeCtx.ToolSessionID, combinedDeferred, pipeCtx.CapturedAuth)
			}
			handlers = append(handlers, searchHandler)
		}

		if expandEnabled {
			ecHandler := NewExpandContextHandler(g.store)
			if g.expandLog != nil {
				ecHandler.WithExpandLog(g.expandLog, requestID, pipeCtx.CostSessionID)
			}
			ecHandler.WithExpandCallsLog(g.tracker.ExpandCallsLogger(), pipeCtx.ToolOutputCompressions)
			handlers = append(handlers, ecHandler)
		}

		// Intercept direct calls to deferred (stubbed) tools.
		// When the LLM bypasses gateway_search_tools and calls a deferred tool
		// directly using training knowledge, this handler intercepts it, injects
		// the full schema, and asks the LLM to retry with the schema now loaded.
		if len(combinedDeferred) > 0 {
			handlers = append(handlers, NewDeferredCallInterceptor(
				combinedDeferred,
				g.toolSessions,
				pipeCtx.ToolSessionID,
			))
		}

		if len(handlers) > 0 {
			requestPhantomLoop = NewPhantomLoop(handlers...)
		}
	}

	// Run phantom tool loop (handles both expand_context and gateway_search_tools)
	var result *PhantomLoopResult
	var err error
	if requestPhantomLoop != nil {
		result, err = requestPhantomLoop.Run(r.Context(), forwardFunc, forwardBody, adapter)
	} else {
		// Fallback: simple forward without phantom tool handling
		resp, fwdErr := forwardFunc(r.Context(), forwardBody)
		if fwdErr != nil {
			err = fwdErr
		} else {
			// defer close so future refactors adding early returns don't leak
			defer resp.Body.Close() //nolint:gocritic // not inside a loop
			// surface read errors so caller gets 502, not a silent partial response
			respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize))
			if readErr != nil {
				err = fmt.Errorf("read upstream response: %w", readErr)
			} else {
				result = &PhantomLoopResult{
					ResponseBody: respBody,
					Response:     resp,
				}
			}
		}
	}

	// Merge initial streaming usage (from search fallback path) into accumulated usage.
	// The API bills for the initial streaming forward, so we must capture it.
	if initialUsage != nil && result != nil {
		result.AccumulatedUsage.InputTokens += initialUsage.InputTokens
		result.AccumulatedUsage.OutputTokens += initialUsage.OutputTokens
		result.AccumulatedUsage.CacheCreationInputTokens += initialUsage.CacheCreationInputTokens
		result.AccumulatedUsage.CacheReadInputTokens += initialUsage.CacheReadInputTokens
		result.AccumulatedUsage.TotalTokens += initialUsage.TotalTokens
	}

	if err != nil || result == nil || result.Response == nil {
		g.logToolDiscoveryAPIFallbacks(requestID, pipeCtx.CostSessionID, searchHandler, pipeCtx.Model, pipeCtx.ToolDiscoveryModel, pipeCtx.Classification.IsMainAgent)
		var forwardLatency time.Duration
		if result != nil {
			forwardLatency = result.ForwardLatency
		}
		g.recordRequestTelemetry(telemetryParams{
			requestID: requestID, startTime: startTime, method: r.Method, path: r.URL.Path,
			clientIP: r.RemoteAddr, requestBodySize: len(originalBody), responseBodySize: 0,
			provider: providerName, pipeType: pipeType, pipeStrategy: pipeStrategy, originalBodySize: originalBodySize,
			compressionUsed: compressionUsed, statusCode: 502, errorMsg: "phantom loop failed",
			compressLatency: compressLatency, forwardLatency: forwardLatency, pipeCtx: pipeCtx,
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody, compressedBodySize: compressedBodySize,
			authModeInitial: authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
			requestHeaders: r.Header, responseHeaders: nil, upstreamURL: "", fallbackReason: "",
		})
		g.writeError(w, "upstream request failed", http.StatusBadGateway)
		return
	}

	responseBody := result.ResponseBody
	g.logToolDiscoveryAPIFallbacks(requestID, pipeCtx.CostSessionID, searchHandler, pipeCtx.Model, pipeCtx.ToolDiscoveryModel, pipeCtx.Classification.IsMainAgent)

	// Update pipeCtx with loop usage for logging
	pipeCtx.ExpandLoopCount = result.LoopCount

	// Log phantom tool usage
	if result.LoopCount > 0 {
		log.Info().
			Int("loops", result.LoopCount).
			Interface("handled", result.HandledCalls).
			Msg("phantom_loop: completed")
	}

	// Query expand log for this request's expand_context stats and penalty counts
	var expandCallsFound, expandCallsNotFound, expandPenaltyTokens int
	if g.expandLog != nil {
		summary, contentTokens := g.expandLog.SummaryForRequest(requestID)
		expandCallsFound = summary.Found
		expandCallsNotFound = summary.NotFound
		expandPenaltyTokens = contentTokens
	}

	// Pass accumulated phantom loop usage when loop ran (LoopCount > 0) or when
	// initialUsage was provided (search fallback path), so telemetry captures
	// costs from ALL API calls, not just the final response.
	var phantomUsage *adapters.UsageInfo
	if result.AccumulatedUsage.TotalTokens > 0 &&
		(result.LoopCount > 0 || initialUsage != nil) {
		phantomUsage = &result.AccumulatedUsage
	}

	// Record telemetry with usage extraction
	g.recordRequestTelemetry(telemetryParams{
		requestID: requestID, startTime: startTime, method: r.Method, path: r.URL.Path,
		clientIP: r.RemoteAddr, requestBodySize: len(originalBody), responseBodySize: len(responseBody),
		provider: providerName, pipeType: pipeType, pipeStrategy: pipeStrategy, originalBodySize: originalBodySize,
		compressionUsed: compressionUsed, statusCode: result.Response.StatusCode,
		compressLatency: compressLatency, forwardLatency: result.ForwardLatency,
		expandLoops:      result.LoopCount,
		expandCallsFound: expandCallsFound, expandCallsNotFound: expandCallsNotFound,
		expandPenaltyTokens: expandPenaltyTokens,
		pipeCtx:             pipeCtx,
		adapter:             adapter, requestBody: originalBody, responseBody: result.ResponseBody,
		phantomLoopUsage:   phantomUsage,
		forwardBody:        forwardBody,
		compressedBodySize: compressedBodySize,
		authModeInitial:    authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
		requestHeaders: r.Header, responseHeaders: result.Response.Header, upstreamURL: func() string {
			if result.Response.Request != nil {
				return result.Response.Request.URL.String()
			}
			return ""
		}(), fallbackReason: "",
	})

	// Log provider errors and compression details
	if result.Response.StatusCode >= 400 {
		g.alerts.FlagProviderError(requestID, providerName, result.Response.StatusCode,
			string(responseBody[:min(500, len(responseBody))]))
	}
	// Log for each pipe that ran; always write session tool catalog regardless of pipes.
	toolOutputRan := len(pipeCtx.ToolOutputCompressions) > 0 || pipeCtx.OutputCompressed
	toolDiscoveryRan := pipeCtx.KeptToolCount > 0 || pipeCtx.ToolsFiltered || pipeCtx.ToolDiscoverySkipReason != ""
	if toolOutputRan {
		g.logCompressionDetails(pipeCtx, requestID, string(PipeToolOutput), originalBody, forwardBody)
	}
	if toolDiscoveryRan {
		g.logCompressionDetails(pipeCtx, requestID, string(PipeToolDiscovery), originalBody, forwardBody)
	}
	if !toolOutputRan && !toolDiscoveryRan {
		// No pipe ran (e.g., all pipes disabled) — still record session_tools.json.
		g.ensureSessionToolsCatalog(pipeCtx, forwardBody)
	}

	// Write response — explicitly set Content-Type to prevent browser MIME sniffing (XSS mitigation).
	copyHeaders(w, result.Response.Header)
	addPreemptiveHeaders(w, pipeCtx.PreemptiveHeaders)
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Always set Content-Length from actual body (phantom loop may rewrite the body,
	// making the upstream Content-Length header stale).
	w.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))
	w.WriteHeader(result.Response.StatusCode)
	_, _ = w.Write(responseBody) //nolint:gosec // G705: Content-Type and X-Content-Type-Options: nosniff set above
}

func (g *Gateway) logToolDiscoveryAPIFallbacks(requestID, sessionID string, searchHandler *SearchToolHandler, providerModel, toolDiscoveryModel string, isMainAgent bool) {
	if searchHandler == nil || !g.tracker.ToolDiscoveryLogEnabled() {
		return
	}

	events := searchHandler.ConsumeAPIFallbackEvents()
	for _, evt := range events {
		status := "api_fallback"
		if evt.Reason != "" {
			status = status + "_" + evt.Reason
		}

		entry := monitoring.ToolSearchResult{
			LogEntryBase: monitoring.LogEntryBase{
				RequestID: requestID,
				EventType: monitoring.EventTypeToolSearchResult,
				SessionID: sessionID,
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			},
			IsMainAgent:      isMainAgent,
			Query:            evt.Query,
			DeferredCount:    evt.DeferredCount,
			ResultsCount:     evt.ReturnedCount,
			OriginalTokens:   evt.OriginalPoolTokens,
			CompressedTokens: evt.OriginalPoolTokens, // fallback kept all tools (regex ran instead of API)
			CompressionRatio: 0,                      // 0 = nothing removed
			Status:           status,
			ProviderModel:    providerModel,
			CompressionModel: toolDiscoveryModel,
			ErrorDetail:      truncateLogValue(evt.Detail, 500),
		}
		g.tracker.LogToolSearch(entry)

		// Record to savings tracker (API fallback = tools still filtered)
		// Use CompressionComparison for the savings tracker since it expects that type.
		if g.savings != nil {
			comparison := monitoring.CompressionComparison{
				RequestID:        requestID,
				EventType:        monitoring.EventTypeToolSearchResult,
				SessionID:        sessionID,
				IsMainAgent:      isMainAgent,
				ProviderModel:    providerModel,
				OriginalTokens:   evt.OriginalPoolTokens,
				CompressedTokens: evt.OriginalPoolTokens, // fallback kept all tools
				CompressionRatio: 0,                      // 0 = nothing removed
				Status:           status,
				CompressionModel: toolDiscoveryModel,
			}
			g.savings.RecordToolDiscovery(comparison, "", isMainAgent)
		}
	}
}

func truncateLogValue(value string, maxLen int) string {
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen] + "..."
}
