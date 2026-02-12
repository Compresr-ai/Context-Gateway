// Package monitoring - trajectory.go provides trajectory logging in ATIF format.
//
// DESIGN: TrajectoryTracker maintains a session-long trajectory of agent interactions.
// Writes each step to a JSONL file (one step per line) for real-time logging.
// Each line contains the step data plus session context for grouping.
//
// Usage:
//  1. Create tracker with NewTrajectoryTracker()
//  2. Record interactions via RecordUserMessage(), RecordAgentResponse(), etc.
//  3. Each record operation appends the step to the JSONL file immediately
//  4. Call Close() on shutdown for final summary
//
// Thread Safety: All methods are safe for concurrent use.
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

// TrajectoryConfig contains trajectory logging configuration.
type TrajectoryConfig struct {
	Enabled   bool   // Enable trajectory logging
	LogPath   string // Path to trajectory.json file
	AgentName string // Agent name (e.g., "claude-code")
	SessionID string // Optional: use this session ID instead of generating UUID
}

// TrajectoryTracker manages trajectory recording for a session.
type TrajectoryTracker struct {
	config     TrajectoryConfig
	trajectory *Trajectory
	mu         sync.Mutex
	closed     bool
}

// NewTrajectoryTracker creates a new trajectory tracker.
func NewTrajectoryTracker(cfg TrajectoryConfig) (*TrajectoryTracker, error) {
	t := &TrajectoryTracker{
		config: cfg,
	}

	if !cfg.Enabled {
		return t, nil
	}

	// Use provided session ID or generate a new UUID
	sessionID := cfg.SessionID
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	// Create trajectory
	agentName := cfg.AgentName
	if agentName == "" {
		agentName = "context-gateway"
	}
	t.trajectory = NewTrajectory(sessionID, agentName, "1.0.0")

	// Ensure directory exists
	if cfg.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.LogPath), 0750); err != nil {
			return nil, err
		}
	}

	log.Info().
		Str("session_id", sessionID).
		Str("path", cfg.LogPath).
		Msg("trajectory tracking enabled")

	return t, nil
}

// RecordUserMessage records a user message in the trajectory.
func (t *TrajectoryTracker) RecordUserMessage(message string) {
	if !t.config.Enabled || t.trajectory == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return
	}

	step := NewUserStep(message)
	t.trajectory.AddStep(step)

	// Flush to disk immediately
	t.flushLocked()

	log.Debug().
		Int("step_id", len(t.trajectory.Steps)).
		Str("source", "user").
		Msg("trajectory: recorded user message")
}

// RecordAgentResponse records an agent response with tool calls and metrics.
func (t *TrajectoryTracker) RecordAgentResponse(response AgentResponseData) {
	if !t.config.Enabled || t.trajectory == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return
	}

	step := NewAgentStep(response.Message, response.Model)
	step.ReasoningContent = response.Reasoning

	// Add tool calls if present
	if len(response.ToolCalls) > 0 {
		step.ToolCalls = response.ToolCalls
	}

	// Add metrics if present
	if response.PromptTokens > 0 || response.CompletionTokens > 0 {
		step.Metrics = &Metrics{
			PromptTokens:     response.PromptTokens,
			CompletionTokens: response.CompletionTokens,
			CachedTokens:     response.CachedTokens,
			CostUSD:          response.CostUSD,
		}
	}

	t.trajectory.AddStep(step)

	// Flush to disk immediately
	t.flushLocked()

	log.Debug().
		Int("step_id", len(t.trajectory.Steps)).
		Str("source", "agent").
		Str("model", response.Model).
		Int("tool_calls", len(response.ToolCalls)).
		Msg("trajectory: recorded agent response")
}

// RecordToolResult records the result of a tool execution.
func (t *TrajectoryTracker) RecordToolResult(toolCallID string, content string) {
	if !t.config.Enabled || t.trajectory == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return
	}

	// Find the last agent step and add observation
	for i := len(t.trajectory.Steps) - 1; i >= 0; i-- {
		step := &t.trajectory.Steps[i]
		if step.Source == StepSourceAgent && len(step.ToolCalls) > 0 {
			// Check if this step has the matching tool call
			for _, tc := range step.ToolCalls {
				if tc.ToolCallID == toolCallID {
					// Add or update observation
					if step.Observation == nil {
						step.Observation = &Observation{
							Results: make([]ObservationResult, 0),
						}
					}
					step.Observation.Results = append(step.Observation.Results, ObservationResult{
						SourceCallID: toolCallID,
						Content:      content,
					})
					// Flush to disk immediately
					t.flushLocked()
					return
				}
			}
		}
	}

	// If no matching step found, log warning
	log.Warn().
		Str("tool_call_id", toolCallID).
		Msg("trajectory: no matching tool call found for result")
}

// RecordSystemMessage records a system message (e.g., system prompt).
func (t *TrajectoryTracker) RecordSystemMessage(message string) {
	if !t.config.Enabled || t.trajectory == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return
	}

	step := NewSystemStep(message)
	t.trajectory.AddStep(step)

	// Flush to disk immediately
	t.flushLocked()

	log.Debug().
		Int("step_id", len(t.trajectory.Steps)).
		Str("source", "system").
		Msg("trajectory: recorded system message")
}

// AgentResponseData contains data for recording an agent response.
type AgentResponseData struct {
	Message          string
	Model            string
	Reasoning        string
	ToolCalls        []ToolCall
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	CostUSD          float64
}

// GetSessionID returns the current session ID.
func (t *TrajectoryTracker) GetSessionID() string {
	if t.trajectory == nil {
		return ""
	}
	return t.trajectory.SessionID
}

// GetStepCount returns the number of steps recorded.
func (t *TrajectoryTracker) GetStepCount() int {
	if t.trajectory == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.trajectory.Steps)
}

// SetAgentModel sets the default model name for the agent.
func (t *TrajectoryTracker) SetAgentModel(model string) {
	if t.trajectory == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.trajectory.Agent.ModelName = model
}

// AddNote appends a note to the trajectory.
func (t *TrajectoryTracker) AddNote(note string) {
	if t.trajectory == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.trajectory.Notes != "" {
		t.trajectory.Notes += "\n"
	}
	t.trajectory.Notes += note
}

// =============================================================================
// SESSION METRICS - Track aggregate metrics for summary
// =============================================================================

// flushLocked writes the current trajectory to disk. Must be called with lock held.
func (t *TrajectoryTracker) flushLocked() {
	if t.config.LogPath == "" || len(t.trajectory.Steps) == 0 {
		return
	}

	// Compute metrics before writing
	t.trajectory.ComputeFinalMetrics()

	// Serialize to JSON
	data, err := t.trajectory.ToJSON()
	if err != nil {
		log.Error().Err(err).Msg("trajectory: failed to serialize for flush")
		return
	}

	// Write to file
	if err := os.WriteFile(t.config.LogPath, data, 0600); err != nil {
		log.Error().Err(err).Str("path", t.config.LogPath).Msg("trajectory: failed to flush")
	}
}

// Close finalizes and writes the trajectory to disk.
func (t *TrajectoryTracker) Close() error {
	if !t.config.Enabled || t.trajectory == nil {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}
	t.closed = true

	// Skip if no steps recorded
	if len(t.trajectory.Steps) == 0 {
		log.Info().Msg("trajectory: no steps recorded, skipping write")
		return nil
	}

	// Compute final metrics
	t.trajectory.ComputeFinalMetrics()

	// Serialize to JSON
	data, err := t.trajectory.ToJSON()
	if err != nil {
		log.Error().Err(err).Msg("trajectory: failed to serialize")
		return err
	}

	// Write trajectory to file
	if t.config.LogPath != "" {
		if err := os.WriteFile(t.config.LogPath, data, 0600); err != nil {
			log.Error().Err(err).Str("path", t.config.LogPath).Msg("trajectory: failed to write")
			return err
		}
	}

	log.Info().
		Str("session_id", t.trajectory.SessionID).
		Str("path", t.config.LogPath).
		Int("steps", len(t.trajectory.Steps)).
		Int("total_prompt_tokens", t.trajectory.FinalMetrics.TotalPromptTokens).
		Int("total_completion_tokens", t.trajectory.FinalMetrics.TotalCompletionTokens).
		Msg("trajectory: saved")

	return nil
}

// Enabled returns whether trajectory tracking is enabled.
func (t *TrajectoryTracker) Enabled() bool {
	return t.config.Enabled && t.trajectory != nil
}

// =============================================================================
// HELPERS - Extract data from OpenAI-style messages
// =============================================================================

// ExtractToolCallsFromResponse extracts tool calls from an OpenAI API response.
func ExtractToolCallsFromResponse(choices []map[string]any) []ToolCall {
	var toolCalls []ToolCall

	for _, choice := range choices {
		msg, ok := choice["message"].(map[string]any)
		if !ok {
			continue
		}

		tcs, ok := msg["tool_calls"].([]any)
		if !ok {
			continue
		}

		for _, tc := range tcs {
			tcMap, ok := tc.(map[string]any)
			if !ok {
				continue
			}

			toolCall := ToolCall{}

			if id, ok := tcMap["id"].(string); ok {
				toolCall.ToolCallID = id
			}

			if fn, ok := tcMap["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					toolCall.FunctionName = name
				}
				if args, ok := fn["arguments"].(string); ok {
					// Parse arguments JSON string
					var argsMap map[string]any
					if err := json.Unmarshal([]byte(args), &argsMap); err == nil {
						toolCall.Arguments = argsMap
					} else {
						toolCall.Arguments = args // Keep as string if parse fails
					}
				}
			}

			if toolCall.ToolCallID != "" && toolCall.FunctionName != "" {
				toolCalls = append(toolCalls, toolCall)
			}
		}
	}

	return toolCalls
}

// ExtractContentFromResponse extracts the assistant message content.
func ExtractContentFromResponse(choices []map[string]any) string {
	for _, choice := range choices {
		msg, ok := choice["message"].(map[string]any)
		if !ok {
			continue
		}
		if content, ok := msg["content"].(string); ok {
			return content
		}
	}
	return ""
}

// ExtractUsageFromResponse extracts token usage from an OpenAI API response.
func ExtractUsageFromResponse(usage map[string]any) (prompt, completion, cached int) {
	if usage == nil {
		return 0, 0, 0
	}

	if p, ok := usage["prompt_tokens"].(float64); ok {
		prompt = int(p)
	}
	if c, ok := usage["completion_tokens"].(float64); ok {
		completion = int(c)
	}
	// Cached tokens might be in different locations depending on provider
	if cache, ok := usage["prompt_tokens_details"].(map[string]any); ok {
		if ct, ok := cache["cached_tokens"].(float64); ok {
			cached = int(ct)
		}
	}

	return prompt, completion, cached
}

// ExtractUserMessages extracts user message content from request messages.
func ExtractUserMessages(messages []map[string]any) []string {
	var userMessages []string
	for _, msg := range messages {
		role, ok := msg["role"].(string)
		if !ok || role != "user" {
			continue
		}
		if content, ok := msg["content"].(string); ok {
			userMessages = append(userMessages, content)
		}
	}
	return userMessages
}

// ExtractLastUserMessage gets the last user message from request.
func ExtractLastUserMessage(messages []map[string]any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		role, ok := msg["role"].(string)
		if !ok || role != "user" {
			continue
		}
		if content, ok := msg["content"].(string); ok {
			return content
		}
	}
	return ""
}

// TrackingStartTime returns the time when the tracker was created.
func (t *TrajectoryTracker) TrackingStartTime() time.Time {
	if t.trajectory == nil || len(t.trajectory.Steps) == 0 {
		return time.Now()
	}
	// Parse first step timestamp
	ts, err := time.Parse(time.RFC3339, t.trajectory.Steps[0].Timestamp)
	if err != nil {
		return time.Now()
	}
	return ts
}

// =============================================================================
// PROXY INTERACTION RECORDING
// =============================================================================

// ProxyInteractionData contains data for recording a proxy interaction.
type ProxyInteractionData struct {
	// Pipeline info
	PipeType     string // Which pipe was used: passthrough, tool_output, tool_discovery
	PipeStrategy string // How it was processed: passthrough, api, llm

	// Request messages
	ClientMessages     []any // Original messages from client
	CompressedMessages []any // Messages after compression (sent to LLM)

	// Token counts
	ClientTokens     int // Tokens in original request
	CompressedTokens int // Tokens in compressed request

	// Compression details
	CompressionEnabled bool
	ToolCompressions   []ToolCompressionEntry // Individual tool compression details

	// LLM response
	ResponseMessages []any // Response from LLM
	ResponseTokens   int   // Tokens in response
}

// RecordProxyInteraction records the full proxy flow for the last agent step.
// Call this after RecordAgentResponse to add proxy interaction details.
func (t *TrajectoryTracker) RecordProxyInteraction(data ProxyInteractionData) {
	if !t.config.Enabled || t.trajectory == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed || len(t.trajectory.Steps) == 0 {
		return
	}

	// Find the last agent step
	for i := len(t.trajectory.Steps) - 1; i >= 0; i-- {
		step := &t.trajectory.Steps[i]
		if step.Source == StepSourceAgent {
			now := time.Now().UTC().Format(time.RFC3339)

			step.ProxyInteraction = &ProxyInteraction{
				PipeType:     data.PipeType,
				PipeStrategy: data.PipeStrategy,
				ClientToProxy: &ProxyMessage{
					Timestamp:  now,
					Messages:   data.ClientMessages,
					TokenCount: data.ClientTokens,
				},
				ProxyToLLM: &ProxyMessage{
					Timestamp:  now,
					Messages:   data.CompressedMessages,
					TokenCount: data.CompressedTokens,
				},
				LLMToProxy: &ProxyMessage{
					Timestamp:  now,
					Messages:   data.ResponseMessages,
					TokenCount: data.ResponseTokens,
				},
			}

			// Add compression info if compression was applied
			if data.CompressionEnabled && data.ClientTokens > 0 {
				tokensSaved := data.ClientTokens - data.CompressedTokens
				ratio := float64(data.CompressedTokens) / float64(data.ClientTokens)

				step.ProxyInteraction.Compression = &ProxyCompressionInfo{
					Enabled:          true,
					OriginalTokens:   data.ClientTokens,
					CompressedTokens: data.CompressedTokens,
					TokensSaved:      tokensSaved,
					CompressionRatio: ratio,
					ToolCompressions: data.ToolCompressions,
				}
			}

			log.Debug().
				Int("step_id", step.StepID).
				Str("pipe_type", data.PipeType).
				Str("pipe_strategy", data.PipeStrategy).
				Int("client_tokens", data.ClientTokens).
				Int("compressed_tokens", data.CompressedTokens).
				Bool("compression", data.CompressionEnabled).
				Int("tool_compressions", len(data.ToolCompressions)).
				Msg("trajectory: recorded proxy interaction")

			// Flush to disk immediately
			t.flushLocked()
			return
		}
	}

	log.Warn().Msg("trajectory: no agent step found for proxy interaction")
}

// =============================================================================
// TRAJECTORY MANAGER - Multiple Trackers by Session ID
// =============================================================================

// TrajectoryManagerConfig contains configuration for the trajectory manager.
type TrajectoryManagerConfig struct {
	Enabled   bool   // Enable trajectory logging
	BaseDir   string // Base directory for trajectory files (e.g., "logs/session_1/")
	AgentName string // Agent name (e.g., "claude-code")
}

// TrajectoryManager manages multiple TrajectoryTrackers by session ID.
// Each unique session ID gets its own trajectory file: trajectory_<sessionID>.json
type TrajectoryManager struct {
	config   TrajectoryManagerConfig
	trackers map[string]*TrajectoryTracker
	mu       sync.RWMutex
}

// NewTrajectoryManager creates a new trajectory manager.
func NewTrajectoryManager(cfg TrajectoryManagerConfig) *TrajectoryManager {
	return &TrajectoryManager{
		config:   cfg,
		trackers: make(map[string]*TrajectoryTracker),
	}
}

// Enabled returns whether trajectory tracking is enabled.
func (m *TrajectoryManager) Enabled() bool {
	return m.config.Enabled
}

// getOrCreateTracker returns existing tracker or creates a new one for the session.
func (m *TrajectoryManager) getOrCreateTracker(sessionID string) *TrajectoryTracker {
	if !m.config.Enabled || sessionID == "" {
		return nil
	}

	// Fast path: check with read lock
	m.mu.RLock()
	tracker, exists := m.trackers[sessionID]
	m.mu.RUnlock()

	if exists {
		return tracker
	}

	// Slow path: create with write lock
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if tracker, exists = m.trackers[sessionID]; exists {
		return tracker
	}

	// Create new tracker with session-specific file path
	logPath := filepath.Join(m.config.BaseDir, fmt.Sprintf("trajectory_%s.json", sessionID))
	tracker, err := NewTrajectoryTracker(TrajectoryConfig{
		Enabled:   true,
		LogPath:   logPath,
		AgentName: m.config.AgentName,
		SessionID: sessionID, // Use the preemptive session ID
	})
	if err != nil {
		log.Error().Err(err).Str("session_id", sessionID).Msg("trajectory manager: failed to create tracker")
		return nil
	}

	m.trackers[sessionID] = tracker
	log.Info().
		Str("session_id", sessionID).
		Str("path", logPath).
		Msg("trajectory manager: created new tracker for session")

	return tracker
}

// RecordUserMessage records a user message for a specific session.
func (m *TrajectoryManager) RecordUserMessage(sessionID string, message string) {
	if tracker := m.getOrCreateTracker(sessionID); tracker != nil {
		tracker.RecordUserMessage(message)
	}
}

// RecordAgentResponse records an agent response for a specific session.
func (m *TrajectoryManager) RecordAgentResponse(sessionID string, response AgentResponseData) {
	if tracker := m.getOrCreateTracker(sessionID); tracker != nil {
		tracker.RecordAgentResponse(response)
	}
}

// RecordToolResult records a tool result for a specific session.
func (m *TrajectoryManager) RecordToolResult(sessionID string, toolCallID string, content string) {
	if tracker := m.getOrCreateTracker(sessionID); tracker != nil {
		tracker.RecordToolResult(toolCallID, content)
	}
}

// RecordSystemMessage records a system message for a specific session.
func (m *TrajectoryManager) RecordSystemMessage(sessionID string, message string) {
	if tracker := m.getOrCreateTracker(sessionID); tracker != nil {
		tracker.RecordSystemMessage(message)
	}
}

// RecordProxyInteraction records proxy interaction for a specific session.
func (m *TrajectoryManager) RecordProxyInteraction(sessionID string, data ProxyInteractionData) {
	if tracker := m.getOrCreateTracker(sessionID); tracker != nil {
		tracker.RecordProxyInteraction(data)
	}
}

// SetAgentModel sets the model for a specific session.
func (m *TrajectoryManager) SetAgentModel(sessionID string, model string) {
	if tracker := m.getOrCreateTracker(sessionID); tracker != nil {
		tracker.SetAgentModel(model)
	}
}

// CloseAll closes all managed trackers.
func (m *TrajectoryManager) CloseAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var lastErr error
	for sessionID, tracker := range m.trackers {
		if err := tracker.Close(); err != nil {
			log.Error().Err(err).Str("session_id", sessionID).Msg("trajectory manager: failed to close tracker")
			lastErr = err
		}
	}
	m.trackers = make(map[string]*TrajectoryTracker)
	return lastErr
}

// Stats returns statistics about managed trackers.
func (m *TrajectoryManager) Stats() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessionStats := make(map[string]int)
	for sessionID, tracker := range m.trackers {
		sessionStats[sessionID] = tracker.GetStepCount()
	}

	return map[string]interface{}{
		"enabled":         m.config.Enabled,
		"active_sessions": len(m.trackers),
		"sessions":        sessionStats,
	}
}
