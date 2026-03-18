// HTTP request handling for the compression gateway.
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
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/compresr/context-gateway/internal/adapters"
	authtypes "github.com/compresr/context-gateway/internal/auth/types"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/dashboard"
	"github.com/compresr/context-gateway/internal/monitoring"
	phantom_tools "github.com/compresr/context-gateway/internal/phantom_tools"
	"github.com/compresr/context-gateway/internal/preemptive"
	"github.com/compresr/context-gateway/internal/prompthistory"
	"github.com/compresr/context-gateway/internal/tokenizer"
	"github.com/compresr/context-gateway/internal/utils"
)

// injectedTagPrefixes are XML tag prefixes used by Claude Code / IDE integrations
// to inject system content into user messages. Text blocks containing these are not user-typed.
var injectedTagPrefixes = []string{
	"<system-reminder>",
	"<available-deferred-tools>",
	"<user-prompt-submit-hook>",
	"<fast_mode_info>",
	"<command-name>",
	"<local-command-caveat>",
	"<antml_thinking>",
	"<antml_thinking_mode>",
	"<antml_reasoning_effort>",
}

// isInjectedText returns true if a text block contains system-injected content
// rather than user-typed text.
func isInjectedText(text string) bool {
	trimmed := strings.TrimSpace(text)
	for _, prefix := range injectedTagPrefixes {
		if strings.Contains(trimmed, prefix) {
			return true
		}
	}
	return false
}

// extractCleanUserPrompt parses the request body and extracts only genuinely
// user-typed text from the last message. Returns non-empty only when the last
// message (role "user") contains text blocks that are NOT tool_result and NOT
// system-injected content (e.g. <system-reminder>, <available-deferred-tools>).
//
// This correctly handles:
//   - First user message → returns the user's text
//   - Tool loop (only tool_result blocks) → returns ""
//   - User feedback alongside tool_result (Bug D) → returns the user text
//   - New user turn with injected system content → returns clean text only
//   - Only injected content → returns ""
//
// Supports Anthropic (content blocks), OpenAI (tool_calls), and Responses API formats.
func extractCleanUserPrompt(body []byte) string {
	// Try Responses API format first (Codex): "input" present AND "messages" absent.
	// Must check both — some providers send both fields; only pure Responses API omits "messages".
	input := gjson.GetBytes(body, "input")
	messages := gjson.GetBytes(body, "messages")
	if input.Exists() && !messages.Exists() {
		return extractCleanUserPromptFromResponsesAPI(input)
	}

	// Chat Completions / Anthropic format: "messages" array
	if !messages.IsArray() {
		return ""
	}

	arr := messages.Array()
	if len(arr) == 0 {
		return ""
	}

	// The very last message must be role "user". If it's "tool" (OpenAI format),
	// "assistant", or anything else, we're in a tool loop — not a new user turn.
	lastMsg := arr[len(arr)-1]
	if lastMsg.Get("role").String() != "user" {
		return ""
	}

	lastUserContent := lastMsg.Get("content")
	if !lastUserContent.Exists() {
		return ""
	}

	// If content is a string, it's always user-typed (simple format)
	if lastUserContent.Type == gjson.String {
		text := lastUserContent.String()
		if isInjectedText(text) {
			return ""
		}
		return strings.TrimSpace(text)
	}

	// Content is an array of blocks.
	// Extract only genuine user-typed text blocks: skip tool_result and injected content.
	// This correctly handles:
	//   - Pure tool loops (only tool_result blocks) → returns ""
	//   - User text alongside tool_result (Bug D: user feedback during tool approval) → returns text
	//   - New user turn with injected system content → returns clean text
	//   - Only injected content (system-reminders, etc.) → returns ""
	if !lastUserContent.IsArray() {
		return ""
	}

	blocks := lastUserContent.Array()

	// Pre-scan: if ANY text block contains <command-name>, the entire message
	// is a skill/command expansion — the user typed a slash command (e.g. /security-scan)
	// which was replaced by expanded instructions across multiple text blocks.
	// Only the <command-name> block has a recognizable tag; the expansion body is
	// plain text that would otherwise pass through isInjectedText. Skip everything.
	for _, block := range blocks {
		if block.Get("type").String() == "text" {
			if strings.Contains(block.Get("text").String(), "<command-name>") {
				return ""
			}
		}
	}

	userTexts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Get("type").String() != "text" {
			continue
		}
		text := block.Get("text").String()
		if text == "" || isInjectedText(text) {
			continue
		}
		userTexts = append(userTexts, strings.TrimSpace(text))
	}

	return strings.TrimSpace(strings.Join(userTexts, "\n"))
}

// extractCleanUserPromptFromResponsesAPI extracts the user-typed prompt from
// the OpenAI Responses API format (used by Codex).
// Format: "input": "string" or "input": [{type: "message", role: "user", content: "..."}]
// Skips tool-loop turns: if the last item is a function_call_output, the user didn't type anything.
func extractCleanUserPromptFromResponsesAPI(input gjson.Result) string {
	// Simple string input: "input": "Say hello"
	if input.Type == gjson.String {
		text := strings.TrimSpace(input.String())
		if text == "" || isInjectedText(text) {
			return ""
		}
		return text
	}

	// Array input: "input": [{type: "message", role: "user", ...}, {type: "function_call", ...}, ...]
	if !input.IsArray() {
		return ""
	}

	items := input.Array()
	if len(items) == 0 {
		return ""
	}

	// If the last item is a function_call_output, we're in a tool loop — not a user turn.
	lastItem := items[len(items)-1]
	lastType := lastItem.Get("type").String()
	if lastType == "function_call_output" {
		return ""
	}

	// If the last item is a function_call, the assistant is calling a tool — not a user turn.
	if lastType == "function_call" {
		return ""
	}

	// Find the last user message by scanning backwards.
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		if item.Get("type").String() != "message" || item.Get("role").String() != "user" {
			continue
		}

		// Check if there's a function_call after this user message (tool loop).
		for j := i + 1; j < len(items); j++ {
			jType := items[j].Get("type").String()
			if jType == "function_call" {
				return "" // Assistant used a tool after this user message — automated turn
			}
		}

		// Extract content (string or array of content blocks)
		content := item.Get("content")
		if content.Type == gjson.String {
			text := strings.TrimSpace(content.String())
			if text == "" || isInjectedText(text) {
				return ""
			}
			return text
		}
		if content.IsArray() {
			var texts []string
			for _, block := range content.Array() {
				// Responses API uses "input_text" type; Chat Completions uses "text"
				bt := block.Get("type").String()
				if bt != "text" && bt != "input_text" {
					continue
				}
				text := block.Get("text").String()
				if text != "" && !isInjectedText(text) {
					texts = append(texts, strings.TrimSpace(text))
				}
			}
			return strings.TrimSpace(strings.Join(texts, "\n"))
		}

		return ""
	}

	return ""
}

// isMainAgentRequest checks if the request is from a main coding agent
// (not a subagent). Subagents have short task-specific system prompts, while
// the main agent has the full system prompt (e.g. "You are Claude Code").
func isMainAgentRequest(body []byte) bool {
	// Check the top-level "system" field (Anthropic format)
	sys := gjson.GetBytes(body, "system")
	if sys.Exists() {
		sysText := ""
		if sys.IsArray() {
			// system can be array of content blocks
			for _, block := range sys.Array() {
				if block.Get("type").String() == "text" {
					sysText += block.Get("text").String()
				}
			}
		} else {
			sysText = sys.String()
		}
		// Main Claude Code agent has this in its system prompt
		if strings.Contains(sysText, "You are Claude Code") {
			return true
		}
		// If there's a system prompt but it doesn't match, it's a subagent or other tool
		if len(sysText) > 0 {
			return false
		}
	}

	// Check Responses API format (Codex): has "input" field.
	// Codex doesn't use subagents, so any Responses API request is a main agent request.
	if gjson.GetBytes(body, "input").Exists() {
		return true
	}

	// Check OpenAI Chat Completions format: first message with role "system" or "developer"
	messages := gjson.GetBytes(body, "messages")
	if messages.IsArray() {
		for _, msg := range messages.Array() {
			role := msg.Get("role").String()
			if role == "system" || role == "developer" {
				content := msg.Get("content").String()
				if strings.Contains(content, "You are Claude Code") {
					return true
				}
				return false // Has system message but not main agent
			}
		}
	}

	// No system prompt found — assume subagent or tool call, not the main agent.
	// The main Claude Code agent always sends a system prompt containing "You are Claude Code".
	return false
}

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
// Uses sjson for byte-level replacement to preserve JSON field ordering and KV-cache prefix.
// Handles formats like "anthropic/claude-3" -> "claude-3", "openai/gpt-4" -> "gpt-4"
func sanitizeModelName(body []byte) []byte {
	model := gjson.GetBytes(body, "model").String()
	if model == "" {
		return body
	}

	for _, prefix := range []string{"anthropic/", "openai/", "google/", "meta/"} {
		if strings.HasPrefix(model, prefix) {
			stripped := strings.TrimPrefix(model, prefix)
			if result, err := sjson.SetBytes(body, "model", stripped); err == nil {
				return result
			}
			break
		}
	}

	return body
}

// writeError writes a JSON error response.
func (g *Gateway) writeError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"message": msg, "type": "gateway_error"},
	}); err != nil {
		log.Warn().Err(err).Msg("writeError: failed to encode JSON error response")
	}
}

// handleHealth returns gateway health status.
func (g *Gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	health := map[string]any{
		"status":  "ok",
		"time":    time.Now().Format(time.RFC3339),
		"version": g.version,
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
	if err := json.NewEncoder(w).Encode(health); err != nil {
		log.Warn().Err(err).Msg("handleHealth: failed to encode JSON response")
	}
}

// handleExpand retrieves raw data from shadow context.
// Restricted to localhost to prevent external access to compressed context data.
func (g *Gateway) handleExpand(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		g.writeError(w, "forbidden", http.StatusForbidden)
		return
	}

	if r.Method != http.MethodPost {
		g.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1024)

	var req struct {
		ID string `json:"id"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || len(req.ID) == 0 || len(req.ID) > 64 {
		g.writeError(w, "invalid request", http.StatusBadRequest)
		return
	}

	data, ok := g.store.Get(req.ID)
	g.tracker.RecordExpand(&monitoring.ExpandEvent{
		Timestamp: time.Now(), ShadowRefID: req.ID, Found: ok, Success: ok,
	})
	if g.expandLog != nil {
		preview := data
		if len(preview) > 100 {
			preview = preview[:100]
		}
		g.expandLog.Record(monitoring.ExpandLogEntry{
			Timestamp:      time.Now(),
			RequestID:      g.getRequestID(r),
			ShadowID:       req.ID,
			Found:          ok,
			ContentPreview: preview,
			ContentLength:  len(data),
			ContentTokens:  tokenizer.CountTokens(data),
		})
	}

	if !ok {
		g.writeError(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"id": req.ID, "content": data}); err != nil {
		log.Warn().Err(err).Msg("handleExpand: failed to encode JSON response")
	}
}

// detectClientAgent identifies which AI client is making a request from its
// User-Agent header. Used by the task_output pipe to select the correct schema.
//
// Detection heuristics (order matters — more specific first):
//   - "claude-code" in User-Agent → ClientClaudeCode
//   - "codex" in User-Agent       → ClientCodex
//   - fallback                    → ClientGeneric (matches no task tools)
func detectClientAgent(headers http.Header) string {
	ua := strings.ToLower(headers.Get("User-Agent"))
	switch {
	case strings.Contains(ua, "claude-code") || strings.Contains(ua, "claude_code"):
		return "claude_code"
	case strings.Contains(ua, "codex"):
		return "codex"
	default:
		return "generic"
	}
}

// handleProxy processes requests through the compression pipeline.
func (g *Gateway) handleProxy(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	requestID := g.getRequestID(r)

	// Validate request
	if r.Method != http.MethodPost {
		g.alerts.FlagInvalidRequest(requestID, "method not allowed", nil)
		g.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
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

		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize))
		copyHeaders(w, resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(responseBody)
		return
	}

	// Lazy session initialization: create session directory on first actual LLM request.
	// This prevents empty session folders when gateway starts but receives no LLM traffic.
	g.EnsureSession()

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
	pipeCtx.RequestCtx = r.Context()
	pipeCtx.RequestID = requestID
	// Initialize tool session for hybrid tool discovery
	// Use canonical session ID from preemptive package (hash of first user message)
	if g.toolSessions != nil && g.cfg().Pipes.ToolDiscovery.Enabled {
		// Use clean first-user-message hash so session ID is stable across turns
		// even when phantom tools are injected (injected XML changes full-body hash).
		sessionID := preemptive.ComputeSessionIDFromClean(pipeCtx.Classification.FirstUserCleanContent)
		if sessionID != "" {
			pipeCtx.ToolSessionID = sessionID
			pipeCtx.SessionID = sessionID // Also set for tool discovery pipe caching
			// BUG-027: Cache isMainAgent per session so turn 2+ doesn't mis-classify
			// when the tools array has shrunk (filtered tools removed from context).
			if cached, ok := g.toolSessions.GetIsMainAgent(sessionID); ok {
				pipeCtx.Classification.IsMainAgent = cached
			} else if pipeCtx.Classification.IsMainAgent {
				g.toolSessions.StoreIsMainAgent(sessionID, true)
			}
			// Load expanded tools from session (tools found via previous searches)
			pipeCtx.ExpandedTools = g.toolSessions.GetExpanded(sessionID)

			// Rewrite inbound messages: client-facing tool names -> gateway_search_tool
			// This ensures the LLM sees a consistent tool=[gateway_search_tool] view
			// even though the client sent real tool_use/tool_result references.
			allMappings := g.toolSessions.GetAllRewriteMappings(sessionID)
			if len(allMappings) > 0 {
				searchToolName := g.cfg().Pipes.ToolDiscovery.SearchToolName
				if searchToolName == "" {
					searchToolName = phantom_tools.SearchToolName
				}
				if rewritten, err := rewriteInboundMessages(body, allMappings, provider, searchToolName); err == nil {
					body = rewritten
					pipeCtx.OriginalRequest = body
				}
			}
		}
	}

	// Capture auth headers from incoming request using centralized helper
	// Used by pipes (tool output compression), preemptive summarizer, and session collector
	capturedAuth := authtypes.CaptureFromHeaders(r.Header)

	// Pass full auth to pipes so they can handle both API key and OAuth users
	pipeCtx.CapturedAuth = capturedAuth

	// Detect AI client agent from request headers for schema-driven task_output detection.
	pipeCtx.ClientAgent = detectClientAgent(r.Header)

	// Capture auth for post-session updater using the same captured auth
	if g.sessionCollector != nil && capturedAuth.HasAuth() {
		sessionAuth := capturedAuth
		if sessionAuth.Endpoint == "" {
			sessionAuth.Endpoint = g.autoDetectTargetURL(r)
		}
		g.sessionCollector.CaptureAuth(sessionAuth)
	}

	// Extract model for preemptive summarization and cost-based compression decisions
	model := adapter.ExtractModel(body)
	pipeCtx.Model = model
	pipeCtx.TargetModel = model // Also pass to pipe context for cost-based skip logic

	// Record session event for post-session CLAUDE.md updates
	if g.sessionCollector != nil {
		msgCount := countMessages(body)
		g.sessionCollector.RecordRequest(model, msgCount)
	}

	// Track session in monitoring dashboard.
	// Use the gateway's stable session ID (e.g. "session_1_20260315_002607") so all
	// requests in this gateway run map to exactly ONE dashboard session.
	// ComputeSessionID was previously used but falls back to a per-request UUID for
	// requests without a user message, creating phantom extra sessions.
	if g.monitorStore != nil {
		monitorSessionID := g.getCurrentSessionID()
		if monitorSessionID == "" {
			monitorSessionID = requestID // only if session dir not yet initialized
		}
		agentType := dashboard.DetectAgent(r.Header)
		g.monitorStore.Track(monitorSessionID, agentType)

		// Update session with request metadata.
		// Model is always updated (to show primary model used).
		// UserQuery is only updated from main agent requests (to avoid exposing internal prompts).
		mc := pipeCtx.Classification
		update := dashboard.SessionUpdate{
			Provider:      adapter.Name(),
			Model:         model, // Always track model (will show latest/primary model)
			ToolUsed:      dashboard.ExtractLastToolUsed(body),
			IsNewUserTurn: mc.IsNewUserTurn && mc.IsMainAgent,
			IsMainAgent:   mc.IsMainAgent,
		}
		if mc.IsMainAgent {
			update.UserQuery = dashboard.ExtractLastUserQuery(body)
		}
		g.monitorStore.Update(monitorSessionID, update)

		// Store monitor session ID in pipeline context for post-response updates
		pipeCtx.MonitorSessionID = monitorSessionID
	}

	// Record user turn metric (human-initiated prompts only, not tool loops or subagents)
	if pipeCtx.Classification.IsNewUserTurn && pipeCtx.Classification.IsMainAgent && g.metrics != nil {
		g.metrics.RecordUserTurn()
	}

	// Check for /savings command - return instant synthetic response
	// Only triggers when the LAST user message is exactly "/savings" (the slash command)
	// Uses LogAggregator (the single source of truth) for instant pre-computed response
	lastUserMsg := adapter.ExtractUserQuery(body)

	if monitoring.IsSavingsRequest(lastUserMsg) {
		extra := g.buildUnifiedReportData()
		// Scope report to current session using the folder-based session ID
		// that the aggregator uses (NOT the hash-based ComputeSessionID which won't match).
		var report string
		sr := g.getSavingsReport(g.getCurrentSessionID())
		// Override with costTracker's authoritative spend
		if extra.TotalSessionCost > 0 {
			sr.CompressedCostUSD = extra.TotalSessionCost
			sr.OriginalCostUSD = sr.CompressedCostUSD + sr.CostSavedUSD
		}
		report = monitoring.FormatUnifiedReportFromReport(sr, extra)
		if report == "" {
			report = "No savings data available"
		}
		streaming := g.isStreamingRequest(body)
		syntheticResp := monitoring.BuildSavingsResponse(report, model, streaming)
		log.Info().
			Str("request_id", requestID).
			Bool("streaming", streaming).
			Int("response_size", len(syntheticResp)).
			Msg("Returning /savings report (instant!)")

		if streaming {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.Header().Set("X-Synthetic-Response", "true")
		w.Header().Set("X-Savings-Report", "true")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(syntheticResp) // #nosec G705 -- JSON API response, not HTML
		if streaming {
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		return
	}

	// Compute a conversation-level session ID (hash of first user message).
	// This is the single source of truth used by cost tracker, prompt history, and trajectory.
	conversationSessionID := preemptive.ComputeSessionID(body)
	if conversationSessionID == "" {
		// Fallback to folder-based session ID, then "default"
		conversationSessionID = g.getCurrentSessionID()
	}
	if conversationSessionID == "" {
		// Generate a unique anonymous ID to keep sessions distinct in monitoring
		conversationSessionID = fmt.Sprintf("anon-%s", uuid.New().String()[:8])
	}
	pipeCtx.CostSessionID = conversationSessionID

	// Compute stable conversation fingerprint from clean first user message text.
	// Unlike CostSessionID (which hashes the full message including injected XML),
	// this is stable across requests because injected content is stripped before hashing.
	stableFingerprint := preemptive.ComputeSessionIDFromClean(pipeCtx.Classification.FirstUserCleanContent)
	if stableFingerprint == "" {
		stableFingerprint = conversationSessionID // fallback
	}
	pipeCtx.StableFingerprint = stableFingerprint

	// Track the main conversation for dashboard session filtering and savings.
	// Uses the stable fingerprint so it works across requests (injected XML doesn't affect it).
	g.setMainConversationOnce(stableFingerprint)

	// Cost control: budget check (before forwarding)
	if g.costTracker != nil {
		budget := g.costTracker.CheckBudget(conversationSessionID)
		if !budget.Allowed {
			g.returnBudgetExceededResponse(w, adapter.Name(), budget, conversationSessionID)
			return
		}
	}

	// Capture original body length before preemptive summarization may modify `body`
	originalBodyLen := len(body)

	// Process preemptive summarization (before compression pipeline)
	// This may modify the body if compaction is requested and ready
	// For SDK compaction with precomputed summary, may return synthetic response
	var preemptiveHeaders map[string]string
	var isCompaction bool
	var syntheticResponse []byte
	if g.preemptive != nil {
		// Resolve endpoint: X-Target-URL header > autoDetect
		xTargetURL := r.Header.Get(HeaderTargetURL)
		targetURL := xTargetURL
		if targetURL == "" {
			targetURL = g.autoDetectTargetURL(r)
		}
		// Pass full auth struct to summarizer — single call, single source of truth
		authForSummarizer := capturedAuth
		authForSummarizer.Endpoint = targetURL
		if authForSummarizer.HasAuth() || authForSummarizer.Endpoint != "" {
			log.Debug().
				Str("auth_type", map[bool]string{true: "x-api-key", false: "Authorization"}[capturedAuth.IsXAPIKey]).
				Str("auth", utils.MaskKey(capturedAuth.Token)).
				Str("endpoint", targetURL).
				Msg("Passing auth to summarizer")
			g.preemptive.SetAuth(authForSummarizer)
		}

		// Pass URL path to preemptive manager for path-based compaction detection (e.g., /responses/compact for Codex)
		requestHeaders := r.Header.Clone()
		requestHeaders.Set("X-Request-Path", r.URL.Path)

		var preemptiveBody []byte
		preemptiveBody, isCompaction, syntheticResponse, preemptiveHeaders, _ = g.preemptive.ProcessRequest(r.Context(), requestHeaders, body, model, adapter.Name())

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
			_, _ = w.Write(syntheticResponse) // #nosec G705 -- JSON API response, not HTML

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
				originalBodySize: len(body),
				compressionUsed:  false,
				statusCode:       http.StatusOK,
				compressLatency:  0,
				forwardLatency:   0,
				pipeCtx:          pipeCtx,
				adapter:          adapter,
				requestBody:      body,
				responseBody:     syntheticResponse,
				forwardBody:      nil,
				requestHeaders:   r.Header,
				responseHeaders:  w.Header(),
				upstreamURL:      "preemptive_summarization",
				fallbackReason:   "",
			})
			return
		}

		// Record compaction for post-session collector
		if isCompaction && g.sessionCollector != nil {
			g.sessionCollector.RecordCompaction(model)
		}

		if isCompaction && preemptiveBody != nil && len(preemptiveBody) > 0 {
			// Merge compacted messages with original request (preserve model, tools, etc.)
			if merged, err := mergeCompactedWithOriginal(preemptiveBody, body); err == nil {
				// Record preemptive summarization savings before updating body
				if g.savings != nil && len(merged) < originalBodyLen {
					origTok := tokenizer.CountBytes(body)
					mergedTok := tokenizer.CountBytes(merged)
					g.savings.RecordPreemptiveSummarization(origTok, mergedTok, model, pipeCtx.CostSessionID, g.isMainConversation(pipeCtx.StableFingerprint))
					g.tracker.RecordPreemptiveStats(origTok, mergedTok)
				}
				body = merged
				// Update pipeCtx with new body
				pipeCtx.OriginalRequest = body
			}
		}
	}

	// Capture pre-compaction body size BEFORE compression pipeline may further modify it.
	// This is the original client request size (before summarization merge changed `body`).
	// For non-compaction requests, preCompactionBodySize == len(body).
	// For compaction requests, the original `body` was already overwritten by the merge above,
	// but we captured originalBodyLen at entry. We use that instead.
	preCompactionBodySize := len(body)
	if isCompaction && len(body) != originalBodyLen {
		// body was overwritten by compaction merge — use the original request body size
		preCompactionBodySize = originalBodyLen
	}

	// Store preemptive headers in context for response
	pipeCtx.PreemptiveHeaders = preemptiveHeaders
	pipeCtx.IsCompaction = isCompaction

	// Capture prompt to persistent history (non-blocking).
	// Four-layer filter:
	//   1. IsMainAgent: only main Claude Code agent, never subagents (Haiku, reviewers, etc.)
	//   2. extractCleanUserPrompt: only returns text for genuine user turns (not tool loops)
	//   3. Stable fingerprint lock: hash of CLEAN first user message text — this is stable
	//      across requests (injected XML stripped) and filters subagents whose first message
	//      differs from the main conversation's first message.
	//   4. isMainPromptConversation: fingerprint must match the main conversation
	if g.promptHistory != nil && lastUserMsg != "" && !isCompaction && pipeCtx.Classification.IsMainAgent {
		cleanedPrompt := extractCleanUserPrompt(body)
		if cleanedPrompt != "" {
			// Compute stable conversation fingerprint from clean first user message.
			// Unlike ComputeSessionID (which hashes the full message including injected
			// <system-reminder> XML that changes between requests), this only hashes the
			// actual user-typed text — stable within a conversation, different for subagents.
			firstCleanText := pipeCtx.Classification.FirstUserCleanContent
			promptFingerprint := preemptive.ComputeSessionIDFromClean(firstCleanText)
			if promptFingerprint != "" {
				g.setPromptConvFingerprintOnce(promptFingerprint)
				if g.isMainPromptConversation(promptFingerprint) {
					sessionName := g.getCurrentSessionID()
					if sessionName == "" {
						sessionName = conversationSessionID
					}
					promptAgentName := dashboard.DetectAgent(r.Header)
					if promptAgentName == "unknown" {
						promptAgentName = ""
					}
					go func() {
						if err := g.promptHistory.Record(context.WithoutCancel(r.Context()), prompthistory.PromptRecord{
							Text:      cleanedPrompt,
							Timestamp: time.Now().Format(time.RFC3339),
							SessionID: sessionName,
							Model:     model,
							Provider:  string(provider),
							RequestID: requestID,
							AgentName: promptAgentName,
						}); err != nil {
							log.Error().Err(err).Str("request_id", requestID).Msg("failed to record prompt history")
						}
					}()
				}
			}
		}
	}

	// Process compression pipeline
	forwardBody, pipeType, pipeStrategy, compressionUsed, compressLatency := g.processCompressionPipeline(body, pipeCtx, requestID)

	// Store deferred tools in session for hybrid search fallback
	if g.toolSessions != nil && pipeCtx.ToolSessionID != "" && len(pipeCtx.DeferredTools) > 0 {
		g.toolSessions.StoreDeferred(pipeCtx.ToolSessionID, pipeCtx.DeferredTools)
	}

	// Capture compressed body size BEFORE tool injection — this is the true
	// post-compression size for metrics. Tool injection adds gateway overhead
	// (expand_context definition) that shouldn't count against compression savings.
	compressedBodySize := len(forwardBody)

	// Always inject all phantom tools (MCP-server pattern).
	// Both expand_context and gateway_search_tools are injected unconditionally,
	// regardless of which pipes are enabled. Config may change mid-session, and
	// the LLM should consistently see both tools from turn one.
	// Dedup in InjectPhantomTool prevents double-injection if a tool already exists.
	isStreaming := g.isStreamingRequest(body)
	if injected, err := phantom_tools.InjectAll(forwardBody, provider); err == nil {
		forwardBody = injected
		pipeCtx.PhantomToolsInjected = true
	}
	// expandEnabled=true: phantom loop always handles calls to either tool.
	// For streaming: needsExpandBuffer still checks compressionUsed + ShadowRefs.
	expandEnabled := true

	// Route to streaming or non-streaming handler
	if isStreaming {
		g.handleStreamingWithExpand(w, r, forwardBody, pipeCtx, requestID, startTime, adapter,
			pipeType, pipeStrategy, preCompactionBodySize, compressionUsed, compressLatency, body, expandEnabled, compressedBodySize)
	} else {
		g.handleNonStreaming(w, r, forwardBody, pipeCtx, requestID, startTime, adapter,
			pipeType, pipeStrategy, preCompactionBodySize, compressionUsed, compressLatency, body, expandEnabled, nil, compressedBodySize)
	}
}

// processCompressionPipeline routes and processes through ALL applicable compression pipes.
// Now processes BOTH tool_output AND tool_discovery if both are present (no priority skipping).
func (g *Gateway) processCompressionPipeline(body []byte, pipeCtx *PipelineContext, requestID string) ([]byte, PipeType, string, bool, time.Duration) {
	compressStart := time.Now()

	// Process all applicable pipes (tool_output first, then tool_discovery)
	forwardBody, flags, _ := g.router.ProcessAll(pipeCtx)

	// Determine primary pipe type for telemetry (tool_output takes precedence)
	var pipeType PipeType
	var pipeStrategy string
	var compressionUsed bool

	if flags.TaskOutput && len(pipeCtx.TaskOutputHandledIDs) > 0 {
		// task_output ran and claimed at least one item — record it as the primary pipe
		// only when no higher-priority pipe (tool_output) also ran.
		if pipeType == PipeNone {
			pipeType = PipeTaskOutput
			pipeStrategy = g.cfg().Pipes.TaskOutput.Strategy
		}
		g.requestLogger.LogPipelineStage(&monitoring.PipelineStageInfo{
			RequestID: requestID, Stage: "process", Pipe: string(PipeTaskOutput),
		})
	}
	if flags.ToolOutput {
		pipeType = PipeToolOutput
		pipeStrategy = g.cfg().Pipes.ToolOutput.Strategy
		compressionUsed = pipeCtx.OutputCompressed
		g.requestLogger.LogPipelineStage(&monitoring.PipelineStageInfo{
			RequestID: requestID, Stage: "process", Pipe: string(PipeToolOutput),
		})
	}
	if flags.ToolDiscovery {
		if pipeType == PipeNone {
			pipeType = PipeToolDiscovery
			pipeStrategy = g.cfg().Pipes.ToolDiscovery.Strategy
		}
		if pipeCtx.ToolsFiltered {
			compressionUsed = true
		}
		g.requestLogger.LogPipelineStage(&monitoring.PipelineStageInfo{
			RequestID: requestID, Stage: "process", Pipe: string(PipeToolDiscovery),
		})
	}

	if pipeType == PipeNone {
		return body, pipeType, config.StrategyPassthrough, false, 0
	}

	compressLatency := time.Since(compressStart)

	// Record compression metrics for tool outputs
	for _, tc := range pipeCtx.ToolOutputCompressions {

		compressionRatio := tokenizer.CompressionRatio(tc.OriginalTokens, tc.CompressedTokens)
		g.requestLogger.LogCompression(&monitoring.CompressionInfo{
			RequestID: requestID, ToolName: tc.ToolName, ToolCallID: tc.ToolCallID,
			ShadowID: tc.ShadowID, OriginalTokens: tc.OriginalTokens, CompressedTokens: tc.CompressedTokens,
			CompressionRatio: compressionRatio,
			CacheHit:         tc.CacheHit, IsLastTool: tc.IsLastTool, MappingStatus: tc.MappingStatus,
			Duration: compressLatency,
		})
		g.metrics.RecordCompression(tc.OriginalTokens, tc.CompressedTokens, true)
		if tc.CacheHit {
			g.metrics.RecordCacheHit()
		} else {
			g.metrics.RecordCacheMiss()
		}
		// Record for post-session collector
		if g.sessionCollector != nil {
			g.sessionCollector.RecordCompression(tc.ToolName, tc.OriginalTokens, tc.CompressedTokens)
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
	// IdentifyAndGetAdapter centralizes all provider detection logic; no overrides needed here.
	provider, _ := adapters.IdentifyAndGetAdapter(g.registry, r.URL.Path, r.Header)

	// Use provider-specific auth handler for fallback logic
	authHandler := g.authRegistry.GetOrDefault(provider)
	initialMode, isSubscriptionAuth := authHandler.DetectAuthMode(r.Header)
	authMeta.InitialMode = initialMode

	canFallbackToAPIKey := isSubscriptionAuth && authHandler.HasFallback()
	sessionID := preemptive.ComputeSessionID(body)
	useAPIKeyForSession := canFallbackToAPIKey && g.authMode != nil && g.authMode.ShouldUseAPIKeyMode(sessionID)

	sendUpstream := func(useAPIKeyMode bool, fallbackHeaders map[string]string) (*http.Response, []byte, error) {
		// #nosec G704 -- targetURL is from configured provider URLs, not user input
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
				// Core auth headers
				"Content-Type", "Content-Encoding", "Authorization", "x-api-key", "x-goog-api-key",
				"api-key", "anthropic-version", "anthropic-beta",
				// OpenAI headers
				"OpenAI-Organization", "OpenAI-Project", "OpenAI-Beta",
				// Codex CLI headers (required for ChatGPT subscription)
				"Chatgpt-Account-Id", "Originator", "Session_id", "Version",
				"X-Codex-Turn-Metadata", "Accept",
			} {
				if v := r.Header.Get(h); v != "" {
					httpReq.Header.Set(h, v)
				}
			}

			// Sticky/triggered fallback mode: apply fallback headers from auth handler
			if useAPIKeyMode && fallbackHeaders != nil {
				// Clear subscription auth headers based on provider
				httpReq.Header.Del("Authorization")
				// Apply fallback headers from auth handler
				for k, v := range fallbackHeaders {
					httpReq.Header.Set(k, v)
				}
			}
		}
		if useAPIKeyMode {
			authMeta.EffectiveMode = "api_key"
		} else {
			authMeta.EffectiveMode = authMeta.InitialMode
		}
		// #nosec G704 -- httpReq uses configured provider URLs, not user input
		resp, doErr := g.httpClient.Do(httpReq)
		if doErr != nil {
			log.Error().Err(doErr).Str("targetURL", targetURL).Msg("upstream request failed")
			return nil, nil, doErr
		}

		// Read body for upstream errors so we can inspect and preserve it.
		if resp.StatusCode >= 400 {
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize))
			resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			log.Error().
				Int("status", resp.StatusCode).
				Str("targetURL", targetURL).
				Bool("api_key_mode", useAPIKeyMode).
				Str("error_type", extractErrorType(bodyBytes)).
				Msg("upstream error response")
			return resp, bodyBytes, nil
		}
		return resp, nil, nil
	}

	// First attempt: sticky mode may already force API key for this session.
	var fallbackHeaders map[string]string
	if useAPIKeyForSession {
		fallbackHeaders = authHandler.GetFallbackHeaders()
	}
	resp, respBody, err := sendUpstream(useAPIKeyForSession, fallbackHeaders)
	if err != nil {
		return nil, authMeta, err
	}

	// One-shot fallback: use provider-specific auth handler to determine if fallback should trigger.
	// Key difference: OpenAI handlers trigger on 401 (auth error), Anthropic only on quota errors.
	if canFallbackToAPIKey && !useAPIKeyForSession && resp != nil {
		fallbackResult := authHandler.ShouldFallback(resp.StatusCode, respBody)
		if fallbackResult.ShouldFallback {
			if g.authMode != nil {
				g.authMode.MarkAPIKeyMode(sessionID)
			}
			authMeta.FallbackUsed = true
			_ = resp.Body.Close()
			log.Info().
				Str("session_id", sessionID).
				Int("status", resp.StatusCode).
				Str("reason", fallbackResult.Reason).
				Str("provider", provider.String()).
				Msg("auth_fallback: switching session to api-key mode")
			retryResp, _, retryErr := sendUpstream(true, fallbackResult.Headers)
			return retryResp, authMeta, retryErr
		}
	}

	return resp, authMeta, nil
}

// isBedrockRequest checks if the request path matches Bedrock URL patterns.
// Returns false if Bedrock support is not explicitly enabled in config.
func (g *Gateway) isBedrockRequest(path string) bool {
	if !g.cfg().Bedrock.Enabled {
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
	return gjson.GetBytes(body, "stream").Bool()
}

// setStreamFlag sets or clears the "stream" field in a request body.
// Uses sjson to preserve field ordering for KV-cache prefix matching.
func setStreamFlag(body []byte, stream bool) []byte {
	result, err := sjson.SetBytes(body, "stream", stream)
	if err != nil {
		return body
	}
	return result
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

// extractErrorType extracts the error type/message from an upstream error response
// without logging the full body (which may contain sensitive data like API keys or PII).
func extractErrorType(body []byte) string {
	if len(body) == 0 {
		return "empty"
	}
	// Try common error response formats: {"error":{"type":"...","message":"..."}}
	if t := gjson.GetBytes(body, "error.type").String(); t != "" {
		return t
	}
	if t := gjson.GetBytes(body, "error.code").String(); t != "" {
		return t
	}
	if t := gjson.GetBytes(body, "type").String(); t != "" {
		return t
	}
	return "unknown"
}

// countMessages is defined in handler_telemetry.go (uses gjson for efficiency).

// protectedHeaders is the set of security headers that must never be overwritten
// by upstream response headers. These are set by the gateway's security middleware
// and must not be replaced by potentially attacker-controlled upstream values.
var protectedHeaders = map[string]bool{
	"X-Content-Type-Options":    true,
	"X-Frame-Options":           true,
	"Content-Security-Policy":   true,
	"Strict-Transport-Security": true,
	"X-Xss-Protection":          true,
}

// copyHeaders copies HTTP headers from source to destination.
// Headers listed in protectedHeaders are never copied from upstream to prevent
// upstream responses from weakening gateway-set security policies.
func copyHeaders(w http.ResponseWriter, src http.Header) {
	for k, v := range src {
		if protectedHeaders[k] {
			continue
		}
		w.Header()[k] = v
	}
}
