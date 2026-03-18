// Telemetry recording, trajectory tracking, and compression logging.
package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/compresr/context-gateway/internal/adapters"
	authtypes "github.com/compresr/context-gateway/internal/auth/types"
	"github.com/compresr/context-gateway/internal/costcontrol"
	"github.com/compresr/context-gateway/internal/dashboard"
	"github.com/compresr/context-gateway/internal/monitoring"
	phantom_tools "github.com/compresr/context-gateway/internal/phantom_tools"
	"github.com/compresr/context-gateway/internal/preemptive"
	"github.com/compresr/context-gateway/internal/tokenizer"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// telemetryParams holds all parameters needed for telemetry recording.
//
// Backward compatibility: Environment variables for is_main_agent defaults.
// - TOOL_OUTPUT_DEFAULT_MAIN_AGENT: default "true" - assume main agent for tool output when unknown
// - TASK_OUTPUT_DEFAULT_MAIN_AGENT: default "false" - assume subagent for task output (always subagent by definition)
var (
	toolOutputDefaultMainAgent = getEnvBool("TOOL_OUTPUT_DEFAULT_MAIN_AGENT", true)
	taskOutputDefaultMainAgent = getEnvBool("TASK_OUTPUT_DEFAULT_MAIN_AGENT", false)
)

// getEnvBool reads a boolean from environment variable with a default value.
func getEnvBool(key string, defaultVal bool) bool {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	return strings.EqualFold(val, "true") || val == "1"
}

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
	expandPenaltyTokens int // Tiktoken count for savings tracker
	pipeCtx             *PipelineContext
	// For usage extraction from API response
	adapter            adapters.Adapter
	requestBody        []byte              // Original request from client
	responseBody       []byte              // Response from LLM
	streamUsage        *adapters.UsageInfo // Pre-extracted usage from SSE stream (streaming only)
	streamStopReason   string              // stop_reason / finish_reason from SSE stream (streaming only)
	phantomLoopUsage   *adapters.UsageInfo // Accumulated usage across all phantom loop iterations
	forwardBody        []byte              // Compressed request sent to LLM (for proxy interaction tracking)
	compressedBodySize int                 // Post-compression, pre-tool-injection body size (for accurate metrics)
	authModeInitial    string
	authModeEffective  string
	authFallbackUsed   bool
	// For verbose payloads logging
	requestHeaders  http.Header // Request headers from client
	responseHeaders http.Header // Response headers from upstream
	upstreamURL     string      // Actual URL that was hit
	fallbackReason  string      // Reason for auth fallback, if any
}

// recordRequestTelemetry records a complete request event.
func (g *Gateway) recordRequestTelemetry(params telemetryParams) {
	// calculateMetrics uses tiktoken on actual bodies.
	m := g.calculateMetrics(params.requestBody, params.forwardBody, params.originalBodySize, params.compressedBodySize)

	// Extract model and usage from request/response using adapter
	var model string
	var usage adapters.UsageInfo

	if params.adapter != nil {
		model = params.adapter.ExtractModel(params.requestBody)

		// Prefer phantom loop accumulated usage (covers ALL iterations, not just the last).
		// Fall back to adapter extraction (single response) or SSE usage (streaming).
		if params.phantomLoopUsage != nil && params.phantomLoopUsage.TotalTokens > 0 {
			usage = *params.phantomLoopUsage
		} else {
			usage = params.adapter.ExtractUsage(params.responseBody)
		}

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
		ToolDiscoveryFiltered:      params.pipeCtx.KeptToolCount,
		TaskOutputCount:            len(params.pipeCtx.TaskOutputHandledIDs),
		HistoryCompactionTriggered: params.pipeCtx.IsCompaction,
		ExpandPenaltyTokens:        params.expandPenaltyTokens,
		IsMainAgent:                g.isMainConversation(params.pipeCtx.StableFingerprint),
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
	if g.cfg().Monitoring.VerbosePayloads {
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

		// Add masked auth header — use CaptureFromHeaders to cover all providers
		// (x-api-key for Anthropic, api-key for Azure, Authorization: Bearer for OpenAI/OAuth)
		if params.requestHeaders != nil {
			if captured := authtypes.CaptureFromHeaders(params.requestHeaders); captured.HasAuth() {
				event.AuthHeaderSent = monitoring.MaskAuthHeader(captured.Token)
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

		// Record expand penalty (tokens re-sent due to expand_context).
		if params.expandPenaltyTokens > 0 {
			g.savings.RecordExpandPenalty(params.expandPenaltyTokens, model, sessionID)
		}
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

	// Update session monitor with post-response data (tokens, cost, status)
	if g.monitorStore != nil && params.pipeCtx != nil && params.pipeCtx.MonitorSessionID != "" {
		// Only include cost for successful requests — match costTracker behavior.
		// Anthropic doesn't bill for failed requests, so including them inflates
		// the monitor cost above the authoritative costTracker value.
		costForMonitor := event.CostUSD
		if params.statusCode >= 400 {
			costForMonitor = 0
		}
		update := dashboard.SessionUpdate{
			TokensIn:          usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens,
			TokensOut:         usage.OutputTokens,
			TokensSaved:       m.tokensSaved,
			CostUSD:           costForMonitor,
			Compressed:        params.compressionUsed,
			IsMainAgent:       event.IsMainAgent,
			IsRequestComplete: true,
		}
		// Emit waiting_for_human only for clean turn boundaries (HumanTurn).
		// AgentWorking → session stays active (mid tool-loop).
		// Truncated     → session stays active (max_tokens hit; agent did not finish its turn).
		// Unknown       → session status unchanged (stop reason unrecognised; safe default).
		// Track() on the next request resets status back to active regardless.
		if params.adapter != nil && params.adapter.ExtractTurnSignal(params.responseBody, params.streamStopReason) == adapters.TurnSignalHumanTurn {
			update.Status = dashboard.StatusWaitingForHuman
		}
		g.monitorStore.Update(params.pipeCtx.MonitorSessionID, update)
	}

	// Record trajectory if enabled (ATIF format)
	g.recordTrajectory(params, model, usage)
}

// recordTrajectory records user messages and agent responses in ATIF format.
// Only the main agent is recorded — subagent requests are skipped to avoid
// creating many small trajectory files per session.
//
// Within the main agent, tool-loop iterations (LLM responds with tool_use,
// client sends back tool_result) are accumulated into the existing agent step
// instead of creating new steps. This keeps the trajectory compact: one user
// step + one agent step per user turn, regardless of how many tool calls occur.
func (g *Gateway) recordTrajectory(params telemetryParams, model string, usage adapters.UsageInfo) {
	if g.trajectory == nil || !g.trajectory.Enabled() {
		return
	}

	// Only record successful requests
	if params.statusCode >= 400 {
		return
	}

	// Only record trajectories for the main agent.
	// Use pre-computed classification from pipeline context.
	if params.pipeCtx == nil || !params.pipeCtx.Classification.IsMainAgent {
		return
	}
	mc := params.pipeCtx.Classification

	// Use the same conversation session ID as prompt history and cost tracker.
	sessionID := ""
	if params.pipeCtx.CostSessionID != "" {
		sessionID = params.pipeCtx.CostSessionID
	}
	if sessionID == "" {
		sessionID = preemptive.ComputeSessionIDFromClean(mc.FirstUserCleanContent)
	}
	if sessionID == "" {
		sessionID = "default"
	}

	// Mark main session unconditionally — don't depend on mainConversationID
	// which may never be set if prompt history init failed.
	g.trajectory.MarkMainSession(sessionID)

	if model != "" {
		g.trajectory.SetAgentModel(sessionID, model)
	}

	// Use pre-computed classification for new user turn detection.
	isNewUserTurn := mc.IsNewUserTurn
	cleanedPrompt := mc.CleanUserPrompt
	isStreaming := len(params.responseBody) == 0

	// Extract the PREVIOUS assistant response from the request body's
	// conversation history. This is critical for streaming: the response
	// body is empty (streamed to client), but the next request always
	// includes the previous response in its message array.
	prevContent, prevToolCalls := extractLastAssistantContent(params.requestBody)

	if isNewUserTurn {
		// Before creating new steps, finalize the previous agent step.
		// For streaming, this captures the assistant's final text/tool calls
		// that weren't available from the empty response body.
		if prevContent != "" || len(prevToolCalls) > 0 {
			g.trajectory.AccumulateAgentResponse(sessionID, monitoring.AgentResponseData{
				Message:   prevContent,
				ToolCalls: prevToolCalls,
			})
		}

		// Record new user turn
		g.trajectory.RecordUserMessage(sessionID, cleanedPrompt)

		// Create new agent step from current response
		var content string
		var toolCalls []monitoring.ToolCall
		if !isStreaming {
			content, toolCalls = extractAgentResponse(params.responseBody)
		}
		g.trajectory.RecordAgentResponse(sessionID, monitoring.AgentResponseData{
			Message:          content,
			Model:            model,
			ToolCalls:        toolCalls,
			PromptTokens:     usage.InputTokens,
			CompletionTokens: usage.OutputTokens,
		})
		g.recordProxyInteraction(params, sessionID, usage)
	} else {
		// Tool-loop iteration: accumulate into the existing agent step.
		if isStreaming {
			// Streaming: extract tool calls/text from request body history
			// (the previous LLM response that was streamed to client).
			g.trajectory.AccumulateAgentResponse(sessionID, monitoring.AgentResponseData{
				Message:          prevContent,
				Model:            model,
				ToolCalls:        prevToolCalls,
				PromptTokens:     usage.InputTokens,
				CompletionTokens: usage.OutputTokens,
			})
		} else {
			// Non-streaming: extract from response body (current response)
			content, toolCalls := extractAgentResponse(params.responseBody)
			g.trajectory.AccumulateAgentResponse(sessionID, monitoring.AgentResponseData{
				Message:          content,
				Model:            model,
				ToolCalls:        toolCalls,
				PromptTokens:     usage.InputTokens,
				CompletionTokens: usage.OutputTokens,
			})
		}
	}
}

// recordProxyInteraction records compression metadata for the trajectory.
// Does NOT store full message arrays — those duplicate the system prompt and
// entire conversation history in every step, causing massive bloat.
// The actual messages are already captured by the step's Message/ToolCalls fields.
func (g *Gateway) recordProxyInteraction(params telemetryParams, sessionID string, usage adapters.UsageInfo) {
	if g.trajectory == nil || !g.trajectory.Enabled() {
		return
	}

	// Get compression info from pipeline context - convert to trajectory format
	var toolCompressions []monitoring.ToolCompressionEntry
	if params.pipeCtx != nil && len(params.pipeCtx.ToolOutputCompressions) > 0 {
		for _, tc := range params.pipeCtx.ToolOutputCompressions {

			ratio := tokenizer.CompressionRatio(tc.OriginalTokens, tc.CompressedTokens)
			// Determine status from MappingStatus
			status := tc.MappingStatus
			if status == "" {
				if tc.CacheHit {
					status = "cache_hit"
				} else if tc.CompressedTokens < tc.OriginalTokens {
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
				OriginalTokens:    tc.OriginalTokens,
				CompressedTokens:  tc.CompressedTokens,
				CompressionRatio:  ratio,
				OriginalContent:   tc.OriginalContent,
				CompressedContent: tc.CompressedContent,
				CacheHit:          tc.CacheHit,
			})
		}
	}

	// Count tokens using tiktoken on actual content.
	clientTokens := tokenizer.CountBytes(params.requestBody)
	compressedTokens := tokenizer.CountBytes(params.forwardBody)

	// Count messages instead of storing them (avoids system prompt duplication)
	clientMsgCount := countMessages(params.requestBody)
	compressedMsgCount := countMessages(params.forwardBody)

	g.trajectory.RecordProxyInteraction(sessionID, monitoring.ProxyInteractionData{
		PipeType:           string(params.pipeType),
		PipeStrategy:       params.pipeStrategy,
		ClientTokens:       clientTokens,
		CompressedTokens:   compressedTokens,
		ClientMsgCount:     clientMsgCount,
		CompressedMsgCount: compressedMsgCount,
		CompressionEnabled: params.compressionUsed,
		ToolCompressions:   toolCompressions,
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

// extractLastAssistantContent extracts content and tool calls from the last
// assistant message in the request body's conversation history. This recovers
// agent response data from streaming responses where the response body is empty —
// the client always includes the previous response in the next request's messages.
//
// Handles both Anthropic format (content blocks with tool_use) and OpenAI format
// (content string + tool_calls array).
func extractLastAssistantContent(body []byte) (string, []monitoring.ToolCall) {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return "", nil
	}

	arr := messages.Array()
	if len(arr) < 2 {
		return "", nil
	}

	// Find the last assistant message (iterating backwards)
	for i := len(arr) - 1; i >= 0; i-- {
		if arr[i].Get("role").String() != "assistant" {
			continue
		}

		msg := arr[i]
		content := msg.Get("content")

		// OpenAI format: content is a string, tool_calls is a separate array
		if content.Type == gjson.String {
			text := content.String()
			var toolCalls []monitoring.ToolCall

			tc := msg.Get("tool_calls")
			if tc.IsArray() {
				for _, call := range tc.Array() {
					toolCall := monitoring.ToolCall{
						ToolCallID:   call.Get("id").String(),
						FunctionName: call.Get("function.name").String(),
					}
					if args := call.Get("function.arguments").String(); args != "" {
						var argsMap map[string]any
						if err := json.Unmarshal([]byte(args), &argsMap); err == nil {
							toolCall.Arguments = argsMap
						}
					}
					if toolCall.ToolCallID != "" && toolCall.FunctionName != "" {
						toolCalls = append(toolCalls, toolCall)
					}
				}
			}
			return text, toolCalls
		}

		// Anthropic format: content is an array of blocks
		if content.IsArray() {
			var text string
			var toolCalls []monitoring.ToolCall

			for _, block := range content.Array() {
				blockType := block.Get("type").String()
				switch blockType {
				case "text":
					text += block.Get("text").String()
				case "tool_use":
					toolCall := monitoring.ToolCall{
						ToolCallID:   block.Get("id").String(),
						FunctionName: block.Get("name").String(),
					}
					if input := block.Get("input"); input.Exists() {
						var argsMap map[string]any
						if err := json.Unmarshal([]byte(input.Raw), &argsMap); err == nil {
							toolCall.Arguments = argsMap
						}
					}
					if toolCall.ToolCallID != "" && toolCall.FunctionName != "" {
						toolCalls = append(toolCalls, toolCall)
					}
				}
			}
			return text, toolCalls
		}

		break // Only process the last assistant message
	}

	return "", nil
}

// requestMetrics holds calculated metrics for a request.
type requestMetrics struct {
	originalTokens, compressedTokens, tokensSaved int
	compressionRatio                              float64
}

// calculateMetrics computes token-based compression metrics using tiktoken.
// This captures all savings sources: tool output compression, preemptive
// summarization, and tool discovery filtering — since all reduce the forwarded body size.

func (g *Gateway) calculateMetrics(requestBody, forwardBody []byte, originalBodySize, compressedBodySize int) requestMetrics {
	// Count tokens using tiktoken on actual content.
	originalTokens := tokenizer.CountBytes(requestBody)
	compressedTokens := tokenizer.CountBytes(forwardBody)

	m := requestMetrics{
		originalTokens:   originalTokens,
		compressedTokens: originalTokens,
		compressionRatio: 0.0, // no compression = 0% removed
	}

	if compressedTokens > 0 && compressedTokens < originalTokens {
		m.compressedTokens = compressedTokens
		m.tokensSaved = originalTokens - compressedTokens
		m.compressionRatio = tokenizer.CompressionRatio(originalTokens, compressedTokens)
	}

	return m
}

// logSessionToolCatalog logs the tool catalog for a session.
// The pretty-printed catalog goes to session_tools.json (via WriteSessionToolsCatalog).
// The lazy_loading compression comparison goes to tool_discovery.jsonl.
// session_tools events are NOT written to tool_discovery.jsonl — session_tools.json is the canonical source.
// forwardBody is the body actually sent to the upstream (post-injection) so phantom tools are included.
func (g *Gateway) logSessionToolCatalog(requestID, costSessionID string, forwardBody []byte, comparison monitoring.CompressionComparison) {
	tools := extractToolsForCatalog(forwardBody)
	if g.tracker.ToolDiscoveryLogEnabled() {
		g.tracker.LogLazyLoading(comparison)
	}
	g.tracker.WriteSessionToolsCatalog(costSessionID, tools)
	g.tracker.SetStatsSession(costSessionID)
}

// logCompressionDetails logs compression comparisons if enabled.
func (g *Gateway) logCompressionDetails(pipeCtx *PipelineContext, requestID, pipeType string, originalBody, compressedBody []byte) {
	costSessionID := ""
	if pipeCtx != nil {
		costSessionID = pipeCtx.CostSessionID
	}
	// Use pre-computed classification for isMainAgent (more reliable than fingerprint matching).
	// Fall back to environment variable defaults when classification is unavailable.
	isMainAgent := toolOutputDefaultMainAgent // Default from TOOL_OUTPUT_DEFAULT_MAIN_AGENT
	if pipeCtx != nil {
		isMainAgent = pipeCtx.Classification.IsMainAgent
	}

	if pipeType == string(PipeToolDiscovery) {
		// Check if tool discovery was skipped
		if pipeCtx.ToolDiscoverySkipReason != "" {
			toolCount := len(extractToolNamesFromRequest(originalBody))
			origToolRaw := extractToolsRaw(originalBody)
			origToolTokens := tokenizer.CountBytes(origToolRaw)
			comparison := monitoring.CompressionComparison{
				RequestID:        requestID,
				EventType:        monitoring.EventTypeLazyLoading,
				SessionID:        costSessionID,
				IsMainAgent:      isMainAgent,
				OriginalTokens:   origToolTokens,
				CompressedTokens: origToolTokens,
				CompressionRatio: 0.0,
				ToolCount:        toolCount,
				StubCount:        0,
				PhantomCount:     0,
				Status:           "skipped_" + pipeCtx.ToolDiscoverySkipReason,
				CompressionModel: pipeCtx.ToolDiscoveryModel,
			}
			g.logSessionToolCatalog(requestID, costSessionID, compressedBody, comparison)
			if g.savings != nil {
				g.savings.RecordToolDiscovery(comparison, costSessionID, isMainAgent)
			}
			return
		}

		status := "passthrough"
		if !bytes.Equal(originalBody, compressedBody) {
			status = "filtered"
		}
		// Check if this was a cache hit from the tool discovery pipe
		if pipeCtx.CacheHit {
			status = "cache_hit"
		}
		allToolNames := extractToolNamesFromRequest(originalBody)
		compToolNames := extractToolNamesFromRequest(compressedBody)
		selectedTools := filterPhantomTools(compToolNames)
		origToolRaw := extractToolsRaw(originalBody)
		compToolRaw := extractToolsRaw(compressedBody)
		comparison := monitoring.CompressionComparison{
			RequestID:        requestID,
			EventType:        monitoring.EventTypeLazyLoading,
			SessionID:        costSessionID,
			IsMainAgent:      isMainAgent,
			OriginalTokens:   tokenizer.CountBytes(origToolRaw),
			CompressedTokens: tokenizer.CountBytes(compToolRaw),
			CompressionRatio: tokenizer.CompressionRatio(tokenizer.CountBytes(origToolRaw), tokenizer.CountBytes(compToolRaw)),
			ToolCount:        len(allToolNames),
			StubCount:        len(allToolNames) - len(selectedTools),
			PhantomCount:     len(compToolNames) - len(selectedTools),
			AllTools:         allToolNames,
			SelectedTools:    selectedTools,
			Status:           status,
			CacheHit:         pipeCtx.CacheHit,
			CompressionModel: pipeCtx.ToolDiscoveryModel,
		}

		// Log session tool catalog once per session (before the lazy_loading entry)
		g.logSessionToolCatalog(requestID, costSessionID, compressedBody, comparison)

		// Always record to savings tracker
		if g.savings != nil {
			g.savings.RecordToolDiscovery(comparison, costSessionID, isMainAgent)
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
			} else if tc.CompressedTokens < tc.OriginalTokens {
				status = "compressed"
			} else {
				status = "passthrough"
			}
		}

		ratio := tokenizer.CompressionRatio(tc.OriginalTokens, tc.CompressedTokens)

		comparison := monitoring.CompressionComparison{
			RequestID:         requestID,
			ProviderModel:     pipeCtx.TargetModel,
			IsMainAgent:       isMainAgent,
			ToolName:          tc.ToolName,
			ShadowID:          tc.ShadowID,
			OriginalTokens:    tc.OriginalTokens,
			CompressedTokens:  tc.CompressedTokens,
			CompressionRatio:  ratio,
			OriginalContent:   tc.OriginalContent,
			CompressedContent: tc.CompressedContent,
			CacheHit:          tc.CacheHit,
			Status:            status,
			MinThreshold:      tc.MinThreshold,
			MaxThreshold:      tc.MaxThreshold,
			CompressionModel:  tc.Model,
			Query:             tc.Query,
			QueryAgnostic:     tc.QueryAgnostic,
			EventType:         monitoring.EventTypeToolOutput,
		}

		// Log to tool_output_compression.jsonl
		// Skip passthrough statuses for historical/small outputs to avoid log explosion.
		// These repeat on every request: passthrough_small (below min threshold),
		// already_compressed (prior turn), passthrough_format (non-compressible format).
		// Only log meaningful entries: compression attempts, cache hits, large passthroughs.
		// Skip Agent/Task tools - those go to task_output_compression.jsonl only (via TaskOutputCompressions loop below).
		if g.tracker.CompressionLogEnabled() && !isTaskOutputTool(tc.ToolName) {
			shouldLog := status == "compressed" || status == "cache_hit" ||
				status == "passthrough_large" || status == "ratio_exceeded" ||
				status == "skipped_by_config"
			if shouldLog {
				g.tracker.LogCompressionComparison(comparison)
			}
		}

		// NOTE: Agent/Task tools are logged via the TaskOutputCompressions loop below to avoid duplication.
		// We removed the separate logging here because:
		// 1. TaskOutputCompressions is populated by the task_output pipe (handles enabled/passthrough modes)
		// 2. If task_output pipe is disabled, we still want to detect and log via isTaskOutputToolInToolOutput (see below)

		// Record to savings tracker for accurate savings calculation
		if g.savings != nil {
			g.savings.RecordToolOutputCompression(comparison, costSessionID, isMainAgent)
		}
	}

	// Record task output events to task_output_compression.jsonl (always, even passthrough).
	// Track which tools were already logged via TaskOutputCompressions to avoid duplication
	taskOutputLoggedToolCallIDs := make(map[string]bool)

	for _, tc := range pipeCtx.TaskOutputCompressions {
		status := tc.MappingStatus
		if status == "" {
			if tc.CompressedTokens < tc.OriginalTokens {
				status = "compressed"
			} else {
				status = "passthrough"
			}
		}
		ratio := tokenizer.CompressionRatio(tc.OriginalTokens, tc.CompressedTokens)
		// Task output is BY DEFINITION subagent output, so is_main_agent defaults to false.
		// Use TASK_OUTPUT_DEFAULT_MAIN_AGENT env var for backward compatibility override.
		comparison := monitoring.CompressionComparison{
			RequestID:         requestID,
			ProviderModel:     pipeCtx.TargetModel,
			IsMainAgent:       taskOutputDefaultMainAgent, // Default: false (subagent output)
			ToolName:          tc.ToolName,
			OriginalTokens:    tc.OriginalTokens,
			CompressedTokens:  tc.CompressedTokens,
			CompressionRatio:  ratio,
			OriginalContent:   tc.OriginalContent,
			CompressedContent: tc.CompressedContent,
			Status:            status,
			EventType:         monitoring.EventTypeTaskOutput,
		}
		if g.tracker.TaskOutputLogEnabled() {
			g.tracker.LogTaskOutputComparison(comparison)
			taskOutputLoggedToolCallIDs[tc.ToolCallID] = true
		}
	}

	// Fallback: Log Agent/Task tools from ToolOutputCompressions that weren't in TaskOutputCompressions.
	// This handles cases where task_output pipe is disabled or in passthrough mode.
	if g.tracker.TaskOutputLogEnabled() {
		for _, tc := range pipeCtx.ToolOutputCompressions {
			if !isTaskOutputTool(tc.ToolName) {
				continue // Not a task output tool
			}
			if taskOutputLoggedToolCallIDs[tc.ToolCallID] {
				continue // Already logged via TaskOutputCompressions
			}
			status := tc.MappingStatus
			if status == "" {
				if tc.CompressedTokens < tc.OriginalTokens {
					status = "compressed"
				} else {
					status = "passthrough"
				}
			}
			ratio := tokenizer.CompressionRatio(tc.OriginalTokens, tc.CompressedTokens)
			comparison := monitoring.CompressionComparison{
				RequestID:         requestID,
				ProviderModel:     pipeCtx.TargetModel,
				IsMainAgent:       taskOutputDefaultMainAgent, // Default: false (subagent output)
				ToolName:          tc.ToolName,
				OriginalTokens:    tc.OriginalTokens,
				CompressedTokens:  tc.CompressedTokens,
				CompressionRatio:  ratio,
				OriginalContent:   tc.OriginalContent,
				CompressedContent: tc.CompressedContent,
				Status:            status,
				EventType:         monitoring.EventTypeTaskOutput,
			}
			g.tracker.LogTaskOutputComparison(comparison)
		}
	}

	if len(pipeCtx.ToolOutputCompressions) == 0 && g.tracker.CompressionLogEnabled() {
		// Preemptive summarization is already recorded in telemetry via HistoryCompactionTriggered.
		// No per-tool passthrough entry is meaningful here — skip.
		if pipeCtx.IsCompaction {
			return
		}

		passOrigTokens := tokenizer.CountBytes(originalBody)
		passCompTokens := tokenizer.CountBytes(compressedBody)

		// Derive EventType from which pipe ran so the log entry is self-describing.
		// tool_output pipe ran but no tools crossed the compression threshold → tool_output passthrough.
		// Any other pipe (passthrough, none) → generic passthrough.
		eventType := "passthrough"
		if pipeType == string(PipeToolOutput) {
			eventType = monitoring.EventTypeToolOutput
		}

		g.tracker.LogCompressionComparison(monitoring.CompressionComparison{
			RequestID:         requestID,
			ProviderModel:     pipeCtx.Model,
			IsMainAgent:       isMainAgent,
			OriginalTokens:    passOrigTokens,
			CompressedTokens:  passCompTokens,
			CompressionRatio:  tokenizer.CompressionRatio(passOrigTokens, passCompTokens),
			OriginalContent:   string(originalBody),
			CompressedContent: string(compressedBody),
			Status:            "passthrough",
			EventType:         eventType,
		})
	}
}

// ensureSessionToolsCatalog writes the session_tools.json catalog when no pipe ran.
// Called as a fallback when both tool-output and tool-discovery conditions are false
// (e.g., all pipes disabled). Uses forwardBody (post-injection) so phantom tools are included.
func (g *Gateway) ensureSessionToolsCatalog(pipeCtx *PipelineContext, forwardBody []byte) {
	if pipeCtx == nil {
		return
	}
	g.tracker.WriteSessionToolsCatalog(pipeCtx.CostSessionID, extractToolsForCatalog(forwardBody))
	g.tracker.SetStatsSession(pipeCtx.CostSessionID)
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

// phantomToolNames lists phantom tools injected after filtering — excluded from tool discovery logs.
var phantomToolNames = map[string]bool{
	phantom_tools.ExpandContextToolName: true,
	phantom_tools.SearchToolName:        true,
}

// filterPhantomTools removes gateway-injected phantom tool names from a list.
func filterPhantomTools(names []string) []string {
	out := names[:0:len(names)]
	for _, n := range names {
		if !phantomToolNames[n] {
			out = append(out, n)
		}
	}
	return out
}

// taskOutputToolNames lists tool names that produce subagent task output.
// These tools spawn subagents and return their results to the main agent.
var taskOutputToolNames = map[string]bool{
	// Claude Code
	"agent": true, // Claude Code subagent delegation tool
	"task":  true, // Alternative name for Claude Code task delegation
	// Codex CLI
	"wait_agent":   true, // Codex: waits for subagent and returns its output
	"spawn_agent":  true, // Codex: spawns subagent (may include initial output)
	"resume_agent": true, // Codex: resumes paused agent and returns output
}

// isTaskOutputTool returns true if the tool name is a subagent task output tool.
// Used to detect Agent/Task tools and log them to task_output_compression.jsonl
// even when the task_output pipe is disabled/passthrough.
func isTaskOutputTool(toolName string) bool {
	for k := range taskOutputToolNames {
		if strings.EqualFold(toolName, k) {
			return true
		}
	}
	return false
}

// extractToolsRaw returns the raw JSON bytes of the tools array from a request body.
func extractToolsRaw(body []byte) []byte {
	return []byte(gjson.GetBytes(body, "tools").Raw)
}

// extractToolsForCatalog returns a SessionToolEntry for every tool in body, including phantom tools.
// Each entry holds the tool name, its full raw JSON schema, and its token count.
func extractToolsForCatalog(body []byte) []monitoring.SessionToolEntry {
	toolsResult := gjson.GetBytes(body, "tools")
	if !toolsResult.Exists() {
		return nil
	}
	seen := make(map[string]bool)
	var entries []monitoring.SessionToolEntry
	toolsResult.ForEach(func(_, tv gjson.Result) bool {
		raw := []byte(tv.Raw)
		name := tv.Get("name").String()
		if name == "" {
			name = tv.Get("function.name").String()
		}
		if name == "" || seen[name] {
			return true
		}
		seen[name] = true
		entries = append(entries, monitoring.SessionToolEntry{
			ToolName:       name,
			OriginalTokens: tokenizer.CountBytes(raw),
			Schema:         json.RawMessage(raw),
		})
		return true
	})
	return entries
}

// mergeCompactedWithOriginal merges compacted messages with original request fields.
// Uses sjson for byte-level replacement to preserve JSON field ordering and KV-cache prefix.
// Preserves model, system, tools, and other fields from original.
func mergeCompactedWithOriginal(compactedMessages []byte, originalBody []byte) ([]byte, error) {
	rawMessages := gjson.GetBytes(compactedMessages, "messages").Raw
	if rawMessages == "" {
		return originalBody, nil
	}
	return sjson.SetRawBytes(originalBody, "messages", []byte(rawMessages))
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

// countMessages counts the number of messages in a request body.
func countMessages(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	result := gjson.GetBytes(body, "messages.#")
	if n := int(result.Int()); n > 0 {
		return n
	}
	return int(gjson.GetBytes(body, "input.#").Int())
}
