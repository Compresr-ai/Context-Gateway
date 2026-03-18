// Package preemptive provides preemptive context summarization to keep requests within token limits.
package preemptive

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sync"

	"github.com/compresr/context-gateway/internal/adapters"
	authtypes "github.com/compresr/context-gateway/internal/auth/types"
	"github.com/compresr/context-gateway/internal/tokenizer"

	"github.com/rs/zerolog/log"
)

// sessionIDRE matches valid X-Session-ID values: alphanumeric, hyphen, underscore only.
// Max 128 characters enforced separately after sanitisation.
var sessionIDRE = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// sanitizeSessionID validates and sanitises a client-supplied X-Session-ID header value.
// Returns the sanitised ID (stripped to alphanumeric + hyphen + underscore, max 128 chars)
// or empty string if the result is empty after sanitisation.
func sanitizeSessionID(id string) string {
	const maxLen = 128
	// Strip all characters not in the allowed set using the compiled regex.
	sanitized := sessionIDRE.ReplaceAllString(id, "")
	if len(sanitized) > maxLen {
		return sanitized[:maxLen]
	}
	return sanitized
}

type Manager struct {
	mu       sync.RWMutex
	config   Config
	sessions *SessionManager
	summary  *Summarizer
	worker   *Worker
	enabled  bool
}

// NewManager creates a preemptive summarization manager.
// If cfg.Enabled is false, returns a no-op manager that passes requests through unchanged.
func NewManager(cfg Config) *Manager {
	cfg = WithDefaults(cfg)
	m := &Manager{config: cfg, enabled: cfg.Enabled}

	if !cfg.Enabled {
		return m
	}

	m.sessions = NewSessionManager(cfg.Session)
	m.summary = NewSummarizer(cfg.Summarizer)
	m.worker = NewWorker(m.summary, m.sessions, cfg.Summarizer, cfg.TriggerThreshold)
	m.worker.Start()

	initLogger(cfg)
	return m
}

// UpdateConfig swaps the manager's configuration (hot-reload).
// Stops the current worker and restarts with new config if enabled.
func (m *Manager) UpdateConfig(cfg Config) {
	cfg = WithDefaults(cfg)

	// Snapshot the current worker under a read lock so we can stop it outside
	// the write lock (Stop may block briefly on channel drain).
	m.mu.RLock()
	oldWorker := m.worker
	existingSessions := m.sessions
	m.mu.RUnlock()

	if oldWorker != nil {
		oldWorker.Stop()
	}

	// Build and start the new worker outside the write lock so its goroutine is
	// already running when m.worker becomes visible to other goroutines.
	var newWorker *Worker
	if cfg.Enabled {
		// Reuse the existing SessionManager so in-flight sessions are preserved.
		if existingSessions == nil {
			existingSessions = NewSessionManager(cfg.Session)
		}
		newSummary := NewSummarizer(cfg.Summarizer)
		newWorker = NewWorker(newSummary, existingSessions, cfg.Summarizer, cfg.TriggerThreshold)
		newWorker.Start()
	}

	m.mu.Lock()
	// Guard against a concurrent UpdateConfig that already installed a newer worker
	// between our read-snapshot above and now. If so, stop our newly started worker
	// to prevent a goroutine leak and keep the most-recently-committed config.
	if m.worker != oldWorker && newWorker != nil {
		newWorker.Stop()
		m.mu.Unlock()
		return
	}
	m.config = cfg
	m.enabled = cfg.Enabled
	if newWorker != nil {
		m.sessions = newWorker.sessions
		m.summary = newWorker.summarizer
	} else {
		m.summary = nil // clear stale summarizer reference when disabling
	}
	m.worker = newWorker
	m.mu.Unlock()
}

func (m *Manager) Stop() {
	m.mu.RLock()
	worker := m.worker
	m.mu.RUnlock()
	if worker != nil {
		worker.Stop()
	}
}

// SetAuth passes captured auth credentials to the summarizer.
// This allows Max/Pro subscription users to use the gateway without a separate API key.
func (m *Manager) SetAuth(auth authtypes.CapturedAuth) {
	m.mu.RLock()
	summary := m.summary
	m.mu.RUnlock()
	if summary != nil {
		summary.SetAuth(auth)
	}
}

// ProcessRequest handles an incoming request.
// Returns: (modifiedBody, isCompaction, syntheticResponse, headers, error)
func (m *Manager) ProcessRequest(ctx context.Context, headers http.Header, body []byte, model, provider string) ([]byte, bool, []byte, map[string]string, error) {
	m.mu.RLock()
	enabled := m.enabled
	cfg := m.config        // snapshot config while holding lock — avoids races in sub-functions
	sessions := m.sessions // snapshot sessions pointer — UpdateConfig may replace it
	summary := m.summary   // snapshot summarizer to avoid race with UpdateConfig
	worker := m.worker     // snapshot worker to avoid race with UpdateConfig
	m.mu.RUnlock()
	if !enabled {
		return body, false, nil, nil, nil
	}

	req, err := m.parseRequest(headers, body, model, provider, cfg, sessions)
	if err != nil {
		return body, false, nil, nil, nil
	}

	if req.detection.IsCompactionRequest {
		return m.handleCompaction(ctx, req, cfg, sessions, summary, worker)
	}

	return m.handleNormalRequest(req, body, cfg, sessions)
}

// parseRequest parses and validates the incoming request.
func (m *Manager) parseRequest(headers http.Header, body []byte, model, providerName string, cfg Config, sessions *SessionManager) (*request, error) {
	messages, err := ParseMessages(body)
	if err != nil || len(messages) == 0 {
		return nil, fmt.Errorf("no messages")
	}

	// HIERARCHICAL SESSION ID MATCHING
	// Priority 0: Explicit X-Session-ID header (client-provided, most reliable)
	// Priority 1: Hash of first USER message (stable identifier)
	// Priority 2: Fuzzy matching based on message count + recency (for subagents)
	// Priority 3: Legacy hash fallback

	var sessionID string
	var sessionSource string

	// LEVEL 0: Explicit X-Session-ID header (most reliable - client provides)
	if rawID := headers.Get("X-Session-ID"); rawID != "" {
		sanitized := sanitizeSessionID(rawID)
		if sanitized != "" {
			sessionID = sanitized
			sessionSource = "explicit_header"
			log.Debug().Str("session_id", sessionID).Msg("Session ID from X-Session-ID header")
		} else {
			log.Warn().Str("raw_id", rawID).Msg("X-Session-ID header contains only invalid characters, ignoring")
		}
	}

	// LEVEL 1: Hash first USER message (most stable approach)
	if sessionID == "" {
		sessionID = sessions.GenerateSessionID(messages)
		if sessionID != "" {
			sessionSource = "first_user_message_hash"
			log.Debug().Str("session_id", sessionID).Msg("Session ID from first user message hash")
		}
	}

	// LEVEL 2: Fuzzy matching (for subagents or when user message not found)
	if sessionID == "" && !cfg.Session.DisableFuzzyMatching {
		log.Info().Int("message_count", len(messages)).Msg("No user message found, attempting fuzzy match")

		if match := sessions.FindBestMatchingSession(len(messages), model, ""); match != nil {
			sessionID = match.Session.ID
			sessionSource = "fuzzy_match"
			log.Info().
				Str("session_id", sessionID).
				Str("match_type", match.MatchType).
				Float64("confidence", match.Confidence).
				Msg("Fuzzy matched to existing session")
		}
	}

	// LEVEL 3: Legacy hash fallback
	if sessionID == "" {
		sessionID = sessions.GenerateSessionIDLegacy(messages)
		sessionSource = "legacy_hash"
		log.Debug().Str("session_id", sessionID).Msg("Fallback to legacy hash")
	}

	if sessionID == "" {
		return nil, fmt.Errorf("no session")
	}

	provider := adapters.ProviderFromString(providerName)
	detector := GetDetector(provider, cfg.Detectors)

	// PRIORITY 0: Generic header-based detection (highest priority)
	// Agents like OpenClaw can send X-Request-Compaction: true header
	var detection DetectionResult
	if genericDetector := GetGenericDetector(cfg.Detectors); genericDetector != nil {
		headerValue := headers.Get(genericDetector.HeaderName())
		detection = genericDetector.DetectFromHeaders(headerValue)
	}

	// PRIORITY 1: Provider-specific detection (path or prompt patterns)
	if !detection.IsCompactionRequest {
		// Get URL path from headers for path-based detection (e.g., Codex /responses/compact)
		requestPath := headers.Get("X-Request-Path")

		// Use path-aware detection for OpenAI (Codex sends to /responses/compact)
		if openaiDetector, ok := detector.(*OpenAIDetector); ok {
			detection = openaiDetector.DetectWithPath(body, requestPath)
		} else {
			detection = detector.Detect(body)
		}
	}

	// SPECIAL HANDLING FOR COMPACTION REQUESTS
	// If this is a compaction request and we don't have a session with a ready summary,
	// try fuzzy matching to find one that does
	if detection.IsCompactionRequest && !cfg.Session.DisableFuzzyMatching {
		existing := sessions.Get(sessionID)
		if existing == nil || (existing.State != StateReady && existing.State != StatePending) {
			log.Info().
				Str("original_session_id", sessionID).
				Str("source", sessionSource).
				Msg("Compaction request: session has no ready summary, trying fuzzy match")

			if match := sessions.FindBestMatchingSession(len(messages), model, sessionID); match != nil {
				log.Info().
					Str("original_id", sessionID).
					Str("matched_id", match.Session.ID).
					Str("match_type", match.MatchType).
					Float64("confidence", match.Confidence).
					Int("message_count", len(messages)).
					Str("source", "fuzzy_match_for_compaction").
					Msg("Fuzzy matched compaction to session with ready summary")
				sessionID = match.Session.ID
			}
		}
	}

	// Capture per-request auth from headers for session isolation
	auth := authtypes.CaptureFromHeaders(headers)

	return &request{
		messages:  messages,
		model:     model,
		sessionID: sessionID,
		provider:  provider,
		detection: detection,
		auth:      auth,
	}, nil
}

// handleNormalRequest processes a non-compaction request.
func (m *Manager) handleNormalRequest(req *request, body []byte, cfg Config, sessions *SessionManager) ([]byte, bool, []byte, map[string]string, error) {
	effectiveMax := getEffectiveMax(req.model, cfg)
	session := sessions.GetOrCreateSession(req.sessionID, req.model, effectiveMax)

	// Update usage tracking
	tokenCount := tokenizer.CountBytes(body)
	usage := CalculateUsage(tokenCount, effectiveMax)
	_ = sessions.Update(req.sessionID, func(s *Session) {
		s.LastKnownTokens = tokenCount
		s.UsagePercent = usage.UsagePercent
	})

	// NOTE: We do NOT invalidate the summary just because new messages arrived.
	// The summary is still valid for the messages it covers. When compaction
	// happens, we use summary + recent messages that weren't summarized.

	// Trigger background summarization if needed (handles staleness check internally)
	m.triggerIfNeeded(session, req, usage.UsagePercent)

	return body, false, nil, buildHeaders(session, usage, cfg), nil
}

// handleCompaction processes a compaction request through the priority chain:
// 1. Precomputed summary (instant)
// 2. Pending background job (wait)
// 3. Synchronous summarization (slow)
func (m *Manager) handleCompaction(ctx context.Context, req *request, cfg Config, sessions *SessionManager, summary *Summarizer, worker *Worker) ([]byte, bool, []byte, map[string]string, error) {
	log.Info().Str("session", req.sessionID).Str("method", req.detection.DetectedBy).Msg("Compaction request")
	logCompactionDetected(req.sessionID, req.model, req.detection)

	session := sessions.Get(req.sessionID)

	// Try each strategy in order
	if result := m.tryPrecomputed(session, req); result != nil {
		body, isCompaction, synthetic, err := m.buildResponse(req, result, true, sessions)
		return body, isCompaction, synthetic, nil, err
	}

	if result := m.tryPending(session, req, cfg, sessions, worker); result != nil {
		body, isCompaction, synthetic, err := m.buildResponse(req, result, true, sessions)
		return body, isCompaction, synthetic, nil, err
	}

	result, err := m.doSynchronous(ctx, req, cfg, sessions, summary)
	if err != nil {
		return nil, true, nil, nil, err
	}
	body, isCompaction, synthetic, err := m.buildResponse(req, result, false, sessions)
	return body, isCompaction, synthetic, nil, err
}

// tryPrecomputed returns cached summary if available.
// The summary covers messages 0..lastIndex. Any messages after that
// will be appended as-is by buildResponse.
func (m *Manager) tryPrecomputed(session *Session, req *request) *summaryResult {
	if session == nil || session.Summary == "" {
		log.Debug().Str("session", req.sessionID).Msg("No precomputed summary")
		return nil
	}
	if session.State != StateReady && session.State != StateUsed {
		log.Debug().Str("session", req.sessionID).Str("state", string(session.State)).Msg("Summary not ready")
		return nil
	}

	log.Info().Str("session", req.sessionID).
		Int("summary_covers", session.SummaryMessageIndex+1).
		Int("request_messages", len(req.messages)).
		Msg("Cache hit - will append recent messages")

	return &summaryResult{
		summary:   session.Summary,
		tokens:    session.SummaryTokens,
		lastIndex: session.SummaryMessageIndex,
	}
}

// tryPending waits for an in-progress background job.
func (m *Manager) tryPending(session *Session, req *request, cfg Config, sessions *SessionManager, worker *Worker) *summaryResult {
	if session == nil || session.State != StatePending {
		return nil
	}
	if worker == nil {
		return nil
	}

	log.Info().Str("session", req.sessionID).Msg("Waiting for background job")
	if !worker.Wait(req.sessionID, cfg.PendingJobTimeout) {
		return nil
	}

	session = sessions.Get(req.sessionID)
	if session == nil || session.State != StateReady || session.Summary == "" {
		return nil
	}

	return &summaryResult{
		summary:   session.Summary,
		tokens:    session.SummaryTokens,
		lastIndex: session.SummaryMessageIndex,
	}
}

// doSynchronous performs summarization synchronously (blocking).
// ctx is the HTTP request context so client disconnects cancel the summary call.
func (m *Manager) doSynchronous(ctx context.Context, req *request, cfg Config, sessions *SessionManager, summary *Summarizer) (*summaryResult, error) {
	log.Info().Str("session", req.sessionID).Msg("Synchronous summarization")
	logCompactionFallback(req.sessionID, req.model)

	ctx, cancel := context.WithTimeout(ctx, cfg.SyncTimeout)
	defer cancel()

	result, err := summary.Summarize(ctx, SummarizeInput{
		Messages:         req.messages,
		TriggerThreshold: cfg.TriggerThreshold,
		KeepRecentTokens: cfg.Summarizer.KeepRecentTokens,
		KeepRecentCount:  cfg.Summarizer.KeepRecentCount,
		Model:            req.model,
		Auth:             req.auth,
	})
	if err != nil {
		logError(req.sessionID, err)
		return nil, fmt.Errorf("summarization failed: %w", err)
	}

	// Cache for potential reuse
	_ = sessions.SetSummaryReady(req.sessionID, result.Summary, result.SummaryTokens, result.LastSummarizedIndex, len(req.messages))

	return &summaryResult{
		summary:   result.Summary,
		tokens:    result.SummaryTokens,
		lastIndex: result.LastSummarizedIndex,
	}, nil
}

// buildResponse constructs the compaction response based on provider.
// Anthropic: returns synthetic response (we intercept, no API call)
// OpenAI: returns modified request (forwarded to API with compacted messages)
//
// NOTE: We keep the summary in StateReady after use, allowing multiple compaction
// requests to reuse the same precomputed summary. The summary will be replaced
// when a new preemptive trigger occurs after the conversation continues.
func (m *Manager) buildResponse(req *request, result *summaryResult, wasPrecomputed bool, sessions *SessionManager) ([]byte, bool, []byte, error) {
	// Increment use counter but keep summary available (StateReady)
	sessions.IncrementUseCount(req.sessionID)
	logCompactionApplied(req.sessionID, req.model, wasPrecomputed, result)

	// Determine if we should exclude the last message (compaction instruction)
	// Prompt-based detection means the last user message triggered compaction
	excludeLastMessage := req.detection.DetectedBy == "claude_code_prompt" ||
		req.detection.DetectedBy == "openai_prompt"

	switch req.provider {
	case adapters.ProviderAnthropic:
		// Summary + recent messages appended (excluding compaction prompt if applicable)
		synthetic := BuildAnthropicResponse(result.summary, req.messages, result.lastIndex, req.model, excludeLastMessage)
		return nil, true, synthetic, nil

	case adapters.ProviderOpenAI:
		compacted := BuildOpenAICompactedRequest(req.messages, result.summary, result.lastIndex, excludeLastMessage)
		return compacted, true, nil, nil

	default:
		synthetic := BuildAnthropicResponse(result.summary, req.messages, result.lastIndex, req.model, excludeLastMessage)
		return nil, true, synthetic, nil
	}
}

func (m *Manager) triggerIfNeeded(session *Session, req *request, usage float64) {
	m.mu.RLock()
	threshold := m.config.TriggerThreshold
	worker := m.worker
	summarizerCfg := m.config.Summarizer
	m.mu.RUnlock()

	if threshold <= 0 {
		return // Preemptive triggering disabled (threshold=0)
	}
	if usage < threshold {
		return
	}

	// Only trigger if idle (no summary exists or summary was already used)
	// - StatePending: already summarizing, wait
	// - StateReady: summary exists and hasn't been used yet, keep it
	// - StateIdle: no summary, trigger one
	if session.State != StateIdle {
		return
	}

	if worker == nil {
		return
	}

	log.Info().Str("session", req.sessionID).Float64("usage", usage).Int("messages", len(req.messages)).Msg("Triggering preemptive summarization")
	summModel, summProvider := summarizerCfg.EffectiveModelAndProvider()
	logPreemptiveTrigger(req.sessionID, req.model, len(req.messages), usage, threshold, summProvider, summModel)

	worker.Submit(req.sessionID, req.messages, req.model, req.auth)
}

func getEffectiveMax(model string, cfg Config) int {
	if cfg.TestContextWindowOverride > 0 {
		return cfg.TestContextWindowOverride
	}
	return GetModelContextWindow(model).EffectiveMax
}

func buildHeaders(session *Session, usage TokenUsage, cfg Config) map[string]string {
	if !cfg.AddResponseHeaders {
		return nil
	}

	headers := map[string]string{
		"X-Context-Usage":  fmt.Sprintf("%.1f%%", usage.UsagePercent),
		"X-Context-Tokens": fmt.Sprintf("%d/%d", usage.InputTokens, usage.MaxTokens),
	}

	if session != nil {
		headers["X-Session-ID"] = session.ID
		headers["X-Session-State"] = string(session.State)
		if session.State == StateReady {
			headers["X-Summary-Ready"] = "true"
			headers["X-Summary-Tokens"] = fmt.Sprintf("%d", session.SummaryTokens)
		}
	}
	return headers
}

func (m *Manager) Stats() map[string]any {
	// snapshot fields under lock to avoid races with UpdateConfig
	m.mu.RLock()
	enabled := m.enabled
	sessions := m.sessions
	worker := m.worker
	m.mu.RUnlock()

	stats := map[string]any{"enabled": enabled}
	if enabled && sessions != nil {
		stats["sessions"] = sessions.Stats()
	}
	if enabled && worker != nil {
		stats["worker"] = worker.Stats()
	}
	return stats
}

func initLogger(cfg Config) {
	// Skip logging if disabled (follows telemetry_enabled setting)
	if !cfg.LoggingEnabled {
		return
	}
	logPath := cfg.CompactionLogPath
	if logPath == "" {
		logPath = cfg.LogDir
	}
	if err := InitCompactionLoggerWithPath(logPath); err != nil {
		log.Warn().Err(err).Msg("Failed to initialize compaction logger")
	}
}

func logCompactionDetected(sessionID, model string, detection DetectionResult) {
	if l := GetCompactionLogger(); l != nil {
		l.LogCompactionDetected(sessionID, model, detection.DetectedBy, detection.Confidence)
	}
}

func logCompactionApplied(sessionID, model string, wasPrecomputed bool, result *summaryResult) {
	if l := GetCompactionLogger(); l != nil {
		l.LogCompactionApplied(sessionID, model, wasPrecomputed, result.lastIndex+1, result.tokens, 0, nil, result.summary)
	}
}

func logCompactionFallback(sessionID, model string) {
	if l := GetCompactionLogger(); l != nil {
		l.LogCompactionFallback(sessionID, model, "no_precomputed_summary")
	}
}

func logPreemptiveTrigger(sessionID, model string, msgCount int, usage, threshold float64, summarizerProvider, summarizerModel string) {
	if l := GetCompactionLogger(); l != nil {
		l.LogPreemptiveTrigger(sessionID, model, msgCount, usage, threshold, summarizerProvider, summarizerModel)
	}
}

func logError(sessionID string, err error) {
	if l := GetCompactionLogger(); l != nil {
		l.LogError(sessionID, "compaction", err, nil)
	}
}
