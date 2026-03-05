// Non-streaming request handling with phantom tool loop support.
package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/compresr/context-gateway/internal/pipes"
)

// handleNonStreaming handles non-streaming requests with phantom tool loop support.
// Phantom tools (expand_context, gateway_search_tools) are handled internally.
func (g *Gateway) handleNonStreaming(w http.ResponseWriter, r *http.Request, forwardBody []byte,
	pipeCtx *PipelineContext, requestID string, startTime time.Time, adapter adapters.Adapter,
	pipeType PipeType, pipeStrategy string, originalBodySize int, compressionUsed bool,
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
	// searchFallback is enabled for:
	// - tool-search strategy: uses gateway_search_tools with local regex search
	searchFallbackEnabled := g.config.Pipes.ToolDiscovery.Enabled &&
		g.config.Pipes.ToolDiscovery.Strategy == config.StrategyToolSearch
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

			// Configure SearchToolHandler based on strategy
			opts := SearchToolHandlerOptions{
				Strategy:   g.config.Pipes.ToolDiscovery.Strategy,
				AlwaysKeep: g.config.Pipes.ToolDiscovery.AlwaysKeep,
			}

			// API strategy: configure Compresr API endpoint for search
			if pipes.IsAPIStrategy(g.config.Pipes.ToolDiscovery.Strategy) {
				apiEndpoint := g.config.Pipes.ToolDiscovery.Compresr.Endpoint
				if apiEndpoint == "" && g.config.URLs.Compresr != "" {
					apiEndpoint = strings.TrimRight(g.config.URLs.Compresr, "/") + "/api/compress/tool-discovery/"
				}
				opts.APIEndpoint = apiEndpoint
				opts.APIKey = g.config.Pipes.ToolDiscovery.Compresr.APIKey
				opts.APITimeout = g.config.Pipes.ToolDiscovery.Compresr.Timeout
			}

			searchHandler = NewSearchToolHandler(searchToolName, maxSearchResults, g.toolSessions, opts)
			if g.searchLog != nil {
				searchHandler.WithSearchLog(g.searchLog, requestID, pipeCtx.CostSessionID)
			}

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
			ecHandler := NewExpandContextHandler(g.store)
			if g.expandLog != nil {
				ecHandler.WithExpandLog(g.expandLog, requestID, pipeCtx.CostSessionID)
			}
			handlers = append(handlers, ecHandler)
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
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize))
			_ = resp.Body.Close()
			result = &PhantomLoopResult{
				ResponseBody: respBody,
				Response:     resp,
			}
		}
	}

	if err != nil || result == nil || result.Response == nil {
		g.logToolDiscoveryAPIFallbacks(requestID, searchHandler, pipeCtx.Model, pipeCtx.ToolDiscoveryModel)
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
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody,
			authModeInitial: authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
			requestHeaders: r.Header, responseHeaders: nil, upstreamURL: "", fallbackReason: "",
		})
		g.writeError(w, "upstream request failed", http.StatusBadGateway)
		return
	}

	responseBody := result.ResponseBody
	g.logToolDiscoveryAPIFallbacks(requestID, searchHandler, pipeCtx.Model, pipeCtx.ToolDiscoveryModel)

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
		provider: providerName, pipeType: pipeType, pipeStrategy: pipeStrategy, originalBodySize: originalBodySize,
		compressionUsed: compressionUsed, statusCode: result.Response.StatusCode,
		compressLatency: compressLatency, forwardLatency: result.ForwardLatency,
		expandLoops: result.LoopCount, pipeCtx: pipeCtx,
		adapter: adapter, requestBody: originalBody, responseBody: result.ResponseBody,
		forwardBody:     forwardBody,
		authModeInitial: authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
		requestHeaders: r.Header, responseHeaders: result.Response.Header, upstreamURL: result.Response.Request.URL.String(), fallbackReason: "",
	})

	// Log provider errors and compression details
	if result.Response.StatusCode >= 400 {
		g.alerts.FlagProviderError(requestID, providerName, result.Response.StatusCode,
			string(responseBody[:min(500, len(responseBody))]))
	}
	// Log for each pipe that ran
	if len(pipeCtx.ToolOutputCompressions) > 0 || pipeCtx.OutputCompressed {
		g.logCompressionDetails(pipeCtx, requestID, string(PipeToolOutput), originalBody, forwardBody)
	}
	if pipeCtx.FilteredToolCount > 0 || pipeCtx.ToolsFiltered {
		g.logCompressionDetails(pipeCtx, requestID, string(PipeToolDiscovery), originalBody, forwardBody)
	}

	// Write response
	copyHeaders(w, result.Response.Header)
	addPreemptiveHeaders(w, pipeCtx.PreemptiveHeaders)
	w.WriteHeader(result.Response.StatusCode)
	_, _ = w.Write(responseBody)
}

func (g *Gateway) logToolDiscoveryAPIFallbacks(requestID string, searchHandler *SearchToolHandler, providerModel, toolDiscoveryModel string) {
	if searchHandler == nil || !g.tracker.ToolDiscoveryLogEnabled() {
		return
	}

	events := searchHandler.ConsumeAPIFallbackEvents()
	for _, evt := range events {
		status := "api_fallback"
		if evt.Reason != "" {
			status = status + "_" + evt.Reason
		}

		comparison := monitoring.CompressionComparison{
			RequestID:         requestID,
			ProviderModel:     providerModel,
			ToolName:          searchHandler.Name(),
			OriginalBytes:     evt.DeferredCount,
			CompressedBytes:   evt.ReturnedCount,
			CompressionRatio:  float64(max(evt.ReturnedCount, 1)) / float64(max(evt.DeferredCount, 1)),
			OriginalContent:   evt.Query,
			CompressedContent: truncateLogValue(evt.Detail, 500),
			Status:            status,
			CompressionModel:  toolDiscoveryModel,
		}
		g.tracker.LogToolDiscoveryComparison(comparison)

		// Record to savings tracker (API fallback = tools still filtered)
		if g.savings != nil {
			g.savings.RecordToolDiscovery(comparison, "")
		}
	}
}

func truncateLogValue(value string, maxLen int) string {
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen] + "..."
}
