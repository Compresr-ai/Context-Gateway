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
//
// STRATEGY: "relevance" — local keyword-based filtering (no external API)
package tooldiscovery

import (
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
)

// Default configuration values.
const (
	DefaultMinTools = 5
	DefaultMaxTools = 25
)

// Score weights for relevance signals.
const (
	scoreRecentlyUsed = 100 // Tool was used in conversation history
	scoreAlwaysKeep   = 100 // Tool is in the always-keep list
	scoreExactName    = 50  // Query contains exact tool name
	scoreWordMatch    = 10  // Per-word overlap between query and tool name/description
)

// Pipe filters tools dynamically based on relevance to the current query.
type Pipe struct {
	enabled     bool
	strategy    string
	minTools    int
	maxTools    int
	targetRatio float64
	alwaysKeep  map[string]bool
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

	return &Pipe{
		enabled:     cfg.Pipes.ToolDiscovery.Enabled,
		strategy:    cfg.Pipes.ToolDiscovery.Strategy,
		minTools:    minTools,
		maxTools:    maxTools,
		targetRatio: targetRatio,
		alwaysKeep:  alwaysKeep,
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

	if p.strategy != config.StrategyRelevance {
		// Only relevance strategy is implemented locally
		return ctx.OriginalRequest, nil
	}

	return p.filterByRelevance(ctx)
}

// filterByRelevance scores and filters tools based on multi-signal relevance.
func (p *Pipe) filterByRelevance(ctx *pipes.PipeContext) ([]byte, error) {
	if ctx.Adapter == nil || len(ctx.OriginalRequest) == 0 {
		return ctx.OriginalRequest, nil
	}

	// Extract tool definitions via adapter
	tools, err := ctx.Adapter.ExtractToolDiscovery(ctx.OriginalRequest, nil)
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

	// Get user query for keyword matching
	query := ctx.Adapter.ExtractUserQuery(ctx.OriginalRequest)

	// Get recently-used tool names from conversation history
	recentTools := p.extractRecentlyUsedTools(ctx)

	// Score each tool
	type scoredTool struct {
		tool  adapters.ExtractedContent
		score int
	}

	scored := make([]scoredTool, 0, totalTools)
	for _, tool := range tools {
		score := p.scoreTool(tool, query, recentTools)
		scored = append(scored, scoredTool{tool: tool, score: score})
	}

	// Determine how many tools to keep
	keepCount := p.calculateKeepCount(totalTools)

	// If we'd keep all tools anyway, skip filtering
	if keepCount >= totalTools {
		log.Debug().
			Int("tools", totalTools).
			Int("keep_count", keepCount).
			Msg("tool_discovery: keep count >= total, skipping")
		return ctx.OriginalRequest, nil
	}

	// Sort by score descending (simple insertion sort — tool counts are small)
	for i := 1; i < len(scored); i++ {
		for j := i; j > 0 && scored[j].score > scored[j-1].score; j-- {
			scored[j], scored[j-1] = scored[j-1], scored[j]
		}
	}

	// Build results with Keep flag
	results := make([]adapters.CompressedResult, 0, totalTools)
	kept := 0
	for _, s := range scored {
		keep := kept < keepCount || p.alwaysKeep[s.tool.ToolName]
		results = append(results, adapters.CompressedResult{
			ID:   s.tool.ID,
			Keep: keep,
		})
		if keep {
			kept++
		}
	}

	// Apply filtered tools back via adapter
	modified, err := ctx.Adapter.ApplyToolDiscovery(ctx.OriginalRequest, results)
	if err != nil {
		log.Warn().Err(err).Msg("tool_discovery: apply failed, returning original")
		return ctx.OriginalRequest, nil
	}

	ctx.ToolsFiltered = true
	ctx.ToolsSent = kept
	ctx.ToolsRemoved = totalTools - kept

	log.Info().
		Int("total", totalTools).
		Int("kept", kept).
		Int("removed", totalTools-kept).
		Msg("tool_discovery: filtered tools by relevance")

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

// scoreTool computes a relevance score for a single tool.
func (p *Pipe) scoreTool(tool adapters.ExtractedContent, query string, recentTools map[string]bool) int {
	score := 0

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

// extractRecentlyUsedTools gets tool names from conversation history.
// Uses ExtractToolOutput to find tool results, which contain tool names.
func (p *Pipe) extractRecentlyUsedTools(ctx *pipes.PipeContext) map[string]bool {
	recent := make(map[string]bool)

	extracted, err := ctx.Adapter.ExtractToolOutput(ctx.OriginalRequest)
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
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
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
