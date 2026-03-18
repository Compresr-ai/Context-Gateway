// Package monitoring - trajectory.go provides simplified trajectory logging in ATIF format.
// Mimics Harbor's clean approach: one trajectory per session, simple data recording.
package monitoring

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// TrajectoryRecorder is a simplified trajectory recorder.
// Handles ONE trajectory per recorder instance - for multi-session management,
// use TrajectoryStore which maps session IDs to individual recorders.
type TrajectoryRecorder struct {
	mu         sync.Mutex
	trajectory *Trajectory
	logPath    string
	closed     bool
	dirty      int // steps added since last flush; flushed when >= flushBatchSize
}

// flushBatchSize is the number of new steps that triggers an automatic flush to disk.
const flushBatchSize = 10

// TrajectoryRecorderConfig contains configuration for the recorder.
type TrajectoryRecorderConfig struct {
	LogPath   string // Path to trajectory.json file
	SessionID string // Unique session identifier (generates UUID if empty)
	AgentName string // Agent name (e.g., "claude-code")
	Version   string // Agent version (defaults to "1.0.0")
}

// NewTrajectoryRecorder creates a new trajectory recorder.
func NewTrajectoryRecorder(cfg TrajectoryRecorderConfig) (*TrajectoryRecorder, error) {
	// Generate session ID if not provided
	sessionID := cfg.SessionID
	if sessionID == "" {
		sessionID = uuid.New().String()[:16] // Short UUID for readability
	}

	// Default values
	agentName := cfg.AgentName
	if agentName == "" {
		agentName = "context-gateway"
	}
	version := cfg.Version
	if version == "" {
		version = "1.0.0"
	}

	// Create trajectory
	traj := &Trajectory{
		SchemaVersion: "ATIF-v1.6",
		SessionID:     sessionID,
		Agent: Agent{
			Name:    agentName,
			Version: version,
		},
		Steps: make([]Step, 0),
	}

	// Ensure directory exists
	if cfg.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.LogPath), 0750); err != nil {
			return nil, fmt.Errorf("create log directory: %w", err)
		}
	}

	return &TrajectoryRecorder{
		trajectory: traj,
		logPath:    cfg.LogPath,
	}, nil
}

// SessionID returns the trajectory's session ID.
func (r *TrajectoryRecorder) SessionID() string {
	if r == nil || r.trajectory == nil {
		return ""
	}
	return r.trajectory.SessionID
}

// SetModel sets the default model name for the agent.
func (r *TrajectoryRecorder) SetModel(model string) {
	if r == nil || r.trajectory == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trajectory.Agent.ModelName = model
}

// RecordUserTurn records a user message and immediately following agent response.
// This is the primary recording method - represents one complete turn.
// Call this once per user turn, not per LLM request.
func (r *TrajectoryRecorder) RecordUserTurn(user UserTurnData, agent AgentTurnData) error {
	if r == nil || r.trajectory == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return fmt.Errorf("recorder closed")
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Add user step
	userStep := Step{
		StepID:    len(r.trajectory.Steps) + 1,
		Timestamp: now,
		Source:    StepSourceUser,
		Message:   user.Message,
	}
	r.trajectory.Steps = append(r.trajectory.Steps, userStep)

	// Add agent step
	agentStep := Step{
		StepID:           len(r.trajectory.Steps) + 1,
		Timestamp:        now,
		Source:           StepSourceAgent,
		Message:          agent.Message,
		ModelName:        agent.Model,
		ReasoningContent: agent.Reasoning,
	}

	// Add tool calls
	if len(agent.ToolCalls) > 0 {
		agentStep.ToolCalls = agent.ToolCalls
	}

	// Add observations (tool results)
	if len(agent.Observations) > 0 {
		agentStep.Observation = &Observation{
			Results: agent.Observations,
		}
	}

	// Add metrics
	if agent.PromptTokens > 0 || agent.CompletionTokens > 0 {
		agentStep.Metrics = &Metrics{
			PromptTokens:     agent.PromptTokens,
			CompletionTokens: agent.CompletionTokens,
			CachedTokens:     agent.CachedTokens,
			CostUSD:          agent.CostUSD,
		}
	}

	// Add proxy interaction if provided
	if agent.ProxyInfo != nil {
		agentStep.ProxyInteraction = agent.ProxyInfo
	}

	r.trajectory.Steps = append(r.trajectory.Steps, agentStep)

	// Batch flush: write to disk every flushBatchSize steps.
	r.batchFlushLocked()

	return nil
}

// AccumulateToolCalls adds tool calls to the last agent step.
// Use this for tool-loop iterations where multiple LLM calls happen per user turn.
func (r *TrajectoryRecorder) AccumulateToolCalls(toolCalls []ToolCall, observations []ObservationResult, metrics *Metrics) {
	if r == nil || r.trajectory == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed || len(r.trajectory.Steps) == 0 {
		return
	}

	// Find last agent step
	for i := len(r.trajectory.Steps) - 1; i >= 0; i-- {
		step := &r.trajectory.Steps[i]
		if step.Source != StepSourceAgent {
			continue
		}

		// Append tool calls (deduplicate by ID)
		if len(toolCalls) > 0 {
			existing := make(map[string]bool)
			for _, tc := range step.ToolCalls {
				existing[tc.ToolCallID] = true
			}
			for _, tc := range toolCalls {
				if !existing[tc.ToolCallID] {
					step.ToolCalls = append(step.ToolCalls, tc)
				}
			}
		}

		// Append observations
		if len(observations) > 0 {
			if step.Observation == nil {
				step.Observation = &Observation{Results: make([]ObservationResult, 0)}
			}
			step.Observation.Results = append(step.Observation.Results, observations...)
		}

		// Accumulate metrics
		if metrics != nil {
			if step.Metrics == nil {
				step.Metrics = &Metrics{}
			}
			step.Metrics.PromptTokens += metrics.PromptTokens
			step.Metrics.CompletionTokens += metrics.CompletionTokens
			step.Metrics.CachedTokens += metrics.CachedTokens
			step.Metrics.CostUSD += metrics.CostUSD
		}

		r.batchFlushLocked()
		return
	}
}

// UpdateLastAgentMessage updates the message of the last agent step.
// Use this when streaming completes and final message is available.
func (r *TrajectoryRecorder) UpdateLastAgentMessage(message string) {
	if r == nil || r.trajectory == nil || message == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return
	}

	for i := len(r.trajectory.Steps) - 1; i >= 0; i-- {
		step := &r.trajectory.Steps[i]
		if step.Source == StepSourceAgent {
			step.Message = message
			r.batchFlushLocked()
			return
		}
	}
}

// RecordSystemMessage records a system message step.
func (r *TrajectoryRecorder) RecordSystemMessage(message string) {
	if r == nil || r.trajectory == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return
	}

	step := Step{
		StepID:    len(r.trajectory.Steps) + 1,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Source:    StepSourceSystem,
		Message:   message,
	}
	r.trajectory.Steps = append(r.trajectory.Steps, step)
	r.batchFlushLocked()
}

// AddNote appends a note to the trajectory.
func (r *TrajectoryRecorder) AddNote(note string) {
	if r == nil || r.trajectory == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.trajectory.Notes != "" {
		r.trajectory.Notes += "\n"
	}
	r.trajectory.Notes += note
}

// Validate checks the trajectory for ATIF compliance.
// Returns nil if valid, error describing the issue otherwise.
func (r *TrajectoryRecorder) Validate() error {
	if r == nil || r.trajectory == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.validateLocked()
}

func (r *TrajectoryRecorder) validateLocked() error {
	t := r.trajectory

	// Validate required top-level fields
	if t.SchemaVersion == "" {
		return fmt.Errorf("schema_version is required")
	}
	if t.SessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	if t.Agent.Name == "" {
		return fmt.Errorf("agent.name is required")
	}

	// Validate step IDs are sequential starting from 1
	for i, step := range t.Steps {
		expected := i + 1
		if step.StepID != expected {
			return fmt.Errorf("step %d: expected step_id %d, got %d", i, expected, step.StepID)
		}
	}

	// Validate tool calls have required fields
	for _, step := range t.Steps {
		for j, tc := range step.ToolCalls {
			if tc.ToolCallID == "" {
				return fmt.Errorf("step %d: tool_call[%d] tool_call_id cannot be empty", step.StepID, j)
			}
			if tc.FunctionName == "" {
				return fmt.Errorf("step %d: tool_call[%d] function_name cannot be empty", step.StepID, j)
			}
		}
	}

	// Validate observation source_call_ids reference valid tool_call_ids
	for _, step := range t.Steps {
		if step.Observation == nil {
			continue
		}

		toolCallIDs := make(map[string]bool)
		for _, tc := range step.ToolCalls {
			toolCallIDs[tc.ToolCallID] = true
		}

		for _, result := range step.Observation.Results {
			if result.SourceCallID != "" && !toolCallIDs[result.SourceCallID] {
				return fmt.Errorf("step %d: observation references unknown tool_call_id %q",
					step.StepID, result.SourceCallID)
			}
		}
	}

	// Validate agent-only fields
	for _, step := range t.Steps {
		if step.Source == StepSourceAgent {
			continue
		}
		if step.ModelName != "" {
			return fmt.Errorf("step %d: model_name only valid for agent steps", step.StepID)
		}
		if len(step.ToolCalls) > 0 {
			return fmt.Errorf("step %d: tool_calls only valid for agent steps", step.StepID)
		}
		if step.Metrics != nil {
			return fmt.Errorf("step %d: metrics only valid for agent steps", step.StepID)
		}
	}

	return nil
}

// batchFlushLocked increments the dirty counter and flushes to disk only when
// flushBatchSize steps have accumulated, deferring most disk writes. Must hold mu.
func (r *TrajectoryRecorder) batchFlushLocked() {
	r.dirty++
	if r.dirty >= flushBatchSize {
		r.dirty = 0
		r.flushLocked()
	}
}

// flushLocked writes the trajectory to disk. Must hold mutex.
func (r *TrajectoryRecorder) flushLocked() {
	if r.logPath == "" || len(r.trajectory.Steps) == 0 {
		return
	}

	// Compute final metrics
	r.trajectory.ComputeFinalMetrics()

	data, err := json.MarshalIndent(r.trajectory, "", "  ")
	if err != nil {
		log.Error().Err(err).Msg("trajectory: marshal failed")
		return
	}

	if err := os.WriteFile(r.logPath, data, 0600); err != nil {
		log.Error().Err(err).Str("path", r.logPath).Msg("trajectory: write failed")
	}
}

// Close finalizes and writes the trajectory.
func (r *TrajectoryRecorder) Close() error {
	if r == nil || r.trajectory == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil
	}
	r.closed = true
	r.dirty = 0 // reset so flushLocked always writes on Close

	if len(r.trajectory.Steps) == 0 {
		log.Debug().Msg("trajectory: no steps recorded")
		return nil
	}

	// Validate before final write
	if err := r.validateLocked(); err != nil {
		log.Warn().Err(err).Msg("trajectory: validation warning")
	}

	r.trajectory.ComputeFinalMetrics()
	r.flushLocked()

	log.Info().
		Str("session_id", r.trajectory.SessionID).
		Str("path", r.logPath).
		Int("steps", len(r.trajectory.Steps)).
		Msg("trajectory: saved")

	return nil
}

// GetStepCount returns the number of recorded steps.
func (r *TrajectoryRecorder) GetStepCount() int {
	if r == nil || r.trajectory == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.trajectory.Steps)
}

// DATA TYPES for recording

// UserTurnData contains data for a user turn.
type UserTurnData struct {
	Message string
}

// AgentTurnData contains data for an agent turn.
type AgentTurnData struct {
	Message          string
	Model            string
	Reasoning        string
	ToolCalls        []ToolCall
	Observations     []ObservationResult
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	CostUSD          float64
	ProxyInfo        *ProxyInteraction
}

// ============================================================================
// TrajectoryStore - Multi-session management (gateway integration layer)
// ============================================================================

// trajectorySessionTTL is the TTL for inactive trajectory sessions.
// After this duration of inactivity, sessions are closed and removed.
const trajectorySessionTTL = 1 * time.Hour

// TrajectoryStore manages multiple trajectory recorders, one per session.
// This is the top-level interface for the gateway.
type TrajectoryStore struct {
	mu           sync.RWMutex
	recorders    map[string]*TrajectoryRecorder
	lastActive   map[string]time.Time // Track last activity per session
	baseDir      string
	agentName    string
	version      string
	enabled      bool
	mainSessions map[string]bool // Track main sessions (vs subagents)
	stopCh       chan struct{}   // Signal cleanup goroutine to stop
}

// TrajectoryStoreConfig contains configuration for the store.
type TrajectoryStoreConfig struct {
	Enabled   bool
	BaseDir   string // Directory for trajectory files
	AgentName string
	Version   string
}

// NewTrajectoryStore creates a new trajectory store.
func NewTrajectoryStore(cfg TrajectoryStoreConfig) *TrajectoryStore {
	if !cfg.Enabled || cfg.BaseDir == "" {
		return &TrajectoryStore{enabled: false}
	}

	agentName := cfg.AgentName
	if agentName == "" {
		agentName = "context-gateway"
	}

	store := &TrajectoryStore{
		recorders:    make(map[string]*TrajectoryRecorder),
		lastActive:   make(map[string]time.Time),
		baseDir:      cfg.BaseDir,
		agentName:    agentName,
		version:      cfg.Version,
		enabled:      true,
		mainSessions: make(map[string]bool),
		stopCh:       make(chan struct{}),
	}

	// Start cleanup goroutine to prevent memory leaks
	go store.cleanup()

	return store
}

// Enabled returns whether trajectory recording is enabled.
func (s *TrajectoryStore) Enabled() bool {
	return s != nil && s.enabled
}

// MarkMainSession marks a session as the main agent session.
func (s *TrajectoryStore) MarkMainSession(sessionID string) {
	if s == nil || !s.enabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mainSessions[sessionID] = true
}

// IsMainSession returns whether the session is a main agent session.
func (s *TrajectoryStore) IsMainSession(sessionID string) bool {
	if s == nil || !s.enabled {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mainSessions[sessionID]
}

// SetAgentModel sets the model name for a session's recorder.
func (s *TrajectoryStore) SetAgentModel(sessionID, model string) {
	r := s.getOrCreate(sessionID)
	if r != nil {
		r.SetModel(model)
	}
}

// getOrCreate returns the recorder for a session, creating if needed.
func (s *TrajectoryStore) getOrCreate(sessionID string) *TrajectoryRecorder {
	if s == nil || !s.enabled || sessionID == "" {
		return nil
	}

	s.mu.RLock()
	r, exists := s.recorders[sessionID]
	s.mu.RUnlock()

	if exists {
		// Update last active time
		s.mu.Lock()
		s.lastActive[sessionID] = time.Now()
		s.mu.Unlock()
		return r
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check after acquiring write lock
	if r, exists = s.recorders[sessionID]; exists {
		s.lastActive[sessionID] = time.Now()
		return r
	}

	// Create new recorder for this session
	logPath := filepath.Join(s.baseDir, fmt.Sprintf("trajectory_%s.json", sessionID))
	cfg := TrajectoryRecorderConfig{
		LogPath:   logPath,
		SessionID: sessionID,
		AgentName: s.agentName,
		Version:   s.version,
	}

	recorder, err := NewTrajectoryRecorder(cfg)
	if err != nil {
		log.Error().Err(err).Str("session", sessionID).Msg("trajectory: failed to create recorder")
		return nil
	}

	s.recorders[sessionID] = recorder
	s.lastActive[sessionID] = time.Now()
	return recorder
}

// RecordUserMessage records a user message step for a session.
// For ATIF compliance, call this followed by RecordAgentResponse for a complete turn.
func (s *TrajectoryStore) RecordUserMessage(sessionID, message string) {
	r := s.getOrCreate(sessionID)
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return
	}

	step := Step{
		StepID:    len(r.trajectory.Steps) + 1,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Source:    StepSourceUser,
		Message:   message,
	}
	r.trajectory.Steps = append(r.trajectory.Steps, step)
	r.batchFlushLocked()
}

// RecordAgentResponse records an agent response step for a session.
func (s *TrajectoryStore) RecordAgentResponse(sessionID string, data AgentResponseData) {
	r := s.getOrCreate(sessionID)
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return
	}

	step := Step{
		StepID:    len(r.trajectory.Steps) + 1,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Source:    StepSourceAgent,
		Message:   data.Message,
		ModelName: data.Model,
	}

	if len(data.ToolCalls) > 0 {
		step.ToolCalls = data.ToolCalls
	}

	if data.PromptTokens > 0 || data.CompletionTokens > 0 {
		step.Metrics = &Metrics{
			PromptTokens:     data.PromptTokens,
			CompletionTokens: data.CompletionTokens,
		}
	}

	r.trajectory.Steps = append(r.trajectory.Steps, step)
	r.batchFlushLocked()
}

// AccumulateAgentResponse accumulates tool calls to the last agent step.
func (s *TrajectoryStore) AccumulateAgentResponse(sessionID string, data AgentResponseData) {
	r := s.getOrCreate(sessionID)
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed || len(r.trajectory.Steps) == 0 {
		return
	}

	// Find last agent step
	for i := len(r.trajectory.Steps) - 1; i >= 0; i-- {
		step := &r.trajectory.Steps[i]
		if step.Source != StepSourceAgent {
			continue
		}

		// Append tool calls (deduplicate by ID)
		if len(data.ToolCalls) > 0 {
			existing := make(map[string]bool)
			for _, tc := range step.ToolCalls {
				existing[tc.ToolCallID] = true
			}
			for _, tc := range data.ToolCalls {
				if !existing[tc.ToolCallID] {
					step.ToolCalls = append(step.ToolCalls, tc)
				}
			}
		}

		// Update message if non-empty
		if data.Message != "" {
			step.Message = data.Message
		}

		// Accumulate metrics
		if data.PromptTokens > 0 || data.CompletionTokens > 0 {
			if step.Metrics == nil {
				step.Metrics = &Metrics{}
			}
			step.Metrics.PromptTokens += data.PromptTokens
			step.Metrics.CompletionTokens += data.CompletionTokens
		}

		r.batchFlushLocked()
		return
	}
}

// RecordProxyInteraction records compression metadata for the current agent step.
func (s *TrajectoryStore) RecordProxyInteraction(sessionID string, data ProxyInteractionData) {
	r := s.getOrCreate(sessionID)
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed || len(r.trajectory.Steps) == 0 {
		return
	}

	// Find last agent step and add proxy interaction
	for i := len(r.trajectory.Steps) - 1; i >= 0; i-- {
		step := &r.trajectory.Steps[i]
		if step.Source != StepSourceAgent {
			continue
		}

		now := time.Now().UTC().Format(time.RFC3339)

		step.ProxyInteraction = &ProxyInteraction{
			PipeType:     data.PipeType,
			PipeStrategy: data.PipeStrategy,
			ClientToProxy: &ProxyMessage{
				Timestamp:    now,
				TokenCount:   data.ClientTokens,
				MessageCount: data.ClientMsgCount,
			},
			ProxyToLLM: &ProxyMessage{
				Timestamp:    now,
				TokenCount:   data.CompressedTokens,
				MessageCount: data.CompressedMsgCount,
			},
			LLMToProxy: &ProxyMessage{
				Timestamp:  now,
				TokenCount: data.ResponseTokens,
			},
		}

		// Add compression info if compression was applied
		if data.CompressionEnabled && data.ClientTokens > 0 {
			ratio := 0.0
			if data.ClientTokens > 0 {
				ratio = 1.0 - float64(data.CompressedTokens)/float64(data.ClientTokens)
			}
			step.ProxyInteraction.Compression = &ProxyCompressionInfo{
				Enabled:          true,
				OriginalTokens:   data.ClientTokens,
				CompressedTokens: data.CompressedTokens,
				CompressionRatio: ratio,
				ToolCompressions: data.ToolCompressions,
			}
		}

		r.batchFlushLocked()
		return
	}
}

// Close closes all recorders and writes final trajectories.
func (s *TrajectoryStore) Close() error {
	if s == nil || !s.enabled {
		return nil
	}

	// Signal cleanup goroutine to stop
	select {
	case <-s.stopCh:
		// Already closed
	default:
		close(s.stopCh)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for sessionID, r := range s.recorders {
		if err := r.Close(); err != nil {
			log.Error().Err(err).Str("session", sessionID).Msg("trajectory: close failed")
		}
	}

	// Clear maps to free memory
	s.recorders = make(map[string]*TrajectoryRecorder)
	s.lastActive = make(map[string]time.Time)
	s.mainSessions = make(map[string]bool)

	return nil
}

// cleanup periodically removes inactive sessions to prevent memory leaks.
func (s *TrajectoryStore) cleanup() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.cleanupStale()
		}
	}
}

// cleanupStale removes sessions that have been inactive for longer than trajectorySessionTTL.
func (s *TrajectoryStore) cleanupStale() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	stale := make([]string, 0)

	for sessionID, lastActive := range s.lastActive {
		if now.Sub(lastActive) > trajectorySessionTTL {
			stale = append(stale, sessionID)
		}
	}

	for _, sessionID := range stale {
		if r, exists := s.recorders[sessionID]; exists {
			if err := r.Close(); err != nil {
				log.Error().Err(err).Str("session", sessionID).Msg("trajectory: cleanup close failed")
			}
			delete(s.recorders, sessionID)
		}
		delete(s.lastActive, sessionID)
		delete(s.mainSessions, sessionID)
		log.Debug().Str("session", sessionID).Msg("trajectory: cleaned up stale session")
	}

	if len(stale) > 0 {
		log.Info().Int("cleaned", len(stale)).Int("remaining", len(s.recorders)).Msg("trajectory: cleanup complete")
	}
}

// GetSessionCount returns the number of active sessions.
func (s *TrajectoryStore) GetSessionCount() int {
	if s == nil || !s.enabled {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.recorders)
}

// AgentResponseData contains data for an agent response (store interface).
type AgentResponseData struct {
	Message          string
	Model            string
	ToolCalls        []ToolCall
	PromptTokens     int
	CompletionTokens int
}

// ProxyInteractionData contains proxy compression metadata (store interface).
type ProxyInteractionData struct {
	PipeType           string
	PipeStrategy       string
	ClientTokens       int
	CompressedTokens   int
	ClientMsgCount     int
	CompressedMsgCount int
	CompressionEnabled bool
	ToolCompressions   []ToolCompressionEntry
	ResponseTokens     int
}
