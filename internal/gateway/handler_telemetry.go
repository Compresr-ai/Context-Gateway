// Telemetry recording, trajectory tracking, and compression logging.
package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/costcontrol"
	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/compresr/context-gateway/internal/preemptive"
)

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
	originalBodySize    int // Pre-compaction request body size (captures summarization savings)
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
	// For verbose payloads logging
	requestHeaders  http.Header // Request headers from client
	responseHeaders http.Header // Response headers from upstream
	upstreamURL     string      // Actual URL that was hit
	fallbackReason  string      // Reason for auth fallback, if any
}

// recordRequestTelemetry records a complete request event.
func (g *Gateway) recordRequestTelemetry(params telemetryParams) {
	originalBodySize := params.originalBodySize
	if originalBodySize == 0 {
		originalBodySize = len(params.requestBody)
	}
	m := g.calculateMetrics(originalBodySize, len(params.forwardBody))

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

	// Build the RequestEvent with base fields
	event := &monitoring.RequestEvent{
		RequestID:                params.requestID,
		Timestamp:                params.startTime,
		Method:                   params.method,
		Path:                     params.path,
		ClientIP:                 params.clientIP,
		Provider:                 params.provider,
		Model:                    model,
		RequestBodySize:          params.requestBodySize,
		ResponseBodySize:         params.responseBodySize,
		StatusCode:               params.statusCode,
		PipeType:                 monitoring.PipeType(params.pipeType),
		PipeStrategy:             params.pipeStrategy,
		OriginalTokens:           m.originalTokens,
		CompressedTokens:         m.compressedTokens,
		TokensSaved:              m.tokensSaved,
		CompressionRatio:         m.compressionRatio,
		CompressionUsed:          params.compressionUsed,
		ShadowRefsCreated:        len(params.pipeCtx.ShadowRefs),
		ExpandLoops:              params.expandLoops,
		ExpandCallsFound:         params.expandCallsFound,
		ExpandCallsNotFound:      params.expandCallsNotFound,
		Success:                  params.statusCode < 400,
		Error:                    params.errorMsg,
		CompressionLatencyMs:     params.compressLatency.Milliseconds(),
		ForwardLatencyMs:         params.forwardLatency.Milliseconds(),
		TotalLatencyMs:           time.Since(params.startTime).Milliseconds(),
		AuthModeInitial:          params.authModeInitial,
		AuthModeEffective:        params.authModeEffective,
		AuthFallbackUsed:         params.authFallbackUsed,
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
		TotalTokens:              usage.TotalTokens,
		// Pipe-specific counts
		ToolOutputCount:            len(params.pipeCtx.ToolOutputCompressions),
		ToolDiscoveryOriginal:      params.pipeCtx.OriginalToolCount,
		ToolDiscoveryFiltered:      params.pipeCtx.FilteredToolCount,
		HistoryCompactionTriggered: params.pipeCtx.IsCompaction,
	}

	// Calculate cost for this request (for debugging/transparency)
	if usage.TotalTokens > 0 && model != "" {
		pricing := costcontrol.GetModelPricing(model)
		if usage.CacheCreationInputTokens > 0 || usage.CacheReadInputTokens > 0 {
			event.CostUSD = costcontrol.CalculateCostWithCache(
				usage.InputTokens, usage.OutputTokens,
				usage.CacheCreationInputTokens, usage.CacheReadInputTokens, pricing)
		} else {
			event.CostUSD = costcontrol.CalculateCost(usage.InputTokens, usage.OutputTokens, pricing)
		}
	}

	// Add verbose payloads if enabled
	if g.config.Monitoring.VerbosePayloads {
		// Sanitize and copy request headers
		if params.requestHeaders != nil {
			reqHeadersMap := make(map[string]string)
			for k, v := range params.requestHeaders {
				if len(v) > 0 {
					reqHeadersMap[k] = v[0]
				}
			}
			event.RequestHeaders = monitoring.SanitizeHeaders(reqHeadersMap)
		}

		// Copy response headers
		if params.responseHeaders != nil {
			respHeadersMap := make(map[string]string)
			for k, v := range params.responseHeaders {
				if len(v) > 0 {
					respHeadersMap[k] = v[0]
				}
			}
			event.ResponseHeaders = monitoring.SanitizeHeaders(respHeadersMap)
		}

		// Add request body preview
		event.RequestBodyPreview = monitoring.PreviewBody(string(params.requestBody), 500)

		// Add response body preview
		event.ResponseBodyPreview = monitoring.PreviewBody(string(params.responseBody), 500)

		// Add masked auth header
		if params.requestHeaders != nil {
			if authHeader := params.requestHeaders.Get("Authorization"); authHeader != "" {
				event.AuthHeaderSent = monitoring.MaskAuthHeader(authHeader)
			} else if apiKeyHeader := params.requestHeaders.Get("X-API-Key"); apiKeyHeader != "" {
				event.AuthHeaderSent = monitoring.MaskAuthHeader(apiKeyHeader)
			}
		}

		// Add upstream URL if available
		event.UpstreamURL = params.upstreamURL

		// Add fallback reason if applicable
		if params.authFallbackUsed && params.fallbackReason != "" {
			event.FallbackReason = params.fallbackReason
		}
	}

	g.tracker.RecordRequest(event)

	// Record to savings tracker for /savings command
	if g.savings != nil {
		sessionID := ""
		if params.pipeCtx != nil {
			sessionID = params.pipeCtx.CostSessionID
		}
		g.savings.RecordRequest(event, sessionID)
	}

	// Record cost tracking (only when we have actual token counts from the API response).
	// Streaming responses have empty bodies so ExtractUsage returns zeros — skip rather
	// than estimate, since estimation ignores caching and overestimates by 10x+.
	// Only record for successful requests — Anthropic doesn't bill for failed requests.
	if g.costTracker != nil && params.pipeCtx != nil && params.pipeCtx.CostSessionID != "" && usage.TotalTokens > 0 && params.statusCode < 400 {
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
		content, toolCalls = extractAgentResponse(params.responseBody)
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

	// Record proxy interaction (client->proxy->LLM->proxy->client flow)
	g.recordProxyInteraction(params, sessionID, usage)
}

// recordProxyInteraction records the full proxy flow for trajectory.
func (g *Gateway) recordProxyInteraction(params telemetryParams, sessionID string, usage adapters.UsageInfo) {
	if g.trajectory == nil || !g.trajectory.Enabled() {
		return
	}

	// Extract messages from original request (client -> proxy)
	var clientMessages []any
	if len(params.requestBody) > 0 {
		var req map[string]any
		if err := json.Unmarshal(params.requestBody, &req); err == nil {
			if msgs, ok := req["messages"].([]any); ok {
				clientMessages = msgs
			}
		}
	}

	// Extract messages from forward body (proxy -> LLM)
	var compressedMessages []any
	if len(params.forwardBody) > 0 {
		var req map[string]any
		if err := json.Unmarshal(params.forwardBody, &req); err == nil {
			if msgs, ok := req["messages"].([]any); ok {
				compressedMessages = msgs
			}
		}
	}

	// Extract messages from response (LLM -> proxy)
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
	if params.originalBodySize > 0 {
		clientTokens = params.originalBodySize / 4
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
func extractAgentResponse(responseBody []byte) (string, []monitoring.ToolCall) {
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

// calculateMetrics computes compression metrics by comparing original vs forwarded body sizes.
// This naturally captures all savings sources: tool output compression, preemptive summarization,
// and tool discovery filtering — since all three reduce the forwarded body size.
func (g *Gateway) calculateMetrics(originalBodySize, forwardBodySize int) requestMetrics {
	m := requestMetrics{
		originalTokens:   originalBodySize / 4,
		compressedTokens: originalBodySize / 4,
		compressionRatio: 1.0,
	}

	if forwardBodySize > 0 && forwardBodySize < originalBodySize {
		m.compressedTokens = forwardBodySize / 4
		m.tokensSaved = m.originalTokens - m.compressedTokens
		m.compressionRatio = float64(forwardBodySize) / float64(originalBodySize)
	}

	return m
}

// logCompressionDetails logs compression comparisons if enabled.
func (g *Gateway) logCompressionDetails(pipeCtx *PipelineContext, requestID, pipeType string, originalBody, compressedBody []byte) {
	costSessionID := ""
	if pipeCtx != nil {
		costSessionID = pipeCtx.CostSessionID
	}

	if pipeType == string(PipeToolDiscovery) {
		// Check if tool discovery was skipped
		if pipeCtx.ToolDiscoverySkipReason != "" {
			comparison := monitoring.CompressionComparison{
				RequestID:        requestID,
				ProviderModel:    pipeCtx.Model,
				OriginalBytes:    len(originalBody),
				CompressedBytes:  len(originalBody), // Same size - no filtering
				CompressionRatio: 1.0,
				AllTools:         extractToolNamesFromRequest(originalBody),
				SelectedTools:    extractToolNamesFromRequest(originalBody),
				Status:           "skipped_" + pipeCtx.ToolDiscoverySkipReason,
				CompressionModel: pipeCtx.ToolDiscoveryModel,
			}
			// Log skip to file if enabled
			if g.tracker.ToolDiscoveryLogEnabled() {
				g.tracker.LogToolDiscoveryComparison(comparison)
			}
			// Record to savings tracker
			if g.savings != nil {
				g.savings.RecordToolDiscovery(comparison, costSessionID)
			}
			return
		}

		status := "passthrough"
		if !bytes.Equal(originalBody, compressedBody) {
			status = "filtered"
		}
		allTools := extractToolNamesFromRequest(originalBody)
		selectedTools := extractToolNamesFromRequest(compressedBody)
		comparison := monitoring.CompressionComparison{
			RequestID:       requestID,
			ProviderModel:   pipeCtx.Model,
			OriginalBytes:   len(originalBody),
			CompressedBytes: len(compressedBody),
			CompressionRatio: float64(len(compressedBody)) /
				float64(max(len(originalBody), 1)),
			AllTools:         allTools,
			SelectedTools:    selectedTools,
			Status:           status,
			CompressionModel: pipeCtx.ToolDiscoveryModel,
		}

		// Log to file if enabled
		if g.tracker.ToolDiscoveryLogEnabled() {
			g.tracker.LogToolDiscoveryComparison(comparison)
		}

		// Always record to savings tracker
		if g.savings != nil {
			g.savings.RecordToolDiscovery(comparison, costSessionID)
		}
		return
	}

	// Record tool output compression savings to savings tracker
	// (always, even if file logging is disabled)
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

		comparison := monitoring.CompressionComparison{
			RequestID:         requestID,
			ProviderModel:     pipeCtx.Model,
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
			CompressionModel:  tc.Model,
		}

		// Log to file if enabled
		if g.tracker.CompressionLogEnabled() {
			g.tracker.LogCompressionComparison(comparison)
		}

		// Record to savings tracker for accurate savings calculation
		if g.savings != nil {
			g.savings.RecordToolOutputCompression(comparison, costSessionID)
		}
	}

	if len(pipeCtx.ToolOutputCompressions) == 0 && g.tracker.CompressionLogEnabled() {
		g.tracker.LogCompressionComparison(monitoring.CompressionComparison{
			RequestID:         requestID,
			ProviderModel:     pipeCtx.Model,
			OriginalBytes:     len(originalBody),
			CompressedBytes:   len(compressedBody),
			CompressionRatio:  float64(len(compressedBody)) / float64(max(len(originalBody), 1)),
			OriginalContent:   string(originalBody),
			CompressedContent: string(compressedBody),
			Status:            "passthrough",
		})
	}
}

// extractToolNamesFromRequest extracts tool names from a request body.
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
