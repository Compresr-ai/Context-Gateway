// Package tooloutput compresses tool outputs (e.g., file contents, API responses).
//
// STATUS: Disabled in current release. This package is retained for future use.
// Enable tool_output compression via config: pipes.tool_output.enabled: true
//
// DESIGN:
//   - KV-Cache Preservation - Same content → same hash → same compressed
//   - Multi-Tool Batch - Compress ALL tools, not just last
//   - Transparent Proxy - Client never sees expand_context
//
// FILES:
//   - types.go:              Structs, constants, constructor
//   - tool_output.go:        Main compression logic
//   - tool_output_expand.go: expand_context loop handling
//   - stream_buffer.go:      Stream buffering for phantom tool suppression
package tooloutput

import (
	"sync"
	"time"

	"github.com/compresr/context-gateway/internal/compresr"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
	"github.com/compresr/context-gateway/internal/store"
	"github.com/rs/zerolog/log"
)

// V2 Configuration constants
const (
	// DefaultMinCompressSize is the minimum content size to consider for compression
	DefaultMinCompressSize = 256

	// DefaultMaxCompressSize is the maximum content size for compression (64KB)
	DefaultMaxCompressSize = 65536

	// DefaultCompressedTTL is how long compressed content is cached (preserves KV-cache)
	DefaultCompressedTTL = 24 * time.Hour

	// DefaultOriginalTTL is how long original content is cached (for expand_context)
	DefaultOriginalTTL = 5 * time.Minute

	// DefaultLLMBuffer is extra TTL added before sending to LLM
	DefaultLLMBuffer = 10 * time.Minute

	// MaxExpandLoops prevents infinite expansion cycles (E10)
	MaxExpandLoops = 5

	// MaxConcurrentCompressions limits parallel compression API calls (C11)
	MaxConcurrentCompressions = 10

	// MaxCompressionsPerSecond rate limit for compression API (C11)
	MaxCompressionsPerSecond = 20

	// ExpandContextToolName is the phantom tool injected for expansion
	ExpandContextToolName = "expand_context"

	// ShadowIDPrefix for shadow references
	ShadowIDPrefix = "shadow_"

	// PrefixFormat for LLM-visible content (E24: unambiguous delimiter)
	PrefixFormat = "<<<SHADOW:%s>>>\n%s"
)

// Pipe compresses tool outputs dynamically and stores raw data for retrieval.
// V2: Compresses ALL tool outputs with dual-TTL caching for KV-cache preservation.
type Pipe struct {
	enabled             bool
	strategy            string
	fallbackStrategy    string  // Strategy to use when primary compression fails/times out
	minBytes            int     // Below this size, no compression
	maxBytes            int     // Above this, skip compression (V2)
	targetRatio         float64 // Target compression ratio (e.g., 0.5 = 50%)
	includeExpandHint   bool    // Add expand_context() hint to compressed output
	enableExpandContext bool    // Enable expand_context feature (tool injection, hint, expand loop)
	store               store.Store

	// Compresr API client (used when strategy=compresr)
	compresrClient *compresr.Client

	// Compresr strategy config (strategy=compresr or strategy=external_provider)
	compresrEndpoint      string
	compresrKey           string
	compresrModel         string
	compresrTimeout       time.Duration
	compresrQueryAgnostic bool // If true, don't send user query. If false, send query for relevance

	// V2: Rate limiting (C11)
	maxConcurrent int
	maxPerSecond  int
	semaphore     chan struct{}
	rateLimiter   *RateLimiter

	// V2: Metrics
	mu      sync.RWMutex
	metrics *Metrics

	// V2: Idempotent tools (E5: safe to re-execute)
	idempotentTools map[string]bool

	// Tools to skip compression for, as generic categories (e.g., "read", "edit")
	skipCategories []string
}

// Metrics tracks compression statistics (V2)
type Metrics struct {
	CacheHits       int64
	CacheMisses     int64
	CompressionOK   int64
	CompressionFail int64
	ExpandRequests  int64
	ExpandCacheMiss int64
	RateLimited     int64
	BytesSaved      int64
}

// RateLimiter implements token bucket rate limiting (C11)
type RateLimiter struct {
	mu         sync.Mutex
	tokens     float64
	maxTokens  float64
	refillRate float64
	lastRefill time.Time
	closed     bool
}

// NewRateLimiter creates a rate limiter
func NewRateLimiter(maxPerSecond int) *RateLimiter {
	return &RateLimiter{
		tokens:     float64(maxPerSecond),
		maxTokens:  float64(maxPerSecond),
		refillRate: float64(maxPerSecond),
		lastRefill: time.Now(),
	}
}

// Acquire blocks until a token is available
func (r *RateLimiter) Acquire() bool {
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return false
		}

		now := time.Now()
		elapsed := now.Sub(r.lastRefill).Seconds()
		r.tokens = minFloat(r.maxTokens, r.tokens+elapsed*r.refillRate)
		r.lastRefill = now

		if r.tokens >= 1 {
			r.tokens--
			r.mu.Unlock()
			return true
		}
		r.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
}

// Close stops the rate limiter
func (r *RateLimiter) Close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// New creates a new tool output compression pipe.
// V2: Initializes rate limiting, metrics, and dual-TTL caching.
func New(cfg *config.Config, st store.Store) *Pipe {
	// Resolve provider settings (endpoint, api_key, model) from providers section
	var compresrEndpoint, compresrKey, compresrModel string
	if cfg.Pipes.ToolOutput.Provider != "" {
		if resolved, err := cfg.ResolveProvider(cfg.Pipes.ToolOutput.Provider); err == nil {
			compresrEndpoint = resolved.Endpoint
			compresrKey = resolved.APIKey
			compresrModel = resolved.Model
		} else {
			log.Warn().Err(err).Str("provider", cfg.Pipes.ToolOutput.Provider).
				Msg("tool_output: failed to resolve provider, falling back to inline compresr config")
		}
	}

	// Inline compresr config overrides provider settings
	if cfg.Pipes.ToolOutput.Compresr.Endpoint != "" {
		if cfg.Pipes.ToolOutput.Strategy == config.StrategyExternalProvider {
			compresrEndpoint = cfg.Pipes.ToolOutput.Compresr.Endpoint
		} else {
			compresrEndpoint = pipes.NormalizeEndpointURL(cfg.URLs.Compresr, cfg.Pipes.ToolOutput.Compresr.Endpoint)
		}
	}
	if cfg.Pipes.ToolOutput.Compresr.APIKey != "" {
		compresrKey = cfg.Pipes.ToolOutput.Compresr.APIKey
	}
	if cfg.Pipes.ToolOutput.Compresr.Model != "" {
		compresrModel = cfg.Pipes.ToolOutput.Compresr.Model
	}

	// Use config fields with sensible defaults
	minBytes := cfg.Pipes.ToolOutput.MinBytes
	if minBytes == 0 {
		minBytes = 2048 // Default: 2KB (~512 tokens)
	}

	maxBytes := cfg.Pipes.ToolOutput.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxCompressSize
	}

	targetRatio := cfg.Pipes.ToolOutput.TargetRatio
	if targetRatio == 0 {
		targetRatio = 0.9 // default: discard compression if savings < 10%
	}

	fallbackStrategy := cfg.Pipes.ToolOutput.FallbackStrategy
	if fallbackStrategy == "" {
		fallbackStrategy = config.StrategyPassthrough // default: passthrough on error
	}

	// V2: Rate limiting defaults
	maxConcurrent := MaxConcurrentCompressions
	maxPerSecond := MaxCompressionsPerSecond

	// V2: Default idempotent tools (E5)
	idempotentTools := map[string]bool{
		"read_file":       true,
		"search_code":     true,
		"list_directory":  true,
		"grep_search":     true,
		"list_dir":        true,
		"semantic_search": true,
	}

	// Store skip categories from config
	skipCategories := cfg.Pipes.ToolOutput.SkipTools

	// Compresr timeout default
	compresrTimeout := cfg.Pipes.ToolOutput.Compresr.Timeout
	if compresrTimeout == 0 {
		compresrTimeout = 30 * time.Second
	}

	p := &Pipe{
		enabled:             cfg.Pipes.ToolOutput.Enabled,
		strategy:            cfg.Pipes.ToolOutput.Strategy,
		fallbackStrategy:    fallbackStrategy,
		minBytes:            minBytes,
		maxBytes:            maxBytes,
		targetRatio:         targetRatio,
		includeExpandHint:   cfg.Pipes.ToolOutput.IncludeExpandHint,
		enableExpandContext: cfg.Pipes.ToolOutput.EnableExpandContext,
		store:               st,

		// Compresr strategy config (used by both compresr and external_provider strategies)
		compresrEndpoint:      compresrEndpoint,
		compresrKey:           compresrKey,
		compresrModel:         compresrModel,
		compresrTimeout:       compresrTimeout,
		compresrQueryAgnostic: cfg.Pipes.ToolOutput.Compresr.QueryAgnostic,

		// V2: Rate limiting
		maxConcurrent: maxConcurrent,
		maxPerSecond:  maxPerSecond,
		semaphore:     make(chan struct{}, maxConcurrent),
		rateLimiter:   NewRateLimiter(maxPerSecond),
		metrics:       &Metrics{},

		// V2: Idempotent tools
		idempotentTools: idempotentTools,

		// Skip tools (categories resolved per-request based on provider)
		skipCategories: skipCategories,
	}

	// Initialize Compresr client when strategy is 'compresr'
	if cfg.Pipes.ToolOutput.Strategy == config.StrategyCompresr {
		// Use Compresr base URL from config, or fall back to default
		baseURL := cfg.URLs.Compresr
		p.compresrClient = compresr.NewClient(baseURL, compresrKey, compresr.WithTimeout(compresrTimeout))
		log.Info().Str("base_url", baseURL).Str("model", compresrModel).Dur("timeout", compresrTimeout).Msg("tool_output: initialized Compresr client for compresr strategy")
	}

	// Log warning if no API key configured (will rely on captured Bearer token from requests)
	if p.compresrKey == "" && cfg.Pipes.ToolOutput.Strategy == config.StrategyExternalProvider {
		log.Info().Msg("tool_output: no API key configured, will use captured Bearer token from incoming requests")
	}
	if len(skipCategories) > 0 {
		log.Info().Strs("categories", skipCategories).Msg("tool_output: skip_tools categories configured (resolved per-request by provider)")
	}

	return p
}

// Name returns the pipe name.
func (p *Pipe) Name() string {
	return "tool_output"
}

// Strategy returns the processing strategy.
func (p *Pipe) Strategy() string {
	return p.strategy
}

// Enabled returns whether the pipe is active.
func (p *Pipe) Enabled() bool {
	return p.enabled
}

// GetMetrics returns a copy of the current metrics (V2).
func (p *Pipe) GetMetrics() Metrics {
	p.mu.Lock()
	defer p.mu.Unlock()
	return *p.metrics
}

// Close releases resources held by the pipe (V2).
func (p *Pipe) Close() {
	if p.rateLimiter != nil {
		p.rateLimiter.Close()
	}
}

// IsIdempotent checks if a tool is idempotent (E5: safe to re-execute).
func (p *Pipe) IsIdempotent(toolName string) bool {
	return p.idempotentTools[toolName]
}

// IsQueryAgnostic returns whether the model should receive an empty query.
// Query-agnostic models (LLM/cmprsr) don't need the user query.
// Query-dependent models (reranker) need the user query for relevance scoring.
func (p *Pipe) IsQueryAgnostic() bool {
	return p.compresrQueryAgnostic
}

// compressionTask holds data for parallel compression
type compressionTask struct {
	index    int
	msg      message
	toolName string
	shadowID string
	original string
}

// message is a minimal message struct for internal use
type message struct {
	Content    string
	ToolCallID string
}

// compressionResult holds the result of a compression task
type compressionResult struct {
	index             int
	shadowID          string
	toolName          string
	toolCallID        string
	originalContent   string
	compressedContent string
	success           bool
	usedFallback      bool // True if fallback strategy was applied
	_                 bool // Reserved for future cache tracking
	err               error
}

// ExpandContextCall represents an expand_context request from the LLM (V2)
type ExpandContextCall struct {
	ToolUseID string // The tool_use block ID
	ShadowID  string // The shadow reference to expand
}

// ExpandContextToolSchema is the JSON schema for the expand_context tool
var ExpandContextToolSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"id": map[string]interface{}{
			"type":        "string",
			"description": "The shadow reference ID to expand (from <<<SHADOW:xxx>>> prefix)",
		},
	},
	"required": []string{"id"},
}
