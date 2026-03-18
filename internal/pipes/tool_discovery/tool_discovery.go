// Package tooldiscovery filters tools dynamically based on relevance.
package tooldiscovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/compresr"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/phantom_tools"
	"github.com/compresr/context-gateway/internal/pipes"
	"github.com/compresr/context-gateway/internal/tokenizer"
)

// Default configuration values.
const (
	DefaultMaxSearchResults = 5
	// DefaultSearchToolName aliases the canonical constant in phantom_tools.SearchToolName.
	DefaultSearchToolName = phantom_tools.SearchToolName
	DefaultTokenThreshold = 512 // ~512 tokens; triggers discovery when total tool definitions exceed this

	// SearchToolDescription and SearchToolSchema were here but are now canonical in
	// internal/phantom_tools/search_tools.go. Do not add them back here.
)

// Score weights for relevance signals.
const (
	scoreRecentlyUsed = 100 // Tool was used in conversation history
	scoreExactName    = 50  // Query contains exact tool name
	scoreWordMatch    = 10  // Per-word overlap between query and tool name/description
)

// cachedResult stores a previously filtered result for a session.
type cachedResult struct {
	hash           string // hash of sorted tool names
	filteredBody   []byte
	deferredTools  []adapters.ExtractedContent
	originalTokens int
	filteredTokens int
}

// Pipe filters tools dynamically based on relevance to the current query.
type Pipe struct {
	enabled          bool
	strategy         string
	tokenThreshold   int // trigger discovery when total tool tokens > this value
	alwaysKeep       map[string]bool
	alwaysKeepList   []string // For API payload
	searchToolName   string
	maxSearchResults int

	// Compresr API client (used when strategy=compresr)
	compresrClient *compresr.Client

	// Compresr strategy fields
	compresrEndpoint string
	compresrKey      string
	compresrModel    string // Model name for compresr strategy (e.g., "tdc_coldbrew_v1")
	compresrTimeout  time.Duration

	// Session-scoped cache for lazy loading (tool stubbing)
	cacheMu sync.RWMutex
	cache   map[string]*cachedResult // sessionID -> cached result
}

// New creates a new tool discovery pipe.
func New(cfg *config.Config) *Pipe {
	alwaysKeep := make(map[string]bool)
	for _, name := range cfg.Pipes.ToolDiscovery.AlwaysKeep {
		alwaysKeep[name] = true
	}

	// NOTE: gateway_search_tools injection is handled by phantom_tools.InjectAll in handler.go.
	// The pipe does not inject it — single injection path keeps dedup logic in one place.

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
		compresrKey := cfg.Pipes.ToolDiscovery.Compresr.APIKey
		if baseURL != "" || compresrKey != "" {
			compresrClient = compresr.NewClient(baseURL, compresrKey, compresr.WithTimeout(compresrTimeout))
			log.Info().Str("base_url", baseURL).Str("strategy", tdStrategy).Msg("tool_discovery: initialized Compresr client")
		} else {
			log.Debug().Str("strategy", tdStrategy).Msg("tool_discovery: API strategy without Compresr credentials, will use local fallback")
		}
	}

	tokenThreshold := cfg.Pipes.ToolDiscovery.TokenThreshold
	if tokenThreshold <= 0 {
		tokenThreshold = DefaultTokenThreshold
	}

	return &Pipe{
		enabled:          cfg.Pipes.ToolDiscovery.Enabled,
		strategy:         cfg.Pipes.ToolDiscovery.Strategy,
		tokenThreshold:   tokenThreshold,
		alwaysKeep:       alwaysKeep,
		alwaysKeepList:   cfg.Pipes.ToolDiscovery.AlwaysKeep,
		searchToolName:   searchToolName,
		maxSearchResults: maxSearchResults,
		compresrClient:   compresrClient,
		compresrEndpoint: compresrEndpoint,
		compresrKey:      cfg.Pipes.ToolDiscovery.Compresr.APIKey,
		compresrTimeout:  compresrTimeout,
		compresrModel:    cfg.Pipes.ToolDiscovery.Compresr.Model,
		cache:            make(map[string]*cachedResult),
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
// For heuristic strategies (relevance, tool-search stub-only), returns empty string.
// For API-backed strategies, returns the configured model.
func (p *Pipe) getEffectiveModel() string {
	// Relevance strategy is pure heuristic filtering — no external model involved
	if p.strategy == config.StrategyRelevance {
		return "" // Logged as "heuristic" in telemetry
	}
	// Tool-search without API client is also heuristic (stub-only)
	if p.strategy == config.StrategyToolSearch && p.compresrClient == nil {
		return ""
	}
	// API-backed strategies use the configured model
	if p.compresrModel != "" {
		return p.compresrModel
	}
	return compresr.DefaultToolDiscoveryModel
}

// computeToolHash generates a SHA-256 hash of sorted tool names.
// Used for cache key to detect if tool catalog has changed.
func computeToolHash(tools []adapters.ExtractedContent) string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.ToolName
	}
	sort.Strings(names)
	h := sha256.Sum256([]byte(strings.Join(names, ",")))
	return hex.EncodeToString(h[:])
}

// getCache retrieves cached result for a session.
func (p *Pipe) getCache(sessionID, hash string) *cachedResult {
	p.cacheMu.RLock()
	defer p.cacheMu.RUnlock()
	if cached, ok := p.cache[sessionID]; ok && cached.hash == hash {
		return cached
	}
	return nil
}

// setCache stores filtered result in cache.
func (p *Pipe) setCache(sessionID string, result *cachedResult) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	p.cache[sessionID] = result
}

// ClearSessionCache removes cache for a specific session.
func (p *Pipe) ClearSessionCache(sessionID string) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	delete(p.cache, sessionID)
}

// Process filters tools before sending to LLM.
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
// This is heuristic-based filtering (no external model).
func (p *Pipe) filterByRelevance(ctx *pipes.PipeContext) ([]byte, error) {
	// Clear model since this is heuristic filtering, not API-based
	ctx.ToolDiscoveryModel = ""
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
//  3. Replace all tool definitions with minimal stubs (name + "[deferred]" description)
//  4. Inject gateway_search_tools at the end of the stubs array
//
// Stubs preserve the tools[] array length across requests so the KV-cache prefix
// for all other request fields remains identical turn after turn.
// This is heuristic-based (no external model for the stub creation).
// Caching: Results are cached per session by tool hash to avoid redundant processing.
func (p *Pipe) prepareToolSearch(ctx *pipes.PipeContext) ([]byte, error) {
	// Clear model since tool stubbing is heuristic, not API-based
	ctx.ToolDiscoveryModel = ""
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

	// Check cache for this session + tool set
	toolHash := computeToolHash(tools)
	if cached := p.getCache(ctx.SessionID, toolHash); cached != nil {
		// Cache hit - reuse cached result
		ctx.DeferredTools = cached.deferredTools
		ctx.ToolsFiltered = true
		ctx.OriginalToolCount = len(tools)
		ctx.KeptToolCount = 0
		ctx.CacheHit = true // Set cache hit flag for telemetry

		log.Info().
			Str("session_id", ctx.SessionID).
			Int("tool_count", len(tools)).
			Int("original_tokens", cached.originalTokens).
			Int("cached_tokens", cached.filteredTokens).
			Msg("tool_discovery(tool-search): cache HIT, using cached stubs")

		return cached.filteredBody, nil
	}

	// Cache miss - process and cache
	// Mark ALL tools as deferred — ApplyToolDiscoveryToParsed emits stubs for Keep=false.
	results := make([]adapters.CompressedResult, 0, len(tools))
	for _, t := range tools {
		results = append(results, adapters.CompressedResult{ID: t.ID, Keep: false})
	}

	modified, err := parsedAdapter.ApplyToolDiscoveryToParsed(parsed, results)
	if err != nil {
		log.Warn().Err(err).Msg("tool_discovery(tool-search): failed to apply stubs")
		return ctx.OriginalRequest, nil
	}

	// Store all original tools for search and eventual re-injection.
	ctx.DeferredTools = tools
	ctx.ToolsFiltered = true
	ctx.OriginalToolCount = len(tools)
	ctx.KeptToolCount = 0 // 0 tools with full definitions (all stubbed)
	ctx.CacheHit = false  // Explicit cache miss

	origTokens := estimateToolTokens(tools)
	// Each stub is ~50 tokens (name + "[deferred]" + minimal schema)
	stubTokens := len(tools) * 50
	ratio := tokenizer.CompressionRatio(origTokens, stubTokens)
	toolNames := make([]string, len(tools))
	for i, t := range tools {
		toolNames[i] = t.ToolName
	}

	// Cache the result
	if ctx.SessionID != "" {
		p.setCache(ctx.SessionID, &cachedResult{
			hash:           toolHash,
			filteredBody:   modified,
			deferredTools:  tools,
			originalTokens: origTokens,
			filteredTokens: stubTokens,
		})
	}

	log.Info().
		Int("total", len(tools)).
		Int("original_tokens", origTokens).
		Int("stub_tokens", stubTokens).
		Float64("compression_ratio", ratio).
		Strs("tool_names", toolNames).
		Bool("cache_hit", false).
		Str("event_type", "init_agent_tools").
		Str("search_tool", p.searchToolName).
		Msg("tool_discovery(tool-search): stubbed all tools")

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

	estimatedTokens := estimateToolTokens(tools)
	if estimatedTokens <= p.tokenThreshold {
		ctx.ToolDiscoverySkipReason = "below_token_threshold"
		ctx.ToolDiscoveryToolCount = totalTools
		return ctx.OriginalRequest, nil
	}

	// Estimate how many tools would be kept based on token budget.
	keepCount := p.calculateTokenBudgetKeepCount(tools)

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
	if filterResp == nil {
		log.Warn().Msg("tool_discovery(compresr): nil response, falling back to local relevance")
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
	ctx.KeptToolCount = len(keptNames)

	// gateway_search_tools is injected unconditionally by phantom_tools.InjectAll in handler.go.

	log.Info().
		Str("query", query).
		Int("total", totalTools).
		Int("kept", len(keptNames)).
		Strs("kept_tools", keptNames).
		Int("deferred", len(deferred)).
		Strs("deferred_tools", deferredNames).
		Bool("tools_deferred", len(deferred) > 0).
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

// SHARED FILTERING LOGIC

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
//     always kept regardless of the token budget, so their guarantee is explicit
//     and does not depend on sort position or score equality.
//  2. The remaining candidate tools are scored, sorted by relevance descending,
//     then greedily admitted until their accumulated token count reaches the
//     tokenThreshold budget.
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

	// Phase 3: greedily admit top-scored candidates until token budget is exhausted.
	// Budget starts at tokenThreshold; each tool consumes its actual tiktoken count.
	budget := p.tokenThreshold
	admittedCount := 0
	for _, s := range scored {
		var toolTokens int
		if raw, ok := s.tool.Metadata["raw_json"].(string); ok && raw != "" {
			toolTokens = tokenizer.CountTokens(raw)
		} else {
			toolTokens = tokenizer.CountTokens(s.tool.Content)
		}
		if admittedCount > 0 && budget-toolTokens < 0 {
			break
		}
		budget -= toolTokens
		admittedCount++
	}
	if admittedCount == 0 && len(scored) > 0 {
		admittedCount = 1 // always keep at least one candidate
	}

	// Build results: protected tools first (always kept), then top candidates.
	results := make([]adapters.CompressedResult, 0, totalTools)
	keptNames := make([]string, 0, admittedCount+len(protected))
	deferred := make([]adapters.ExtractedContent, 0)
	deferredNames := make([]string, 0)

	for _, tool := range protected {
		results = append(results, adapters.CompressedResult{ID: tool.ID, Keep: true})
		keptNames = append(keptNames, tool.ToolName)
	}

	for i, s := range scored {
		keep := i < admittedCount
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
	ctx.KeptToolCount = output.keptCount

	// gateway_search_tools is injected unconditionally by phantom_tools.InjectAll in handler.go.

	// Detailed logging: show query, kept tools, and deferred tools
	log.Info().
		Str("query", query).
		Int("total", totalTools).
		Int("kept", output.keptCount).
		Strs("kept_tools", output.keptNames).
		Int("deferred", len(output.deferred)).
		Strs("deferred_tools", output.deferredNames).
		Bool("tools_deferred", len(output.deferred) > 0).
		Msg("tool_discovery: filtered tools by relevance")

	return modified
}

// PARSED PATH (optimized single-parse)

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

	// Skip filtering if below token threshold (token-based trigger).
	// estimateToolTokens uses tiktoken for accurate counts.
	// Falls back to minTools count when tokenThreshold is the default.
	estimatedTokens := estimateToolTokens(tools)
	if estimatedTokens <= p.tokenThreshold {
		log.Debug().
			Int("tools", totalTools).
			Int("estimated_tokens", estimatedTokens).
			Int("token_threshold", p.tokenThreshold).
			Msg("tool_discovery: below token threshold, skipping")
		ctx.ToolDiscoverySkipReason = "below_token_threshold"
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

	// Check if filtering would be a no-op (all tools already fit in budget)
	keepCount := p.calculateTokenBudgetKeepCount(tools)
	if keepCount >= totalTools {
		log.Debug().
			Int("tools", totalTools).
			Int("keep_count", keepCount).
			Msg("tool_discovery: all tools fit in budget, skipping")
		ctx.ToolDiscoverySkipReason = "all_tools_fit"
		ctx.ToolDiscoveryToolCount = totalTools
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

// calculateTokenBudgetKeepCount estimates how many tools would be kept under
// the token budget without scoring. It counts tools from the beginning of the
// slice until their accumulated tiktoken count exhausts the budget.
// Used to determine whether filtering is worth doing (all tools fit check).
func (p *Pipe) calculateTokenBudgetKeepCount(tools []adapters.ExtractedContent) int {
	budget := p.tokenThreshold
	kept := 0
	for _, t := range tools {
		var toolTokens int
		if raw, ok := t.Metadata["raw_json"].(string); ok && raw != "" {
			toolTokens = tokenizer.CountTokens(raw)
		} else {
			toolTokens = tokenizer.CountTokens(t.Content)
		}
		if kept > 0 && budget-toolTokens < 0 {
			break
		}
		budget -= toolTokens
		kept++
	}
	if kept == 0 && len(tools) > 0 {
		kept = 1
	}
	return kept
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

// SEARCH TOOL INJECTION

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

// estimateToolTokens returns the total tiktoken count for a set of tool definitions.
// Uses raw JSON when available (most accurate), falls back to Content field.
func estimateToolTokens(tools []adapters.ExtractedContent) int {
	total := 0
	for _, t := range tools {
		if raw, ok := t.Metadata["raw_json"].(string); ok && raw != "" {
			total += tokenizer.CountTokens(raw)
		} else {
			total += tokenizer.CountTokens(t.Content)
		}
	}
	return total
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
