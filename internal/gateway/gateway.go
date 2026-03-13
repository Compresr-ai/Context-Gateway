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
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/auth"
	"github.com/compresr/context-gateway/internal/compresr"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/costcontrol"
	"github.com/compresr/context-gateway/internal/dashboard"
	"github.com/compresr/context-gateway/internal/monitoring"
	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
	"github.com/compresr/context-gateway/internal/postsession"
	"github.com/compresr/context-gateway/internal/preemptive"
	"github.com/compresr/context-gateway/internal/prompthistory"
	"github.com/compresr/context-gateway/internal/store"
)

// Header constants for gateway requests.
const (
	HeaderRequestID            = "X-Request-ID"
	HeaderTargetURL            = "X-Target-URL"
	HeaderProvider             = "X-Provider"
	HeaderCompressionThreshold = "X-Compression-Threshold" // User-selectable: off, 256, 1k, 2k, 4k, 8k, 16k, 32k, 64k, 128k
)

// Re-export centralized defaults for backward compatibility within this package.
const (
	MaxRequestBodySize     = config.MaxRequestBodySize
	MaxResponseSize        = config.MaxResponseSize
	MaxStreamBufferSize    = config.MaxStreamBufferSize
	DefaultRateLimit       = config.DefaultRateLimit
	MaxRateLimitBuckets    = config.MaxRateLimitBuckets
	DefaultBufferSize      = config.DefaultBufferSize
	DefaultCleanupInterval = config.DefaultCleanupInterval
	DefaultDialTimeout     = config.DefaultDialTimeout
	DefaultStaleTimeout    = config.DefaultStaleTimeout
)

// allowedHosts contains the approved LLM provider domains for SSRF protection.
// Additional hosts can be added via GATEWAY_ALLOWED_HOSTS env var (comma-separated).
var allowedHosts = map[string]bool{
	// Core providers
	"api.openai.com":                    true,
	"chatgpt.com":                       true, // ChatGPT subscription backend
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
	"bedrock-runtime.amazonaws.com": true,
	"aiplatform.googleapis.com":     true,
	"cognitiveservices.azure.com":   true,
	"openai.azure.com":              true,
	"api-inference.huggingface.co":  true,
	"ai-gateway.cloudflare.com":     true,

	// Localhost removed from default allowlist to prevent SSRF
	// Use GATEWAY_ALLOW_LOCAL=true for local development
}

func init() {
	// Allow local development via explicit opt-in
	if os.Getenv("GATEWAY_ALLOW_LOCAL") == "true" {
		allowedHosts["localhost"] = true
		allowedHosts["127.0.0.1"] = true
		log.Warn().Msg("SSRF protection: localhost enabled via GATEWAY_ALLOW_LOCAL (dev mode only)")
	}

	// Allow additional hosts via environment variable
	if extra := os.Getenv("GATEWAY_ALLOWED_HOSTS"); extra != "" {
		var addedHosts []string
		for _, host := range strings.Split(extra, ",") {
			host = strings.TrimSpace(strings.ToLower(host))
			if host != "" {
				allowedHosts[host] = true
				addedHosts = append(addedHosts, host)
			}
		}
		if len(addedHosts) > 0 {
			log.Info().
				Strs("hosts", addedHosts).
				Msg("SSRF allowlist extended via GATEWAY_ALLOWED_HOSTS")
		}
	}
}

// EnableLocalHostsForTesting adds localhost to the SSRF allowlist.
// This should only be called from test setup (TestMain).
func EnableLocalHostsForTesting() {
	allowedHosts["localhost"] = true
	allowedHosts["127.0.0.1"] = true
}

// registerBedrockHosts adds Bedrock Runtime hosts to the SSRF allowlist.
// Only called when Bedrock is explicitly enabled in config.
func registerBedrockHosts() {
	bedrockRegions := []string{
		"us-east-1", "us-east-2", "us-west-1", "us-west-2",
		"eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1", "eu-north-1",
		"ap-southeast-1", "ap-southeast-2", "ap-northeast-1", "ap-northeast-2", "ap-south-1",
		"ca-central-1", "sa-east-1",
	}
	for _, region := range bedrockRegions {
		allowedHosts[fmt.Sprintf("bedrock-runtime.%s.amazonaws.com", region)] = true
	}
}

// Gateway is the main context compression gateway.
type Gateway struct {
	config           *config.Config
	registry         *adapters.Registry
	router           *Router
	store            store.Store
	tracker          *monitoring.Tracker
	savings          *monitoring.SavingsTracker // Legacy: Real-time compression savings
	aggregator       *monitoring.LogAggregator  // New: Background log aggregator (single source of truth)
	trajectory       *monitoring.TrajectoryManager
	httpClient       *http.Client
	server           *http.Server
	dashboardServer  *http.Server // Centralized dashboard on fixed port 18080
	dashboardStarted bool         // Whether this instance owns the dashboard server
	rateLimiter      *rateLimiter

	// Config hot-reload
	configReloader *config.Reloader
	watchCancel    context.CancelFunc // cancels the file-watcher goroutine

	// Cost control
	costTracker *costcontrol.Tracker

	// Preemptive summarization
	preemptive *preemptive.Manager

	// Tool sessions for hybrid tool discovery.
	toolSessions *ToolSessionStore
	authMode     *authFallbackStore

	// Provider-specific auth handlers (subscription/fallback)
	authRegistry *auth.Registry

	// Expander rewrites compressed history for streaming expand_context
	expander *tooloutput.Expander

	// AWS Bedrock support
	bedrockSigner *BedrockSigner

	// Expand context log (in-memory ring buffer for dashboard)
	expandLog *monitoring.ExpandLog

	// Search tool log (in-memory ring buffer for dashboard)
	searchLog *monitoring.SearchLog

	// Persistent prompt history (SQLite)
	promptHistory prompthistory.Store

	// Main conversation session ID — only record prompts for this conversation.
	// Set once from the first valid request; subagent requests have different IDs and are excluded.
	mainConversationID string
	mainConvOnce       sync.Once

	// Current session ID (for filtering dashboard/savings to current session)
	currentSessionID   string
	currentSessionIDMu sync.RWMutex

	// Logging components
	logger        *monitoring.Logger
	requestLogger *monitoring.RequestLogger
	metrics       *monitoring.MetricsCollector
	alerts        *monitoring.AlertManager

	// Optional status reporter (CLI display)
	statusReporter StatusReporter

	// Embedded dashboard SPA (optional, set via SetDashboardFS)
	dashboardFS http.Handler

	// Compresr API client for account status (optional)
	compresrClient *compresr.Client

	// Post-session CLAUDE.md updater
	sessionCollector *postsession.SessionCollector

	// Session monitoring dashboard
	monitorHub   *dashboard.Hub
	monitorStore *dashboard.SessionStore

	// Lazy session initialization
	// Session directory is created on first LLM request, not at gateway startup
	lazySessionPath   string     // Prepared session path (may not exist yet)
	lazySessionConfig []byte     // Config data to write when session is created
	lazySessionOnce   sync.Once  // Ensures session is created only once
	lazySessionMu     sync.Mutex // Protects lazySessionPath during creation
}

// getCurrentSessionID returns the current session ID (thread-safe).
func (g *Gateway) getCurrentSessionID() string {
	g.currentSessionIDMu.RLock()
	defer g.currentSessionIDMu.RUnlock()
	return g.currentSessionID
}

// setCurrentSessionID updates the current session ID (thread-safe).
func (g *Gateway) setCurrentSessionID(id string) {
	g.currentSessionIDMu.Lock()
	defer g.currentSessionIDMu.Unlock()
	g.currentSessionID = id
}

// StatusReporter allows the gateway to update a CLI status display.
type StatusReporter interface {
	IncrementRequests()
	MaybeRefreshCompact() bool
}

// New creates a new gateway.
// configFilePath is optional — if provided, enables hot-reload via the config API.
func New(cfg *config.Config, configFilePath ...string) *Gateway {
	st := store.NewMemoryStoreWithDualTTL(store.DefaultOriginalTTL, store.DefaultCompressedTTL)
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
		Enabled:              cfg.Monitoring.TelemetryEnabled,
		LogPath:              cfg.Monitoring.TelemetryPath,
		LogToStdout:          cfg.Monitoring.LogToStdout,
		VerbosePayloads:      cfg.Monitoring.VerbosePayloads,
		CompressionLogPath:   cfg.Monitoring.CompressionLogPath,
		ToolDiscoveryLogPath: cfg.Monitoring.ToolDiscoveryLogPath,
	})
	if err != nil {
		log.Error().Err(err).Msg("failed to initialize telemetry")
		tracker, _ = monitoring.NewTracker(monitoring.TelemetryConfig{Enabled: false})
	}
	tracker.RecordInit(buildInitEvent(cfg))

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
		Enabled:         cfg.Monitoring.TrajectoryEnabled,
		BaseDir:         trajectoryBaseDir,
		AgentName:       cfg.Monitoring.AgentName,
		SessionTTL:      time.Hour,
		CleanupInterval: 5 * time.Minute,
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

	// Initialize AWS Bedrock signer only when explicitly enabled
	var bedrockSigner *BedrockSigner
	if cfg.Bedrock.Enabled {
		registerBedrockHosts()
		bedrockSigner = NewBedrockSigner()
	}

	// Initialize tool session store for hybrid tool discovery
	toolSessions := NewToolSessionStore(time.Hour) // 1 hour TTL

	// Initialize provider-specific auth handlers
	authRegistry, err := auth.SetupRegistry(cfg)
	if err != nil {
		log.Warn().Err(err).Msg("failed to initialize auth registry, fallback disabled")
		authRegistry = auth.NewRegistry() // Empty registry
	}

	// Initialize log aggregator for /savings (parses logs incrementally in background)
	// Determine logs directory and current session ID
	var logsDir string
	var currentSessionID string
	if cfg.Monitoring.TelemetryPath != "" {
		// TelemetryPath is like "logs/session_xxx/telemetry.jsonl"
		// sessionDir = "logs/session_xxx", logsDir = "logs"
		sessionDir := filepath.Dir(cfg.Monitoring.TelemetryPath)
		logsDir = filepath.Dir(sessionDir)
		currentSessionID = filepath.Base(sessionDir) // "session_xxx"
	} else {
		logsDir = "logs"
	}
	aggregator := monitoring.NewLogAggregator(logsDir, 10*time.Second)
	if currentSessionID != "" {
		// Restrict to current session only — don't accumulate stale data from old sessions.
		aggregator.ResetForNewSession(currentSessionID)
	}
	aggregator.Start()

	// Initialize session monitoring dashboard
	monitorHub := dashboard.NewHub()
	monitorStore := dashboard.NewSessionStore(monitorHub)

	// Initialize prompt history store (SQLite)
	promptHistoryStore, phErr := prompthistory.NewDefault()
	if phErr != nil {
		log.Warn().Err(phErr).Msg("failed to initialize prompt history (prompts will not be recorded)")
	}

	g := &Gateway{
		config:           cfg,
		registry:         registry,
		router:           r,
		store:            st,
		tracker:          tracker,
		savings:          monitoring.NewSavingsTracker(),
		aggregator:       aggregator,
		trajectory:       trajectoryMgr,
		expander:         tooloutput.NewExpander(st, tracker), // Legacy for streaming
		httpClient:       &http.Client{Timeout: clientTimeout, Transport: transport},
		rateLimiter:      newRateLimiter(DefaultRateLimit),
		costTracker:      costcontrol.NewTracker(cfg.CostControl),
		preemptive:       preemptive.NewManager(cfg.ResolvePreemptiveProviderWithLogging(cfg.Monitoring.TelemetryEnabled)),
		toolSessions:     toolSessions,
		authMode:         newAuthFallbackStore(time.Hour),
		authRegistry:     authRegistry,
		bedrockSigner:    bedrockSigner,
		expandLog:        monitoring.NewExpandLog(),
		searchLog:        monitoring.NewSearchLog(),
		promptHistory:    promptHistoryStore,
		currentSessionID: currentSessionID,
		logger:           logger,
		requestLogger:    requestLogger,
		metrics:          metrics,
		alerts:           alerts,
		compresrClient:   compresr.NewClient("", ""), // Uses env vars COMPRESR_BASE_URL, COMPRESR_API_KEY
		sessionCollector: postsession.NewSessionCollector(),
		monitorHub:       monitorHub,
		monitorStore:     monitorStore,
	}

	// Initialize config reloader (hot-reload support)
	var cfgPath string
	if len(configFilePath) > 0 {
		cfgPath = configFilePath[0]
	}
	g.configReloader = config.NewReloader(cfg, cfgPath)

	// Start file watcher so changes to the YAML config file are picked up live.
	// The watcher goroutine is stopped in Shutdown() via watchCancel.
	watchCtx, watchCancel := context.WithCancel(context.Background())
	g.watchCancel = watchCancel
	if cfgPath != "" {
		go g.configReloader.WatchFile(watchCtx, 3*time.Second)
	}

	// Subscribe subsystems to config changes
	g.configReloader.Subscribe(func(newCfg *config.Config) {
		if g.costTracker != nil {
			g.costTracker.UpdateConfig(newCfg.CostControl)
		}
		if g.router != nil {
			g.router.UpdateConfig(newCfg)
		}
		if g.preemptive != nil {
			g.preemptive.UpdateConfig(newCfg.ResolvePreemptiveProviderWithLogging(newCfg.Monitoring.TelemetryEnabled))
		}
	})

	// Start background refresh for instant /savings and /dashboard responses
	// Refreshes every 5s to match dashboard auto-refresh rate
	g.compresrClient.StartBackgroundRefresh(5 * time.Second)

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

	// Try to start centralized dashboard server on fixed port 18080.
	// Only the first gateway instance wins; others skip gracefully.
	g.tryStartDashboardServer()

	return g
}

// cfg returns the current live configuration (thread-safe).
// Always use cfg() in handlers instead of g.config so hot-reload changes take effect.
func (g *Gateway) cfg() *config.Config {
	return g.configReloader.Current()
}

// ConfigReloader returns the gateway's config reloader (for hot-reload support).
func (g *Gateway) ConfigReloader() *config.Reloader {
	return g.configReloader
}

// CostTracker returns the gateway's cost tracker (for CLI status display).
func (g *Gateway) CostTracker() *costcontrol.Tracker {
	return g.costTracker
}

// SavingsTracker returns the gateway's savings tracker (for CLI summary display).
func (g *Gateway) SavingsTracker() *monitoring.SavingsTracker {
	return g.savings
}

// DashboardStarted reports whether this instance owns the centralized dashboard server.
func (g *Gateway) DashboardStarted() bool {
	return g.dashboardStarted
}

// SetStatusReporter attaches a status reporter for CLI usage display.
func (g *Gateway) SetStatusReporter(sr StatusReporter) {
	g.statusReporter = sr
}

// SessionCollector returns the gateway's session event collector for post-session updates.
func (g *Gateway) SessionCollector() *postsession.SessionCollector {
	return g.sessionCollector
}

// PostSessionConfig returns the post-session configuration.
func (g *Gateway) PostSessionConfig() postsession.Config {
	return g.config.PostSession
}

// SetDashboardFS sets the embedded filesystem for the React cost dashboard SPA.
func (g *Gateway) SetDashboardFS(fsys fs.FS) {
	g.dashboardFS = http.FileServerFS(fsys)
}

// SetLazySession configures lazy session creation.
// The session directory is only created when the first LLM request arrives.
// This prevents empty session folders when the gateway starts but receives no traffic.
func (g *Gateway) SetLazySession(sessionPath string, configData []byte) {
	g.lazySessionMu.Lock()
	defer g.lazySessionMu.Unlock()
	g.lazySessionPath = sessionPath
	g.lazySessionConfig = configData
}

// EnsureSession writes the session config if it hasn't been written yet.
// Called on first LLM request to lazily write config.yaml.
// Session directory is created at gateway startup (for gateway.log), but config.yaml
// is only written when actual LLM traffic arrives.
// Returns true if config was just written, false if already done.
func (g *Gateway) EnsureSession() bool {
	created := false
	g.lazySessionOnce.Do(func() {
		g.lazySessionMu.Lock()
		sessionPath := g.lazySessionPath
		configData := g.lazySessionConfig
		g.lazySessionMu.Unlock()

		if sessionPath == "" {
			return // No lazy session configured
		}

		// Ensure session directory exists (should already exist from cmd/agent.go)
		if err := os.MkdirAll(sessionPath, 0750); err != nil {
			log.Error().Err(err).Str("path", sessionPath).Msg("failed to create session directory")
			return
		}

		// Write config.yaml if provided (marks this as a "real" session with LLM traffic)
		if len(configData) > 0 {
			configPath := filepath.Join(sessionPath, "config.yaml")
			if err := os.WriteFile(configPath, configData, 0600); err != nil {
				log.Warn().Err(err).Str("path", configPath).Msg("failed to write session config")
			}
		}

		// Update current session ID (thread-safe)
		g.setCurrentSessionID(filepath.Base(sessionPath))

		// Reset all trackers so every variable starts at 0 for the new session
		g.resetForNewSession()

		log.Info().Str("session", g.getCurrentSessionID()).Msg("session config written on first LLM request")
		created = true
	})
	return created
}

// resetForNewSession zeros out all accumulated metrics and state
// so that every variable starts at 0 for the new session.
func (g *Gateway) resetForNewSession() {
	// Reset in-memory savings tracker
	if g.savings != nil {
		g.savings.Reset()
	}

	// Reset log aggregator — restrict to current session only (ignore old logs on disk)
	if g.aggregator != nil {
		g.aggregator.ResetForNewSession(g.getCurrentSessionID())
	}

	// Reset cost tracker
	if g.costTracker != nil {
		g.costTracker.ResetGlobalCost()
	}

	// Reset expand context log
	if g.expandLog != nil {
		g.expandLog.Reset()
	}

	// Reset search tool log
	if g.searchLog != nil {
		g.searchLog.Reset()
	}

	// Reset operational metrics
	if g.metrics != nil {
		g.metrics.Reset()
	}

	// Reset shadow context store (cached compressed content from previous sessions)
	if ms, ok := g.store.(*store.MemoryStore); ok {
		ms.Reset()
	}

	// Reset tool session store (deferred/expanded tools from previous sessions)
	if g.toolSessions != nil {
		g.toolSessions.Reset()
	}

	// Reset auth fallback state
	if g.authMode != nil {
		g.authMode.Reset()
	}

	log.Debug().Msg("all session variables reset to 0")
}

// setupRoutes configures the HTTP routes for the gateway proxy server.
// Dashboard routes are NOT registered here — they run on the dedicated dashboard port (18080).
func (g *Gateway) setupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", g.handleHealth)
	mux.HandleFunc("/expand", g.handleExpand)
	// API endpoints still available on proxy port for internal use (e.g., /savings slash command)
	mux.HandleFunc("/api/dashboard", g.handleDashboardAPI)
	mux.HandleFunc("/api/savings", g.handleSavingsAPI)
	mux.HandleFunc("/api/account", g.handleAccountAPI)
	mux.HandleFunc("/api/config", g.handleConfigAPI)
	mux.HandleFunc("/api/prompts", g.handlePromptsAPI)
	mux.HandleFunc("/api/prompts/erase", g.handleErasePrompts)
	mux.HandleFunc("/api/compress/", g.handleCompressAPINotFound)
	mux.HandleFunc("/stats", g.handleStats)
	mux.HandleFunc("/v1/models", g.handleModels)

	// Session monitoring dashboard
	monitorHandlers := dashboard.NewHandlers(g.monitorStore, g.monitorHub)
	monitorHandlers.SetPort(g.config.Server.Port)
	monitorHandlers.RegisterRoutes(mux)

	mux.HandleFunc("/", g.handleProxy)
}

// setupDashboardRoutes configures routes for the centralized dashboard server on port 18080.
func (g *Gateway) setupDashboardRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", g.handleHealth)
	mux.HandleFunc("/api/dashboard", g.handleAggregatedDashboardAPI)
	mux.HandleFunc("/api/savings", g.handleSavingsAPI)
	mux.HandleFunc("/api/account", g.handleAccountAPI)
	mux.HandleFunc("/api/config", g.handleConfigAPI)
	mux.HandleFunc("/api/prompts", g.handlePromptsAPI)
	mux.HandleFunc("/api/prompts/erase", g.handleErasePrompts)
	mux.HandleFunc("/api/monitor", g.handleAggregatedMonitorAPI)
	mux.HandleFunc("/api/monitor/rename", g.handleRenameInstance)
	mux.HandleFunc("/api/instance/config", g.handleInstanceConfigProxy)
	mux.HandleFunc("/api/focus", g.handleFocusTerminal)
	mux.HandleFunc("/dashboard", g.handleDashboard)
	mux.HandleFunc("/dashboard/", g.handleDashboard)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/", http.StatusMovedPermanently)
	})
}

// handleCompressAPINotFound returns a helpful error when Compresr API calls hit the gateway.
// This prevents misconfigured clients from sending compression requests to the proxy endpoint.
func (g *Gateway) handleCompressAPINotFound(w http.ResponseWriter, r *http.Request) {
	log.Warn().
		Str("path", r.URL.Path).
		Str("client_ip", r.RemoteAddr).
		Msg("rejected Compresr API call to gateway - this endpoint should target api.compresr.ai")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{
			"message": "Compresr API endpoint not available on gateway. Use https://api.compresr.ai or check your COMPRESR_BASE_URL configuration.",
			"type":    "configuration_error",
		},
	})
}

// Start starts the gateway.
func (g *Gateway) Start() error {
	log.Info().Int("port", g.config.Server.Port).Msg("Context Gateway starting")
	if g.dashboardStarted {
		log.Info().
			Int("port", config.DefaultDashboardPort).
			Str("dashboard", fmt.Sprintf("http://localhost:%d/dashboard/", config.DefaultDashboardPort)).
			Msg("dashboard available")
	}
	return g.server.ListenAndServe()
}

// Handler returns the HTTP handler for testing purposes.
func (g *Gateway) Handler() http.Handler {
	return g.server.Handler
}

// IsAllowedHostForTest exposes isAllowedHost for unit testing SSRF protection.
func (g *Gateway) IsAllowedHostForTest(host string) bool {
	return g.isAllowedHost(host)
}

// Shutdown gracefully shuts down the gateway.
func (g *Gateway) Shutdown(ctx context.Context) error {
	log.Info().Msg("gateway shutting down")

	// Stop file-watcher goroutine
	if g.watchCancel != nil {
		g.watchCancel()
	}

	// Stop cleanup goroutines
	if g.rateLimiter != nil {
		g.rateLimiter.Stop()
	}
	if g.authMode != nil {
		g.authMode.Stop()
	}
	if g.authRegistry != nil {
		g.authRegistry.Stop()
	}

	// Stop preemptive summarization manager
	if g.preemptive != nil {
		g.preemptive.Stop()
	}

	// Stop metrics collector
	if g.metrics != nil {
		g.metrics.Stop()
	}

	// Stop savings tracker cleanup goroutine
	if g.savings != nil {
		g.savings.Stop()
	}

	// Stop log aggregator
	if g.aggregator != nil {
		g.aggregator.Stop()
	}

	// Stop session monitor
	if g.monitorStore != nil {
		g.monitorStore.Stop()
	}

	// Stop Compresr client background refresh
	if g.compresrClient != nil {
		g.compresrClient.StopBackgroundRefresh()
	}

	// Close all trajectory trackers (writes final trajectory files per session)
	if g.trajectory != nil {
		if err := g.trajectory.CloseAll(); err != nil {
			log.Error().Err(err).Msg("failed to close trajectory trackers")
		}
	}

	// Close telemetry tracker
	if g.tracker != nil {
		_ = g.tracker.Close()
	}

	// Close prompt history store
	if g.promptHistory != nil {
		if err := g.promptHistory.Close(); err != nil {
			log.Error().Err(err).Msg("failed to close prompt history store")
		}
	}

	// Shutdown dashboard server if this instance owns it
	if g.dashboardServer != nil {
		if err := g.dashboardServer.Shutdown(ctx); err != nil {
			log.Error().Err(err).Msg("dashboard server shutdown error")
		}
	}

	_ = g.store.Close()
	return g.server.Shutdown(ctx)
}
