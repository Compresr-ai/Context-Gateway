// Package gateway implements the context compression proxy.
//
// DESIGN: Transparent proxy that compresses LLM requests to save tokens:
//  1. Receive request from client (Claude Code, Cursor, etc.)
//  2. Identify provider (OpenAI, Anthropic) from request format
//  3. Route through compression pipe based on content type
//  4. Forward to upstream LLM provider
//  5. Handle expand_context loop if LLM needs full content
//  6. Return response to client
//
// FILES: gateway.go (init), handler.go (HTTP), router.go (pipes), middleware.go (security)
package gateway

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/monitoring"
	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
	"github.com/compresr/context-gateway/internal/preemptive"
	"github.com/compresr/context-gateway/internal/store"
)

const (
	MaxRequestBodySize         = 50 * 1024 * 1024
	HeaderRequestID            = "X-Request-ID"
	HeaderTargetURL            = "X-Target-URL"
	HeaderProvider             = "X-Provider"
	HeaderCompressionThreshold = "X-Compression-Threshold" // User-selectable: off, 256, 1k, 2k, 4k, 8k, 16k, 32k, 64k, 128k
	DefaultRateLimit           = 100
	MaxRateLimitBuckets        = 10000 // Prevent memory exhaustion
)

// allowedHosts contains the approved LLM provider domains for SSRF protection.
// Additional hosts can be added via GATEWAY_ALLOWED_HOSTS env var (comma-separated).
var allowedHosts = map[string]bool{
	// Core providers
	"api.openai.com":                    true,
	"api.anthropic.com":                 true,
	"generativelanguage.googleapis.com": true,

	// OpenCode ecosystem
	"opencode.ai":   true,
	"openrouter.ai": true,

	// Popular LLM providers
	"api.together.ai":       true,
	"api.groq.com":          true,
	"api.fireworks.ai":      true,
	"api.deepseek.com":      true,
	"api.mistral.ai":        true,
	"api.cohere.ai":         true,
	"api.perplexity.ai":     true,
	"inference.cerebras.ai": true,
	"api.x.ai":              true,

	// Cloud providers
	"bedrock-runtime.amazonaws.com":       true,
	"aiplatform.googleapis.com":           true,
	"cognitiveservices.azure.com":         true,
	"openai.azure.com":                    true,
	"api-inference.huggingface.co":        true,
	"ai-gateway.cloudflare.com":           true,

	// Local/self-hosted
	"localhost": true,
	"127.0.0.1": true,
}

func init() {
	// Allow additional hosts via environment variable
	if extra := os.Getenv("GATEWAY_ALLOWED_HOSTS"); extra != "" {
		for _, host := range strings.Split(extra, ",") {
			host = strings.TrimSpace(strings.ToLower(host))
			if host != "" {
				allowedHosts[host] = true
			}
		}
	}
}

// Gateway is the main context compression gateway.
type Gateway struct {
	config      *config.Config
	registry    *adapters.Registry
	router      *Router
	store       store.Store
	tracker     *monitoring.Tracker
	trajectory  *monitoring.TrajectoryManager
	expander    *tooloutput.Expander
	httpClient  *http.Client
	server      *http.Server
	rateLimiter *rateLimiter

	// Preemptive summarization
	preemptive *preemptive.Manager

	// Logging components
	logger        *monitoring.Logger
	requestLogger *monitoring.RequestLogger
	metrics       *monitoring.MetricsCollector
	alerts        *monitoring.AlertManager
}

// New creates a new gateway.
func New(cfg *config.Config) *Gateway {
	st := store.NewMemoryStore(cfg.Store.TTL)
	registry := adapters.NewRegistry()
	r := NewRouter(cfg, st)

	// Initialize logging
	loggerCfg := monitoring.LoggerConfig{
		Level:  cfg.Monitoring.LogLevel,
		Format: cfg.Monitoring.LogFormat,
		Output: cfg.Monitoring.LogOutput,
	}
	logger := monitoring.New(loggerCfg)
	monitoring.Global(loggerCfg)

	// Initialize monitoring components
	requestLogger := monitoring.NewRequestLogger(logger)
	metrics := monitoring.NewMetricsCollector()
	alerts := monitoring.NewAlertManager(logger, monitoring.AlertConfig{
		HighLatencyThreshold: 5 * time.Second,
	})

	// Initialize telemetry
	tracker, err := monitoring.NewTracker(monitoring.TelemetryConfig{
		Enabled:            cfg.Monitoring.TelemetryEnabled,
		LogPath:            cfg.Monitoring.TelemetryPath,
		LogToStdout:        cfg.Monitoring.LogToStdout,
		CompressionLogPath: cfg.Monitoring.CompressionLogPath,
	})
	if err != nil {
		log.Error().Err(err).Msg("failed to initialize telemetry")
		tracker, _ = monitoring.NewTracker(monitoring.TelemetryConfig{Enabled: false})
	}

	// Initialize trajectory manager (ATIF format) - separate files per session ID
	// TrajectoryPath is treated as base directory for per-session files
	trajectoryBaseDir := cfg.Monitoring.TrajectoryPath
	if trajectoryBaseDir != "" {
		// If TrajectoryPath looks like a file path, use its directory
		if filepath.Ext(trajectoryBaseDir) != "" {
			trajectoryBaseDir = filepath.Dir(trajectoryBaseDir)
		}
	}
	trajectoryMgr := monitoring.NewTrajectoryManager(monitoring.TrajectoryManagerConfig{
		Enabled:   cfg.Monitoring.TrajectoryEnabled,
		BaseDir:   trajectoryBaseDir,
		AgentName: cfg.Monitoring.AgentName,
	})

	// Use config write_timeout for upstream requests
	// If 0, no timeout (recommended for LLM proxies to avoid client retries on timeout)
	clientTimeout := cfg.Server.WriteTimeout
	headerTimeout := cfg.Server.WriteTimeout
	if clientTimeout == 0 {
		headerTimeout = 0 // No response header timeout if no client timeout
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		MaxConnsPerHost:       100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: headerTimeout, // 0 = no timeout (safe for LLM with extended thinking)
	}

	g := &Gateway{
		config:        cfg,
		registry:      registry,
		router:        r,
		store:         st,
		tracker:       tracker,
		trajectory:    trajectoryMgr,
		expander:      tooloutput.NewExpander(st, tracker),
		httpClient:    &http.Client{Timeout: clientTimeout, Transport: transport}, // 0 = no timeout
		rateLimiter:   newRateLimiter(DefaultRateLimit),
		preemptive:    preemptive.NewManager(cfg.Preemptive),
		logger:        logger,
		requestLogger: requestLogger,
		metrics:       metrics,
		alerts:        alerts,
	}

	mux := http.NewServeMux()
	g.setupRoutes(mux)

	handler := g.panicRecovery(g.rateLimit(g.loggingMiddleware(g.security(mux))))

	// Server write timeout: how long to write response to client
	// For streaming, this resets on each write, so it's per-chunk not total
	serverWriteTimeout := cfg.Server.WriteTimeout
	if serverWriteTimeout == 0 {
		serverWriteTimeout = 10 * time.Minute // Default to 10 min if not set (safe for streaming)
	}

	g.server = &http.Server{
		Addr:           fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:        handler,
		ReadTimeout:    cfg.Server.ReadTimeout,
		WriteTimeout:   serverWriteTimeout,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	return g
}

// setupRoutes configures the HTTP routes for the gateway.
func (g *Gateway) setupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", g.handleHealth)
	mux.HandleFunc("/expand", g.handleExpand)
	mux.HandleFunc("/", g.handleProxy)
}

// Start starts the gateway.
func (g *Gateway) Start() error {
	log.Info().Int("port", g.config.Server.Port).Msg("gateway starting")
	return g.server.ListenAndServe()
}

// Handler returns the HTTP handler for testing purposes.
func (g *Gateway) Handler() http.Handler {
	return g.server.Handler
}

// Shutdown gracefully shuts down the gateway.
func (g *Gateway) Shutdown(ctx context.Context) error {
	log.Info().Msg("gateway shutting down")

	// Stop preemptive summarization manager
	if g.preemptive != nil {
		g.preemptive.Stop()
	}

	// Stop metrics collector
	if g.metrics != nil {
		g.metrics.Stop()
	}

	// Close all trajectory trackers (writes final trajectory files per session)
	if g.trajectory != nil {
		if err := g.trajectory.CloseAll(); err != nil {
			log.Error().Err(err).Msg("failed to close trajectory trackers")
		}
	}

	// Close telemetry tracker
	if g.tracker != nil {
		g.tracker.Close()
	}

	g.store.Close()
	return g.server.Shutdown(ctx)
}
