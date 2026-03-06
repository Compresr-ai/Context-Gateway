// Streaming request handling with expand_context support and SSE usage parsing.
package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"

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
			provider: provider, pipeType: pipeType, pipeStrategy: pipeStrategy + "_streaming", originalBodySize: originalBodySize,
			compressionUsed: compressionUsed, statusCode: 502, errorMsg: err.Error(),
			compressLatency: compressLatency, forwardLatency: time.Since(forwardStart), pipeCtx: pipeCtx,
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody,
			authModeInitial: authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
			requestHeaders: r.Header, responseHeaders: nil, upstreamURL: "", fallbackReason: "",
		})
		log.Error().Err(err).Str("request_id", requestID).Msg("upstream streaming request failed")
		g.writeError(w, "upstream request failed", http.StatusBadGateway)
		return
	}

	// If expand not enabled, stream directly
	if !expandEnabled || !compressionUsed || len(pipeCtx.ShadowRefs) == 0 {
		defer func() { _ = resp.Body.Close() }()
		writeStreamingHeaders(w, resp.Header, pipeCtx.PreemptiveHeaders)
		w.WriteHeader(resp.StatusCode)
		sseUsage := g.streamResponse(w, resp.Body)

		g.recordRequestTelemetry(telemetryParams{
			requestID: requestID, startTime: startTime, method: r.Method, path: r.URL.Path,
			clientIP: r.RemoteAddr, requestBodySize: len(originalBody), responseBodySize: 0,
			provider: provider, pipeType: pipeType, pipeStrategy: pipeStrategy + "_streaming", originalBodySize: originalBodySize,
			compressionUsed: compressionUsed, statusCode: resp.StatusCode,
			compressLatency: compressLatency, forwardLatency: time.Since(forwardStart), pipeCtx: pipeCtx,
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody, streamUsage: &sseUsage,
			authModeInitial: authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
			requestHeaders: r.Header, responseHeaders: resp.Header, upstreamURL: resp.Request.URL.String(), fallbackReason: "",
		})
		// Log for each pipe that ran
		if len(pipeCtx.ToolOutputCompressions) > 0 || pipeCtx.OutputCompressed {
			g.logCompressionDetails(pipeCtx, requestID, string(PipeToolOutput), originalBody, forwardBody)
		}
		if pipeCtx.FilteredToolCount > 0 || pipeCtx.ToolsFiltered {
			g.logCompressionDetails(pipeCtx, requestID, string(PipeToolDiscovery), originalBody, forwardBody)
		}
		return
	}

	// expand_context enabled: buffer response to detect expand calls
	streamBuffer := tooloutput.NewStreamBuffer()
	usageParser := newSSEUsageParser()
	var bufferedChunks [][]byte

	// Read and buffer the entire stream (bounded to prevent OOM)
	buf := make([]byte, DefaultBufferSize)
	totalBuffered := 0
	for {
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
			g.flushBufferedResponse(w, resp.Header, pipeCtx.PreemptiveHeaders, bufferedChunks, resp.StatusCode)
			return
		}

		// Invalidate compressed mappings for expanded IDs
		g.expander.InvalidateExpandedMappings(expandedIDs)

		// Re-send with rewritten history
		retryResp, retryMeta, err := g.forwardPassthrough(r.Context(), r, rewrittenBody)
		if err != nil {
			log.Error().Err(err).Msg("streaming: failed to re-send after expansion")
			g.flushBufferedResponse(w, resp.Header, pipeCtx.PreemptiveHeaders, bufferedChunks, resp.StatusCode)
			return
		}
		mergeForwardAuthMeta(&authMeta, retryMeta)
		defer func() { _ = retryResp.Body.Close() }()

		// Stream the retry response (filter expand_context if present)
		writeStreamingHeaders(w, retryResp.Header, pipeCtx.PreemptiveHeaders)
		w.WriteHeader(retryResp.StatusCode)

		g.streamResponseWithFilter(w, retryResp.Body)

		g.recordRequestTelemetry(telemetryParams{
			requestID: requestID, startTime: startTime, method: r.Method, path: r.URL.Path,
			clientIP: r.RemoteAddr, requestBodySize: len(originalBody), responseBodySize: 0,
			provider: provider, pipeType: pipeType, pipeStrategy: pipeStrategy + "_streaming_expanded",
			originalBodySize: originalBodySize, compressionUsed: compressionUsed, statusCode: retryResp.StatusCode,
			compressLatency: compressLatency, forwardLatency: time.Since(forwardStart), pipeCtx: pipeCtx,
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody, streamUsage: &bufferedUsage,
			authModeInitial: authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
			requestHeaders: r.Header, responseHeaders: retryResp.Header, upstreamURL: retryResp.Request.URL.String(), fallbackReason: "",
		})

		log.Info().
			Int("expanded_ids", len(expandedIDs)).
			Str("request_id", requestID).
			Msg("streaming: expansion complete")
	} else {
		// No expand_context detected - flush buffered response
		g.flushBufferedResponse(w, resp.Header, pipeCtx.PreemptiveHeaders, bufferedChunks, resp.StatusCode)

		// If stream was truncated, inject an SSE error event so the client knows
		if pipeCtx.StreamTruncated {
			errorEvent := []byte("event: error\ndata: {\"type\":\"stream_truncated\",\"message\":\"Response exceeded buffer limit\"}\n\n")
			_, _ = w.Write(errorEvent)
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
			adapter: adapter, requestBody: originalBody, forwardBody: forwardBody, streamUsage: &bufferedUsage,
			authModeInitial: authMeta.InitialMode, authModeEffective: authMeta.EffectiveMode, authFallbackUsed: authMeta.FallbackUsed,
			requestHeaders: r.Header, responseHeaders: resp.Header, upstreamURL: resp.Request.URL.String(), fallbackReason: "",
		})
	}

	// Log for each pipe that ran
	if len(pipeCtx.ToolOutputCompressions) > 0 || pipeCtx.OutputCompressed {
		g.logCompressionDetails(pipeCtx, requestID, string(PipeToolOutput), originalBody, forwardBody)
	}
	if pipeCtx.FilteredToolCount > 0 || pipeCtx.ToolsFiltered {
		g.logCompressionDetails(pipeCtx, requestID, string(PipeToolDiscovery), originalBody, forwardBody)
	}
}

// writeStreamingHeaders sets common streaming response headers.
func writeStreamingHeaders(w http.ResponseWriter, upstream http.Header, preemptiveHeaders map[string]string) {
	copyHeaders(w, upstream)
	addPreemptiveHeaders(w, preemptiveHeaders)
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

// =============================================================================
// SSE Usage Parser
// =============================================================================

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
		// Anthropic's input_tokens includes cache tokens; subtract them
		// so InputTokens represents only non-cached input (avoids double-counting in cost calculation).
		nonCached := u.InputTokens - u.CacheCreationInputTokens - u.CacheReadInputTokens
		if nonCached < 0 {
			nonCached = 0
		}
		log.Debug().
			Int("raw_input", u.InputTokens).
			Int("cache_create", u.CacheCreationInputTokens).
			Int("cache_read", u.CacheReadInputTokens).
			Int("non_cached", nonCached).
			Int("output", u.OutputTokens).
			Msg("sse_usage: applyUsage")
		p.usage.InputTokens = nonCached
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

	// TotalTokens = original input_tokens (which includes cache) + output
	p.usage.TotalTokens = p.usage.InputTokens + p.usage.OutputTokens +
		p.usage.CacheCreationInputTokens + p.usage.CacheReadInputTokens
}
