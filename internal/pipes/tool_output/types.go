// Package tooloutput compresses tool outputs and stores originals for expansion.
package tooloutput

import (
	"sync"
	"time"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/circuitbreaker"
	"github.com/compresr/context-gateway/internal/compresr"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
	"github.com/compresr/context-gateway/internal/store"
	"github.com/rs/zerolog/log"
)

const (
	// DefaultLLMBuffer is extra TTL added before sending to LLM.
	DefaultLLMBuffer = 10 * time.Minute

	// MaxExpandLoops prevents infinite expansion cycles.
	MaxExpandLoops = 5

	// MaxConcurrentCompressions limits parallel compression API calls.
	MaxConcurrentCompressions = 10

	// MaxCompressionsPerSecond is the rate limit for compression API calls.
	MaxCompressionsPerSecond = 20

	// DefaultRefusalThreshold is the minimum token savings fraction required to accept compression.
	DefaultRefusalThreshold = 0.05

	// ExpandContextToolName is the phantom tool injected for expansion.
	ExpandContextToolName = "expand_context"

	// ShadowIDPrefix is the prefix for shadow reference IDs.
	ShadowIDPrefix = "shadow_"

	// PrefixFormat is the LLM-visible format for compressed content with shadow ID.
	// Uses [REF:id] format for brevity and readability.
	PrefixFormat = "[REF:%s]\n%s"

	// PrefixFormatWithHint includes expand_context usage hint before compressed content.
	PrefixFormatWithHint = "[COMPRESSED — call expand_context(id=\"%s\") for full content]\n[REF:%s]\n%s"

	// ShadowPrefixMarker is used to detect already-compressed content.
	ShadowPrefixMarker = "[REF:"

	// ExpandContextTextPrefix is the prefix for text-based expand_context patterns.
	ExpandContextTextPrefix = "<<<EXPAND:"

	// ExpandContextTextSuffix is the suffix for text-based expand_context patterns.
	ExpandContextTextSuffix = ">>>"

	// StructuredSeparator separates verbatim prefix from compressed tail.
	StructuredSeparator = "--- COMPRESSED SUMMARY (above is verbatim) ---"
)

// Pipe compresses tool outputs dynamically and stores raw data for retrieval.
type Pipe struct {
	enabled                bool
	strategy               string
	fallbackStrategy       string
	minTokens              int
	maxTokens              int
	targetCompressionRatio float64
	refusalThreshold       float64
	includeExpandHint      bool
	enableExpandContext    bool
	bypassCostCheck        bool
	store                  store.Store

	compresrClient *compresr.Client

	compresrEndpoint      string
	compresrKey           string
	compresrModel         string
	compresrTimeout       time.Duration
	compresrQueryAgnostic bool

	maxConcurrent int
	maxPerSecond  int
	semaphore     chan struct{}
	rateLimiter   *RateLimiter

	mu      sync.RWMutex
	metrics *Metrics

	skipCategories []string

	// effectiveFormats is the resolved set of content formats eligible for compression.
	effectiveFormats map[adapters.ContentFormat]bool

	circuit *circuitbreaker.CircuitBreaker
}

// Metrics tracks compression statistics.
type Metrics struct {
	CacheHits       int64
	CacheMisses     int64
	CompressionOK   int64
	CompressionFail int64
	ExpandRequests  int64
	ExpandCacheMiss int64
	RateLimited     int64
	TokensSaved     int64
}

// RateLimiter implements token bucket rate limiting.
type RateLimiter struct {
	mu         sync.Mutex
	tokens     float64
	maxTokens  float64
	refillRate float64
	lastRefill time.Time
	closed     bool
}

// NewRateLimiter creates a rate limiter.
// A zero or negative rate would set refillRate=0 and permanently block all requests;
// clamp to 1 as a safe minimum so the limiter always makes progress.
func NewRateLimiter(maxPerSecond int) *RateLimiter {
	if maxPerSecond <= 0 {
		maxPerSecond = 1
	}
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
func New(cfg *config.Config, st store.Store) *Pipe {
	// Resolve provider settings (endpoint, api_key, model) from providers section
	var compresrEndpoint, compresrKey, compresrModel string
	if cfg.Pipes.ToolOutput.Provider != "" {
		if resolved, err := cfg.ResolveProvider(cfg.Pipes.ToolOutput.Provider); err == nil {
			compresrEndpoint = resolved.Endpoint
			compresrKey = resolved.ProviderAuth
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

	// Use config fields with sensible defaults (tokens, not bytes)
	minTokens := cfg.Pipes.ToolOutput.MinTokens
	if minTokens == 0 {
		minTokens = config.DefaultMinTokens
	}

	maxTokens := cfg.Pipes.ToolOutput.MaxTokens
	if maxTokens == 0 {
		maxTokens = config.DefaultMaxTokens
	}

	targetCompressionRatio := cfg.Pipes.ToolOutput.TargetCompressionRatio

	refusalThreshold := cfg.Pipes.ToolOutput.RefusalThreshold
	if refusalThreshold == 0 {
		refusalThreshold = DefaultRefusalThreshold
	}

	fallbackStrategy := cfg.Pipes.ToolOutput.FallbackStrategy
	if fallbackStrategy == "" {
		fallbackStrategy = config.StrategyPassthrough
	}

	maxConcurrent := MaxConcurrentCompressions
	maxPerSecond := MaxCompressionsPerSecond

	skipCategories := cfg.Pipes.ToolOutput.SkipTools.Categories

	effectiveFormats := adapters.BuildEffectiveFormats(
		cfg.Pipes.ToolOutput.ContentFormats.Allowed,
		cfg.Pipes.ToolOutput.ContentFormats.Forbidden,
	)

	compresrTimeout := cfg.Pipes.ToolOutput.Compresr.Timeout
	if compresrTimeout == 0 {
		compresrTimeout = 30 * time.Second
	}

	p := &Pipe{
		enabled:                cfg.Pipes.ToolOutput.Enabled,
		strategy:               cfg.Pipes.ToolOutput.Strategy,
		fallbackStrategy:       fallbackStrategy,
		minTokens:              minTokens,
		maxTokens:              maxTokens,
		targetCompressionRatio: targetCompressionRatio,
		refusalThreshold:       refusalThreshold,
		includeExpandHint:      cfg.Pipes.ToolOutput.IncludeExpandHint || cfg.Pipes.ToolOutput.EnableExpandContext,
		enableExpandContext:    cfg.Pipes.ToolOutput.EnableExpandContext,
		bypassCostCheck:        cfg.Pipes.ToolOutput.BypassCostCheck,
		store:                  st,

		compresrEndpoint:      compresrEndpoint,
		compresrKey:           compresrKey,
		compresrModel:         compresrModel,
		compresrTimeout:       compresrTimeout,
		compresrQueryAgnostic: cfg.Pipes.ToolOutput.Compresr.QueryAgnostic,

		maxConcurrent:    maxConcurrent,
		maxPerSecond:     maxPerSecond,
		semaphore:        make(chan struct{}, maxConcurrent),
		rateLimiter:      NewRateLimiter(maxPerSecond),
		metrics:          &Metrics{},
		skipCategories:   skipCategories,
		effectiveFormats: effectiveFormats,
		circuit:          circuitbreaker.New(),
	}

	if cfg.Pipes.ToolOutput.Strategy == config.StrategyCompresr {
		baseURL := cfg.URLs.Compresr
		p.compresrClient = compresr.NewClient(baseURL, compresrKey, compresr.WithTimeout(compresrTimeout))
		log.Info().Str("base_url", baseURL).Str("model", compresrModel).Dur("timeout", compresrTimeout).Msg("tool_output: initialized Compresr client for compresr strategy")
	}

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

// GetMetrics returns a copy of the current metrics.
func (p *Pipe) GetMetrics() Metrics {
	p.mu.Lock()
	defer p.mu.Unlock()
	return *p.metrics
}

// Close releases resources held by the pipe.
func (p *Pipe) Close() {
	if p.rateLimiter != nil {
		p.rateLimiter.Close()
	}
}

// IsQueryAgnostic returns whether the model should receive an empty query.
// Query-agnostic models (LLM/cmprsr) don't need the user query.
// Query-dependent models (reranker) need the user query for relevance scoring.
func (p *Pipe) IsQueryAgnostic() bool {
	return p.compresrQueryAgnostic
}

// compressionTask holds data for parallel compression.
type compressionTask struct {
	index        int
	msg          message
	toolName     string
	shadowID     string
	original     string
	messageIndex int
	blockIndex   int
}

// message is a minimal message struct for internal use
type message struct {
	Content    string
	ToolCallID string
}

// compressionResult holds the result of a compression task.
type compressionResult struct {
	index             int
	shadowID          string
	toolName          string
	toolCallID        string
	originalContent   string
	compressedContent string
	success           bool
	usedFallback      bool
	err               error
	messageIndex      int
	blockIndex        int
}

// ExpandContextCall represents an expand_context request from the LLM.
type ExpandContextCall struct {
	ToolUseID string
	ShadowID  string
}
