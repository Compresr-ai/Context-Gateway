// Package config - defaults.go centralizes magic numbers and default values.
package config

import (
	"time"

	"github.com/compresr/context-gateway/internal/compresr"
)

// STORE DEFAULTS

// DefaultOriginalTTL is the TTL for original content in shadow context.
// Needed for expand_context calls to retrieve uncompressed content.
const DefaultOriginalTTL = 5 * time.Hour

// DefaultCompressedTTL is the long TTL for compressed content.
// Longer TTL preserves KV-cache across requests.
const DefaultCompressedTTL = 24 * time.Hour

// CLEANUP AND MAINTENANCE

// DefaultCleanupInterval is the frequency for background cleanup goroutines.
const DefaultCleanupInterval = 5 * time.Minute

// DefaultStaleTimeout is when entries are considered stale for cleanup.
const DefaultStaleTimeout = 10 * time.Minute

// RATE LIMITING

// DefaultRateLimit is requests per second per IP.
const DefaultRateLimit = 100

// MaxRateLimitBuckets prevents memory exhaustion from too many IP buckets.
const MaxRateLimitBuckets = 10000

// HTTP AND NETWORKING

// DefaultBufferSize is the standard I/O buffer size.
const DefaultBufferSize = 4096

// DefaultDialTimeout is the TCP dial timeout.
const DefaultDialTimeout = 30 * time.Second

// MaxRequestBodySize is the maximum allowed request body (50MB).
const MaxRequestBodySize = 50 * 1024 * 1024

// MaxResponseSize is the maximum allowed upstream response body (50MB).
const MaxResponseSize = 50 * 1024 * 1024

// MaxStreamBufferSize is the maximum bytes to buffer when capturing streaming
// responses for expand_context detection. Prevents OOM on very large streams.
const MaxStreamBufferSize = 50 * 1024 * 1024

// TOOL DISCOVERY DEFAULTS

// DefaultMaxSearchResults from gateway_search_tools.
const DefaultMaxSearchResults = 5

// DefaultSearchToolName is the phantom tool name for search fallback.
const DefaultSearchToolName = "gateway_search_tools"

// COMPRESSION DEFAULTS

// DefaultMinTokens is the minimum token count for compression (512 tokens).
// Smaller outputs pass through uncompressed.
const DefaultMinTokens = 512

// DefaultMaxTokens is the maximum token count for compression (50K tokens).
// Larger outputs skip compression (too expensive to process).
const DefaultMaxTokens = 50000

// GATEWAY PORT RANGE

// DefaultDashboardPort is the fixed port for the centralized dashboard.
const DefaultDashboardPort = 18080

// DefaultSessionIdleTimeout is the inactivity window before the heartbeat liveness
// check fires. After this window the gateway pings itself; if alive the session
// heartbeat is reset (stays active). Sessions only become "finished" on gateway shutdown.
const DefaultSessionIdleTimeout = 10 * time.Minute

// DefaultGatewayBasePort is the starting port for gateway instances.
const DefaultGatewayBasePort = 18081

// MaxGatewayPorts is the maximum concurrent gateway instances.
const MaxGatewayPorts = 10

// COMPRESR PLATFORM URLS

// DefaultCompresrAPIBaseURL is the production Compresr API base URL.
// Re-exported from compresr package where the canonical definition lives.
const DefaultCompresrAPIBaseURL = compresr.DefaultCompresrAPIBaseURL

// DefaultCompresrFrontendBaseURL is the production Compresr frontend URL.
const DefaultCompresrFrontendBaseURL = "https://compresr.ai"

// DefaultCompresrDocsURL is the gateway documentation URL.
const DefaultCompresrDocsURL = "https://docs.compresr.ai/gateway"

// DefaultCompresrInstallURL is the gateway install script URL.
const DefaultCompresrInstallURL = DefaultCompresrFrontendBaseURL + "/install_gateway.sh"

// DefaultCompresrDashboardURL is the API key dashboard URL.
const DefaultCompresrDashboardURL = DefaultCompresrFrontendBaseURL + "/dashboard"
