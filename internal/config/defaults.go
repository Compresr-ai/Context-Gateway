// Package config - defaults.go centralizes magic numbers and default values.
//
// DESIGN: All default values that appear in multiple places should be defined here.
// This makes configuration more maintainable and auditable.
package config

import "time"

// =============================================================================
// TOKEN ESTIMATION
// =============================================================================

// TokenEstimateRatio is the approximate number of characters per token.
// Used for rough token counting when exact counts aren't available.
const TokenEstimateRatio = 4

// =============================================================================
// PREEMPTIVE SUMMARIZATION DEFAULTS
// =============================================================================

// DefaultTriggerThreshold is the context usage percentage at which
// background summarization is triggered (e.g., 85 = trigger at 85% usage).
const DefaultTriggerThreshold = 85.0

// DefaultKeepRecentPercent is derived from trigger threshold.
// If trigger is 85%, we keep 15% of context as recent messages.
const DefaultKeepRecentPercent = 15.0

// DefaultSummaryTTL is how long summaries are cached.
const DefaultSummaryTTL = 3 * time.Hour

// DefaultHashMessageCount is how many messages to hash for session ID.
const DefaultHashMessageCount = 3

// =============================================================================
// STORE DEFAULTS
// =============================================================================

// DefaultOriginalTTL is the short TTL for original content in shadow context.
// Only needed for expand_context calls during a request.
const DefaultOriginalTTL = 5 * time.Minute

// DefaultCompressedTTL is the long TTL for compressed content.
// Longer TTL preserves KV-cache across requests.
const DefaultCompressedTTL = 24 * time.Hour

// =============================================================================
// CLEANUP AND MAINTENANCE
// =============================================================================

// DefaultCleanupInterval is the frequency for background cleanup goroutines.
const DefaultCleanupInterval = 5 * time.Minute

// DefaultStaleTimeout is when entries are considered stale for cleanup.
const DefaultStaleTimeout = 10 * time.Minute

// DefaultSessionTTL is the TTL for session-scoped data (auth fallback, tool sessions).
const DefaultSessionTTL = 1 * time.Hour

// =============================================================================
// RATE LIMITING
// =============================================================================

// DefaultRateLimit is requests per second per IP.
const DefaultRateLimit = 100

// MaxRateLimitBuckets prevents memory exhaustion from too many IP buckets.
const MaxRateLimitBuckets = 10000

// =============================================================================
// HTTP AND NETWORKING
// =============================================================================

// DefaultBufferSize is the standard I/O buffer size.
const DefaultBufferSize = 4096

// DefaultDialTimeout is the TCP dial timeout.
const DefaultDialTimeout = 30 * time.Second

// MaxRequestBodySize is the maximum allowed request body (50MB).
const MaxRequestBodySize = 50 * 1024 * 1024

// MaxResponseSize is the maximum allowed upstream response body (50MB).
const MaxResponseSize = 50 * 1024 * 1024

// MaxErrorBodyLogLen limits error response body in logs to prevent bloat.
const MaxErrorBodyLogLen = 500

// DefaultServerWriteTimeout for HTTP server (safe for streaming).
const DefaultServerWriteTimeout = 10 * time.Minute

// =============================================================================
// TOOL DISCOVERY DEFAULTS
// =============================================================================

// DefaultMinTools is the minimum number of tools to keep after filtering.
const DefaultMinTools = 5

// DefaultMaxTools is the maximum number of tools to include.
const DefaultMaxTools = 25

// DefaultTargetRatio is the target percentage of tools to keep (0.8 = 80%).
const DefaultTargetRatio = 0.8

// DefaultMaxSearchResults from gateway_search_tools.
const DefaultMaxSearchResults = 5

// DefaultSearchToolName is the phantom tool name for search fallback.
const DefaultSearchToolName = "gateway_search_tools"

// =============================================================================
// COMPRESSION DEFAULTS
// =============================================================================

// DefaultMinBytes is the minimum content size for compression.
const DefaultMinBytes = 1024

// DefaultMaxBytes is the maximum content size for compression.
const DefaultMaxBytes = 512 * 1024

// =============================================================================
// GATEWAY PORT RANGE
// =============================================================================

// DefaultGatewayBasePort is the starting port for gateway instances.
const DefaultGatewayBasePort = 18080

// MaxGatewayPorts is the maximum concurrent gateway instances.
const MaxGatewayPorts = 10

// =============================================================================
// COST CONTROL
// =============================================================================

// DefaultCostSessionTTL is how long cost sessions are tracked.
const DefaultCostSessionTTL = 24 * time.Hour
