// Package preemptive provides preemptive summarization for context window management.
//
// FLOW:
//  1. Request arrives → detect if it's a compaction request
//  2. If compaction:
//     a. Check for precomputed summary (cache hit) → return immediately
//     b. Wait for pending background job → return when ready
//     c. Fall back to synchronous summarization
//  3. If normal request:
//     a. Track token usage
//     b. Trigger background summarization if usage > threshold
//
// The goal is to have summaries ready BEFORE they're needed (preemptive).
package preemptive

import (
	"context"
	"fmt"
	"net/http"

	"github.com/compresr/context-gateway/internal/adapters"

	"github.com/rs/zerolog/log"
)

// =============================================================================
// MANAGER
// =============================================================================

type Manager struct {
	config   Config
	sessions *SessionManager
	summary  *Summarizer
	worker   *Worker
	enabled  bool
}

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

func (m *Manager) Stop() {
	if m.worker != nil {
		m.worker.Stop()
	}
}

// SetAuthToken passes an auth token to the summarizer for use when no API key is configured.
// This allows Max/Pro subscription users to use the gateway without a separate API key.
// isFromXAPIKeyHeader indicates if token came from x-api-key header (vs Authorization: Bearer).
func (m *Manager) SetAuthToken(token string, isFromXAPIKeyHeader bool) {
	if m.summary != nil {
		m.summary.SetAuthToken(token, isFromXAPIKeyHeader)
	}
}

// SetEndpoint passes the upstream endpoint URL to the summarizer.
// Used when no explicit endpoint is configured - uses same endpoint as user's requests.
func (m *Manager) SetEndpoint(endpoint string) {
	if m.summary != nil {
		m.summary.SetEndpoint(endpoint)
	}
}

// =============================================================================
// REQUEST PROCESSING
// =============================================================================

// ProcessRequest handles an incoming request.
// Returns: (modifiedBody, isCompaction, syntheticResponse, headers, error)
func (m *Manager) ProcessRequest(headers http.Header, body []byte, model, provider string) ([]byte, bool, []byte, map[string]string, error) {
	if !m.enabled {
		return body, false, nil, nil, nil
	}

	req, err := m.parseRequest(headers, body, model, provider)
	if err != nil {
		return body, false, nil, nil, nil
	}

	if req.detection.IsCompactionRequest {
		return m.handleCompaction(req)
	}

	return m.handleNormalRequest(req, body)
}

// parseRequest parses and validates the incoming request.
func (m *Manager) parseRequest(headers http.Header, body []byte, model, providerName string) (*request, error) {
	messages, err := ParseMessages(body)
	if err != nil || len(messages) == 0 {
		return nil, fmt.Errorf("no messages")
	}

	// ==========================================================================
	// HIERARCHICAL SESSION ID MATCHING
	// ==========================================================================
	// Priority 1: Hash of first USER message (stable identifier)
	// Priority 2: Fuzzy matching based on message count + recency (for subagents)
	// Priority 3: Legacy hash fallback
	// ==========================================================================

	var sessionID string
	var sessionSource string

	// LEVEL 1: Hash first USER message (most stable approach)
	sessionID = m.sessions.GenerateSessionID(messages)
	if sessionID != "" {
		sessionSource = "first_user_message_hash"
		log.Debug().Str("session_id", sessionID).Msg("Session ID from first user message hash")
	}

	// LEVEL 2: Fuzzy matching (for subagents or when user message not found)
	if sessionID == "" {
		log.Info().Int("message_count", len(messages)).Msg("No user message found, attempting fuzzy match")

		if match := m.sessions.FindBestMatchingSession(len(messages), model, ""); match != nil {
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
		sessionID = m.sessions.GenerateSessionIDLegacy(messages)
		sessionSource = "legacy_hash"
		log.Debug().Str("session_id", sessionID).Msg("Fallback to legacy hash")
	}

	if sessionID == "" {
		return nil, fmt.Errorf("no session")
	}

	provider := adapters.ProviderFromString(providerName)
	detector := GetDetector(provider, m.config.Detectors)
	detection := detector.Detect(body)

	// SPECIAL HANDLING FOR COMPACTION REQUESTS
	// If this is a compaction request and we don't have a session with a ready summary,
	// try fuzzy matching to find one that does
	if detection.IsCompactionRequest {
		existing := m.sessions.Get(sessionID)
		if existing == nil || (existing.State != StateReady && existing.State != StatePending) {
			log.Info().
				Str("original_session_id", sessionID).
				Str("source", sessionSource).
				Msg("Compaction request: session has no ready summary, trying fuzzy match")

			if match := m.sessions.FindBestMatchingSession(len(messages), model, sessionID); match != nil {
				log.Info().
					Str("original_id", sessionID).
					Str("matched_id", match.Session.ID).
					Str("match_type", match.MatchType).
					Float64("confidence", match.Confidence).
					Int("message_count", len(messages)).
					Msg("Fuzzy matched compaction to session with ready summary")
				sessionID = match.Session.ID
				_ = sessionSource // Used for logging above
			}
		}
	}

	return &request{
		messages:  messages,
		model:     model,
		sessionID: sessionID,
		provider:  provider,
		detection: detection,
	}, nil
}

// handleNormalRequest processes a non-compaction request.
func (m *Manager) handleNormalRequest(req *request, body []byte) ([]byte, bool, []byte, map[string]string, error) {
	effectiveMax := m.getEffectiveMax(req.model)
	session := m.sessions.GetOrCreateSession(req.sessionID, req.model, effectiveMax)

	// Update usage tracking
	tokenCount := len(body) / m.config.TokenEstimateRatio
	usage := CalculateUsage(tokenCount, effectiveMax)
	_ = m.sessions.Update(req.sessionID, func(s *Session) {
		s.LastKnownTokens = tokenCount
		s.UsagePercent = usage.UsagePercent
	})

	// NOTE: We do NOT invalidate the summary just because new messages arrived.
	// The summary is still valid for the messages it covers. When compaction
	// happens, we use summary + recent messages that weren't summarized.

	// Trigger background summarization if needed (handles staleness check internally)
	m.triggerIfNeeded(session, req, usage.UsagePercent)

	return body, false, nil, m.buildHeaders(session, usage), nil
}

// =============================================================================
// COMPACTION HANDLING
// =============================================================================

// handleCompaction processes a compaction request through the priority chain:
// 1. Precomputed summary (instant)
// 2. Pending background job (wait)
// 3. Synchronous summarization (slow)
func (m *Manager) handleCompaction(req *request) ([]byte, bool, []byte, map[string]string, error) {
	log.Info().Str("session", req.sessionID).Str("method", req.detection.DetectedBy).Msg("Compaction request")
	logCompactionDetected(req.sessionID, req.model, req.detection)

	session := m.sessions.Get(req.sessionID)

	// Try each strategy in order
	if result := m.tryPrecomputed(session, req); result != nil {
		body, isCompaction, synthetic, err := m.buildResponse(req, result, true)
		return body, isCompaction, synthetic, nil, err
	}

	if result := m.tryPending(session, req); result != nil {
		body, isCompaction, synthetic, err := m.buildResponse(req, result, true)
		return body, isCompaction, synthetic, nil, err
	}

	result, err := m.doSynchronous(req)
	if err != nil {
		return nil, true, nil, nil, err
	}
	body, isCompaction, synthetic, err := m.buildResponse(req, result, false)
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
func (m *Manager) tryPending(session *Session, req *request) *summaryResult {
	if session == nil || session.State != StatePending {
		return nil
	}

	log.Info().Str("session", req.sessionID).Msg("Waiting for background job")
	if !m.worker.Wait(req.sessionID, m.config.PendingJobTimeout) {
		return nil
	}

	session = m.sessions.Get(req.sessionID)
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
func (m *Manager) doSynchronous(req *request) (*summaryResult, error) {
	log.Info().Str("session", req.sessionID).Msg("Synchronous summarization")
	logCompactionFallback(req.sessionID, req.model)

	ctx, cancel := context.WithTimeout(context.Background(), m.config.SyncTimeout)
	defer cancel()

	result, err := m.summary.Summarize(ctx, SummarizeInput{
		Messages:         req.messages,
		TriggerThreshold: m.config.TriggerThreshold,
		KeepRecentTokens: m.config.Summarizer.KeepRecentTokens,
		KeepRecentCount:  m.config.Summarizer.KeepRecentCount,
		Model:            req.model,
	})
	if err != nil {
		logError(req.sessionID, err)
		return nil, fmt.Errorf("summarization failed: %w", err)
	}

	// Cache for potential reuse
	_ = m.sessions.SetSummaryReady(req.sessionID, result.Summary, result.SummaryTokens, result.LastSummarizedIndex, len(req.messages))

	return &summaryResult{
		summary:   result.Summary,
		tokens:    result.SummaryTokens,
		lastIndex: result.LastSummarizedIndex,
	}, nil
}

// =============================================================================
// RESPONSE BUILDING
// =============================================================================

// buildResponse constructs the compaction response based on provider.
// Anthropic: returns synthetic response (we intercept, no API call)
// OpenAI: returns modified request (forwarded to API with compacted messages)
//
// NOTE: We keep the summary in StateReady after use, allowing multiple compaction
// requests to reuse the same precomputed summary. The summary will be replaced
// when a new preemptive trigger occurs after the conversation continues.
func (m *Manager) buildResponse(req *request, result *summaryResult, wasPrecomputed bool) ([]byte, bool, []byte, error) {
	// Increment use counter but keep summary available (StateReady)
	m.sessions.IncrementUseCount(req.sessionID)
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

// =============================================================================
// BACKGROUND SUMMARIZATION
// =============================================================================

func (m *Manager) triggerIfNeeded(session *Session, req *request, usage float64) {
	if usage < m.config.TriggerThreshold {
		return
	}

	// Only trigger if idle (no summary exists or summary was already used)
	// - StatePending: already summarizing, wait
	// - StateReady: summary exists and hasn't been used yet, keep it
	// - StateIdle: no summary, trigger one
	if session.State != StateIdle {
		return
	}

	log.Info().Str("session", req.sessionID).Float64("usage", usage).Int("messages", len(req.messages)).Msg("Triggering preemptive summarization")
	logPreemptiveTrigger(req.sessionID, req.model, len(req.messages), usage, m.config.TriggerThreshold, m.config.Summarizer.Provider, m.config.Summarizer.Model)
	m.worker.Submit(req.sessionID, req.messages, req.model)
}

// =============================================================================
// HELPERS
// =============================================================================

func (m *Manager) getEffectiveMax(model string) int {
	if m.config.TestContextWindowOverride > 0 {
		return m.config.TestContextWindowOverride
	}
	return GetModelContextWindow(model).EffectiveMax
}

func (m *Manager) buildHeaders(session *Session, usage TokenUsage) map[string]string {
	if !m.config.AddResponseHeaders {
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

func (m *Manager) Stats() map[string]interface{} {
	stats := map[string]interface{}{"enabled": m.enabled}
	if m.enabled && m.sessions != nil {
		stats["sessions"] = m.sessions.Stats()
	}
	if m.enabled && m.worker != nil {
		stats["worker"] = m.worker.Stats()
	}
	return stats
}

// =============================================================================
// INITIALIZATION (side effects isolated)
// =============================================================================

func initLogger(cfg Config) {
	logPath := cfg.CompactionLogPath
	if logPath == "" {
		logPath = cfg.LogDir
	}
	if err := InitCompactionLoggerWithPath(logPath); err != nil {
		log.Warn().Err(err).Msg("Failed to initialize compaction logger")
	}
}

// =============================================================================
// LOGGING (side effects isolated)
// =============================================================================

func logCompactionDetected(sessionID, model string, detection DetectionResult) {
	if l := GetCompactionLogger(); l != nil {
		l.LogCompactionDetected(sessionID, model, detection.DetectedBy, detection.Confidence)
	}
}

func logCompactionApplied(sessionID, model string, wasPrecomputed bool, result *summaryResult) {
	if l := GetCompactionLogger(); l != nil {
		l.LogCompactionApplied(sessionID, model, wasPrecomputed, result.lastIndex+1, result.tokens, 0, nil)
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
