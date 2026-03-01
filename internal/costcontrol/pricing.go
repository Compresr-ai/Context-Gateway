package costcontrol

import "strings"

// ModelPricing holds per-million-token pricing for a model.
type ModelPricing struct {
	InputPerMTok         float64 // USD per million input tokens
	OutputPerMTok        float64 // USD per million output tokens
	CacheWriteMultiplier float64 // Multiplier for cache creation tokens (e.g., 1.25 for Anthropic). 0 = inferred from model.
	CacheReadMultiplier  float64 // Multiplier for cache read tokens (e.g., 0.1 for Anthropic, 0.5 for OpenAI). 0 = inferred from model.
}

// modelPricingTable maps model names to their pricing.
// Sources: platform.claude.com, developers.openai.com, ai.google.dev (Feb 2026)
var modelPricingTable = map[string]ModelPricing{
	// =========================================================================
	// ANTHROPIC CLAUDE (platform.claude.com/docs/en/about-claude/pricing)
	// =========================================================================

	// Claude Opus 4.x
	"claude-opus-4-6": {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-5": {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-1": {InputPerMTok: 15, OutputPerMTok: 75},
	"claude-opus-4":   {InputPerMTok: 15, OutputPerMTok: 75},

	// Claude Sonnet 4.x
	"claude-sonnet-4-6": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-sonnet-4-5": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-sonnet-4":   {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-sonnet-3-7": {InputPerMTok: 3, OutputPerMTok: 15}, // deprecated

	// Claude Haiku 4.x
	"claude-haiku-4-5": {InputPerMTok: 1, OutputPerMTok: 5},
	"claude-haiku-3-5": {InputPerMTok: 0.80, OutputPerMTok: 4},

	// =========================================================================
	// OPENAI (developers.openai.com/api/docs/pricing)
	// =========================================================================

	// GPT-5.x series
	"gpt-5.2":            {InputPerMTok: 1.75, OutputPerMTok: 14},
	"gpt-5.1":            {InputPerMTok: 1.25, OutputPerMTok: 10},
	"gpt-5":              {InputPerMTok: 1.25, OutputPerMTok: 10},
	"gpt-5-mini":         {InputPerMTok: 0.25, OutputPerMTok: 2},
	"gpt-5-nano":         {InputPerMTok: 0.05, OutputPerMTok: 0.40},
	"gpt-5.2-pro":        {InputPerMTok: 21, OutputPerMTok: 168},
	"gpt-5-pro":          {InputPerMTok: 15, OutputPerMTok: 120},
	"gpt-5.3-codex":      {InputPerMTok: 1.75, OutputPerMTok: 14},
	"gpt-5.2-codex":      {InputPerMTok: 1.75, OutputPerMTok: 14},
	"gpt-5.1-codex":      {InputPerMTok: 1.25, OutputPerMTok: 10},
	"gpt-5-codex":        {InputPerMTok: 1.25, OutputPerMTok: 10},
	"gpt-5.1-codex-mini": {InputPerMTok: 0.25, OutputPerMTok: 2},

	// GPT-4.1 series
	"gpt-4.1":      {InputPerMTok: 2, OutputPerMTok: 8},
	"gpt-4.1-mini": {InputPerMTok: 0.40, OutputPerMTok: 1.60},
	"gpt-4.1-nano": {InputPerMTok: 0.10, OutputPerMTok: 0.40},

	// GPT-4o
	"gpt-4o":            {InputPerMTok: 2.5, OutputPerMTok: 10},
	"gpt-4o-2024-11-20": {InputPerMTok: 2.5, OutputPerMTok: 10},
	"gpt-4o-2024-08-06": {InputPerMTok: 2.5, OutputPerMTok: 10},
	"gpt-4o-2024-05-13": {InputPerMTok: 5, OutputPerMTok: 15},
	"chatgpt-4o-latest": {InputPerMTok: 5, OutputPerMTok: 15},

	// GPT-4o mini
	"gpt-4o-mini":            {InputPerMTok: 0.15, OutputPerMTok: 0.60},
	"gpt-4o-mini-2024-07-18": {InputPerMTok: 0.15, OutputPerMTok: 0.60},

	// o-series reasoning models
	"o1":      {InputPerMTok: 15, OutputPerMTok: 60},
	"o1-pro":  {InputPerMTok: 150, OutputPerMTok: 600},
	"o1-mini": {InputPerMTok: 1.10, OutputPerMTok: 4.40},
	"o3":      {InputPerMTok: 2, OutputPerMTok: 8},
	"o3-pro":  {InputPerMTok: 20, OutputPerMTok: 80},
	"o3-mini": {InputPerMTok: 1.10, OutputPerMTok: 4.40},
	"o4-mini": {InputPerMTok: 1.10, OutputPerMTok: 4.40},

	// GPT-4 Turbo
	"gpt-4-turbo":            {InputPerMTok: 10, OutputPerMTok: 30},
	"gpt-4-turbo-2024-04-09": {InputPerMTok: 10, OutputPerMTok: 30},
	"gpt-4-turbo-preview":    {InputPerMTok: 10, OutputPerMTok: 30},
	"gpt-4-1106-preview":     {InputPerMTok: 10, OutputPerMTok: 30},
	"gpt-4-0125-preview":     {InputPerMTok: 10, OutputPerMTok: 30},

	// GPT-4 (original)
	"gpt-4":          {InputPerMTok: 30, OutputPerMTok: 60},
	"gpt-4-0613":     {InputPerMTok: 30, OutputPerMTok: 60},
	"gpt-4-32k":      {InputPerMTok: 60, OutputPerMTok: 120},
	"gpt-4-32k-0613": {InputPerMTok: 60, OutputPerMTok: 120},

	// GPT-3.5 Turbo
	"gpt-3.5-turbo":          {InputPerMTok: 0.5, OutputPerMTok: 1.5},
	"gpt-3.5-turbo-0125":     {InputPerMTok: 0.5, OutputPerMTok: 1.5},
	"gpt-3.5-turbo-1106":     {InputPerMTok: 1, OutputPerMTok: 2},
	"gpt-3.5-turbo-instruct": {InputPerMTok: 1.5, OutputPerMTok: 2},
	"gpt-3.5-turbo-16k":      {InputPerMTok: 3, OutputPerMTok: 4},

	// =========================================================================
	// GOOGLE GEMINI (ai.google.dev/gemini-api/docs/pricing)
	// =========================================================================

	// Gemini 3.x
	"gemini-3.1-pro-preview": {InputPerMTok: 2, OutputPerMTok: 12},
	"gemini-3-pro-preview":   {InputPerMTok: 2, OutputPerMTok: 12},
	"gemini-3-flash-preview": {InputPerMTok: 0.50, OutputPerMTok: 3},

	// Gemini 2.5
	"gemini-2.5-pro":        {InputPerMTok: 1.25, OutputPerMTok: 10},
	"gemini-2.5-flash":      {InputPerMTok: 0.30, OutputPerMTok: 2.50},
	"gemini-2.5-flash-lite": {InputPerMTok: 0.10, OutputPerMTok: 0.40},

	// Gemini 2.0
	"gemini-2.0-flash":      {InputPerMTok: 0.10, OutputPerMTok: 0.40},
	"gemini-2.0-flash-lite": {InputPerMTok: 0.075, OutputPerMTok: 0.30},
	"gemini-2.0-pro":        {InputPerMTok: 1.25, OutputPerMTok: 5},

	// Gemini 1.5
	"gemini-1.5-pro":          {InputPerMTok: 1.25, OutputPerMTok: 5},
	"gemini-1.5-pro-latest":   {InputPerMTok: 1.25, OutputPerMTok: 5},
	"gemini-1.5-flash":        {InputPerMTok: 0.075, OutputPerMTok: 0.30},
	"gemini-1.5-flash-latest": {InputPerMTok: 0.075, OutputPerMTok: 0.30},
	"gemini-1.5-flash-8b":     {InputPerMTok: 0.0375, OutputPerMTok: 0.15},

	// Gemini 1.0
	"gemini-1.0-pro":    {InputPerMTok: 0.5, OutputPerMTok: 1.5},
	"gemini-pro":        {InputPerMTok: 0.5, OutputPerMTok: 1.5},
	"gemini-pro-vision": {InputPerMTok: 0.5, OutputPerMTok: 1.5},
}

// defaultPricing is used for unknown models (conservative to prevent silent overspend).
var defaultPricing = ModelPricing{InputPerMTok: 15, OutputPerMTok: 75}

// modelFamilyPricing maps model family prefixes to pricing.
// Ordered longest-prefix-first in lookup to avoid e.g. "claude-opus" ($15)
// matching when "claude-opus-4-6" ($5) is the correct match.
var modelFamilyPricing = map[string]ModelPricing{
	// Anthropic (version-specific first, then broad)
	"claude-opus-4-6":   {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-5":   {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-1":   {InputPerMTok: 15, OutputPerMTok: 75},
	"claude-opus-4":     {InputPerMTok: 15, OutputPerMTok: 75},
	"claude-opus-3":     {InputPerMTok: 15, OutputPerMTok: 75},
	"claude-opus":       {InputPerMTok: 15, OutputPerMTok: 75},
	"claude-sonnet-4-6": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-sonnet-4-5": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-sonnet-4":   {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-sonnet-3":   {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-sonnet":     {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-haiku-4-5":  {InputPerMTok: 1, OutputPerMTok: 5},
	"claude-haiku-3-5":  {InputPerMTok: 0.80, OutputPerMTok: 4},
	"claude-haiku-3":    {InputPerMTok: 0.25, OutputPerMTok: 1.25},
	"claude-haiku":      {InputPerMTok: 1, OutputPerMTok: 5},
	"claude-3-5-sonnet": {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-3-5-haiku":  {InputPerMTok: 0.80, OutputPerMTok: 4},
	"claude-3-opus":     {InputPerMTok: 15, OutputPerMTok: 75},
	"claude-3-sonnet":   {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-3-haiku":    {InputPerMTok: 0.25, OutputPerMTok: 1.25},

	// OpenAI
	"gpt-5.2-pro":       {InputPerMTok: 21, OutputPerMTok: 168},
	"gpt-5.2-codex":     {InputPerMTok: 1.75, OutputPerMTok: 14},
	"gpt-5.2":           {InputPerMTok: 1.75, OutputPerMTok: 14},
	"gpt-5.1-codex":     {InputPerMTok: 1.25, OutputPerMTok: 10},
	"gpt-5.1":           {InputPerMTok: 1.25, OutputPerMTok: 10},
	"gpt-5-pro":         {InputPerMTok: 15, OutputPerMTok: 120},
	"gpt-5-mini":        {InputPerMTok: 0.25, OutputPerMTok: 2},
	"gpt-5-nano":        {InputPerMTok: 0.05, OutputPerMTok: 0.40},
	"gpt-5-codex":       {InputPerMTok: 1.25, OutputPerMTok: 10},
	"gpt-5":             {InputPerMTok: 1.25, OutputPerMTok: 10},
	"gpt-4.1-mini":      {InputPerMTok: 0.40, OutputPerMTok: 1.60},
	"gpt-4.1-nano":      {InputPerMTok: 0.10, OutputPerMTok: 0.40},
	"gpt-4.1":           {InputPerMTok: 2, OutputPerMTok: 8},
	"gpt-4o-mini":       {InputPerMTok: 0.15, OutputPerMTok: 0.60},
	"gpt-4o":            {InputPerMTok: 2.5, OutputPerMTok: 10},
	"gpt-4-turbo":       {InputPerMTok: 10, OutputPerMTok: 30},
	"gpt-4-32k":         {InputPerMTok: 60, OutputPerMTok: 120},
	"gpt-4":             {InputPerMTok: 30, OutputPerMTok: 60},
	"gpt-3.5-turbo-16k": {InputPerMTok: 3, OutputPerMTok: 4},
	"gpt-3.5-turbo":     {InputPerMTok: 0.5, OutputPerMTok: 1.5},
	"o1-pro":            {InputPerMTok: 150, OutputPerMTok: 600},
	"o1-mini":           {InputPerMTok: 1.10, OutputPerMTok: 4.40},
	"o1":                {InputPerMTok: 15, OutputPerMTok: 60},
	"o3-pro":            {InputPerMTok: 20, OutputPerMTok: 80},
	"o3-mini":           {InputPerMTok: 1.10, OutputPerMTok: 4.40},
	"o3":                {InputPerMTok: 2, OutputPerMTok: 8},
	"o4-mini":           {InputPerMTok: 1.10, OutputPerMTok: 4.40},

	// Google Gemini
	"gemini-3.1-pro":        {InputPerMTok: 2, OutputPerMTok: 12},
	"gemini-3-pro":          {InputPerMTok: 2, OutputPerMTok: 12},
	"gemini-3-flash":        {InputPerMTok: 0.50, OutputPerMTok: 3},
	"gemini-2.5-pro":        {InputPerMTok: 1.25, OutputPerMTok: 10},
	"gemini-2.5-flash-lite": {InputPerMTok: 0.10, OutputPerMTok: 0.40},
	"gemini-2.5-flash":      {InputPerMTok: 0.30, OutputPerMTok: 2.50},
	"gemini-2.0-flash-lite": {InputPerMTok: 0.075, OutputPerMTok: 0.30},
	"gemini-2.0-flash":      {InputPerMTok: 0.10, OutputPerMTok: 0.40},
	"gemini-2.0-pro":        {InputPerMTok: 1.25, OutputPerMTok: 5},
	"gemini-1.5-flash-8b":   {InputPerMTok: 0.0375, OutputPerMTok: 0.15},
	"gemini-1.5-flash":      {InputPerMTok: 0.075, OutputPerMTok: 0.30},
	"gemini-1.5-pro":        {InputPerMTok: 1.25, OutputPerMTok: 5},
	"gemini-1.0-pro":        {InputPerMTok: 0.5, OutputPerMTok: 1.5},
	"gemini-pro":            {InputPerMTok: 0.5, OutputPerMTok: 1.5},
}

// GetModelPricing returns pricing for a model.
// Tries exact match, then prefix/family match (longest prefix wins), then default.
// Cache multipliers are inferred from the model name if not explicitly set.
func GetModelPricing(model string) ModelPricing {
	var p ModelPricing

	// Exact match
	if exact, ok := modelPricingTable[model]; ok {
		p = exact
	} else {
		// Family/prefix match (longest prefix wins)
		bestPrefix := ""
		for prefix, fp := range modelFamilyPricing {
			if strings.HasPrefix(model, prefix) && len(prefix) > len(bestPrefix) {
				bestPrefix = prefix
				p = fp
			}
		}
		if bestPrefix == "" {
			p = defaultPricing
		}
	}

	// Infer cache multipliers from model name if not explicitly set
	if p.CacheWriteMultiplier == 0 && p.CacheReadMultiplier == 0 {
		p.CacheWriteMultiplier, p.CacheReadMultiplier = inferCacheMultipliers(model)
	}

	return p
}

// inferCacheMultipliers returns provider-appropriate cache pricing multipliers.
//
//   - Anthropic (claude-*): write=1.25x (5-min TTL), read=0.1x
//   - OpenAI (gpt-*, o1-*, o3-*, o4-*): write=1.0x (no premium), read=0.5x
//   - Default: Anthropic-style (conservative)
func inferCacheMultipliers(model string) (writeMultiplier, readMultiplier float64) {
	lower := strings.ToLower(model)
	switch {
	case strings.HasPrefix(lower, "gpt-"),
		strings.HasPrefix(lower, "o1"),
		strings.HasPrefix(lower, "o3"),
		strings.HasPrefix(lower, "o4"),
		strings.HasPrefix(lower, "chatgpt-"):
		return 1.0, 0.5
	default:
		// Anthropic and unknown models: conservative Anthropic-style
		return 1.25, 0.1
	}
}

// CalculateCost computes the cost in USD from token counts.
func CalculateCost(inputTokens, outputTokens int, pricing ModelPricing) float64 {
	inputCost := float64(inputTokens) / 1_000_000 * pricing.InputPerMTok
	outputCost := float64(outputTokens) / 1_000_000 * pricing.OutputPerMTok
	return inputCost + outputCost
}

// CalculateCostWithCache computes cost accounting for provider-specific cache pricing.
// inputTokens must be non-cached input only (adapters normalize this at extraction time).
// Cache multipliers come from ModelPricing (inferred per-provider by GetModelPricing).
func CalculateCostWithCache(inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int, pricing ModelPricing) float64 {
	inputCost := float64(inputTokens) / 1_000_000 * pricing.InputPerMTok
	outputCost := float64(outputTokens) / 1_000_000 * pricing.OutputPerMTok
	writeMult := pricing.CacheWriteMultiplier
	readMult := pricing.CacheReadMultiplier
	if writeMult == 0 {
		writeMult = 1.25
	}
	if readMult == 0 {
		readMult = 0.1
	}
	cacheWriteCost := float64(cacheCreationTokens) / 1_000_000 * pricing.InputPerMTok * writeMult
	cacheReadCost := float64(cacheReadTokens) / 1_000_000 * pricing.InputPerMTok * readMult
	return inputCost + outputCost + cacheWriteCost + cacheReadCost
}
