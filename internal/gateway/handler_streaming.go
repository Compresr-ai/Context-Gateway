// Streaming request handling with expand_context and tool-search support, plus SSE usage parsing.
package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/monitoring"
	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
)

// handleStreamingWithExpand handles streaming requests with expand_context support.
// When expand_context is enabled:
//  1. Buffer the streaming response (detect expand_context calls)
//  2. If expand_context detected -> rewrite history, re-send to LLM
//  3. If not detected -> flush buffer to client
//
// This implements "selective replace" design: only requested tools are expanded,
// keeping history clean and maximizing KV-cache prefix hits.
func (g *Gateway) handleStreamingWithExpand(w http.ResponseWriter, r *http.Request, forwardBody []byte,
	pipeCtx *PipelineContext, requestID string, startTime time.Time, adapter adapters.Adapter,
	pipeType PipeType, pipeStrategy string, originalBodySize int, compressionUsed bool,
	compressLatency time.Duration, originalBody []byte, expandEnabled bool, compressedBodySize int) {

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
			provider: provider, pipeType: pipeType, pipeStrategy: pipeStrategy + "_streaming", originalBodySize: originalBodySize,
			compressionUsed: compressionUsed, statusCode: 502, errorMsg: err.Error(),
			compressLatency: compressLatency, forwardLatency: time.Since(forwardStart), pipeCtx: pipeCtx,
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody, compressedBodySize: compressedBodySize,
			authModeInitial: authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
			requestHeaders: r.Header, responseHeaders: nil, upstreamURL: "", fallbackReason: "",
		})
		log.Error().Err(err).Str("request_id", requestID).Msg("upstream streaming request failed")
		g.writeError(w, "upstream request failed", http.StatusBadGateway)
		return
	}

	// Buffer when phantom tools were injected (the model may call them and we must intercept),
	// OR when tool discovery filtered tools (gateway_search_tools may be called),
	// OR when compressed content exists (expand_context may be called).
	// toolSearchActive must include PhantomToolsInjected so that:
	//   (a) the scan loop detects phantom tool calls for small tool sets, and
	//   (b) the re-route condition fires and sends the request through handleNonStreaming.
	toolSearchActive := pipeCtx.ToolsFiltered || pipeCtx.PhantomToolsInjected
	needsExpandBuffer := compressionUsed && len(pipeCtx.ShadowRefs) > 0

	// If no buffering needed, stream directly to client
	if !needsExpandBuffer && !toolSearchActive && !pipeCtx.PhantomToolsInjected {
		defer func() { _ = resp.Body.Close() }()
		writeStreamingHeaders(w, resp.Header, pipeCtx.PreemptiveHeaders)
		w.WriteHeader(resp.StatusCode)
		sseUsage, sseStopReason := g.streamResponse(w, resp.Body)

		upstreamURL := ""
		if resp.Request != nil {
			upstreamURL = resp.Request.URL.String()
		}
		g.recordRequestTelemetry(telemetryParams{
			requestID: requestID, startTime: startTime, method: r.Method, path: r.URL.Path,
			clientIP: r.RemoteAddr, requestBodySize: len(originalBody), responseBodySize: 0,
			provider: provider, pipeType: pipeType, pipeStrategy: pipeStrategy + "_streaming", originalBodySize: originalBodySize,
			compressionUsed: compressionUsed, statusCode: resp.StatusCode,
			compressLatency: compressLatency, forwardLatency: time.Since(forwardStart), pipeCtx: pipeCtx,
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody, compressedBodySize: compressedBodySize, streamUsage: &sseUsage, streamStopReason: sseStopReason,
			authModeInitial: authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
			requestHeaders: r.Header, responseHeaders: resp.Header, upstreamURL: upstreamURL, fallbackReason: "",
		})
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
			g.ensureSessionToolsCatalog(pipeCtx, forwardBody)
		}
		return
	}

	// Buffer response to detect phantom tool calls (expand_context and/or gateway_search_tools)
	streamBuffer := tooloutput.NewStreamBuffer()
	usageParser := newSSEUsageParser()
	var bufferedChunks [][]byte

	searchToolName := g.searchToolName()

	// Build set of deferred tool names for direct-call detection.
	// When a model bypasses gateway_search_tools and calls a stub directly (training
	// knowledge), we must detect the call here and re-route through handleNonStreaming
	// so the DeferredCallInterceptor can inject the full schema and ask the model to retry.
	deferredToolNames := make(map[string]bool, len(pipeCtx.DeferredTools))
	for _, dt := range pipeCtx.DeferredTools {
		deferredToolNames[dt.ToolName] = true
	}

	// Read and buffer the entire stream (bounded to prevent OOM)
	buf := make([]byte, DefaultBufferSize)
	totalBuffered := 0
	hasSearchToolCall := false
	hasDeferredToolCall := false
	for {
		if r.Context().Err() != nil {
			log.Debug().Str("request_id", requestID).Msg("client disconnected during stream buffering")
			break
		}
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			totalBuffered += n
			if totalBuffered > MaxStreamBufferSize {
				log.Warn().Int("bytes", totalBuffered).Msg("stream buffer exceeded max size, stopping buffer")
				pipeCtx.StreamTruncated = true
				break
			}
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			bufferedChunks = append(bufferedChunks, chunk)
			usageParser.Feed(chunk)

			// Process for expand_context detection
			if needsExpandBuffer {
				_, _ = streamBuffer.ProcessChunk(chunk)
			}

			// Detect gateway_search_tools calls via byte scan
			if toolSearchActive && !hasSearchToolCall {
				if bytes.Contains(chunk, []byte(searchToolName)) {
					hasSearchToolCall = true
				}
			}

			// Detect direct calls to deferred (stubbed) tools — training knowledge bypass.
			// When the model skips gateway_search_tools and calls a stub directly,
			// the tool name appears in the stream. We re-route through handleNonStreaming
			// so DeferredCallInterceptor can inject the schema and prompt a retry.
			if !hasDeferredToolCall && len(deferredToolNames) > 0 {
				for name := range deferredToolNames {
					if bytes.Contains(chunk, []byte(name)) {
						hasDeferredToolCall = true
						break
					}
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	_ = resp.Body.Close()

	// Extract usage and stop_reason from buffered SSE chunks
	bufferedUsage := usageParser.Usage()
	bufferedStopReason := usageParser.StopReason()

	// If gateway_search_tools OR a direct deferred-tool call was detected, re-send as
	// non-streaming through the phantom loop. The phantom loop handles both SearchToolHandler
	// (for gateway_search_tools) and DeferredCallInterceptor (for direct stub bypasses).
	// The phantom loop produces a non-streaming JSON response which we convert back to SSE.
	if (hasSearchToolCall || hasDeferredToolCall) && toolSearchActive {
		log.Info().
			Str("request_id", requestID).
			Bool("search_tool", hasSearchToolCall).
			Bool("deferred_direct", hasDeferredToolCall).
			Msg("streaming: phantom tool detected, re-sending through phantom loop")

		// Capture the non-streaming response from handleNonStreaming
		capture := &responseCaptureWriter{header: make(http.Header)}
		nonStreamBody := setStreamFlag(forwardBody, false)
		g.handleNonStreaming(capture, r, nonStreamBody, pipeCtx, requestID, startTime, adapter,
			pipeType, pipeStrategy+"_streaming_search_fallback", originalBodySize, compressionUsed, compressLatency, originalBody, expandEnabled, &bufferedUsage, compressedBodySize)

		// Convert the captured JSON response to SSE format for the streaming client
		isResponsesAPI := isResponsesAPI(forwardBody)
		var sseBody []byte
		provider := adapter.Provider()
		if provider == adapters.ProviderAnthropic || provider == adapters.ProviderBedrock {
			sseBody = jsonToAnthropicSSE(capture.body.Bytes())
		} else if isResponsesAPI {
			sseBody = jsonToResponsesAPISSE(capture.body.Bytes())
		} else {
			sseBody = jsonToOpenAISSE(capture.body.Bytes())
		}

		writeStreamingHeaders(w, capture.header, pipeCtx.PreemptiveHeaders)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Del("Content-Length") // SSE streams have no Content-Length
		w.WriteHeader(capture.statusCode)
		if _, err := w.Write(sseBody); err != nil {
			log.Debug().Err(err).Str("request_id", requestID).Msg("client write failed during SSE flush")
		}
		return
	}

	// Check if expand_context was called
	expandCalls := streamBuffer.GetSuppressedCalls()

	if len(expandCalls) > 0 {
		// expand_context detected — use append-only approach (Option B).
		// Instead of rewriting history (which breaks KV cache), we:
		// 1. Build the expand_context tool_results with original content from store
		// 2. Append them to the request as new messages
		// 3. Re-send to LLM — the prefix (all prior messages) remains unchanged for KV cache
		log.Info().
			Int("expand_calls", len(expandCalls)).
			Str("request_id", requestID).
			Msg("streaming: expand_context detected, appending expanded content")

		// Convert stream buffer calls to PhantomToolCalls for the handler
		phantomCalls := make([]PhantomToolCall, 0, len(expandCalls))
		for _, ec := range expandCalls {
			phantomCalls = append(phantomCalls, PhantomToolCall{
				ToolUseID: ec.ToolUseID,
				Input:     map[string]any{"id": ec.ShadowID},
			})
		}

		// Use ExpandContextHandler to build tool_results (same as non-streaming path)
		ecHandler := NewExpandContextHandler(g.store)
		if g.expandLog != nil {
			ecHandler.WithExpandLog(g.expandLog, requestID, pipeCtx.CostSessionID)
		}
		ecHandler.WithExpandCallsLog(g.tracker.ExpandCallsLogger(), pipeCtx.ToolOutputCompressions)
		phantomResult := ecHandler.HandleCalls(phantomCalls, adapter, forwardBody)

		// Build append body: original forwardBody + assistant expand_context call + tool_results
		// This preserves KV cache — all existing messages are unchanged, we only append at the end
		appendBody, err := buildExpandAppendBody(forwardBody, expandCalls, phantomResult.ToolResults, adapter)
		if err != nil {
			log.Error().Err(err).Msg("streaming: failed to build expand append body")
			g.flushBufferedResponse(w, resp.Header, pipeCtx.PreemptiveHeaders, bufferedChunks, resp.StatusCode)
			return
		}

		// Remove expand_context from tools array in the retry request.
		// Without this, the model calls expand_context again creating an infinite loop.
		appendBody = removeToolFromRequest(appendBody, tooloutput.ExpandContextToolName)

		// Re-send with appended messages (KV cache prefix preserved)
		retryResp, retryMeta, err := g.forwardPassthrough(r.Context(), r, appendBody)
		if err != nil {
			log.Error().Err(err).Msg("streaming: failed to re-send after expansion")
			g.flushBufferedResponse(w, resp.Header, pipeCtx.PreemptiveHeaders, bufferedChunks, resp.StatusCode)
			return
		}
		mergeForwardAuthMeta(&authMeta, retryMeta)
		defer func() { _ = retryResp.Body.Close() }()

		// Stream the retry response (filter expand_context if it calls again)
		// Also parse usage from the retry stream so we can track the full cost.
		writeStreamingHeaders(w, retryResp.Header, pipeCtx.PreemptiveHeaders)
		w.WriteHeader(retryResp.StatusCode)

		retryUsage, retryStopReason := g.streamResponseWithFilterAndUsage(w, retryResp.Body)

		// Combine usage from both streams (initial buffered + retry)
		combinedUsage := adapters.UsageInfo{
			InputTokens:              bufferedUsage.InputTokens + retryUsage.InputTokens,
			OutputTokens:             bufferedUsage.OutputTokens + retryUsage.OutputTokens,
			CacheCreationInputTokens: bufferedUsage.CacheCreationInputTokens + retryUsage.CacheCreationInputTokens,
			CacheReadInputTokens:     bufferedUsage.CacheReadInputTokens + retryUsage.CacheReadInputTokens,
			TotalTokens:              bufferedUsage.TotalTokens + retryUsage.TotalTokens,
		}

		expandedCount := 0
		for _, ec := range expandCalls {
			if ec.ShadowID != "" {
				expandedCount++
			}
		}

		// Query expand log for found/not-found counts and penalty tokens
		var streamExpandFound, streamExpandNotFound, streamExpandPenaltyTokens int
		if g.expandLog != nil {
			summary, contentTokens := g.expandLog.SummaryForRequest(requestID)
			streamExpandFound = summary.Found
			streamExpandNotFound = summary.NotFound
			streamExpandPenaltyTokens = contentTokens
		}

		g.recordRequestTelemetry(telemetryParams{
			requestID: requestID, startTime: startTime, method: r.Method, path: r.URL.Path,
			clientIP: r.RemoteAddr, requestBodySize: len(originalBody), responseBodySize: 0,
			provider: provider, pipeType: pipeType, pipeStrategy: pipeStrategy + "_streaming_expanded",
			originalBodySize: originalBodySize, compressionUsed: compressionUsed, statusCode: retryResp.StatusCode,
			compressLatency: compressLatency, forwardLatency: time.Since(forwardStart), pipeCtx: pipeCtx,
			expandLoops: 1, expandCallsFound: streamExpandFound, expandCallsNotFound: streamExpandNotFound,
			expandPenaltyTokens: streamExpandPenaltyTokens,
			adapter:             adapter, requestBody: originalBody, forwardBody: forwardBody, compressedBodySize: compressedBodySize, streamUsage: &combinedUsage, streamStopReason: retryStopReason,
			authModeInitial: authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
			requestHeaders: r.Header, responseHeaders: retryResp.Header, upstreamURL: func() string {
				if retryResp.Request != nil {
					return retryResp.Request.URL.String()
				}
				return ""
			}(), fallbackReason: "",
		})

		log.Info().
			Int("expanded_count", expandedCount).
			Str("request_id", requestID).
			Msg("streaming: expansion complete (append-only)")

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
			g.ensureSessionToolsCatalog(pipeCtx, forwardBody)
		}
		return
	} else {
		// No expand_context detected - flush buffered response
		g.flushBufferedResponse(w, resp.Header, pipeCtx.PreemptiveHeaders, bufferedChunks, resp.StatusCode)

		// If stream was truncated, inject an SSE error event so the client knows
		if pipeCtx.StreamTruncated {
			errorEvent := []byte("event: error\ndata: {\"type\":\"stream_truncated\",\"message\":\"Response exceeded buffer limit\"}\n\n")
			if _, err := w.Write(errorEvent); err != nil {
				log.Debug().Err(err).Msg("client write failed for stream_truncated event")
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}

		g.recordRequestTelemetry(telemetryParams{
			requestID: requestID, startTime: startTime, method: r.Method, path: r.URL.Path,
			clientIP: r.RemoteAddr, requestBodySize: len(originalBody), responseBodySize: 0,
			provider: provider, pipeType: pipeType, pipeStrategy: pipeStrategy + "_streaming", originalBodySize: originalBodySize,
			compressionUsed: compressionUsed, statusCode: resp.StatusCode,
			compressLatency: compressLatency, forwardLatency: time.Since(forwardStart), pipeCtx: pipeCtx,
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody, compressedBodySize: compressedBodySize, streamUsage: &bufferedUsage, streamStopReason: bufferedStopReason,
			authModeInitial: authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
			requestHeaders: r.Header, responseHeaders: resp.Header, upstreamURL: func() string {
				if resp.Request != nil {
					return resp.Request.URL.String()
				}
				return ""
			}(), fallbackReason: "",
		})

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
			g.ensureSessionToolsCatalog(pipeCtx, forwardBody)
		}
	}
}

// writeStreamingHeaders sets common streaming response headers.
func writeStreamingHeaders(w http.ResponseWriter, upstream http.Header, preemptiveHeaders map[string]string) {
	copyHeaders(w, upstream)
	addPreemptiveHeaders(w, preemptiveHeaders)
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

// flushBufferedResponse writes buffered chunks to the response writer.
func (g *Gateway) flushBufferedResponse(w http.ResponseWriter, headers http.Header, preemptiveHeaders map[string]string, chunks [][]byte, statusCode int) {
	writeStreamingHeaders(w, headers, preemptiveHeaders)
	w.WriteHeader(statusCode)

	flusher, ok := w.(http.Flusher)
	for _, chunk := range chunks {
		if _, err := w.Write(chunk); err != nil {
			log.Debug().Err(err).Msg("client write failed during buffered flush")
			return
		}
		if ok {
			flusher.Flush()
		}
	}
}

// streamResponseWithFilterAndUsage is like streamResponseWithFilter but also
// parses SSE usage from the stream. Returns the extracted usage info and stop_reason.
func (g *Gateway) streamResponseWithFilterAndUsage(w http.ResponseWriter, reader io.Reader) (adapters.UsageInfo, string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Warn().Msg("streaming not supported, falling back to buffered")
		_, _ = io.Copy(w, reader)
		return adapters.UsageInfo{}, ""
	}

	streamBuffer := tooloutput.NewStreamBuffer()
	usageParser := newSSEUsageParser()
	buf := make([]byte, DefaultBufferSize)

	for {
		n, err := reader.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			usageParser.Feed(chunk)

			// Filter expand_context from the stream
			filtered, _ := streamBuffer.ProcessChunk(chunk)
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
	return usageParser.Usage(), usageParser.StopReason()
}

// streamResponse streams data from reader to writer with flushing.
// Returns usage and stop_reason extracted from SSE events.
func (g *Gateway) streamResponse(w http.ResponseWriter, reader io.Reader) (adapters.UsageInfo, string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Warn().Msg("streaming not supported, falling back to buffered")
		_, _ = io.Copy(w, reader)
		return adapters.UsageInfo{}, ""
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
	return usageParser.Usage(), usageParser.StopReason()
}

// SSE Usage Parser

type sseUsage struct {
	// Anthropic + Responses API fields
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	// OpenAI Chat Completions fields
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type ssePayload struct {
	Type    string   `json:"type"`
	Usage   sseUsage `json:"usage"`
	Message struct {
		Usage sseUsage `json:"usage"`
	} `json:"message"`
	// Responses API: response.completed wraps usage and output inside "response"
	Response struct {
		Usage  sseUsage `json:"usage"`
		Output []struct {
			Type string `json:"type"`
		} `json:"output"`
	} `json:"response"`
	// Anthropic: message_delta carries stop_reason
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	// OpenAI Chat Completions: choices carry finish_reason
	Choices []struct {
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// sseUsageParser incrementally parses Anthropic SSE events and extracts usage.
// It only reads structured "data: {json}" events to avoid false positives from
// arbitrary text that might contain token-like key names.
type sseUsageParser struct {
	buffer     []byte
	usage      adapters.UsageInfo
	stopReason string // last non-empty stop_reason / finish_reason seen
}

// StopReason returns the stop/finish reason extracted from the SSE stream.
func (p *sseUsageParser) StopReason() string {
	p.parse(true)
	return p.stopReason
}

func newSSEUsageParser() *sseUsageParser {
	return &sseUsageParser{
		buffer: make([]byte, 0, DefaultBufferSize),
	}
}

// MaxSSEParserBufferSize caps the internal parser buffer to prevent unbounded
// growth when an upstream sends a giant SSE event with no \n\n delimiter.
const MaxSSEParserBufferSize = 1 << 20 // 1 MB

func (p *sseUsageParser) Feed(chunk []byte) {
	p.buffer = append(p.buffer, chunk...)
	if len(p.buffer) > MaxSSEParserBufferSize {
		log.Warn().Int("buffer_size", len(p.buffer)).Msg("SSE parser buffer exceeded max, clearing")
		p.buffer = p.buffer[:0]
		return
	}
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
	var payload ssePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return
	}

	// Patch OpenAI nested cached_tokens into flat fields for applyUsage.
	// OpenAI nests cached tokens under prompt_tokens_details.cached_tokens
	// which json.Unmarshal can't reach with the flat sseUsage struct.
	if payload.Usage.CacheReadInputTokens == 0 {
		if cached := gjson.GetBytes(data, "usage.prompt_tokens_details.cached_tokens").Int(); cached > 0 {
			payload.Usage.CacheReadInputTokens = int(cached)
		}
	}
	if payload.Message.Usage.CacheReadInputTokens == 0 {
		if cached := gjson.GetBytes(data, "message.usage.prompt_tokens_details.cached_tokens").Int(); cached > 0 {
			payload.Message.Usage.CacheReadInputTokens = int(cached)
		}
	}
	if payload.Response.Usage.CacheReadInputTokens == 0 {
		if cached := gjson.GetBytes(data, "response.usage.prompt_tokens_details.cached_tokens").Int(); cached > 0 {
			payload.Response.Usage.CacheReadInputTokens = int(cached)
		}
	}

	p.applyUsage(payload.Message.Usage)
	p.applyUsage(payload.Usage)

	// Responses API: response.completed events have usage nested under "response"
	if payload.Type == "response.completed" {
		p.applyUsage(payload.Response.Usage)
		// Derive a synthetic stop reason from the output array.
		// If any output item is a function_call the agent is still working;
		// otherwise the turn is complete and the session should become waiting_for_human.
		hasFunctionCall := false
		for _, item := range payload.Response.Output {
			if item.Type == "function_call" {
				hasFunctionCall = true
				break
			}
		}
		if hasFunctionCall {
			p.stopReason = "function_call"
		} else {
			p.stopReason = "stop"
		}
	}

	// Capture stop reason from Anthropic message_delta or OpenAI choices
	if payload.Delta.StopReason != "" {
		p.stopReason = payload.Delta.StopReason
	}
	for _, c := range payload.Choices {
		if c.FinishReason != "" {
			p.stopReason = c.FinishReason
			break
		}
	}
}

func (p *sseUsageParser) applyUsage(u sseUsage) {
	// Merge Anthropic/Responses API fields with OpenAI Chat Completions fields.
	// input_tokens is used by Anthropic & Responses API; prompt_tokens by Chat Completions.
	inputTokens := u.InputTokens
	if inputTokens == 0 {
		inputTokens = u.PromptTokens
	}
	outputTokens := u.OutputTokens
	if outputTokens == 0 {
		outputTokens = u.CompletionTokens
	}

	if inputTokens > 0 {
		// Anthropic's input_tokens includes cache tokens; subtract them
		// so InputTokens represents only non-cached input (avoids double-counting in cost calculation).
		nonCached := inputTokens - u.CacheCreationInputTokens - u.CacheReadInputTokens
		if nonCached < 0 {
			nonCached = 0
		}
		log.Debug().
			Int("raw_input", inputTokens).
			Int("cache_create", u.CacheCreationInputTokens).
			Int("cache_read", u.CacheReadInputTokens).
			Int("non_cached", nonCached).
			Int("output", outputTokens).
			Msg("sse_usage: applyUsage")
		p.usage.InputTokens = nonCached
	}
	if outputTokens > p.usage.OutputTokens {
		p.usage.OutputTokens = outputTokens
	}
	if u.CacheCreationInputTokens > 0 {
		p.usage.CacheCreationInputTokens = u.CacheCreationInputTokens
	}
	if u.CacheReadInputTokens > 0 {
		p.usage.CacheReadInputTokens = u.CacheReadInputTokens
	}

	// TotalTokens = original input_tokens (which includes cache) + output
	p.usage.TotalTokens = p.usage.InputTokens + p.usage.OutputTokens +
		p.usage.CacheCreationInputTokens + p.usage.CacheReadInputTokens
}

// RESPONSE CAPTURE & JSON-TO-SSE CONVERSION

// responseCaptureWriter captures an http.ResponseWriter's output in memory.
// Used to intercept handleNonStreaming's output so we can convert it to SSE.
type responseCaptureWriter struct {
	header     http.Header
	body       bytes.Buffer
	statusCode int
}

func (w *responseCaptureWriter) Header() http.Header         { return w.header }
func (w *responseCaptureWriter) WriteHeader(statusCode int)  { w.statusCode = statusCode }
func (w *responseCaptureWriter) Write(b []byte) (int, error) { return w.body.Write(b) }

// jsonToAnthropicSSE converts an Anthropic non-streaming JSON response to SSE events.
// Follows the same event structure as BuildSavingsResponse in monitoring/savings.go.
func jsonToAnthropicSSE(jsonBody []byte) []byte {
	var response map[string]any
	if err := json.Unmarshal(jsonBody, &response); err != nil {
		log.Warn().Err(err).Msg("jsonToAnthropicSSE: failed to parse response, returning raw body")
		return jsonBody
	}

	var b strings.Builder

	// Extract fields
	content, _ := response["content"].([]any)
	usage, _ := response["usage"].(map[string]any)
	inputTokens := 0
	outputTokens := 0
	if usage != nil {
		if v, ok := usage["input_tokens"].(float64); ok {
			inputTokens = int(v)
		}
		if v, ok := usage["output_tokens"].(float64); ok {
			outputTokens = int(v)
		}
	}

	// event: message_start
	msgStart := map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": response["id"], "type": "message", "role": "assistant",
			"model": response["model"], "stop_reason": nil, "stop_sequence": nil,
			"content": []any{},
			"usage": map[string]any{
				"input_tokens": inputTokens, "output_tokens": 0,
				"cache_creation_input_tokens": getIntFromMap(usage, "cache_creation_input_tokens"),
				"cache_read_input_tokens":     getIntFromMap(usage, "cache_read_input_tokens"),
			},
		},
	}
	writeSSEEvent(&b, "message_start", msgStart)

	// Content blocks
	for i, block := range content {
		blockMap, ok := block.(map[string]any)
		if !ok {
			continue
		}

		blockType, _ := blockMap["type"].(string)

		switch blockType {
		case "text":
			text, _ := blockMap["text"].(string)
			// content_block_start
			writeSSEEvent(&b, "content_block_start", map[string]any{
				"type": "content_block_start", "index": i,
				"content_block": map[string]any{"type": "text", "text": ""},
			})
			// content_block_delta
			writeSSEEvent(&b, "content_block_delta", map[string]any{
				"type": "content_block_delta", "index": i,
				"delta": map[string]any{"type": "text_delta", "text": text},
			})
			// content_block_stop
			writeSSEEvent(&b, "content_block_stop", map[string]any{
				"type": "content_block_stop", "index": i,
			})

		case "tool_use":
			name, _ := blockMap["name"].(string)
			id, _ := blockMap["id"].(string)
			input, _ := blockMap["input"].(map[string]any)

			// content_block_start (tool_use with empty input)
			writeSSEEvent(&b, "content_block_start", map[string]any{
				"type": "content_block_start", "index": i,
				"content_block": map[string]any{
					"type": "tool_use", "id": id, "name": name, "input": map[string]any{},
				},
			})
			// content_block_delta (input as JSON)
			inputJSON, _ := json.Marshal(input)
			writeSSEEvent(&b, "content_block_delta", map[string]any{
				"type": "content_block_delta", "index": i,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": string(inputJSON)},
			})
			// content_block_stop
			writeSSEEvent(&b, "content_block_stop", map[string]any{
				"type": "content_block_stop", "index": i,
			})

		default:
			// Unknown block type — emit as-is
			writeSSEEvent(&b, "content_block_start", map[string]any{
				"type": "content_block_start", "index": i,
				"content_block": blockMap,
			})
			writeSSEEvent(&b, "content_block_stop", map[string]any{
				"type": "content_block_stop", "index": i,
			})
		}
	}

	// event: message_delta
	writeSSEEvent(&b, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": response["stop_reason"], "stop_sequence": response["stop_sequence"]},
		"usage": map[string]any{"output_tokens": outputTokens},
	})

	// event: message_stop
	writeSSEEvent(&b, "message_stop", map[string]any{"type": "message_stop"})

	return []byte(b.String())
}

// jsonToOpenAISSE converts an OpenAI non-streaming JSON response to SSE events.
func jsonToOpenAISSE(jsonBody []byte) []byte {
	var response map[string]any
	if err := json.Unmarshal(jsonBody, &response); err != nil {
		log.Warn().Err(err).Msg("jsonToOpenAISSE: failed to parse response, returning raw body")
		return jsonBody
	}

	var b strings.Builder

	choices, _ := response["choices"].([]any)
	if len(choices) == 0 {
		// No choices — just wrap as a single data event
		b.WriteString("data: ")
		b.Write(jsonBody)
		b.WriteString("\n\ndata: [DONE]\n\n")
		return []byte(b.String())
	}

	// Build streaming delta from the non-streaming message
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	finishReason, _ := choice["finish_reason"].(string)

	// First chunk: delta with content
	delta := make(map[string]any)
	if role, ok := message["role"].(string); ok {
		delta["role"] = role
	}
	if content, ok := message["content"].(string); ok && content != "" {
		delta["content"] = content
	}
	if toolCalls, ok := message["tool_calls"].([]any); ok {
		delta["tool_calls"] = toolCalls
	}

	chunk1 := map[string]any{
		"id":      response["id"],
		"object":  "chat.completion.chunk",
		"created": response["created"],
		"model":   response["model"],
		"choices": []any{map[string]any{
			"index": 0, "delta": delta, "finish_reason": nil,
		}},
	}
	data1, _ := json.Marshal(chunk1)
	fmt.Fprintf(&b, "data: %s\n\n", data1)

	// Final chunk: finish_reason
	chunk2 := map[string]any{
		"id":      response["id"],
		"object":  "chat.completion.chunk",
		"created": response["created"],
		"model":   response["model"],
		"choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{}, "finish_reason": finishReason,
		}},
	}
	if usage, ok := response["usage"].(map[string]any); ok {
		chunk2["usage"] = usage
	}
	data2, _ := json.Marshal(chunk2)
	fmt.Fprintf(&b, "data: %s\n\n", data2)

	b.WriteString("data: [DONE]\n\n")

	return []byte(b.String())
}

// jsonToResponsesAPISSE converts a non-streaming Responses API JSON response to SSE events.
// Responses API uses typed events (response.created, response.output_text.delta, response.completed)
// instead of Chat Completions format (choices[], data: [DONE]).
func jsonToResponsesAPISSE(jsonBody []byte) []byte {
	var response map[string]any
	if err := json.Unmarshal(jsonBody, &response); err != nil {
		log.Warn().Err(err).Msg("jsonToResponsesAPISSE: failed to parse response, returning raw body")
		return jsonBody
	}

	var b strings.Builder
	responseID, _ := response["id"].(string)
	model, _ := response["model"].(string)

	// event: response.created
	writeSSEEvent(&b, "response.created", map[string]any{
		"type":     "response.created",
		"response": map[string]any{"id": responseID, "model": model, "status": "in_progress"},
	})

	// event: response.in_progress
	writeSSEEvent(&b, "response.in_progress", map[string]any{
		"type":     "response.in_progress",
		"response": map[string]any{"id": responseID, "model": model, "status": "in_progress"},
	})

	// Emit output items from response.output[]
	outputItems, _ := response["output"].([]any)
	for idx, item := range outputItems {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}

		writeSSEEvent(&b, "response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"output_index": idx,
			"item":         itemMap,
		})

		itemType, _ := itemMap["type"].(string)
		switch itemType {
		case "message":
			contentList, _ := itemMap["content"].([]any)
			for ci, contentAny := range contentList {
				contentMap, ok := contentAny.(map[string]any)
				if !ok {
					continue
				}
				contentType, _ := contentMap["type"].(string)
				if contentType == "output_text" {
					text, _ := contentMap["text"].(string)
					writeSSEEvent(&b, "response.content_part.added", map[string]any{
						"type":          "response.content_part.added",
						"output_index":  idx,
						"content_index": ci,
						"part":          map[string]any{"type": "output_text", "text": ""},
					})
					writeSSEEvent(&b, "response.output_text.delta", map[string]any{
						"type":          "response.output_text.delta",
						"output_index":  idx,
						"content_index": ci,
						"delta":         text,
					})
					writeSSEEvent(&b, "response.output_text.done", map[string]any{
						"type":          "response.output_text.done",
						"output_index":  idx,
						"content_index": ci,
						"text":          text,
					})
					writeSSEEvent(&b, "response.content_part.done", map[string]any{
						"type":          "response.content_part.done",
						"output_index":  idx,
						"content_index": ci,
						"part":          contentMap,
					})
				}
			}
		case "function_call":
			args, _ := itemMap["arguments"].(string)
			writeSSEEvent(&b, "response.function_call_arguments.delta", map[string]any{
				"type":         "response.function_call_arguments.delta",
				"output_index": idx,
				"delta":        args,
			})
			writeSSEEvent(&b, "response.function_call_arguments.done", map[string]any{
				"type":         "response.function_call_arguments.done",
				"output_index": idx,
				"arguments":    args,
			})
		}

		writeSSEEvent(&b, "response.output_item.done", map[string]any{
			"type":         "response.output_item.done",
			"output_index": idx,
			"item":         itemMap,
		})
	}

	// Build usage for response.completed
	usage, _ := response["usage"].(map[string]any)
	inputTokens := getIntFromMap(usage, "input_tokens")
	outputTokens := getIntFromMap(usage, "output_tokens")
	if inputTokens == 0 {
		inputTokens = getIntFromMap(usage, "prompt_tokens")
	}
	if outputTokens == 0 {
		outputTokens = getIntFromMap(usage, "completion_tokens")
	}

	// event: response.completed
	writeSSEEvent(&b, "response.completed", map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":     responseID,
			"model":  model,
			"status": "completed",
			"output": outputItems,
			"usage": map[string]any{
				"input_tokens":  inputTokens,
				"output_tokens": outputTokens,
				"total_tokens":  inputTokens + outputTokens,
			},
		},
	})

	return []byte(b.String())
}

// writeSSEEvent writes a single SSE event line.
func writeSSEEvent(b *strings.Builder, event string, data any) {
	jsonData, _ := json.Marshal(data)
	b.WriteString("event: ")
	b.WriteString(event)
	b.WriteString("\ndata: ")
	b.Write(jsonData)
	b.WriteString("\n\n")
}

// getIntFromMap safely extracts an int from a map[string]any (JSON numbers are float64).
// Uses an explicit int64 intermediate to prevent silent overflow on 32-bit systems.
func getIntFromMap(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	if v, ok := m[key].(float64); ok {
		return int(int64(v))
	}
	return 0
}

// buildExpandAppendBody appends the assistant's expand_context tool call and the
// tool results with expanded content to the request body. Uses sjson to append
// messages at the end, preserving the entire KV-cache prefix.
func buildExpandAppendBody(body []byte, expandCalls []tooloutput.ExpandContextCall, toolResults []map[string]any, adapter adapters.Adapter) ([]byte, error) {
	modified := body

	if adapter.Provider() == adapters.ProviderAnthropic || adapter.Provider() == adapters.ProviderBedrock {
		// Anthropic: append assistant message with expand_context tool_use blocks
		contentBlocks := make([]any, 0, len(expandCalls))
		for _, ec := range expandCalls {
			contentBlocks = append(contentBlocks, map[string]any{
				"type": "tool_use",
				"id":   ec.ToolUseID,
				"name": ExpandContextToolName,
				"input": map[string]any{
					"id": ec.ShadowID,
				},
			})
		}
		assistantMsg := map[string]any{
			"role":    "assistant",
			"content": contentBlocks,
		}
		assistantJSON, err := json.Marshal(assistantMsg)
		if err != nil {
			return body, fmt.Errorf("marshal assistant message: %w", err)
		}
		modified, err = sjson.SetRawBytes(modified, "messages.-1", assistantJSON)
		if err != nil {
			return body, fmt.Errorf("append assistant message: %w", err)
		}

		// Append tool_results (already formatted by ExpandContextHandler)
		for _, tr := range toolResults {
			trJSON, err := json.Marshal(tr)
			if err != nil {
				log.Warn().Err(err).Msg("buildExpandAppendBody: failed to marshal tool result")
				continue
			}
			modified, err = sjson.SetRawBytes(modified, "messages.-1", trJSON)
			if err != nil {
				log.Warn().Err(err).Msg("buildExpandAppendBody: failed to append tool result")
				continue
			}
		}
	} else {
		isResponses := isResponsesAPI(body)

		if isResponses {
			// Responses API: append function_call and function_call_output items to input[]
			for _, ec := range expandCalls {
				funcCall := map[string]any{
					"type":      "function_call",
					"call_id":   ec.ToolUseID,
					"name":      ExpandContextToolName,
					"arguments": fmt.Sprintf(`{"id":"%s"}`, ec.ShadowID),
				}
				fcJSON, err := json.Marshal(funcCall)
				if err != nil {
					return body, fmt.Errorf("marshal function_call: %w", err)
				}
				modified, err = sjson.SetRawBytes(modified, "input.-1", fcJSON)
				if err != nil {
					return body, fmt.Errorf("append function_call: %w", err)
				}
			}

			// Append function_call_output items
			for _, tr := range toolResults {
				content, _ := tr["content"].(string)
				if content == "" {
					contentBytes, _ := json.Marshal(tr["content"])
					content = string(contentBytes)
				}
				callID, _ := tr["call_id"].(string)
				if callID == "" {
					callID, _ = tr["tool_call_id"].(string)
				}
				funcOutput := map[string]any{
					"type":    "function_call_output",
					"call_id": callID,
					"output":  content,
				}
				foJSON, err := json.Marshal(funcOutput)
				if err != nil {
					log.Warn().Err(err).Msg("buildExpandAppendBody: failed to marshal function_call_output")
					continue
				}
				modified, err = sjson.SetRawBytes(modified, "input.-1", foJSON)
				if err != nil {
					log.Warn().Err(err).Msg("buildExpandAppendBody: failed to append function_call_output")
					continue
				}
			}
		} else {
			// OpenAI Chat Completions: append assistant message with tool_calls, then tool messages
			var toolCallDefs []any
			for _, ec := range expandCalls {
				toolCallDefs = append(toolCallDefs, map[string]any{
					"id":   ec.ToolUseID,
					"type": "function",
					"function": map[string]any{
						"name":      ExpandContextToolName,
						"arguments": fmt.Sprintf(`{"id":"%s"}`, ec.ShadowID),
					},
				})
			}
			assistantMsg := map[string]any{
				"role":       "assistant",
				"content":    nil,
				"tool_calls": toolCallDefs,
			}
			assistantJSON, err := json.Marshal(assistantMsg)
			if err != nil {
				return body, fmt.Errorf("marshal assistant message: %w", err)
			}
			modified, err = sjson.SetRawBytes(modified, "messages.-1", assistantJSON)
			if err != nil {
				return body, fmt.Errorf("append assistant message: %w", err)
			}

			// Append tool result messages
			for _, tr := range toolResults {
				trJSON, err := json.Marshal(tr)
				if err != nil {
					log.Warn().Err(err).Msg("buildExpandAppendBody: failed to marshal tool result")
					continue
				}
				modified, err = sjson.SetRawBytes(modified, "messages.-1", trJSON)
				if err != nil {
					log.Warn().Err(err).Msg("buildExpandAppendBody: failed to append tool result")
					continue
				}
			}
		}
	}

	return modified, nil
}

// removeToolFromRequest removes a tool by name from the tools[] array.
// Uses gjson to find and sjson to rebuild, preserving KV-cache for other tools.
func removeToolFromRequest(body []byte, toolName string) []byte {
	toolsResult := gjson.GetBytes(body, "tools")
	if !toolsResult.Exists() || !toolsResult.IsArray() {
		return body
	}

	isResponses := isResponsesAPI(body)

	var kept []byte
	kept = append(kept, '[')
	first := true
	toolsResult.ForEach(func(_, value gjson.Result) bool {
		var name string
		if isResponses {
			name = value.Get("name").String()
		} else {
			name = value.Get("function.name").String()
			if name == "" {
				name = value.Get("name").String()
			}
		}
		if name == toolName {
			return true // skip
		}
		if !first {
			kept = append(kept, ',')
		}
		kept = append(kept, value.Raw...)
		first = false
		return true
	})
	kept = append(kept, ']')

	result, err := sjson.SetRawBytes(body, "tools", kept)
	if err != nil {
		return body
	}
	return result
}
