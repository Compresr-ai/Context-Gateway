// Search tool handler for hybrid tool discovery.
//
// DESIGN: Implements PhantomToolHandler for gateway_search_tools.
// When LLM calls gateway_search_tools(query), this handler:
//  1. Searches deferred tools using the query
//  2. Returns tool_result with matching tool descriptions
//  3. Injects found tools into the tools array for the next forward
//  4. Marks tools as expanded in session for subsequent requests
//
// This allows the LLM to "discover" tools that were filtered out.
package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/utils"
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
}

// SearchToolHandlerOptions configures gateway_search_tools behavior.
type SearchToolHandlerOptions struct {
	Strategy    string
	APIEndpoint string
	APIKey      string
	APITimeout  time.Duration
	AlwaysKeep  []string
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
		apiKey:       opts.APIKey,
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

// HandleCalls processes search tool calls and returns results.
func (h *SearchToolHandler) HandleCalls(calls []PhantomToolCall, isAnthropic bool) *PhantomToolResult {
	result := &PhantomToolResult{}

	// Get per-request context (thread-safe copy)
	reqCtx := h.getRequestContext()

	var allMatches []adapters.ExtractedContent
	var expandedNames []string

	// Log available deferred tools for debugging
	deferredNames := make([]string, len(reqCtx.DeferredTools))
	for i, t := range reqCtx.DeferredTools {
		deferredNames[i] = t.ToolName
	}

	// Anthropic: group all tool_results in one user message
	if isAnthropic {
		contentBlocks := make([]any, 0, len(calls))
		for _, call := range calls {
			query, _ := call.Input["query"].(string)

			// Search deferred tools
			matches := h.resolveMatches(reqCtx.DeferredTools, query)

			// Collect matches
			for _, match := range matches {
				expandedNames = append(expandedNames, match.ToolName)
				allMatches = append(allMatches, match)
			}

			// Format result
			resultText := formatSearchResults(matches)

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
				Int("matches", len(matches)).
				Strs("found", extractToolNames(matches)).
				Msg("search_tool: handled search")
		}

		result.ToolResults = []map[string]any{{
			"role":    "user",
			"content": contentBlocks,
		}}
	} else {
		// OpenAI: separate tool messages
		for _, call := range calls {
			query, _ := call.Input["query"].(string)

			// Search deferred tools
			matches := h.resolveMatches(reqCtx.DeferredTools, query)

			// Collect matches
			for _, match := range matches {
				expandedNames = append(expandedNames, match.ToolName)
				allMatches = append(allMatches, match)
			}

			// Format result
			resultText := formatSearchResults(matches)

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
				Int("matches", len(matches)).
				Strs("found", extractToolNames(matches)).
				Msg("search_tool: handled search")
		}
	}

	// Mark expanded tools in session
	if len(expandedNames) > 0 && h.sessionStore != nil && reqCtx.SessionID != "" {
		h.sessionStore.MarkExpanded(reqCtx.SessionID, expandedNames)
	}

	// Create request modifier to inject found tools
	if len(allMatches) > 0 {
		result.ModifyRequest = func(body []byte) ([]byte, error) {
			return injectToolsIntoRequest(body, allMatches, isAnthropic)
		}
	}

	return result
}

// resolveMatches picks search backend by strategy.
func (h *SearchToolHandler) resolveMatches(deferred []adapters.ExtractedContent, query string) []adapters.ExtractedContent {
	if h.strategy == "api" {
		if h.apiEndpoint == "" {
			h.recordAPIFallback(query, "missing_api_endpoint", "api endpoint is empty", len(deferred), len(deferred))
			log.Warn().Msg("search_tool(api): api endpoint is empty, restoring all deferred tools")
			return deferred
		}

		result, err := h.searchViaAPI(deferred, query)
		if err != nil {
			h.recordAPIFallback(query, "api_error", err.Error(), len(deferred), len(deferred))
			log.Warn().Err(err).Msg("search_tool(api): API failed, restoring all deferred tools")
			return deferred
		}
		if !result.Meaningful {
			h.recordAPIFallback(query, result.Reason, result.Detail, len(deferred), len(deferred))
			log.Warn().
				Str("reason", result.Reason).
				Str("detail", result.Detail).
				Msg("search_tool(api): API returned non-meaningful selection, restoring all deferred tools")
			return deferred
		}
		return result.Matches
	}
	return SearchDeferredTools(deferred, query, h.maxResults)
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

	req, err := http.NewRequest(http.MethodPost, h.apiEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.apiKey)
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)
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

// formatSearchResults formats tool matches as human-readable text.
func formatSearchResults(matches []adapters.ExtractedContent) string {
	var sb strings.Builder
	if len(matches) == 0 {
		sb.WriteString("No matching tools found. Try a different search query.")
	} else {
		sb.WriteString("Found the following tools:\n\n")
		for _, m := range matches {
			desc := m.Content
			if len(desc) > 200 {
				desc = desc[:200] + "..."
			}
			_, _ = fmt.Fprintf(&sb, "- %s: %s\n", m.ToolName, desc)
		}
		sb.WriteString("\nThese tools are now available. You can use them in this response.")
	}
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

// injectToolsIntoRequest adds found tools to the request's tools array.
func injectToolsIntoRequest(body []byte, tools []adapters.ExtractedContent, isAnthropic bool) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return body, err
	}

	existingTools, _ := request["tools"].([]any)
	if existingTools == nil {
		existingTools = []any{}
	}

	// Check which tools already exist
	existingNames := make(map[string]bool)
	for _, t := range existingTools {
		toolMap, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if isAnthropic {
			if name, _ := toolMap["name"].(string); name != "" {
				existingNames[name] = true
			}
		} else {
			if fn, ok := toolMap["function"].(map[string]any); ok {
				if name, _ := fn["name"].(string); name != "" {
					existingNames[name] = true
				}
			}
		}
	}

	// Add missing tools
	for _, tool := range tools {
		if existingNames[tool.ToolName] {
			continue // Already exists
		}

		// Get the stored full tool definition from Metadata
		rawJSON, ok := tool.Metadata["raw_json"].(string)
		if !ok || rawJSON == "" {
			log.Warn().
				Str("tool", tool.ToolName).
				Msg("search_tool: cannot inject tool without raw_json in metadata")
			continue
		}

		// Parse the stored tool definition
		var toolDef map[string]any
		if err := json.Unmarshal([]byte(rawJSON), &toolDef); err != nil {
			log.Warn().
				Str("tool", tool.ToolName).
				Err(err).
				Msg("search_tool: cannot parse tool definition")
			continue
		}

		existingTools = append(existingTools, toolDef)
		existingNames[tool.ToolName] = true

		log.Debug().
			Str("tool", tool.ToolName).
			Msg("search_tool: injected tool into request")
	}

	request["tools"] = existingTools
	return utils.MarshalNoEscape(request)
}
