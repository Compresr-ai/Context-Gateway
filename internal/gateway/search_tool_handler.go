// Search tool handler for universal dispatcher tool discovery.
package gateway

import (
	"bytes"
	"context"
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
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/compresr/context-gateway/external"
	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/circuitbreaker"
	"github.com/compresr/context-gateway/internal/compresr"
	"github.com/compresr/context-gateway/internal/monitoring"
	phantom_tools "github.com/compresr/context-gateway/internal/phantom_tools"
	"github.com/compresr/context-gateway/internal/tokenizer"
)

// SearchRequestContext holds per-request state for search operations.
// This is separate from the handler to avoid race conditions.
type SearchRequestContext struct {
	Ctx           context.Context // request context for API calls
	SessionID     string
	DeferredTools []adapters.ExtractedContent
	CapturedAuth  CapturedAuth // User auth for fallback (centralized auth pattern)
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
	apiModel     string // Compresr compression model name (e.g., "tdc_coldbrew_v1")
	apiTimeout   time.Duration
	alwaysKeep   []string
	httpClient   *http.Client

	// Stage 2: Per-tool schema compression
	schemaCompression SchemaCompressionOpts

	// Per-request context (protected by mutex for concurrent safety)
	requestCtx *SearchRequestContext
	mu         sync.RWMutex

	// API fallback events captured during this request for telemetry.
	apiFallbackEvents []ToolDiscoveryAPIFallbackEvent

	// Search logging (for dashboard display)
	searchLog *monitoring.SearchLog
	requestID string
	sessionID string

	// is Main agent classification
	isMainAgent bool

	// Telemetry tracker (for JSONL persistence)
	tracker *monitoring.Tracker

	// Circuit breaker for the Compresr API (BUG-025).
	// Shared across requests for this handler instance.
	apiCircuit *circuitbreaker.CircuitBreaker
}

// SearchToolHandlerOptions configures gateway_search_tools behavior.
type SearchToolHandlerOptions struct {
	Strategy     string
	APIEndpoint  string
	ProviderAuth string
	APIModel     string // Compresr compression model name (e.g., "tdc_coldbrew_v1")
	APITimeout   time.Duration
	AlwaysKeep   []string

	// Stage 2: Per-tool schema compression (compresses each tool individually)
	SchemaCompression SchemaCompressionOpts
}

// SchemaCompressionOpts configures Stage 2 per-tool schema compression.
// Uses /api/compress/tool-output/ with toc_latte_v1 (different from Stage 1).
type SchemaCompressionOpts struct {
	Enabled        bool             // Enable per-tool compression (default: false)
	Endpoint       string           // API endpoint (default: /api/compress/tool-output/)
	APIKey         string           // API key (falls back to parent compresr.api_key)
	Model          string           // Compression model (default: toc_latte_v1)
	Timeout        time.Duration    // Request timeout (default: 10s)
	TokenThreshold int              // Skip tools below this token count (default: 200)
	Parallel       bool             // Compress tools in parallel (default: true)
	MaxConcurrent  int              // Max parallel workers (default: 5)
	CompresrClient *compresr.Client // Compresr API client
}

// ToolDiscoveryAPIFallbackEvent captures a degraded API search outcome.
type ToolDiscoveryAPIFallbackEvent struct {
	Query              string
	Reason             string
	Detail             string
	DeferredCount      int
	ReturnedCount      int
	OriginalPoolTokens int // total token count of the deferred pool at time of fallback
}

// NewSearchToolHandler creates a new search tool handler.
func NewSearchToolHandler(toolName string, maxResults int, sessionStore *ToolSessionStore, opts SearchToolHandlerOptions) *SearchToolHandler {
	if toolName == "" {
		toolName = phantom_tools.SearchToolName
	}
	if maxResults <= 0 {
		maxResults = 5
	}
	timeout := opts.APITimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return &SearchToolHandler{
		toolName:          toolName,
		maxResults:        maxResults,
		sessionStore:      sessionStore,
		strategy:          opts.Strategy,
		apiEndpoint:       opts.APIEndpoint,
		apiKey:            opts.ProviderAuth,
		apiModel:          opts.APIModel,
		apiTimeout:        timeout,
		alwaysKeep:        opts.AlwaysKeep,
		httpClient:        &http.Client{Timeout: timeout},
		schemaCompression: opts.SchemaCompression,
		apiCircuit:        circuitbreaker.New(),
	}
}

// SetRequestContext sets the context for the current request.
// Must be called before using in PhantomLoop. Thread-safe.
// ctx is stored so searchViaAPI can cancel the API call if the client disconnects.
// auth is the centralized captured auth for user auth fallback.
func (h *SearchToolHandler) SetRequestContext(ctx context.Context, sessionID string, deferredTools []adapters.ExtractedContent, auth CapturedAuth) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	h.requestCtx = &SearchRequestContext{
		Ctx:           ctx,
		SessionID:     sessionID,
		DeferredTools: deferredTools,
		CapturedAuth:  auth,
	}
	h.apiFallbackEvents = nil
}

// WithSearchLog sets the search log for recording gateway_search_tools calls.
func (h *SearchToolHandler) WithSearchLog(sl *monitoring.SearchLog, requestID, sessionID string) *SearchToolHandler {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.searchLog = sl
	h.requestID = requestID
	h.sessionID = sessionID
	return h
}

// WithTracker sets the telemetry tracker for JSONL persistence of search calls.
func (h *SearchToolHandler) WithTracker(t *monitoring.Tracker) *SearchToolHandler {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tracker = t
	return h
}

// WithIsMainAgent sets the main agent classification for logging.
func (h *SearchToolHandler) WithIsMainAgent(isMainAgent bool) *SearchToolHandler {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.isMainAgent = isMainAgent
	return h
}

// getRequestContext returns a copy of the current request context.
// Thread-safe.
func (h *SearchToolHandler) getRequestContext() *SearchRequestContext {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.requestCtx == nil {
		return &SearchRequestContext{Ctx: context.Background()}
	}
	// Return a copy to avoid races
	ctx := h.requestCtx.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return &SearchRequestContext{
		Ctx:           ctx,
		SessionID:     h.requestCtx.SessionID,
		DeferredTools: h.requestCtx.DeferredTools,
		CapturedAuth:  h.requestCtx.CapturedAuth,
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
func (h *SearchToolHandler) HandleCalls(calls []PhantomToolCall, adapter adapters.Adapter, requestBody []byte) *PhantomToolResult {
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
		return h.handleExecCalls(execCalls, adapter, requestBody)
	}

	// Otherwise, handle search calls (loop continues)
	return h.handleSearchCalls(searchCalls, adapter, requestBody)
}

// handleSearchCalls handles search-mode calls: search deferred tools and return results.
func (h *SearchToolHandler) handleSearchCalls(calls []PhantomToolCall, adapter adapters.Adapter, requestBody []byte) *PhantomToolResult {
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
			return h.buildToolResultMessage(calls, adapter, requestBody, hint)
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

	adapterCalls := make([]adapters.ToolCall, 0, len(calls))
	contentPerCall := make([]string, 0, len(calls))

	for _, call := range calls {
		query, _ := call.Input["query"].(string)
		matches := h.resolveMatches(reqCtx.Ctx, reqCtx.DeferredTools, query)

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
		var compressionResult *searchCompressionResult
		if len(newMatches) > 0 {
			resultText = formatSearchResults(newMatches)
			// Compress search results if enabled and above threshold
			cr := h.compressSearchResultsIfEnabled(reqCtx.Ctx, resultText, query, newNames, reqCtx.CapturedAuth)
			resultText = cr.Text
			compressionResult = &cr
		} else if len(matches) > 0 {
			// All matches were already expanded - no new tools to show
			resultText = "No additional tools found. The relevant tools are already available in your current tool set."
		} else {
			resultText = "No tools found matching the query."
		}

		adapterCalls = append(adapterCalls, adapters.ToolCall{
			ToolUseID: call.ToolUseID,
			ToolName:  call.ToolName,
			Input:     call.Input,
		})
		contentPerCall = append(contentPerCall, resultText)

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

		// Stage 1 metrics: Tool Selection (pool → selected)
		poolSchemaTokens := countSchemaTokens(reqCtx.DeferredTools)
		selectedSchemaTokens := countSchemaTokens(newMatches)
		stage1 := stage1Metrics{
			OriginalToolCount: len(reqCtx.DeferredTools),
			SelectedToolCount: len(newMatches),
			SelectedTools:     newNames,
			OriginalTokens:    poolSchemaTokens,
			CompressedTokens:  selectedSchemaTokens,
			CompressionRatio:  tokenizer.CompressionRatio(poolSchemaTokens, selectedSchemaTokens),
		}
		h.recordSearchEvent(query, stage1, compressionResult, h.isMainAgent)
	}

	// Delegate message construction to adapter (no more isAnthropic)
	result.ToolResults = adapter.BuildToolResultMessages(adapterCalls, contentPerCall, requestBody)

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
			return injectToolsIntoRequest(body, allNewMatches)
		}
	}

	return result
}

// handleExecCalls handles call-mode: validate, record mapping, stop loop with rewrite.
func (h *SearchToolHandler) handleExecCalls(calls []PhantomToolCall, adapter adapters.Adapter, requestBody []byte) *PhantomToolResult {
	reqCtx := h.getRequestContext()
	mappings := make([]*ToolCallMapping, 0, len(calls))
	provider := adapter.Provider()

	for _, call := range calls {
		toolName, _ := call.Input["tool_name"].(string)
		toolInput, _ := call.Input["tool_input"].(map[string]any)

		// Validate tool_name exists in deferred tools
		if !h.isKnownTool(reqCtx.DeferredTools, toolName) {
			return h.buildErrorResult(call, adapter, requestBody,
				fmt.Sprintf("Unknown tool '%s'. Use a search query to find available tools first.", toolName))
		}

		// Validate tool_input is present
		if toolInput == nil {
			return h.buildErrorResult(call, adapter, requestBody,
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
			return rewriteResponseForClient(responseBody, mappings, provider)
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
func (h *SearchToolHandler) buildErrorResult(call PhantomToolCall, adapter adapters.Adapter, requestBody []byte, errMsg string) *PhantomToolResult {
	return h.buildToolResultMessage([]PhantomToolCall{call}, adapter, requestBody, errMsg)
}

// buildToolResultMessage builds a PhantomToolResult containing tool_result messages.
func (h *SearchToolHandler) buildToolResultMessage(calls []PhantomToolCall, adapter adapters.Adapter, requestBody []byte, text string) *PhantomToolResult {
	adapterCalls := make([]adapters.ToolCall, 0, len(calls))
	contentPerCall := make([]string, 0, len(calls))
	for _, call := range calls {
		adapterCalls = append(adapterCalls, adapters.ToolCall{ToolUseID: call.ToolUseID, ToolName: call.ToolName, Input: call.Input})
		contentPerCall = append(contentPerCall, text)
	}
	return &PhantomToolResult{
		StopLoop:    false,
		ToolResults: adapter.BuildToolResultMessages(adapterCalls, contentPerCall, requestBody),
	}
}

// resolveMatches picks search backend by strategy.
// For tool-search: tries Compresr API first (if circuit is closed), falls back to local regex.
func (h *SearchToolHandler) resolveMatches(ctx context.Context, deferred []adapters.ExtractedContent, query string) []adapters.ExtractedContent {
	// Try API-backed search if endpoint is configured and circuit breaker allows it
	if h.apiEndpoint != "" {
		// Pre-compute pool token count once for all fallback paths.
		poolTokens := 0
		for _, t := range deferred {
			poolTokens += tokenizer.CountTokens(t.Content)
		}

		if !h.apiCircuit.Allow() {
			// Circuit is open — skip API call immediately and use local fallback
			log.Warn().Msg("search_tool: circuit breaker open, skipping Compresr API call")
			h.recordAPIFallback(query, "circuit_open", "circuit breaker open after repeated failures", len(deferred), len(deferred), poolTokens)
			return h.searchByRegex(deferred, query)
		}

		result, err := h.searchViaAPI(ctx, deferred, query)
		if err != nil {
			h.apiCircuit.RecordFailure()
			h.recordAPIFallback(query, "api_error", err.Error(), len(deferred), len(deferred), poolTokens)
			log.Warn().Err(err).Msg("search_tool: API failed, falling back to local regex search")
			return h.searchByRegex(deferred, query)
		}
		if !result.Meaningful {
			h.apiCircuit.RecordFailure()
			h.recordAPIFallback(query, result.Reason, result.Detail, len(deferred), len(deferred), poolTokens)
			log.Warn().
				Str("reason", result.Reason).
				Str("detail", result.Detail).
				Msg("search_tool: API returned non-meaningful selection, falling back to local regex search")
			return h.searchByRegex(deferred, query)
		}
		h.apiCircuit.RecordSuccess()
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

	// Compile regex pattern (case-insensitive).
	// QuoteMeta escapes all regex metacharacters so LLM-provided queries are
	// treated as literal keyword searches, preventing ReDoS injection.
	re, err := regexp.Compile("(?i)" + regexp.QuoteMeta(query))
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

// extractDescriptionFromDef extracts description from raw tool definition.
// Handles multiple wire formats:
//   - Anthropic: { "name": "...", "description": "..." }
//   - OpenAI nested: { "function": { "description": "..." } }
//   - OpenAI flat: { "description": "..." }
func extractDescriptionFromDef(def map[string]any) string {
	// Try direct description field (Anthropic format)
	if desc, ok := def["description"].(string); ok && desc != "" {
		return desc
	}
	// Try OpenAI nested function format
	if fn, ok := def["function"].(map[string]any); ok {
		if desc, ok := fn["description"].(string); ok && desc != "" {
			return desc
		}
	}
	return ""
}

type toolDiscoverySearchRequest struct {
	Query                string                 `json:"query"`
	MaxTools             int                    `json:"max_tools"`
	AlwaysKeep           []string               `json:"always_keep,omitempty"`
	Tools                []toolDiscoveryAPITool `json:"tools"`
	CompressionModelName string                 `json:"compression_model_name"`
}

type toolDiscoveryAPITool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Definition  map[string]any `json:"definition,omitempty"`
}

// toolDiscoverySearchResponse wraps the API response from Compresr tool-discovery endpoint.
type toolDiscoverySearchResponse struct {
	Success bool                           `json:"success"`
	Data    *toolDiscoverySearchResultData `json:"data,omitempty"`
	Error   string                         `json:"error,omitempty"`
}

type toolDiscoverySearchResultData struct {
	RelevantTools []string `json:"relevant_tools"`
}

type apiSearchResult struct {
	Matches    []adapters.ExtractedContent
	Meaningful bool
	Reason     string
	Detail     string
}

// searchViaAPI calls the external selector endpoint and maps selected names back to deferred tools.
// Accepts ctx so the API call is cancelled if the HTTP client disconnects.
func (h *SearchToolHandler) searchViaAPI(ctx context.Context, deferred []adapters.ExtractedContent, query string) (*apiSearchResult, error) {
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
		Query:                query,
		MaxTools:             h.maxResults,
		AlwaysKeep:           h.alwaysKeep,
		Tools:                make([]toolDiscoveryAPITool, 0, len(deferred)),
		CompressionModelName: h.apiModel,
	}

	for _, t := range deferred {
		apiTool := toolDiscoveryAPITool{
			Name:        t.ToolName,
			Description: t.Content, // Default to Content (may be "[deferred]")
		}
		// Extract original description from raw_json if available — deferred tools
		// have Content="[deferred]" which is useless for semantic search.
		if rawJSON, ok := t.Metadata["raw_json"].(string); ok && rawJSON != "" {
			var def map[string]any
			if err := json.Unmarshal([]byte(rawJSON), &def); err == nil {
				apiTool.Definition = def
				// Try to extract description from the full tool definition
				if desc := extractDescriptionFromDef(def); desc != "" {
					apiTool.Description = desc
				}
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsedURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.apiKey != "" {
		req.Header.Set("X-API-Key", h.apiKey)
	}

	resp, err := h.httpClient.Do(req) //nolint:gosec // G704: URL is parsed and scheme-validated (http/https only) above
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// surface read errors rather than silently passing partial bytes to json.Unmarshal
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize))
	if readErr != nil {
		return nil, fmt.Errorf("read API response body: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var parsed toolDiscoverySearchResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, err
	}
	if !parsed.Success {
		return nil, fmt.Errorf("API error: %s", parsed.Error)
	}
	if parsed.Data == nil || len(parsed.Data.RelevantTools) == 0 {
		return &apiSearchResult{
			Meaningful: false,
			Reason:     "empty_selection",
			Detail:     "relevant_tools was empty",
		}, nil
	}

	selectedSet := make(map[string]bool, len(parsed.Data.RelevantTools))
	for _, name := range parsed.Data.RelevantTools {
		selectedSet[name] = true
	}

	matches := make([]adapters.ExtractedContent, 0, len(parsed.Data.RelevantTools))
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
			Detail:     fmt.Sprintf("relevant_tools did not match deferred tools: %v", parsed.Data.RelevantTools),
		}, nil
	}
	return &apiSearchResult{
		Matches:    matches,
		Meaningful: true,
	}, nil
}

func (h *SearchToolHandler) recordAPIFallback(query, reason, detail string, deferredCount, returnedCount, originalPoolTokens int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.apiFallbackEvents = append(h.apiFallbackEvents, ToolDiscoveryAPIFallbackEvent{
		Query:              query,
		Reason:             reason,
		Detail:             detail,
		DeferredCount:      deferredCount,
		ReturnedCount:      returnedCount,
		OriginalPoolTokens: originalPoolTokens,
	})
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

// searchCompressionResult holds the result of search result compression.
type searchCompressionResult struct {
	Text             string // The final text (compressed or original)
	OriginalTokens   int    // Token count before compression
	CompressedTokens int    // Token count after compression (same as original if passthrough)
	Strategy         string // Strategy used: passthrough | compresr | external_provider
	Compressed       bool   // Whether compression was actually applied
	// Stage tracking
	Stage1OriginalTokens     int
	Stage1CompressedTokens   int
	Stage1CompressionRatio   float64
	Stage2OriginalTokens     int
	Stage2CompressedTokens   int
	Stage2CompressionRatio   float64
	Stage2Strategy           string
	EndToEndOriginalTokens   int
	EndToEndCompressionRatio float64
}

// compressSearchResultsIfEnabled compresses the search result text if enabled and exceeds the token threshold.
// Returns compression result with Stage 2 (schema compression) metrics for logging.
// Uses schemaCompression settings (Stage 2 - per-tool schema compression).
// auth is the centralized captured auth for user auth fallback when Compresr client fails.
func (h *SearchToolHandler) compressSearchResultsIfEnabled(ctx context.Context, resultText, query string, toolNames []string, auth CapturedAuth) searchCompressionResult {
	cfg := h.schemaCompression

	// Count tokens using tiktoken (this is Stage 2 input = formatted search results)
	stage2InputTokens := tokenizer.CountTokens(resultText)

	// If not enabled, return original with no Stage 2 metrics
	if !cfg.Enabled {
		return searchCompressionResult{
			Text:                 resultText,
			OriginalTokens:       stage2InputTokens,
			CompressedTokens:     stage2InputTokens,
			Strategy:             "",
			Compressed:           false,
			Stage2OriginalTokens: stage2InputTokens,
		}
	}

	const strategy = "compresr"
	tokenThreshold := cfg.TokenThreshold
	if tokenThreshold <= 0 {
		tokenThreshold = 200 // Default threshold
	}

	// Below threshold - passthrough but record Stage 2 metrics
	if stage2InputTokens <= tokenThreshold {
		log.Debug().
			Int("stage2_input_tokens", stage2InputTokens).
			Int("threshold", tokenThreshold).
			Str("strategy", strategy).
			Msg("search_tool: Stage 2 below threshold, passthrough")
		return searchCompressionResult{
			Text:                   resultText,
			OriginalTokens:         stage2InputTokens,
			CompressedTokens:       stage2InputTokens,
			Strategy:               strategy,
			Compressed:             false,
			Stage2OriginalTokens:   stage2InputTokens,
			Stage2CompressedTokens: stage2InputTokens,
			Stage2CompressionRatio: 0.0,
			Stage2Strategy:         "passthrough_small",
		}
	}

	// Need Compresr API client - or user auth to fallback to external provider
	if cfg.CompresrClient == nil {
		// Try external provider fallback if user has auth
		if auth.HasAuth() {
			log.Info().
				Str("strategy", "external_provider_fallback").
				Msg("search_tool: no Compresr client, falling back to user auth")
			return h.compressViaExternalProvider(ctx, resultText, query, toolNames, auth, stage2InputTokens, stage2InputTokens)
		}
		log.Warn().
			Str("strategy", strategy).
			Msg("search_tool: compression enabled but no client configured and no user auth, passthrough")
		return searchCompressionResult{
			Text:                   resultText,
			OriginalTokens:         stage2InputTokens,
			CompressedTokens:       stage2InputTokens,
			Strategy:               strategy,
			Compressed:             false,
			Stage2OriginalTokens:   stage2InputTokens,
			Stage2CompressedTokens: stage2InputTokens,
			Stage2CompressionRatio: 0.0,
			Stage2Strategy:         "passthrough_no_client",
		}
	}

	// Build tool name for API call (join matched tool names)
	toolName := phantom_tools.SearchToolName
	if len(toolNames) > 0 {
		toolName = fmt.Sprintf("search_results[%s]", strings.Join(toolNames, ","))
	}

	// Call Compresr API to compress the search results (Stage 2 schema compression)
	params := compresr.CompressToolOutputParams{
		ToolOutput: resultText,
		UserQuery:  query,
		ToolName:   toolName,
		Source:     "gateway:schema_compression",
	}

	compressed, err := cfg.CompresrClient.CompressToolOutput(params)
	if err != nil {
		// Try external provider fallback if user has auth
		if auth.HasAuth() {
			log.Info().
				Err(err).
				Str("strategy", "external_provider_fallback").
				Msg("search_tool: Compresr API failed, falling back to user auth")
			return h.compressViaExternalProvider(ctx, resultText, query, toolNames, auth, stage2InputTokens, stage2InputTokens)
		}
		log.Warn().
			Err(err).
			Int("stage2_input_tokens", stage2InputTokens).
			Str("strategy", strategy).
			Msg("search_tool: Stage 2 compression failed and no user auth for fallback, passthrough")
		return searchCompressionResult{
			Text:                   resultText,
			OriginalTokens:         stage2InputTokens,
			CompressedTokens:       stage2InputTokens,
			Strategy:               strategy,
			Compressed:             false,
			Stage2OriginalTokens:   stage2InputTokens,
			Stage2CompressedTokens: stage2InputTokens,
			Stage2CompressionRatio: 0.0,
			Stage2Strategy:         "passthrough_api_error",
		}
	}

	// Count compressed tokens using tiktoken (Stage 2 output)
	stage2OutputTokens := tokenizer.CountTokens(compressed.CompressedOutput)

	// Validate compression actually reduced size
	if stage2OutputTokens >= stage2InputTokens {
		log.Debug().
			Int("stage2_input_tokens", stage2InputTokens).
			Int("stage2_output_tokens", stage2OutputTokens).
			Str("strategy", strategy).
			Msg("search_tool: Stage 2 compression ineffective (no token savings), passthrough")
		return searchCompressionResult{
			Text:                   resultText,
			OriginalTokens:         stage2InputTokens,
			CompressedTokens:       stage2InputTokens,
			Strategy:               strategy,
			Compressed:             false,
			Stage2OriginalTokens:   stage2InputTokens,
			Stage2CompressedTokens: stage2InputTokens,
			Stage2CompressionRatio: 0.0,
			Stage2Strategy:         "passthrough_ineffective",
		}
	}

	stage2Ratio := tokenizer.CompressionRatio(stage2InputTokens, stage2OutputTokens)

	log.Info().
		Int("stage2_input_tokens", stage2InputTokens).
		Int("stage2_output_tokens", stage2OutputTokens).
		Float64("stage2_ratio", stage2Ratio).
		Str("strategy", strategy).
		Strs("tools", toolNames).
		Msg("search_tool: Stage 2 compressed search results (schema_compression)")

	return searchCompressionResult{
		Text:                   compressed.CompressedOutput,
		OriginalTokens:         stage2InputTokens,
		CompressedTokens:       stage2OutputTokens,
		Strategy:               strategy,
		Compressed:             true,
		Stage2OriginalTokens:   stage2InputTokens,
		Stage2CompressedTokens: stage2OutputTokens,
		Stage2CompressionRatio: stage2Ratio,
		Stage2Strategy:         "compresr",
	}
}

// compressViaExternalProvider compresses search results using an external LLM provider.
// This is the fallback path when Compresr API is not configured but user has auth.
// Uses the captured user auth (API key or OAuth) to call the provider directly.
// This is Stage 2 compression via external provider.
func (h *SearchToolHandler) compressViaExternalProvider(ctx context.Context, resultText, query string, toolNames []string, auth CapturedAuth, stage2InputTokens, _ int) searchCompressionResult {
	const strategy = "external_provider"

	// Build tool name for logging
	toolName := phantom_tools.SearchToolName
	if len(toolNames) > 0 {
		toolName = fmt.Sprintf("search_results[%s]", strings.Join(toolNames, ","))
	}

	// Build prompts for tool result compression
	var systemPrompt, userPrompt string
	if query == "" {
		systemPrompt = external.SystemPromptQueryAgnostic
		userPrompt = external.UserPromptQueryAgnostic(toolName, resultText)
	} else {
		systemPrompt = external.SystemPromptQuerySpecific
		userPrompt = external.UserPromptQuerySpecific(query, toolName, resultText)
	}

	// Auto-calculate max tokens: allow at most half the input token count as output
	maxTokens := stage2InputTokens / 2
	if maxTokens < 256 {
		maxTokens = 256
	}
	if maxTokens > 4096 {
		maxTokens = 4096
	}

	// Build LLM call params with user auth fallback
	params := external.CallLLMParams{
		Endpoint:     auth.Endpoint, // Use endpoint from captured auth
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
		MaxTokens:    maxTokens,
		Timeout:      h.apiTimeout,
	}

	// Apply user auth (handles both API key and OAuth bearer token)
	if auth.IsXAPIKey {
		params.ProviderKey = auth.Token
	} else {
		params.BearerAuth = auth.Token
		if auth.BetaHeader != "" {
			params.ExtraHeaders = map[string]string{"anthropic-beta": auth.BetaHeader}
		}
	}

	result, err := external.CallLLM(ctx, params)
	if err != nil {
		log.Warn().
			Err(err).
			Int("stage2_input_tokens", stage2InputTokens).
			Str("strategy", strategy).
			Msg("search_tool: Stage 2 external provider compression failed, passthrough")
		return searchCompressionResult{
			Text:                   resultText,
			OriginalTokens:         stage2InputTokens,
			CompressedTokens:       stage2InputTokens,
			Strategy:               strategy,
			Compressed:             false,
			Stage2OriginalTokens:   stage2InputTokens,
			Stage2CompressedTokens: stage2InputTokens,
			Stage2CompressionRatio: 0.0,
			Stage2Strategy:         "passthrough_external_error",
		}
	}

	compressed := result.Content
	stage2OutputTokens := tokenizer.CountTokens(compressed)

	// Validate compression reduced size
	if stage2OutputTokens >= stage2InputTokens {
		log.Debug().
			Int("stage2_input_tokens", stage2InputTokens).
			Int("stage2_output_tokens", stage2OutputTokens).
			Str("strategy", strategy).
			Msg("search_tool: Stage 2 external provider compression ineffective (no token savings), passthrough")
		return searchCompressionResult{
			Text:                   resultText,
			OriginalTokens:         stage2InputTokens,
			CompressedTokens:       stage2InputTokens,
			Strategy:               strategy,
			Compressed:             false,
			Stage2OriginalTokens:   stage2InputTokens,
			Stage2CompressedTokens: stage2InputTokens,
			Stage2CompressionRatio: 0.0,
			Stage2Strategy:         "passthrough_external_ineffective",
		}
	}

	stage2Ratio := tokenizer.CompressionRatio(stage2InputTokens, stage2OutputTokens)

	log.Info().
		Int("stage2_input_tokens", stage2InputTokens).
		Int("stage2_output_tokens", stage2OutputTokens).
		Float64("stage2_ratio", stage2Ratio).
		Str("strategy", strategy).
		Strs("tools", toolNames).
		Msg("search_tool: Stage 2 compressed via external provider (user auth fallback)")

	return searchCompressionResult{
		Text:                   compressed,
		OriginalTokens:         stage2InputTokens,
		CompressedTokens:       stage2OutputTokens,
		Strategy:               strategy,
		Compressed:             true,
		Stage2OriginalTokens:   stage2InputTokens,
		Stage2CompressedTokens: stage2OutputTokens,
		Stage2CompressionRatio: stage2Ratio,
		Stage2Strategy:         "external_provider",
	}
}

// extractToolNames extracts tool names from matches.
func extractToolNames(matches []adapters.ExtractedContent) []string {
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m.ToolName
	}
	return names
}

// countSchemaTokens counts tokens from raw_json metadata for a list of tools.
// Uses raw_json (full schema) if available, falls back to Content (description).
func countSchemaTokens(tools []adapters.ExtractedContent) int {
	total := 0
	for _, t := range tools {
		// Prefer raw_json (full schema) for accurate token count
		if rawJSON, ok := t.Metadata["raw_json"].(string); ok && rawJSON != "" {
			total += tokenizer.CountTokens(rawJSON)
		} else {
			// Fallback to Content (description only)
			total += tokenizer.CountTokens(t.Content)
		}
	}
	return total
}

// stage1Metrics captures Stage 1 (tool selection) metrics.
type stage1Metrics struct {
	OriginalToolCount int
	SelectedToolCount int
	SelectedTools     []string
	OriginalTokens    int     // full pool schema tokens
	CompressedTokens  int     // selected tools schema tokens
	CompressionRatio  float64 // selection compression ratio
}

// recordSearchEvent records a search tool call to the in-memory log and persists to JSONL.
// Logs as "tool_search_result" event type in all cases; compression metrics are included when compressionResult is non-nil.
// stage1 contains Stage 1 (tool selection) metrics; compressionResult contains Stage 2 (schema compression) metrics.
func (h *SearchToolHandler) recordSearchEvent(query string, stage1 stage1Metrics, compressionResult *searchCompressionResult, isMainAgent bool) {
	h.mu.RLock()
	sl := h.searchLog
	tracker := h.tracker
	sessionID := h.sessionID
	requestID := h.requestID
	h.mu.RUnlock()

	// Record to in-memory ring buffer for dashboard display.
	if sl != nil {
		sl.Record(monitoring.SearchLogEntry{
			Timestamp:     time.Now(),
			SessionID:     sessionID,
			RequestID:     requestID,
			Query:         query,
			DeferredCount: stage1.OriginalToolCount,
			ResultsCount:  stage1.SelectedToolCount,
			ToolsFound:    stage1.SelectedTools,
			Strategy:      h.strategy,
		})
	}

	// Persist to tool_discovery.jsonl for aggregator consumption.
	if tracker != nil {
		eventType := monitoring.EventTypeToolSearchResult
		if compressionResult != nil && compressionResult.Strategy != "" {
			eventType = monitoring.EventTypeToolSearchSelect
		}

		// Final tokens returned to LLM
		finalTokens := stage1.CompressedTokens
		if compressionResult != nil && compressionResult.Compressed {
			finalTokens = compressionResult.CompressedTokens
		}

		entry := monitoring.ToolSearchResult{
			LogEntryBase: monitoring.LogEntryBase{
				RequestID: requestID,
				EventType: eventType,
				SessionID: sessionID,
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			},
			IsMainAgent: isMainAgent,
			Query:       query,

			// Stage 1: Tool Selection (pool → selected)
			OriginalToolCount:      stage1.OriginalToolCount,
			SelectedToolCount:      stage1.SelectedToolCount,
			SelectedTools:          stage1.SelectedTools,
			Stage1OriginalTokens:   stage1.OriginalTokens,
			Stage1CompressedTokens: stage1.CompressedTokens,
			Stage1CompressionRatio: stage1.CompressionRatio,

			// Deprecated fields (kept for backward compatibility)
			DeferredCount:    stage1.OriginalToolCount,
			ResultsCount:     stage1.SelectedToolCount,
			ToolsProvided:    stage1.SelectedTools,
			OriginalTokens:   stage1.OriginalTokens,
			CompressedTokens: finalTokens,
			CompressionRatio: tokenizer.CompressionRatio(stage1.OriginalTokens, finalTokens),
		}

		// Stage 2: Schema Compression (if applied)
		if compressionResult != nil && compressionResult.Strategy != "" {
			entry.Strategy = compressionResult.Strategy
			entry.Stage2OriginalTokens = compressionResult.Stage2OriginalTokens
			entry.Stage2CompressedTokens = compressionResult.Stage2CompressedTokens
			entry.Stage2CompressionRatio = compressionResult.Stage2CompressionRatio
			entry.Stage2Strategy = compressionResult.Stage2Strategy

			// End-to-end metrics
			entry.EndToEndOriginalTokens = stage1.OriginalTokens
			entry.EndToEndCompressedTokens = compressionResult.CompressedTokens
			entry.EndToEndCompressionRatio = tokenizer.CompressionRatio(stage1.OriginalTokens, compressionResult.CompressedTokens)
		} else {
			// No Stage 2 — end-to-end equals Stage 1
			entry.EndToEndOriginalTokens = stage1.OriginalTokens
			entry.EndToEndCompressedTokens = stage1.CompressedTokens
			entry.EndToEndCompressionRatio = stage1.CompressionRatio
		}

		tracker.LogToolSearch(entry)
	}
}

// injectToolsIntoRequest replaces deferred stubs in the tools array with full definitions.
// Finds each stub by tool name and replaces it in-place to avoid duplicate tool names,
// which both Anthropic and OpenAI reject with HTTP 400 ("Tool names must be unique").
// Falls back to append if the stub is not found (e.g. session resumed after restart).
func injectToolsIntoRequest(body []byte, tools []adapters.ExtractedContent) ([]byte, error) {
	if len(tools) == 0 {
		return body, nil
	}

	for _, tool := range tools {
		if tool.Content == "" {
			continue
		}
		var toolDef json.RawMessage
		if err := json.Unmarshal([]byte(tool.Content), &toolDef); err != nil {
			log.Warn().Str("tool", tool.ToolName).Err(err).Msg("search_tool: skipping invalid tool JSON")
			continue
		}
		modified, err := replaceStubInRequest(body, tool.ToolName, toolDef)
		if err != nil {
			return body, fmt.Errorf("failed to inject tool %s: %w", tool.ToolName, err)
		}
		body = modified
	}
	return body, nil
}

// replaceStubInRequest replaces a deferred stub at its current array position with the full
// tool definition. Supports Anthropic (tools[].name), OpenAI Chat (tools[].function.name),
// and OpenAI Responses API (tools[].name, flat format).
// Falls back to append if the stub is not found in the array.
func replaceStubInRequest(body []byte, toolName string, fullDef json.RawMessage) ([]byte, error) {
	toolsResult := gjson.GetBytes(body, "tools")
	if !toolsResult.Exists() {
		return body, nil
	}

	// Format detection: OpenAI Chat uses {type:"function", function:{name:...}} nesting.
	// Anthropic and OpenAI Responses API use flat {name:...} format.
	// We detect by inspecting the first tool element for a "function" subkey — this is
	// more reliable than checking top-level request keys because both Anthropic and
	// OpenAI Chat use a "messages" key, making request-shape detection ambiguous.
	isOpenAIChat := toolsResult.Get("0.function").Exists()

	// Find the stub index by matching tool name in the array.
	foundIdx := -1
	toolsResult.ForEach(func(key, value gjson.Result) bool {
		var name string
		if isOpenAIChat {
			// OpenAI Chat Completions: {type:"function", function:{name:...}}
			name = value.Get("function.name").String()
		} else {
			// Anthropic and Responses API: {name:...} flat
			name = value.Get("name").String()
		}
		if name == toolName {
			foundIdx = int(key.Int())
			return false // stop iteration
		}
		return true
	})

	if foundIdx < 0 {
		// Stub not found — fall back to append (handles session-resume after gateway restart).
		log.Debug().Str("tool", toolName).Msg("search_tool: stub not found, falling back to append")
		return sjson.SetRawBytes(body, "tools.-1", fullDef)
	}

	path := fmt.Sprintf("tools.%d", foundIdx)
	return sjson.SetRawBytes(body, path, fullDef)
}
