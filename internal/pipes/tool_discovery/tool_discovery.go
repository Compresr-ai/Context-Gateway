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
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/compresr"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
)

// Default configuration values.
const (
	DefaultMinTools         = 5
	DefaultMaxTools         = 25
	DefaultMaxSearchResults = 5
	DefaultSearchToolName   = "gateway_search_tools"

	// SearchToolDescription is the description for the gateway_search_tools tool.
	// Two modes: search (provide query) and call (provide tool_name + tool_input).
	SearchToolDescription = `Search for or execute available tools.

MODE 1 - SEARCH: Provide "query" to find tools by describing what you need.
Returns tool names, descriptions, and full input schemas.
Example: {"query": "read a file from disk"}

MODE 2 - CALL: Provide "tool_name" and "tool_input" to execute a discovered tool.
Use the exact parameter names and types from the schema returned by search.
Example: {"tool_name": "Read", "tool_input": {"file_path": "/foo/bar.txt"}}

Always search first if you haven't seen the tool's schema yet.`
)

// SearchToolSchema is the JSON schema for the gateway_search_tools tool.
// Supports two modes: search (query) and call (tool_name + tool_input).
// Validation of which fields are required happens in the handler, not the schema.
var SearchToolSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"query": map[string]any{
			"type":        "string",
			"description": "Search query to find available tools. Describe what you need to do. Example: 'read a file', 'search code', 'run a shell command'.",
		},
		"tool_name": map[string]any{
			"type":        "string",
			"description": "Name of a previously discovered tool to execute. Use this after finding a tool via query.",
		},
		"tool_input": map[string]any{
			"type":                 "object",
			"description":          "Input parameters for the tool being called. Must match the schema returned by the search results.",
			"additionalProperties": true,
		},
	},
	"required": []string{},
}

// Pre-computed search tool JSON bytes per provider format.
// Computed once at init time using fmt.Sprintf for deterministic output.
// This ensures byte-identical output across calls, preserving KV-cache stability.
var (
	searchToolJSON_Anthropic  []byte
	searchToolJSON_OpenAIChat []byte
	searchToolJSON_Responses  []byte
)

func init() {
	// Marshal the schema once (map ordering is alphabetical in Go's json.Marshal)
	schemaBytes, _ := json.Marshal(SearchToolSchema)
	descBytes, _ := json.Marshal(SearchToolDescription)

	// Anthropic format: {name, description, input_schema}
	searchToolJSON_Anthropic = []byte(`{"name":` + `"` + DefaultSearchToolName + `"` +
		`,"description":` + string(descBytes) +
		`,"input_schema":` + string(schemaBytes) + `}`)

	// OpenAI Chat Completions: {type, function: {name, description, parameters}}
	searchToolJSON_OpenAIChat = []byte(`{"type":"function","function":{"name":"` + DefaultSearchToolName + `"` +
		`,"description":` + string(descBytes) +
		`,"parameters":` + string(schemaBytes) + `}}`)

	// OpenAI Responses API: {type, name, description, parameters} (flat)
	searchToolJSON_Responses = []byte(`{"type":"function","name":"` + DefaultSearchToolName + `"` +
		`,"description":` + string(descBytes) +
		`,"parameters":` + string(schemaBytes) + `}`)
}

// getSearchToolJSON returns the pre-computed search tool bytes for the given provider.
func getSearchToolJSON(provider adapters.Provider, isResponsesAPI bool) []byte {
	if isResponsesAPI {
		return searchToolJSON_Responses
	}
	if provider == adapters.ProviderOpenAI {
		return searchToolJSON_OpenAIChat
	}
	return searchToolJSON_Anthropic
}

// Score weights for relevance signals.
const (
	scoreRecentlyUsed = 100 // Tool was used in conversation history
	scoreExactName    = 50  // Query contains exact tool name
	scoreWordMatch    = 10  // Per-word overlap between query and tool name/description
)

// Pipe filters tools dynamically based on relevance to the current query.
type Pipe struct {
	enabled              bool
	strategy             string
	minTools             int
	maxTools             int
	targetRatio          float64
	minRemoval           int
	alwaysKeep           map[string]bool
	alwaysKeepList       []string // For API payload
	enableSearchFallback bool
	searchToolName       string
	maxSearchResults     int

	// Compresr API client (used when strategy=compresr)
	compresrClient *compresr.Client

	// Compresr strategy fields
	compresrEndpoint string
	compresrKey      string
	compresrModel    string // Model name for compresr strategy (e.g., "tdc_coldbrew_v1")
	compresrTimeout  time.Duration
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
	// - tool-search strategy: always enabled (universal dispatcher)
	// - relevance strategy: never enabled (tools are filtered locally, nothing deferred to search)
	// - other strategies: respect enable_search_fallback config
	// - disabled pipe: forced off
	enableSearchFallback := cfg.Pipes.ToolDiscovery.Enabled &&
		cfg.Pipes.ToolDiscovery.Strategy != pipes.StrategyRelevance &&
		(cfg.Pipes.ToolDiscovery.Strategy == config.StrategyToolSearch || cfg.Pipes.ToolDiscovery.EnableSearchFallback)

	searchToolName := cfg.Pipes.ToolDiscovery.SearchToolName
	if searchToolName == "" {
		searchToolName = DefaultSearchToolName
	}

	maxSearchResults := cfg.Pipes.ToolDiscovery.MaxSearchResults
	if maxSearchResults == 0 {
		maxSearchResults = DefaultMaxSearchResults
	}

	// Compresr API configuration for tool-search strategy (API-backed search)
	compresrEndpoint := cfg.Pipes.ToolDiscovery.Compresr.Endpoint
	if cfg.Pipes.ToolDiscovery.Strategy == config.StrategyToolSearch {
		if compresrEndpoint != "" {
			// Prepend Compresr base URL if endpoint is relative
			if !strings.HasPrefix(compresrEndpoint, "http://") && !strings.HasPrefix(compresrEndpoint, "https://") {
				compresrEndpoint = pipes.NormalizeEndpointURL(cfg.URLs.Compresr, compresrEndpoint)
			}
		} else if cfg.URLs.Compresr != "" {
			// Default to compresr URL with standard path
			compresrEndpoint = strings.TrimRight(cfg.URLs.Compresr, "/") + "/api/compress/tool-discovery/"
		}
	}
	compresrTimeout := cfg.Pipes.ToolDiscovery.Compresr.Timeout
	if compresrTimeout <= 0 {
		compresrTimeout = 10 * time.Second
	}

	// Initialize Compresr client for API-backed strategies (compresr + tool-search).
	var compresrClient *compresr.Client
	tdStrategy := cfg.Pipes.ToolDiscovery.Strategy
	if tdStrategy == config.StrategyCompresr || tdStrategy == config.StrategyToolSearch {
		baseURL := cfg.URLs.Compresr
		compresrKey := cfg.Pipes.ToolDiscovery.Compresr.AuthParam
		if baseURL != "" || compresrKey != "" {
			compresrClient = compresr.NewClient(baseURL, compresrKey, compresr.WithTimeout(compresrTimeout))
			log.Info().Str("base_url", baseURL).Str("strategy", tdStrategy).Msg("tool_discovery: initialized Compresr client")
		} else {
			log.Debug().Str("strategy", tdStrategy).Msg("tool_discovery: API strategy without Compresr credentials, will use local fallback")
		}
	}

	// minRemoval: skip filtering if fewer than N tools would be removed.
	// Config value 0 = unset → default 3. Use negative value to always filter.
	minRemoval := cfg.Pipes.ToolDiscovery.MinRemoval
	if minRemoval == 0 {
		minRemoval = 3 // default: skip if removal < 3 (token savings negligible)
	} else if minRemoval < 0 {
		minRemoval = 0 // negative = always filter
	}

	return &Pipe{
		enabled:              cfg.Pipes.ToolDiscovery.Enabled,
		strategy:             cfg.Pipes.ToolDiscovery.Strategy,
		minTools:             minTools,
		maxTools:             maxTools,
		targetRatio:          targetRatio,
		minRemoval:           minRemoval,
		alwaysKeep:           alwaysKeep,
		alwaysKeepList:       cfg.Pipes.ToolDiscovery.AlwaysKeep,
		enableSearchFallback: enableSearchFallback,
		searchToolName:       searchToolName,
		maxSearchResults:     maxSearchResults,
		compresrClient:       compresrClient,
		compresrEndpoint:     compresrEndpoint,
		compresrKey:          cfg.Pipes.ToolDiscovery.Compresr.AuthParam,
		compresrTimeout:      compresrTimeout,
		compresrModel:        cfg.Pipes.ToolDiscovery.Compresr.Model,
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

// getEffectiveModel returns the model name for logging.
// Returns configured API model, or default if not configured.
func (p *Pipe) getEffectiveModel() string {
	if p.compresrModel != "" {
		return p.compresrModel
	}
	return compresr.DefaultToolDiscoveryModel
}

// Process filters tools before sending to LLM.
//
// DESIGN: Pipes ALWAYS delegate extraction to adapters. Pipes contain NO
// provider-specific logic — they only implement filtering logic.
func (p *Pipe) Process(ctx *pipes.PipeContext) ([]byte, error) {
	if !p.enabled || p.strategy == config.StrategyPassthrough {
		return ctx.OriginalRequest, nil
	}

	// Set the model for logging
	ctx.ToolDiscoveryModel = p.getEffectiveModel()

	switch p.strategy {
	case config.StrategyRelevance:
		return p.filterByRelevance(ctx)
	case config.StrategyCompresr:
		return p.filterViaCompresr(ctx)
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
	ctx.OriginalToolCount = len(tools)
	ctx.FilteredToolCount = 1 // Only the gateway_search_tools tool

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

// filterViaCompresr calls the Compresr API to select relevant tools.
// Falls back to local relevance filtering if the client is unavailable, the query
// is empty, or the API call fails — so the pipe is always safe to enable.
func (p *Pipe) filterViaCompresr(ctx *pipes.PipeContext) ([]byte, error) {
	if p.compresrClient == nil {
		log.Warn().Msg("tool_discovery(compresr): client not initialized, falling back to local relevance")
		return p.filterByRelevance(ctx)
	}

	query := ctx.UserQuery
	if query == "" {
		log.Debug().Msg("tool_discovery(compresr): no query available, falling back to local relevance")
		return p.filterByRelevance(ctx)
	}

	parsedAdapter, ok := ctx.Adapter.(adapters.ParsedRequestAdapter)
	if !ok {
		log.Warn().Str("adapter", ctx.Adapter.Name()).Msg("tool_discovery(compresr): adapter does not implement ParsedRequestAdapter, skipping")
		return ctx.OriginalRequest, nil
	}

	parsed, err := parsedAdapter.ParseRequest(ctx.OriginalRequest)
	if err != nil {
		log.Warn().Err(err).Msg("tool_discovery(compresr): parse failed, skipping")
		return ctx.OriginalRequest, nil
	}

	tools, err := parsedAdapter.ExtractToolDiscoveryFromParsed(parsed, nil)
	if err != nil {
		log.Warn().Err(err).Msg("tool_discovery(compresr): extraction failed, skipping")
		ctx.ToolDiscoverySkipReason = "extraction_failed"
		return ctx.OriginalRequest, nil
	}

	totalTools := len(tools)
	if totalTools == 0 {
		ctx.ToolDiscoverySkipReason = "no_tools"
		ctx.ToolDiscoveryToolCount = 0
		return ctx.OriginalRequest, nil
	}

	if totalTools <= p.minTools {
		ctx.ToolDiscoverySkipReason = "below_min_tools"
		ctx.ToolDiscoveryToolCount = totalTools
		return ctx.OriginalRequest, nil
	}

	keepCount := p.calculateKeepCount(totalTools)
	if totalTools-keepCount < p.minRemoval {
		log.Debug().
			Int("tools", totalTools).
			Int("would_remove", totalTools-keepCount).
			Int("min_removal", p.minRemoval).
			Msg("tool_discovery(compresr): too few tools to filter, skipping")
		return ctx.OriginalRequest, nil
	}

	// Build ToolDefinitions for Compresr API.
	toolDefs := make([]compresr.ToolDefinition, 0, len(tools))
	for _, t := range tools {
		def := compresr.ToolDefinition{
			Name:        t.ToolName,
			Description: t.Content,
		}
		if rawJSON, ok := t.Metadata["raw_json"].(string); ok && rawJSON != "" {
			var rawDef map[string]any
			if jsonErr := json.Unmarshal([]byte(rawJSON), &rawDef); jsonErr == nil {
				def.Parameters = extractToolParameters(rawDef)
			}
		}
		toolDefs = append(toolDefs, def)
	}

	filterResp, err := p.compresrClient.FilterTools(compresr.FilterToolsParams{
		Query:      query,
		AlwaysKeep: p.alwaysKeepList,
		Tools:      toolDefs,
		MaxTools:   keepCount,
		ModelName:  p.getEffectiveModel(),
		Source:     "gateway:" + string(ctx.Adapter.Provider()),
	})
	if err != nil {
		log.Warn().Err(err).Msg("tool_discovery(compresr): API call failed, falling back to local relevance")
		return p.filterByRelevance(ctx)
	}

	// Build keep set from API response — always_keep is already handled by the API
	// but we add it locally too for safety.
	keepSet := make(map[string]bool, len(filterResp.RelevantTools)+len(p.alwaysKeepList))
	for _, name := range filterResp.RelevantTools {
		keepSet[name] = true
	}
	for _, name := range p.alwaysKeepList {
		keepSet[name] = true
	}

	results := make([]adapters.CompressedResult, 0, len(tools))
	keptNames := make([]string, 0, len(filterResp.RelevantTools))
	deferred := make([]adapters.ExtractedContent, 0)
	deferredNames := make([]string, 0)

	for _, t := range tools {
		keep := keepSet[t.ToolName]
		results = append(results, adapters.CompressedResult{ID: t.ID, Keep: keep})
		if keep {
			keptNames = append(keptNames, t.ToolName)
		} else {
			deferred = append(deferred, t)
			deferredNames = append(deferredNames, t.ToolName)
		}
	}

	modified, err := parsedAdapter.ApplyToolDiscoveryToParsed(parsed, results)
	if err != nil {
		log.Warn().Err(err).Msg("tool_discovery(compresr): apply failed, returning original")
		return ctx.OriginalRequest, nil
	}

	ctx.DeferredTools = deferred
	ctx.ToolsFiltered = true
	ctx.OriginalToolCount = totalTools
	ctx.FilteredToolCount = len(keptNames)

	if p.enableSearchFallback && len(deferred) > 0 {
		if injected, injErr := p.injectSearchTool(modified, ctx.Adapter.Provider()); injErr == nil {
			modified = injected
		} else {
			log.Warn().Err(injErr).Msg("tool_discovery(compresr): failed to inject search tool")
		}
	}

	log.Info().
		Str("query", query).
		Int("total", totalTools).
		Int("kept", len(keptNames)).
		Strs("kept_tools", keptNames).
		Int("deferred", len(deferred)).
		Strs("deferred_tools", deferredNames).
		Bool("search_fallback", p.enableSearchFallback && len(deferred) > 0).
		Msg("tool_discovery(compresr): filtered tools via Compresr API")

	return modified, nil
}

// extractToolParameters extracts the JSON schema from a raw tool definition.
// Handles all three wire formats: Anthropic (input_schema), OpenAI nested
// (function.parameters), and OpenAI flat / Responses API (parameters).
func extractToolParameters(def map[string]any) map[string]any {
	// OpenAI nested: {type:"function", function:{parameters:{...}}}
	if fn, ok := def["function"].(map[string]any); ok {
		if params, ok := fn["parameters"].(map[string]any); ok {
			return params
		}
	}
	// OpenAI flat / Responses API: {parameters:{...}}
	if params, ok := def["parameters"].(map[string]any); ok {
		return params
	}
	// Anthropic: {input_schema:{...}}
	if schema, ok := def["input_schema"].(map[string]any); ok {
		return schema
	}
	return nil
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
//
// Two-phase approach:
//  1. Protected tools (always_keep + expanded) are separated upfront — they are
//     always kept regardless of the cap, so their guarantee is explicit and does
//     not depend on sort position or score equality.
//  2. The remaining candidate tools are scored, sorted, and fill the leftover
//     slots (keepCount - len(protected)), up to the configured max.
func (p *Pipe) scoreAndFilterTools(input *filterInput) *filterOutput {
	totalTools := len(input.tools)

	// Phase 1: separate protected tools from candidates.
	protected := make([]adapters.ExtractedContent, 0)
	candidates := make([]adapters.ExtractedContent, 0, totalTools)
	for _, tool := range input.tools {
		if p.alwaysKeep[tool.ToolName] || input.expandedTools[tool.ToolName] {
			protected = append(protected, tool)
		} else {
			candidates = append(candidates, tool)
		}
	}

	// Phase 2: score and sort candidates by relevance.
	scored := make([]scoredTool, 0, len(candidates))
	for _, tool := range candidates {
		score := p.scoreTool(tool, input.query, input.recentTools)
		scored = append(scored, scoredTool{tool: tool, score: score})
	}

	// Sort by score descending (insertion sort — tool counts are small).
	for i := 1; i < len(scored); i++ {
		for j := i; j > 0 && scored[j].score > scored[j-1].score; j-- {
			scored[j], scored[j-1] = scored[j-1], scored[j]
		}
	}

	// Determine remaining slots after accounting for protected tools.
	keepCount := p.calculateKeepCount(totalTools)
	remainingSlots := keepCount - len(protected)
	if remainingSlots < 0 {
		remainingSlots = 0
		log.Warn().
			Int("always_keep_count", len(protected)).
			Int("max_tools", p.maxTools).
			Msg("tool_discovery: always_keep tools exceed max_tools cap; all candidates will be deferred")
	}

	// Build results: protected tools first (always kept), then top candidates.
	results := make([]adapters.CompressedResult, 0, totalTools)
	keptNames := make([]string, 0, keepCount)
	deferred := make([]adapters.ExtractedContent, 0)
	deferredNames := make([]string, 0)

	for _, tool := range protected {
		results = append(results, adapters.CompressedResult{ID: tool.ID, Keep: true})
		keptNames = append(keptNames, tool.ToolName)
	}

	for i, s := range scored {
		keep := i < remainingSlots
		results = append(results, adapters.CompressedResult{ID: s.tool.ID, Keep: keep})
		if keep {
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
		keptCount:     len(keptNames),
	}
}

// applyFilterResults applies filtering output to context and logs.
func (p *Pipe) applyFilterResults(ctx *pipes.PipeContext, output *filterOutput, query string, totalTools int, modified []byte) []byte {
	// Store deferred tools in context for session storage
	ctx.DeferredTools = output.deferred
	ctx.ToolsFiltered = true

	// Set counts for telemetry
	ctx.OriginalToolCount = totalTools
	ctx.FilteredToolCount = output.keptCount

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
		ctx.ToolDiscoverySkipReason = "extraction_failed"
		return ctx.OriginalRequest, nil
	}

	totalTools := len(tools)
	if totalTools == 0 {
		ctx.ToolDiscoverySkipReason = "no_tools"
		ctx.ToolDiscoveryToolCount = 0
		return ctx.OriginalRequest, nil
	}

	// Skip filtering if below minimum threshold
	if totalTools <= p.minTools {
		log.Debug().
			Int("tools", totalTools).
			Int("min_tools", p.minTools).
			Msg("tool_discovery: below min threshold, skipping")
		// Track skip reason for logging
		ctx.ToolDiscoverySkipReason = "below_min_tools"
		ctx.ToolDiscoveryToolCount = totalTools
		return ctx.OriginalRequest, nil
	}

	// Get user query from pipeline context (pre-computed, injected tags stripped)
	query := ctx.UserQuery

	// Get recently-used tool names from parsed request (no JSON parsing)
	recentTools := p.extractRecentlyUsedToolsParsed(parsedAdapter, parsed)

	// Get expanded tools from session context (tools found via search)
	expandedTools := ctx.ExpandedTools
	if expandedTools == nil {
		expandedTools = make(map[string]bool)
	}

	// Check if filtering would be a no-op or barely useful.
	// Don't filter if we'd remove fewer than 3 tools — the token savings
	// are negligible and removing tools can break agents with few tools.
	keepCount := p.calculateKeepCount(totalTools)
	removedCount := totalTools - keepCount
	if keepCount >= totalTools || removedCount < p.minRemoval {
		log.Debug().
			Int("tools", totalTools).
			Int("keep_count", keepCount).
			Int("would_remove", removedCount).
			Int("min_removal", p.minRemoval).
			Msg("tool_discovery: too few tools to filter, skipping")
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

// scoreTool computes a relevance score for a candidate tool (not in always_keep or expanded).
func (p *Pipe) scoreTool(tool adapters.ExtractedContent, query string, recentTools map[string]bool) int {
	score := 0

	// Signal 0: Recently used in conversation
	if recentTools[tool.ToolName] {
		score += scoreRecentlyUsed
	}

	if query == "" {
		return score
	}

	queryLower := strings.ToLower(query)
	toolNameLower := strings.ToLower(tool.ToolName)

	// Signal 1: Exact tool name appears in query
	if strings.Contains(queryLower, toolNameLower) {
		score += scoreExactName
	}

	// Signal 2: Word overlap between query and tool name + description
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

// injectSearchTool appends the gateway_search_tools tool to the request.
// Uses sjson + pre-computed bytes to preserve KV-cache prefix (no unmarshal/marshal).
func (p *Pipe) injectSearchTool(body []byte, provider adapters.Provider) ([]byte, error) {
	// Detect API format
	hasInput := gjson.GetBytes(body, "input").Exists()
	hasMessages := gjson.GetBytes(body, "messages").Exists()
	isResponsesAPI := hasInput && !hasMessages && provider == adapters.ProviderOpenAI

	toolJSON := getSearchToolJSON(provider, isResponsesAPI)

	toolsResult := gjson.GetBytes(body, "tools")
	if !toolsResult.Exists() {
		return sjson.SetRawBytes(body, "tools", append(append([]byte{'['}, toolJSON...), ']'))
	}

	return sjson.SetRawBytes(body, "tools.-1", toolJSON)
}

// replaceWithSearchToolOnly replaces the entire tools[] with just the search tool.
// Uses sjson to replace only the tools field, preserving all other request bytes (KV-cache safe).
func (p *Pipe) replaceWithSearchToolOnly(body []byte, provider adapters.Provider) ([]byte, error) {
	hasInput := gjson.GetBytes(body, "input").Exists()
	hasMessages := gjson.GetBytes(body, "messages").Exists()
	isResponsesAPI := hasInput && !hasMessages && provider == adapters.ProviderOpenAI

	toolJSON := getSearchToolJSON(provider, isResponsesAPI)

	// Replace entire tools array with single-element array containing just the search tool
	newTools := append(append([]byte{'['}, toolJSON...), ']')
	return sjson.SetRawBytes(body, "tools", newTools)
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
