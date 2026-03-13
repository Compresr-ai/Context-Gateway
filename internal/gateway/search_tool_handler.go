// Search tool handler for universal dispatcher tool discovery.
//
// DESIGN: Implements PhantomToolHandler for gateway_search_tools.
// When LLM calls gateway_search_tools(query), this handler:
//  1. Searches deferred tools using the query
//  2. Filters out tools already "expanded" (previously injected) to preserve KV-cache
//  3. Returns tool_result with NEW matching tool descriptions only
//  4. Appends only NEW tools to the tools array (append-only, never modifies history)
//  5. Marks newly found tools as expanded in session
//
// KV-CACHE PRESERVATION: Tools are only injected once. If a search returns tools
// that were already injected in a previous search, they are filtered out and
// not re-injected. This ensures the tools array only grows (append-only).
//
// Two modes determined by which input fields are provided:
//
// Mode 1 — SEARCH: LLM provides "query". Handler searches deferred tools
// and returns names, descriptions, and full input schemas as tool_result text.
// The phantom loop continues (StopLoop=false).
//
// Mode 2 — CALL: LLM provides "tool_name" and "tool_input". Handler validates
// the tool exists, records a rewrite mapping, and returns StopLoop=true with a
// RewriteResponse func that transforms gateway_search_tool -> real tool_use.
// The client sees a normal tool_use for the real tool.
package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/tidwall/sjson"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/monitoring"
)

// SearchRequestContext holds per-request state for search operations.
// This is separate from the handler to avoid race conditions.
type SearchRequestContext struct {
	SessionID     string
	DeferredTools []adapters.ExtractedContent
}

// SearchToolHandler implements PhantomToolHandler for gateway_search_tools.
// The handler itself is stateless; per-request state is stored in requestCtx.
type SearchToolHandler struct {
	toolName     string
	maxResults   int
	sessionStore *ToolSessionStore
	strategy     string
	apiEndpoint  string
	apiKey       string
	apiTimeout   time.Duration
	alwaysKeep   []string
	httpClient   *http.Client

	// Per-request context (protected by mutex for concurrent safety)
	requestCtx *SearchRequestContext
	mu         sync.RWMutex

	// API fallback events captured during this request for telemetry.
	apiFallbackEvents []ToolDiscoveryAPIFallbackEvent

	// Search logging (for dashboard display)
	searchLog *monitoring.SearchLog
	requestID string
	sessionID string
}

// SearchToolHandlerOptions configures gateway_search_tools behavior.
type SearchToolHandlerOptions struct {
	Strategy     string
	APIEndpoint  string
	ProviderAuth string
	APITimeout   time.Duration
	AlwaysKeep   []string
}

// ToolDiscoveryAPIFallbackEvent captures a degraded API search outcome.
type ToolDiscoveryAPIFallbackEvent struct {
	Query         string
	Reason        string
	Detail        string
	DeferredCount int
	ReturnedCount int
}

// NewSearchToolHandler creates a new search tool handler.
func NewSearchToolHandler(toolName string, maxResults int, sessionStore *ToolSessionStore, opts SearchToolHandlerOptions) *SearchToolHandler {
	if toolName == "" {
		toolName = "gateway_search_tools"
	}
	if maxResults <= 0 {
		maxResults = 5
	}
	timeout := opts.APITimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &SearchToolHandler{
		toolName:     toolName,
		maxResults:   maxResults,
		sessionStore: sessionStore,
		strategy:     opts.Strategy,
		apiEndpoint:  opts.APIEndpoint,
		apiKey:       opts.ProviderAuth,
		apiTimeout:   timeout,
		alwaysKeep:   opts.AlwaysKeep,
		httpClient:   &http.Client{Timeout: timeout},
	}
}

// SetRequestContext sets the context for the current request.
// Must be called before using in PhantomLoop. Thread-safe.
func (h *SearchToolHandler) SetRequestContext(sessionID string, deferredTools []adapters.ExtractedContent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requestCtx = &SearchRequestContext{
		SessionID:     sessionID,
		DeferredTools: deferredTools,
	}
	h.apiFallbackEvents = nil
}

// WithSearchLog sets the search log for recording gateway_search_tools calls.
func (h *SearchToolHandler) WithSearchLog(sl *monitoring.SearchLog, requestID, sessionID string) *SearchToolHandler {
	h.searchLog = sl
	h.requestID = requestID
	h.sessionID = sessionID
	return h
}

// getRequestContext returns a copy of the current request context.
// Thread-safe.
func (h *SearchToolHandler) getRequestContext() *SearchRequestContext {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.requestCtx == nil {
		return &SearchRequestContext{}
	}
	// Return a copy to avoid races
	return &SearchRequestContext{
		SessionID:     h.requestCtx.SessionID,
		DeferredTools: h.requestCtx.DeferredTools,
	}
}

// ConsumeAPIFallbackEvents returns and clears captured API fallback events.
func (h *SearchToolHandler) ConsumeAPIFallbackEvents() []ToolDiscoveryAPIFallbackEvent {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.apiFallbackEvents) == 0 {
		return nil
	}
	events := make([]ToolDiscoveryAPIFallbackEvent, len(h.apiFallbackEvents))
	copy(events, h.apiFallbackEvents)
	h.apiFallbackEvents = nil
	return events
}

// Name returns the phantom tool name.
func (h *SearchToolHandler) Name() string {
	return h.toolName
}

// HandleCalls processes search tool calls — routes to search or call mode.
// It filters out tools that have already been "expanded" (injected in previous searches)
// to avoid KV-cache invalidation through repeated tool injections.
// Only truly NEW tools are appended to the request.
func (h *SearchToolHandler) HandleCalls(calls []PhantomToolCall, isAnthropic bool) *PhantomToolResult {
	// Separate calls by mode
	var searchCalls []PhantomToolCall
	var execCalls []PhantomToolCall

	for _, call := range calls {
		if toolName, ok := call.Input["tool_name"].(string); ok && toolName != "" {
			execCalls = append(execCalls, call)
		} else {
			searchCalls = append(searchCalls, call)
		}
	}

	// If we have exec calls, handle them (takes priority, stops loop)
	if len(execCalls) > 0 {
		return h.handleExecCalls(execCalls, isAnthropic)
	}

	// Otherwise, handle search calls (loop continues)
	return h.handleSearchCalls(searchCalls, isAnthropic)
}

// handleSearchCalls handles search-mode calls: search deferred tools and return results.
func (h *SearchToolHandler) handleSearchCalls(calls []PhantomToolCall, isAnthropic bool) *PhantomToolResult {
	result := &PhantomToolResult{}
	reqCtx := h.getRequestContext()

	// Loop-breaking: check if model is searching too many times without calling
	if h.sessionStore != nil && reqCtx.SessionID != "" {
		count := h.sessionStore.IncrementSearchCount(reqCtx.SessionID)
		if count > 3 {
			session := h.sessionStore.Get(reqCtx.SessionID)
			var discoveredNames []string
			if session != nil {
				discoveredNames = session.DiscoveredToolNames
			}
			hint := fmt.Sprintf(
				"You have searched %d times without calling a tool. "+
					"Previously discovered tools: %s. "+
					"Please call one using {\"tool_name\": \"<name>\", \"tool_input\": {<params>}}.",
				count, strings.Join(discoveredNames, ", "))
			return h.buildToolResultMessage(calls, hint, isAnthropic)
		}
	}

	// Get already-expanded tools from session to avoid re-injecting them (KV-cache preservation)
	var alreadyExpanded map[string]bool
	if h.sessionStore != nil && reqCtx.SessionID != "" {
		alreadyExpanded = h.sessionStore.GetExpanded(reqCtx.SessionID)
	}
	if alreadyExpanded == nil {
		alreadyExpanded = make(map[string]bool)
	}

	var allNewMatches []adapters.ExtractedContent
	var newExpandedNames []string
	var discoveredNames []string

	// Log available deferred tools for debugging
	deferredNames := make([]string, len(reqCtx.DeferredTools))
	for i, t := range reqCtx.DeferredTools {
		deferredNames[i] = t.ToolName
	}

	if isAnthropic {
		contentBlocks := make([]any, 0, len(calls))
		for _, call := range calls {
			query, _ := call.Input["query"].(string)
			matches := h.resolveMatches(reqCtx.DeferredTools, query)

			// Filter out already-expanded tools (ones we've seen before)
			// Only keep truly NEW matches to preserve KV-cache
			var newMatches []adapters.ExtractedContent
			var newNames []string
			for _, match := range matches {
				discoveredNames = append(discoveredNames, match.ToolName)
				if !alreadyExpanded[match.ToolName] {
					newMatches = append(newMatches, match)
					newNames = append(newNames, match.ToolName)
				}
			}

			// Collect only new matches for injection
			allNewMatches = append(allNewMatches, newMatches...)
			newExpandedNames = append(newExpandedNames, newNames...)

			// Format result - tell LLM about new tools only, or that no new tools were found
			var resultText string
			if len(newMatches) > 0 {
				resultText = formatSearchResults(newMatches)
			} else if len(matches) > 0 {
				// All matches were already expanded - no new tools to show
				resultText = "No additional tools found. The relevant tools are already available in your current tool set."
			} else {
				resultText = "No tools found matching the query."
			}

			contentBlocks = append(contentBlocks, map[string]any{
				"type":        "tool_result",
				"tool_use_id": call.ToolUseID,
				"content":     resultText,
			})

			log.Info().
				Str("query", query).
				Str("session_id", reqCtx.SessionID).
				Int("deferred_count", len(reqCtx.DeferredTools)).
				Strs("deferred_tools", deferredNames).
				Int("total_matches", len(matches)).
				Int("new_matches", len(newMatches)).
				Strs("found_new", newNames).
				Int("already_expanded", len(matches)-len(newMatches)).
				Msg("search_tool: handled search (append-only mode)")

			h.recordSearchEvent(query, len(reqCtx.DeferredTools), newMatches)
		}
		result.ToolResults = []map[string]any{{
			"role":    "user",
			"content": contentBlocks,
		}}
	} else {
		for _, call := range calls {
			query, _ := call.Input["query"].(string)
			matches := h.resolveMatches(reqCtx.DeferredTools, query)

			// Filter out already-expanded tools (ones we've seen before)
			// Only keep truly NEW matches to preserve KV-cache
			var newMatches []adapters.ExtractedContent
			var newNames []string
			for _, match := range matches {
				discoveredNames = append(discoveredNames, match.ToolName)
				if !alreadyExpanded[match.ToolName] {
					newMatches = append(newMatches, match)
					newNames = append(newNames, match.ToolName)
				}
			}

			// Collect only new matches for injection
			allNewMatches = append(allNewMatches, newMatches...)
			newExpandedNames = append(newExpandedNames, newNames...)

			// Format result - tell LLM about new tools only, or that no new tools were found
			var resultText string
			if len(newMatches) > 0 {
				resultText = formatSearchResults(newMatches)
			} else if len(matches) > 0 {
				// All matches were already expanded - no new tools to show
				resultText = "No additional tools found. The relevant tools are already available in your current tool set."
			} else {
				resultText = "No tools found matching the query."
			}

			result.ToolResults = append(result.ToolResults, map[string]any{
				"role":         "tool",
				"tool_call_id": call.ToolUseID,
				"content":      resultText,
			})

			log.Info().
				Str("query", query).
				Str("session_id", reqCtx.SessionID).
				Int("deferred_count", len(reqCtx.DeferredTools)).
				Strs("deferred_tools", deferredNames).
				Int("total_matches", len(matches)).
				Int("new_matches", len(newMatches)).
				Strs("found_new", newNames).
				Int("already_expanded", len(matches)-len(newMatches)).
				Msg("search_tool: handled search (append-only mode)")

			h.recordSearchEvent(query, len(reqCtx.DeferredTools), newMatches)
		}
	}

	// Track discovered tool names in session
	if len(discoveredNames) > 0 && h.sessionStore != nil && reqCtx.SessionID != "" {
		h.sessionStore.AddDiscoveredToolNames(reqCtx.SessionID, discoveredNames)
	}

	// Mark newly expanded tools in session (only truly new ones)
	if len(newExpandedNames) > 0 && h.sessionStore != nil && reqCtx.SessionID != "" {
		h.sessionStore.MarkExpanded(reqCtx.SessionID, newExpandedNames)
	}

	// Only create request modifier to inject found tools if there are NEW tools
	// This is critical for KV-cache preservation - never re-inject tools we've already added
	if len(allNewMatches) > 0 {
		result.ModifyRequest = func(body []byte) ([]byte, error) {
			return injectToolsIntoRequest(body, allNewMatches, isAnthropic)
		}
	}

	return result
}

// handleExecCalls handles call-mode: validate, record mapping, stop loop with rewrite.
func (h *SearchToolHandler) handleExecCalls(calls []PhantomToolCall, isAnthropic bool) *PhantomToolResult {
	reqCtx := h.getRequestContext()
	mappings := make([]*ToolCallMapping, 0, len(calls))

	for _, call := range calls {
		toolName, _ := call.Input["tool_name"].(string)
		toolInput, _ := call.Input["tool_input"].(map[string]any)

		// Validate tool_name exists in deferred tools
		if !h.isKnownTool(reqCtx.DeferredTools, toolName) {
			return h.buildErrorResult(call, isAnthropic,
				fmt.Sprintf("Unknown tool '%s'. Use a search query to find available tools first.", toolName))
		}

		// Validate tool_input is present
		if toolInput == nil {
			return h.buildErrorResult(call, isAnthropic,
				"tool_input is required when calling a tool. Provide the input parameters matching the tool's schema.")
		}

		// Generate new client-facing tool_use_id
		clientToolUseID := "toolu_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:24]

		mapping := &ToolCallMapping{
			ProxyToolUseID:  call.ToolUseID,
			ClientToolName:  toolName,
			ClientToolUseID: clientToolUseID,
			OriginalInput:   toolInput,
		}
		mappings = append(mappings, mapping)

		// Record in session
		if h.sessionStore != nil && reqCtx.SessionID != "" {
			h.sessionStore.RecordCallRewrite(reqCtx.SessionID, mapping)
			h.sessionStore.ResetSearchCount(reqCtx.SessionID)
		}

		log.Info().
			Str("tool_name", toolName).
			Str("proxy_id", call.ToolUseID).
			Str("client_id", clientToolUseID).
			Str("session_id", reqCtx.SessionID).
			Msg("search_tool: call mode — dispatching tool")
	}

	return &PhantomToolResult{
		StopLoop: true,
		RewriteResponse: func(responseBody []byte) ([]byte, error) {
			return rewriteResponseForClient(responseBody, mappings, isAnthropic)
		},
	}
}

// isKnownTool checks if a tool name exists in the deferred tools list.
func (h *SearchToolHandler) isKnownTool(deferred []adapters.ExtractedContent, name string) bool {
	for _, t := range deferred {
		if t.ToolName == name {
			return true
		}
	}
	return false
}

// buildErrorResult returns a PhantomToolResult with an error message that lets the model retry.
func (h *SearchToolHandler) buildErrorResult(call PhantomToolCall, isAnthropic bool, errMsg string) *PhantomToolResult {
	return h.buildToolResultMessage([]PhantomToolCall{call}, errMsg, isAnthropic)
}

// buildToolResultMessage builds a PhantomToolResult containing tool_result messages.
func (h *SearchToolHandler) buildToolResultMessage(calls []PhantomToolCall, text string, isAnthropic bool) *PhantomToolResult {
	result := &PhantomToolResult{StopLoop: false}

	if isAnthropic {
		contentBlocks := make([]any, 0, len(calls))
		for _, call := range calls {
			contentBlocks = append(contentBlocks, map[string]any{
				"type":        "tool_result",
				"tool_use_id": call.ToolUseID,
				"content":     text,
			})
		}
		result.ToolResults = []map[string]any{{
			"role":    "user",
			"content": contentBlocks,
		}}
	} else {
		for _, call := range calls {
			result.ToolResults = append(result.ToolResults, map[string]any{
				"role":         "tool",
				"tool_call_id": call.ToolUseID,
				"content":      text,
			})
		}
	}
	return result
}

// resolveMatches picks search backend by strategy.
// For tool-search: tries Compresr API first, falls back to local regex.
func (h *SearchToolHandler) resolveMatches(deferred []adapters.ExtractedContent, query string) []adapters.ExtractedContent {
	// Try API-backed search if endpoint is configured
	if h.apiEndpoint != "" {
		result, err := h.searchViaAPI(deferred, query)
		if err != nil {
			h.recordAPIFallback(query, "api_error", err.Error(), len(deferred), len(deferred))
			log.Warn().Err(err).Msg("search_tool: API failed, falling back to local regex search")
			return h.searchByRegex(deferred, query)
		}
		if !result.Meaningful {
			h.recordAPIFallback(query, result.Reason, result.Detail, len(deferred), len(deferred))
			log.Warn().
				Str("reason", result.Reason).
				Str("detail", result.Detail).
				Msg("search_tool: API returned non-meaningful selection, falling back to local regex search")
			return h.searchByRegex(deferred, query)
		}
		return result.Matches
	}

	// No API configured — use local regex search
	return h.searchByRegex(deferred, query)
}

// searchByRegex performs local regex-based search on deferred tools.
// The query is treated as a regex pattern that matches against tool names,
// descriptions, and parameter names/descriptions (case-insensitive).
func (h *SearchToolHandler) searchByRegex(deferred []adapters.ExtractedContent, query string) []adapters.ExtractedContent {
	if len(deferred) == 0 || strings.TrimSpace(query) == "" {
		return nil
	}

	// Compile regex pattern (case-insensitive)
	re, err := regexp.Compile("(?i)" + query)
	if err != nil {
		log.Warn().
			Err(err).
			Str("pattern", query).
			Msg("search_tool(tool-search): invalid regex pattern, falling back to keyword search")
		return SearchDeferredTools(deferred, query, h.maxResults)
	}

	var matches []adapters.ExtractedContent

	// Check always-keep tools first
	alwaysKeepSet := make(map[string]bool, len(h.alwaysKeep))
	for _, name := range h.alwaysKeep {
		alwaysKeepSet[name] = true
	}

	for _, tool := range deferred {
		// Always-keep tools are always included
		if alwaysKeepSet[tool.ToolName] {
			matches = append(matches, tool)
			continue
		}

		// Build searchable text: tool name + description + parameter info
		searchText := tool.ToolName + " " + tool.Content

		// Include parameter names and descriptions if available
		if rawJSON, ok := tool.Metadata["raw_json"].(string); ok && rawJSON != "" {
			var def map[string]any
			if err := json.Unmarshal([]byte(rawJSON), &def); err == nil {
				searchText += " " + extractParameterText(def)
			}
		}

		if re.MatchString(searchText) {
			matches = append(matches, tool)
		}
	}

	log.Info().
		Str("pattern", query).
		Int("deferred", len(deferred)).
		Int("matches", len(matches)).
		Strs("found", extractToolNames(matches)).
		Msg("search_tool(tool-search): regex search completed")

	return matches
}

// extractParameterText extracts searchable text from tool parameter definitions.
func extractParameterText(def map[string]any) string {
	var parts []string

	// Extract from input_schema (Anthropic style)
	if inputSchema, ok := def["input_schema"].(map[string]any); ok {
		parts = append(parts, extractPropertiesText(inputSchema)...)
	}

	// Extract from parameters (OpenAI style)
	if params, ok := def["parameters"].(map[string]any); ok {
		parts = append(parts, extractPropertiesText(params)...)
	}

	// Extract from function.parameters (OpenAI nested style)
	if fn, ok := def["function"].(map[string]any); ok {
		if params, ok := fn["parameters"].(map[string]any); ok {
			parts = append(parts, extractPropertiesText(params)...)
		}
	}

	return strings.Join(parts, " ")
}

// extractPropertiesText extracts parameter names and descriptions from a schema.
func extractPropertiesText(schema map[string]any) []string {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil
	}

	// Pre-allocate: each property contributes name + possibly description (max 2 items each)
	parts := make([]string, 0, len(props)*2)

	for name, propVal := range props {
		parts = append(parts, name)
		if prop, ok := propVal.(map[string]any); ok {
			if desc, ok := prop["description"].(string); ok {
				parts = append(parts, desc)
			}
		}
	}

	return parts
}

type toolDiscoverySearchRequest struct {
	Pattern    string                 `json:"pattern"`
	TopK       int                    `json:"top_k"`
	AlwaysKeep []string               `json:"always_keep,omitempty"`
	Tools      []toolDiscoveryAPITool `json:"tools"`
}

type toolDiscoveryAPITool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Definition  map[string]any `json:"definition,omitempty"`
}

type toolDiscoverySearchResponse struct {
	SelectedNames []string `json:"selected_names"`
}

type apiSearchResult struct {
	Matches    []adapters.ExtractedContent
	Meaningful bool
	Reason     string
	Detail     string
}

// searchViaAPI calls the external selector endpoint and maps selected names back to deferred tools.
func (h *SearchToolHandler) searchViaAPI(deferred []adapters.ExtractedContent, query string) (*apiSearchResult, error) {
	if len(deferred) == 0 {
		return &apiSearchResult{Meaningful: true}, nil
	}
	if strings.TrimSpace(query) == "" {
		return &apiSearchResult{
			Meaningful: false,
			Reason:     "empty_query",
			Detail:     "query was empty",
		}, nil
	}

	payload := toolDiscoverySearchRequest{
		Pattern:    query,
		TopK:       h.maxResults,
		AlwaysKeep: h.alwaysKeep,
		Tools:      make([]toolDiscoveryAPITool, 0, len(deferred)),
	}

	for _, t := range deferred {
		apiTool := toolDiscoveryAPITool{
			Name:        t.ToolName,
			Description: t.Content,
		}
		if rawJSON, ok := t.Metadata["raw_json"].(string); ok && rawJSON != "" {
			var def map[string]any
			if err := json.Unmarshal([]byte(rawJSON), &def); err == nil {
				apiTool.Definition = def
			}
		}
		payload.Tools = append(payload.Tools, apiTool)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	parsedURL, err := url.Parse(h.apiEndpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid API endpoint URL: %w", err)
	}
	if parsedURL.Scheme != "https" && parsedURL.Scheme != "http" {
		return nil, fmt.Errorf("API endpoint must use http or https scheme, got %q", parsedURL.Scheme)
	}

	req, err := http.NewRequest(http.MethodPost, parsedURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.apiKey)
	}

	resp, err := h.httpClient.Do(req) //nolint:gosec // G704: URL is parsed and scheme-validated (http/https only) above
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var parsed toolDiscoverySearchResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.SelectedNames) == 0 {
		return &apiSearchResult{
			Meaningful: false,
			Reason:     "empty_selection",
			Detail:     "selected_names was empty",
		}, nil
	}

	selectedSet := make(map[string]bool, len(parsed.SelectedNames))
	for _, name := range parsed.SelectedNames {
		selectedSet[name] = true
	}

	matches := make([]adapters.ExtractedContent, 0, len(parsed.SelectedNames))
	for _, t := range deferred {
		if selectedSet[t.ToolName] {
			matches = append(matches, t)
		}
		if len(matches) >= h.maxResults {
			break
		}
	}
	if len(matches) == 0 {
		return &apiSearchResult{
			Meaningful: false,
			Reason:     "unknown_selection_names",
			Detail:     fmt.Sprintf("selected_names did not match deferred tools: %v", parsed.SelectedNames),
		}, nil
	}
	return &apiSearchResult{
		Matches:    matches,
		Meaningful: true,
	}, nil
}

func (h *SearchToolHandler) recordAPIFallback(query, reason, detail string, deferredCount, returnedCount int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.apiFallbackEvents = append(h.apiFallbackEvents, ToolDiscoveryAPIFallbackEvent{
		Query:         query,
		Reason:        reason,
		Detail:        detail,
		DeferredCount: deferredCount,
		ReturnedCount: returnedCount,
	})
}

// FilterFromResponse removes gateway_search_tools from the final response.
func (h *SearchToolHandler) FilterFromResponse(responseBody []byte) ([]byte, bool) {
	var response map[string]any
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return responseBody, false
	}

	modified := false

	// Anthropic format
	if content, ok := response["content"].([]any); ok {
		filteredContent := make([]any, 0, len(content))
		for _, block := range content {
			blockMap, ok := block.(map[string]any)
			if !ok {
				filteredContent = append(filteredContent, block)
				continue
			}

			if blockMap["type"] == "tool_use" {
				name, _ := blockMap["name"].(string)
				if name == h.toolName {
					modified = true
					continue
				}
			}
			filteredContent = append(filteredContent, block)
		}
		response["content"] = filteredContent
	}

	// OpenAI format
	if choices, ok := response["choices"].([]any); ok {
		for i, choice := range choices {
			choiceMap, ok := choice.(map[string]any)
			if !ok {
				continue
			}

			message, ok := choiceMap["message"].(map[string]any)
			if !ok {
				continue
			}

			toolCalls, ok := message["tool_calls"].([]any)
			if !ok {
				continue
			}

			filteredCalls := make([]any, 0, len(toolCalls))
			for _, tc := range toolCalls {
				tcMap, ok := tc.(map[string]any)
				if !ok {
					filteredCalls = append(filteredCalls, tc)
					continue
				}

				function, ok := tcMap["function"].(map[string]any)
				if ok {
					name, _ := function["name"].(string)
					if name == h.toolName {
						modified = true
						continue
					}
				}
				filteredCalls = append(filteredCalls, tc)
			}

			message["tool_calls"] = filteredCalls
			choiceMap["message"] = message
			choices[i] = choiceMap
		}
		response["choices"] = choices
	}

	if !modified {
		return responseBody, false
	}

	result, err := json.Marshal(response)
	if err != nil {
		return responseBody, false
	}
	return result, true
}

// formatSearchResults formats tool matches with full descriptions and input schemas.
func formatSearchResults(matches []adapters.ExtractedContent) string {
	if len(matches) == 0 {
		return "No tools found matching your query. Try a broader or different description."
	}

	var sb strings.Builder
	sb.WriteString("Found the following tools:\n\n")

	for _, m := range matches {
		fmt.Fprintf(&sb, "## %s\n", m.ToolName)
		fmt.Fprintf(&sb, "Description: %s\n", m.Content)

		// Full input schema from raw_json metadata
		if rawJSON, ok := m.Metadata["raw_json"].(string); ok && rawJSON != "" {
			var def map[string]any
			if err := json.Unmarshal([]byte(rawJSON), &def); err == nil {
				schema := extractInputSchemaForDisplay(def)
				if schema != nil {
					if schemaJSON, err := json.MarshalIndent(schema, "", "  "); err == nil {
						fmt.Fprintf(&sb, "Input Schema:\n```json\n%s\n```\n", string(schemaJSON))
					}
				}
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("To call a tool, use: {\"tool_name\": \"<name>\", \"tool_input\": {<parameters matching schema>}}")
	return sb.String()
}

// extractToolNames extracts tool names from matches.
func extractToolNames(matches []adapters.ExtractedContent) []string {
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m.ToolName
	}
	return names
}

// recordSearchEvent records a search tool call to the search log for dashboard display.
func (h *SearchToolHandler) recordSearchEvent(query string, deferredCount int, matches []adapters.ExtractedContent) {
	if h.searchLog == nil {
		return
	}
	matchNames := make([]string, 0, len(matches))
	for _, m := range matches {
		matchNames = append(matchNames, m.ToolName)
	}
	h.searchLog.Record(monitoring.SearchLogEntry{
		Timestamp:     time.Now(),
		SessionID:     h.sessionID,
		RequestID:     h.requestID,
		Query:         query,
		DeferredCount: deferredCount,
		ResultsCount:  len(matches),
		ToolsFound:    matchNames,
		Strategy:      h.strategy,
	})
}

// injectToolsIntoRequest appends tool definitions to the request body's tools array.
// Uses sjson for KV-cache-safe append (only modifies the tools[] path).
func injectToolsIntoRequest(body []byte, tools []adapters.ExtractedContent, isAnthropic bool) ([]byte, error) {
	if len(tools) == 0 {
		return body, nil
	}

	// Parse each tool's Content as raw JSON and append to tools array
	for _, tool := range tools {
		if tool.Content == "" {
			continue
		}
		// The Content field contains the full JSON tool definition
		var toolDef json.RawMessage
		if err := json.Unmarshal([]byte(tool.Content), &toolDef); err != nil {
			log.Warn().Str("tool", tool.ToolName).Err(err).Msg("search_tool: skipping invalid tool JSON")
			continue
		}
		// sjson append: tools.-1 means "append to end of array"
		modified, err := sjsonSetRawBytes(body, "tools.-1", toolDef)
		if err != nil {
			return body, fmt.Errorf("failed to inject tool %s: %w", tool.ToolName, err)
		}
		body = modified
	}
	return body, nil
}

// sjsonSetRawBytes wraps sjson.SetRawBytes for tool injection.
func sjsonSetRawBytes(body []byte, path string, value json.RawMessage) ([]byte, error) {
	return sjson.SetRawBytes(body, path, value)
}
