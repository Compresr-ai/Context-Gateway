// Package tooldiscovery filters tools dynamically based on relevance.
//
// DESIGN: Filters tool definitions based on relevance to the current
// query, reducing token overhead when many tools are registered.
//
// FLOW:
//  1. Receives adapter via PipeContext
//  2. Calls adapter.ExtractToolDiscovery() to get tool definitions
//  3. Scores tools using multi-signal relevance (recently used, keyword match, always-keep)
//  4. Keeps top-scoring tools up to MaxTools or TargetRatio
//  5. Calls adapter.ApplyToolDiscovery() to patch filtered tools back
//  6. (Hybrid) Stores deferred tools in context for session storage
//  7. (Hybrid) Injects gateway_search_tools for fallback search
//
// STRATEGY: "relevance" — local keyword-based filtering (no external API)
package tooldiscovery

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/compresr"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
	"github.com/compresr/context-gateway/internal/utils"
)

// Default configuration values.
const (
	DefaultMinTools         = 5
	DefaultMaxTools         = 25
	DefaultMaxSearchResults = 5
	DefaultSearchToolName   = "gateway_search_tools"

	// SearchToolDescription is the description for the gateway_search_tools tool
	SearchToolDescription = "Retrieve the full definition of a tool that isn't currently loaded. Use when you need a capability that isn't available in your current tools."
)

// SearchToolSchema is the JSON schema for the gateway_search_tools tool
var SearchToolSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"query": map[string]any{
			"type":        "string",
			"description": "The tool name or keywords describing the capability you need",
		},
	},
	"required": []string{"query"},
}

// Score weights for relevance signals.
const (
	scoreExpanded     = 1000 // Tool was found via search (never filter again)
	scoreRecentlyUsed = 100  // Tool was used in conversation history
	scoreAlwaysKeep   = 100  // Tool is in the always-keep list
	scoreExactName    = 50   // Query contains exact tool name
	scoreWordMatch    = 10   // Per-word overlap between query and tool name/description
)

// Pipe filters tools dynamically based on relevance to the current query.
type Pipe struct {
	enabled              bool
	strategy             string
	minTools             int
	maxTools             int
	targetRatio          float64
	alwaysKeep           map[string]bool
	alwaysKeepList       []string // For API payload
	enableSearchFallback bool
	searchToolName       string
	maxSearchResults     int

	// Compresr API client (used when strategy=api)
	compresrClient *compresr.Client

	// API strategy fields (legacy, used for non-compresr endpoints)
	apiEndpoint string
	apiKey      string
	apiTimeout  time.Duration
}

// New creates a new tool discovery pipe.
func New(cfg *config.Config) *Pipe {
	minTools := cfg.Pipes.ToolDiscovery.MinTools
	if minTools == 0 {
		minTools = DefaultMinTools
	}

	maxTools := cfg.Pipes.ToolDiscovery.MaxTools
	if maxTools == 0 {
		maxTools = DefaultMaxTools
	}

	targetRatio := cfg.Pipes.ToolDiscovery.TargetRatio
	if targetRatio == 0 {
		targetRatio = 0.8 // Keep 80% of tools by default
	}

	alwaysKeep := make(map[string]bool)
	for _, name := range cfg.Pipes.ToolDiscovery.AlwaysKeep {
		alwaysKeep[name] = true
	}

	// Search fallback behavior:
	// - relevance strategy: disabled (pure score-based filtering only)
	// - tool-search strategy: enabled (LLM uses gateway_search_tools phantom tool)
	// - api strategy: disabled (direct API filtering, no phantom tool)
	// - disabled pipe: forced off
	enableSearchFallback := cfg.Pipes.ToolDiscovery.EnableSearchFallback
	if cfg.Pipes.ToolDiscovery.Strategy == config.StrategyRelevance {
		enableSearchFallback = false
	}
	if cfg.Pipes.ToolDiscovery.Strategy == config.StrategyAPI {
		enableSearchFallback = false // API strategy filters directly, no phantom tool
	}
	if cfg.Pipes.ToolDiscovery.Strategy == config.StrategyToolSearch {
		enableSearchFallback = true
	}
	if !cfg.Pipes.ToolDiscovery.Enabled {
		enableSearchFallback = false
	}

	searchToolName := cfg.Pipes.ToolDiscovery.SearchToolName
	if searchToolName == "" {
		searchToolName = DefaultSearchToolName
	}

	maxSearchResults := cfg.Pipes.ToolDiscovery.MaxSearchResults
	if maxSearchResults == 0 {
		maxSearchResults = DefaultMaxSearchResults
	}

	// API strategy configuration
	apiEndpoint := cfg.Pipes.ToolDiscovery.API.Endpoint
	if cfg.Pipes.ToolDiscovery.Strategy == config.StrategyAPI {
		if apiEndpoint != "" {
			// Prepend Compresr base URL if endpoint is relative
			if !strings.HasPrefix(apiEndpoint, "http://") && !strings.HasPrefix(apiEndpoint, "https://") {
				apiEndpoint = cfg.URLs.Compresr + apiEndpoint
				// Clean up double slashes (except in http://)
				apiEndpoint = strings.Replace(apiEndpoint, "://", "::SCHEME::", 1)
				apiEndpoint = strings.ReplaceAll(apiEndpoint, "//", "/")
				apiEndpoint = strings.Replace(apiEndpoint, "::SCHEME::", "://", 1)
			}
		} else if cfg.URLs.Compresr != "" {
			// Default to compresr URL with standard path
			apiEndpoint = strings.TrimRight(cfg.URLs.Compresr, "/") + "/api/compress/tool-discovery/"
		}
	}
	apiTimeout := cfg.Pipes.ToolDiscovery.API.Timeout
	if apiTimeout <= 0 {
		apiTimeout = 10 * time.Second
	}

	// Initialize Compresr client when strategy is 'api'
	var compresrClient *compresr.Client
	if cfg.Pipes.ToolDiscovery.Strategy == config.StrategyAPI {
		baseURL := cfg.URLs.Compresr
		compresrClient = compresr.NewClient(baseURL, cfg.Pipes.ToolDiscovery.API.APISecret)
		log.Info().Str("base_url", baseURL).Msg("tool_discovery: initialized Compresr client for API strategy")
	}

	return &Pipe{
		enabled:              cfg.Pipes.ToolDiscovery.Enabled,
		strategy:             cfg.Pipes.ToolDiscovery.Strategy,
		minTools:             minTools,
		maxTools:             maxTools,
		targetRatio:          targetRatio,
		alwaysKeep:           alwaysKeep,
		alwaysKeepList:       cfg.Pipes.ToolDiscovery.AlwaysKeep,
		enableSearchFallback: enableSearchFallback,
		searchToolName:       searchToolName,
		maxSearchResults:     maxSearchResults,
		compresrClient:       compresrClient,
		apiEndpoint:          apiEndpoint,
		apiKey:               cfg.Pipes.ToolDiscovery.API.APISecret,
		apiTimeout:           apiTimeout,
	}
}

// Name returns the pipe name.
func (p *Pipe) Name() string {
	return "tool_discovery"
}

// Strategy returns the processing strategy.
func (p *Pipe) Strategy() string {
	return p.strategy
}

// Enabled returns whether the pipe is active.
func (p *Pipe) Enabled() bool {
	return p.enabled
}

// Process filters tools before sending to LLM.
//
// DESIGN: Pipes ALWAYS delegate extraction to adapters. Pipes contain NO
// provider-specific logic — they only implement filtering logic.
func (p *Pipe) Process(ctx *pipes.PipeContext) ([]byte, error) {
	if !p.enabled || p.strategy == config.StrategyPassthrough {
		return ctx.OriginalRequest, nil
	}

	switch p.strategy {
	case config.StrategyRelevance:
		return p.filterByRelevance(ctx)
	case config.StrategyAPI:
		return p.filterByAPI(ctx)
	case config.StrategyToolSearch:
		return p.prepareToolSearch(ctx)
	default:
		return ctx.OriginalRequest, nil
	}
}

// filterByRelevance scores and filters tools based on multi-signal relevance.
func (p *Pipe) filterByRelevance(ctx *pipes.PipeContext) ([]byte, error) {
	if ctx.Adapter == nil || len(ctx.OriginalRequest) == 0 {
		return ctx.OriginalRequest, nil
	}

	// All adapters must implement ParsedRequestAdapter for single-parse optimization
	parsedAdapter, ok := ctx.Adapter.(adapters.ParsedRequestAdapter)
	if !ok {
		log.Warn().Str("adapter", ctx.Adapter.Name()).Msg("tool_discovery: adapter does not implement ParsedRequestAdapter, skipping")
		return ctx.OriginalRequest, nil
	}

	return p.filterByRelevanceParsed(ctx, parsedAdapter)
}

// prepareToolSearch prepares requests for tool-search strategy.
// Strategy behavior:
//  1. Extract all tools from the request
//  2. Store them as deferred tools for session-scoped lookup
//  3. Replace tools[] with only gateway_search_tools (phantom tool)
func (p *Pipe) prepareToolSearch(ctx *pipes.PipeContext) ([]byte, error) {
	if ctx.Adapter == nil || len(ctx.OriginalRequest) == 0 {
		return ctx.OriginalRequest, nil
	}

	parsedAdapter, ok := ctx.Adapter.(adapters.ParsedRequestAdapter)
	if !ok {
		log.Warn().Str("adapter", ctx.Adapter.Name()).Msg("tool_discovery(tool-search): adapter does not implement ParsedRequestAdapter, skipping")
		return ctx.OriginalRequest, nil
	}

	parsed, err := parsedAdapter.ParseRequest(ctx.OriginalRequest)
	if err != nil {
		log.Warn().Err(err).Msg("tool_discovery(tool-search): parse failed, skipping")
		return ctx.OriginalRequest, nil
	}

	tools, err := parsedAdapter.ExtractToolDiscoveryFromParsed(parsed, nil)
	if err != nil {
		log.Warn().Err(err).Msg("tool_discovery(tool-search): extraction failed, skipping")
		return ctx.OriginalRequest, nil
	}
	if len(tools) == 0 {
		return ctx.OriginalRequest, nil
	}

	// Store all original tools for search and eventual re-injection.
	ctx.DeferredTools = tools
	ctx.ToolsFiltered = true

	modified, err := p.replaceWithSearchToolOnly(ctx.OriginalRequest, ctx.Adapter.Provider())
	if err != nil {
		log.Warn().Err(err).Msg("tool_discovery(tool-search): failed to replace tools with search tool")
		return ctx.OriginalRequest, nil
	}

	log.Info().
		Int("total", len(tools)).
		Str("search_tool", p.searchToolName).
		Msg("tool_discovery(tool-search): replaced tools with gateway search tool")

	return modified, nil
}

// filterByAPI filters tools by calling an external API.
// Strategy behavior:
//  1. Extract all tools and user query from the request
//  2. Call external API with tools and query
//  3. API returns selected tool names
//  4. Filter request to keep only selected tools
//  5. Forward filtered request to LLM (no phantom tool)
func (p *Pipe) filterByAPI(ctx *pipes.PipeContext) ([]byte, error) {
	if ctx.Adapter == nil || len(ctx.OriginalRequest) == 0 {
		return ctx.OriginalRequest, nil
	}

	parsedAdapter, ok := ctx.Adapter.(adapters.ParsedRequestAdapter)
	if !ok {
		log.Warn().Str("adapter", ctx.Adapter.Name()).Msg("tool_discovery(api): adapter does not implement ParsedRequestAdapter, skipping")
		return ctx.OriginalRequest, nil
	}

	parsed, err := parsedAdapter.ParseRequest(ctx.OriginalRequest)
	if err != nil {
		log.Warn().Err(err).Msg("tool_discovery(api): parse failed, skipping")
		return ctx.OriginalRequest, nil
	}

	tools, err := parsedAdapter.ExtractToolDiscoveryFromParsed(parsed, nil)
	if err != nil {
		log.Warn().Err(err).Msg("tool_discovery(api): extraction failed, skipping")
		return ctx.OriginalRequest, nil
	}
	if len(tools) == 0 {
		return ctx.OriginalRequest, nil
	}

	// Extract user query from the request
	query := parsedAdapter.ExtractUserQueryFromParsed(parsed)

	// Get provider name for source tracking
	provider := ctx.Adapter.Name()

	// Call external API to select relevant tools
	selectedNames, err := p.callToolSelectionAPI(tools, query, provider)
	if err != nil {
		log.Warn().Err(err).Msg("tool_discovery(api): API call failed, returning all tools")
		return ctx.OriginalRequest, nil
	}

	// If no tools selected, return original
	if len(selectedNames) == 0 {
		log.Warn().Msg("tool_discovery(api): API returned no tools, returning all tools")
		return ctx.OriginalRequest, nil
	}

	// Build set of selected names for fast lookup
	selectedSet := make(map[string]bool, len(selectedNames))
	for _, name := range selectedNames {
		selectedSet[name] = true
	}

	// Also include always-keep tools
	for _, name := range p.alwaysKeepList {
		selectedSet[name] = true
	}

	// Build filter results
	results := make([]adapters.CompressedResult, 0, len(tools))
	keptNames := make([]string, 0)
	for _, tool := range tools {
		keep := selectedSet[tool.ToolName]
		results = append(results, adapters.CompressedResult{
			ID:   tool.ID,
			Keep: keep,
		})
		if keep {
			keptNames = append(keptNames, tool.ToolName)
		}
	}

	// Apply filtered tools to request
	modified, err := parsedAdapter.ApplyToolDiscoveryToParsed(parsed, results)
	if err != nil {
		log.Warn().Err(err).Msg("tool_discovery(api): apply failed, returning original")
		return ctx.OriginalRequest, nil
	}

	// Mark that filtering occurred
	ctx.ToolsFiltered = true

	log.Info().
		Str("query", query).
		Int("total", len(tools)).
		Int("kept", len(keptNames)).
		Strs("kept_tools", keptNames).
		Msg("tool_discovery(api): filtered tools via API")

	return modified, nil
}

// callToolSelectionAPI calls the Compresr API to select relevant tools.
func (p *Pipe) callToolSelectionAPI(tools []adapters.ExtractedContent, query, provider string) ([]string, error) {
	// Use the centralized Compresr client
	if p.compresrClient == nil {
		return nil, fmt.Errorf("compresr client not initialized")
	}

	// Convert to compresr.ToolDefinition slice
	toolDefs := make([]compresr.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		apiTool := compresr.ToolDefinition{
			Name:        tool.ToolName,
			Description: tool.Content,
		}
		// Include full parameters schema if available (backend expects 'parameters' field)
		if rawJSON, ok := tool.Metadata["raw_json"].(string); ok && rawJSON != "" {
			var def map[string]any
			if err := json.Unmarshal([]byte(rawJSON), &def); err == nil {
				apiTool.Parameters = def
			}
		}
		toolDefs = append(toolDefs, apiTool)
	}

	// Build source string: gateway:anthropic or gateway:openai
	source := "gateway:" + provider

	params := compresr.FilterToolsParams{
		Query:      query,
		AlwaysKeep: p.alwaysKeepList,
		Tools:      toolDefs,
		MaxTools:   p.maxTools, // Use configured max from pipe config
		Source:     source,
	}

	result, err := p.compresrClient.FilterTools(params)
	if err != nil {
		return nil, fmt.Errorf("compresr API call failed: %w", err)
	}

	return result.RelevantTools, nil
}

// =============================================================================
// SHARED FILTERING LOGIC
// =============================================================================

// filterInput contains extracted data needed for filtering.
type filterInput struct {
	tools         []adapters.ExtractedContent
	query         string
	recentTools   map[string]bool
	expandedTools map[string]bool
}

// filterOutput contains the filtering results.
type filterOutput struct {
	results       []adapters.CompressedResult
	deferred      []adapters.ExtractedContent
	keptNames     []string
	deferredNames []string
	keptCount     int
}

// scoredTool pairs a tool with its relevance score.
type scoredTool struct {
	tool  adapters.ExtractedContent
	score int
}

// scoreAndFilterTools scores tools and determines which to keep.
func (p *Pipe) scoreAndFilterTools(input *filterInput) *filterOutput {
	totalTools := len(input.tools)

	// Score each tool
	scored := make([]scoredTool, 0, totalTools)
	for _, tool := range input.tools {
		score := p.scoreToolWithExpanded(tool, input.query, input.recentTools, input.expandedTools)
		scored = append(scored, scoredTool{tool: tool, score: score})
	}

	// Sort by score descending (simple insertion sort — tool counts are small)
	for i := 1; i < len(scored); i++ {
		for j := i; j > 0 && scored[j].score > scored[j-1].score; j-- {
			scored[j], scored[j-1] = scored[j-1], scored[j]
		}
	}

	// Determine how many tools to keep
	keepCount := p.calculateKeepCount(totalTools)

	// Build results with Keep flag and track deferred tools
	results := make([]adapters.CompressedResult, 0, totalTools)
	deferred := make([]adapters.ExtractedContent, 0)
	keptNames := make([]string, 0)
	deferredNames := make([]string, 0)
	kept := 0

	for _, s := range scored {
		// Force-keep: alwaysKeep list OR expanded tools (found via search)
		forceKeep := p.alwaysKeep[s.tool.ToolName] || input.expandedTools[s.tool.ToolName]
		keep := kept < keepCount || forceKeep
		results = append(results, adapters.CompressedResult{
			ID:   s.tool.ID,
			Keep: keep,
		})
		if keep {
			kept++
			keptNames = append(keptNames, s.tool.ToolName)
		} else {
			deferred = append(deferred, s.tool)
			deferredNames = append(deferredNames, s.tool.ToolName)
		}
	}

	return &filterOutput{
		results:       results,
		deferred:      deferred,
		keptNames:     keptNames,
		deferredNames: deferredNames,
		keptCount:     kept,
	}
}

// applyFilterResults applies filtering output to context and logs.
func (p *Pipe) applyFilterResults(ctx *pipes.PipeContext, output *filterOutput, query string, totalTools int, modified []byte) []byte {
	// Store deferred tools in context for session storage
	ctx.DeferredTools = output.deferred
	ctx.ToolsFiltered = true

	// Inject search tool if enabled and we filtered tools
	if p.enableSearchFallback && len(output.deferred) > 0 {
		var err error
		modified, err = p.injectSearchTool(modified, ctx.Adapter.Provider())
		if err != nil {
			log.Warn().Err(err).Msg("tool_discovery: failed to inject search tool")
			// Continue without search tool - not fatal
		}
	}

	// Detailed logging: show query, kept tools, and deferred tools
	log.Info().
		Str("query", query).
		Int("total", totalTools).
		Int("kept", output.keptCount).
		Strs("kept_tools", output.keptNames).
		Int("deferred", len(output.deferred)).
		Strs("deferred_tools", output.deferredNames).
		Bool("search_fallback", p.enableSearchFallback && len(output.deferred) > 0).
		Msg("tool_discovery: filtered tools by relevance")

	return modified
}

// =============================================================================
// PARSED PATH (optimized single-parse)
// =============================================================================

// filterByRelevanceParsed is the optimized path that parses JSON once.
func (p *Pipe) filterByRelevanceParsed(ctx *pipes.PipeContext, parsedAdapter adapters.ParsedRequestAdapter) ([]byte, error) {
	// Parse request ONCE
	parsed, err := parsedAdapter.ParseRequest(ctx.OriginalRequest)
	if err != nil {
		log.Warn().Err(err).Msg("tool_discovery: parse failed, skipping filtering")
		return ctx.OriginalRequest, nil
	}

	// Extract tool definitions from parsed request (no JSON parsing)
	tools, err := parsedAdapter.ExtractToolDiscoveryFromParsed(parsed, nil)
	if err != nil {
		log.Warn().Err(err).Msg("tool_discovery: extraction failed, skipping filtering")
		return ctx.OriginalRequest, nil
	}

	totalTools := len(tools)
	if totalTools == 0 {
		return ctx.OriginalRequest, nil
	}

	// Skip filtering if below minimum threshold
	if totalTools <= p.minTools {
		log.Debug().
			Int("tools", totalTools).
			Int("min_tools", p.minTools).
			Msg("tool_discovery: below min threshold, skipping")
		return ctx.OriginalRequest, nil
	}

	// Get user query from parsed request (no JSON parsing)
	query := parsedAdapter.ExtractUserQueryFromParsed(parsed)

	// Get recently-used tool names from parsed request (no JSON parsing)
	recentTools := p.extractRecentlyUsedToolsParsed(parsedAdapter, parsed)

	// Get expanded tools from session context (tools found via search)
	expandedTools := ctx.ExpandedTools
	if expandedTools == nil {
		expandedTools = make(map[string]bool)
	}

	// Check if filtering would be a no-op
	keepCount := p.calculateKeepCount(totalTools)
	if keepCount >= totalTools {
		log.Debug().
			Int("tools", totalTools).
			Int("keep_count", keepCount).
			Msg("tool_discovery: keep count >= total, skipping")
		return ctx.OriginalRequest, nil
	}

	// Score and filter tools using shared logic
	output := p.scoreAndFilterTools(&filterInput{
		tools:         tools,
		query:         query,
		recentTools:   recentTools,
		expandedTools: expandedTools,
	})

	// Apply filtered tools using parsed structure (single marshal at end)
	modified, err := parsedAdapter.ApplyToolDiscoveryToParsed(parsed, output.results)
	if err != nil {
		log.Warn().Err(err).Msg("tool_discovery: apply failed, returning original")
		return ctx.OriginalRequest, nil
	}

	// Apply results, inject search tool, and log
	modified = p.applyFilterResults(ctx, output, query, totalTools, modified)

	return modified, nil
}

// calculateKeepCount returns how many tools to keep based on config.
func (p *Pipe) calculateKeepCount(total int) int {
	// Apply target ratio
	byRatio := int(float64(total) * p.targetRatio)

	// Cap at MaxTools
	keep := byRatio
	if keep > p.maxTools {
		keep = p.maxTools
	}

	// Ensure we keep at least MinTools
	if keep < p.minTools {
		keep = p.minTools
	}

	return keep
}

// scoreToolWithExpanded computes a relevance score including expanded tools.
func (p *Pipe) scoreToolWithExpanded(tool adapters.ExtractedContent, query string, recentTools map[string]bool, expandedTools map[string]bool) int {
	score := 0

	// Signal 0: Expanded tools (found via search) - highest priority
	if expandedTools != nil && expandedTools[tool.ToolName] {
		score += scoreExpanded
	}

	// Signal 1: Always-keep list
	if p.alwaysKeep[tool.ToolName] {
		score += scoreAlwaysKeep
	}

	// Signal 2: Recently used in conversation
	if recentTools[tool.ToolName] {
		score += scoreRecentlyUsed
	}

	if query == "" {
		return score
	}

	queryLower := strings.ToLower(query)
	toolNameLower := strings.ToLower(tool.ToolName)

	// Signal 3: Exact tool name appears in query
	if strings.Contains(queryLower, toolNameLower) {
		score += scoreExactName
	}

	// Signal 4: Word overlap between query and tool name + description
	queryWords := tokenize(queryLower)
	toolWords := tokenize(toolNameLower + " " + strings.ToLower(tool.Content))

	toolWordSet := make(map[string]bool, len(toolWords))
	for _, w := range toolWords {
		toolWordSet[w] = true
	}

	for _, w := range queryWords {
		if toolWordSet[w] {
			score += scoreWordMatch
		}
	}

	return score
}

// =============================================================================
// SEARCH TOOL INJECTION
// =============================================================================

// injectSearchTool adds the gateway_search_tools tool to the request.
// This allows the LLM to request tools that were filtered out.
func (p *Pipe) injectSearchTool(body []byte, provider adapters.Provider) ([]byte, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body, err
	}

	tools, ok := req["tools"].([]any)
	if !ok {
		tools = []any{}
	}

	// Detect if this is Responses API format (has "input" field) or Chat Completions
	_, hasInput := req["input"]
	isResponsesAPI := hasInput && provider == adapters.ProviderOpenAI

	// Build the search tool definition based on provider and API format
	searchTool := p.buildSearchToolDefinitionForFormat(provider, isResponsesAPI)

	tools = append(tools, searchTool)
	req["tools"] = tools

	return utils.MarshalNoEscape(req)
}

// replaceWithSearchToolOnly sets tools[] to just the search tool definition.
func (p *Pipe) replaceWithSearchToolOnly(body []byte, provider adapters.Provider) ([]byte, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body, err
	}

	// Detect if this is Responses API format (has "input" field) or Chat Completions
	_, hasInput := req["input"]
	isResponsesAPI := hasInput && provider == adapters.ProviderOpenAI

	searchTool := p.buildSearchToolDefinitionForFormat(provider, isResponsesAPI)
	req["tools"] = []any{searchTool}

	return utils.MarshalNoEscape(req)
}

// buildSearchToolDefinitionForFormat creates the search tool in the appropriate format.
// isResponsesAPI indicates whether to use flat format (Responses API) or nested (Chat Completions).
func (p *Pipe) buildSearchToolDefinitionForFormat(provider adapters.Provider, isResponsesAPI bool) map[string]any {
	if isResponsesAPI {
		// OpenAI Responses API format (flat): {"type":"function","name":"...","parameters":...}
		return map[string]any{
			"type":        "function",
			"name":        p.searchToolName,
			"description": SearchToolDescription,
			"parameters":  SearchToolSchema,
		}
	}
	return p.buildSearchToolDefinition(provider)
}

// buildSearchToolDefinition creates the search tool in the appropriate provider format.
// Note: For OpenAI Responses API, use buildSearchToolDefinitionForFormat instead.
func (p *Pipe) buildSearchToolDefinition(provider adapters.Provider) map[string]any {
	switch provider {
	case adapters.ProviderOpenAI:
		// OpenAI format: wrapped in "function"
		return map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        p.searchToolName,
				"description": SearchToolDescription,
				"parameters":  SearchToolSchema,
			},
		}
	default:
		// Anthropic format (default)
		return map[string]any{
			"name":         p.searchToolName,
			"description":  SearchToolDescription,
			"input_schema": SearchToolSchema,
		}
	}
}

// extractRecentlyUsedToolsParsed gets tool names from a pre-parsed request.
// Uses ExtractToolOutputFromParsed to find tool results without re-parsing JSON.
func (p *Pipe) extractRecentlyUsedToolsParsed(parsedAdapter adapters.ParsedRequestAdapter, parsed *adapters.ParsedRequest) map[string]bool {
	recent := make(map[string]bool)

	extracted, err := parsedAdapter.ExtractToolOutputFromParsed(parsed)
	if err != nil || len(extracted) == 0 {
		return recent
	}

	for _, ext := range extracted {
		if ext.ToolName != "" {
			recent[ext.ToolName] = true
		}
	}

	return recent
}

// tokenize splits text into lowercase words, filtering short ones and stop words.
func tokenize(text string) []string {
	words := strings.FieldsFunc(text, func(r rune) bool {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		return !isAlphaNum
	})

	filtered := make([]string, 0, len(words))
	for _, w := range words {
		if len(w) >= 3 && !stopWords[w] {
			filtered = append(filtered, w)
		}
	}
	return filtered
}

// stopWords are common English words filtered during tokenization.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "are": true, "but": true,
	"not": true, "you": true, "all": true, "can": true, "has": true,
	"her": true, "was": true, "one": true, "our": true, "out": true,
	"this": true, "that": true, "with": true, "have": true, "from": true,
	"they": true, "been": true, "will": true, "each": true, "make": true,
	"like": true, "just": true, "than": true, "them": true, "some": true,
	"into": true, "when": true, "what": true, "which": true, "their": true,
	"there": true, "about": true, "would": true, "these": true, "other": true,
}
