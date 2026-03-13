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
	"strings"
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
	lastActive time.Time
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
	t.lastActive = time.Now()

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
	t.lastActive = time.Now()

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
	t.lastActive = time.Now()

	// Flush to disk immediately
	t.flushLocked()

	log.Debug().
		Int("step_id", len(t.trajectory.Steps)).
		Str("source", "agent").
		Str("model", response.Model).
		Int("tool_calls", len(response.ToolCalls)).
		Msg("trajectory: recorded agent response")
}

// AccumulateAgentResponse appends tool calls and metrics to the last agent step
// instead of creating a new one. Used for tool-loop iterations where multiple
// LLM API calls are part of the same logical agent turn.
func (t *TrajectoryTracker) AccumulateAgentResponse(response AgentResponseData) {
	if !t.config.Enabled || t.trajectory == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return
	}

	// Find the last agent step to accumulate into
	for i := len(t.trajectory.Steps) - 1; i >= 0; i-- {
		step := &t.trajectory.Steps[i]
		if step.Source == StepSourceAgent {
			// Append new tool calls (deduplicate by ID to avoid double-counting
			// when the same tool call appears in both response body and request history)
			if len(response.ToolCalls) > 0 {
				existing := make(map[string]bool, len(step.ToolCalls))
				for _, tc := range step.ToolCalls {
					existing[tc.ToolCallID] = true
				}
				for _, tc := range response.ToolCalls {
					if !existing[tc.ToolCallID] {
						step.ToolCalls = append(step.ToolCalls, tc)
					}
				}
			}

			// Update message with the latest non-empty content.
			// Tool-loop iterations often have empty content; keep the most
			// recent meaningful message (the final response to the user).
			if response.Message != "" && response.Message != "[streaming response]" {
				step.Message = response.Message
			}

			// Accumulate token metrics
			if response.PromptTokens > 0 || response.CompletionTokens > 0 {
				if step.Metrics == nil {
					step.Metrics = &Metrics{}
				}
				step.Metrics.PromptTokens += response.PromptTokens
				step.Metrics.CompletionTokens += response.CompletionTokens
			}

			t.lastActive = time.Now()
			t.flushLocked()

			log.Debug().
				Int("step_id", step.StepID).
				Int("total_tool_calls", len(step.ToolCalls)).
				Msg("trajectory: accumulated tool calls into existing step")
			return
		}
	}

	// No existing agent step found — create new one as fallback
	step := NewAgentStep(response.Message, response.Model)
	step.ReasoningContent = response.Reasoning
	if len(response.ToolCalls) > 0 {
		step.ToolCalls = response.ToolCalls
	}
	if response.PromptTokens > 0 || response.CompletionTokens > 0 {
		step.Metrics = &Metrics{
			PromptTokens:     response.PromptTokens,
			CompletionTokens: response.CompletionTokens,
		}
	}
	t.trajectory.AddStep(step)
	t.lastActive = time.Now()
	t.flushLocked()
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
					t.lastActive = time.Now()
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
	t.lastActive = time.Now()

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
	t.lastActive = time.Now()
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
	t.lastActive = time.Now()
}

// LastActivity returns the timestamp of the last tracker update.
func (t *TrajectoryTracker) LastActivity() time.Time {
	if t == nil {
		return time.Time{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastActive.IsZero() {
		return time.Now()
	}
	return t.lastActive
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

// extractContentText returns the text from a message's "content" field,
// handling both string content and Anthropic-style array-of-blocks content.
func extractContentText(content any) string {
	// Case 1: Simple string content (OpenAI / simple Anthropic)
	if s, ok := content.(string); ok {
		return s
	}

	// Case 2: Array of content blocks (Anthropic format)
	blocks, ok := content.([]any)
	if !ok {
		return ""
	}
	var texts []string
	for _, block := range blocks {
		m, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "text" {
			if t, ok := m["text"].(string); ok && t != "" {
				texts = append(texts, t)
			}
		}
	}
	return strings.Join(texts, "\n")
}

// ExtractUserMessages extracts user message content from request messages.
func ExtractUserMessages(messages []map[string]any) []string {
	var userMessages []string
	for _, msg := range messages {
		role, ok := msg["role"].(string)
		if !ok || role != "user" {
			continue
		}
		if text := extractContentText(msg["content"]); text != "" {
			userMessages = append(userMessages, text)
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
		if text := extractContentText(msg["content"]); text != "" {
			return text
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
// Uses message counts instead of full arrays to avoid duplicating the system
// prompt and growing conversation history in every trajectory step.
type ProxyInteractionData struct {
	// Pipeline info
	PipeType     string // Which pipe was used: passthrough, tool_output, tool_discovery
	PipeStrategy string // How it was processed: passthrough, api, llm

	// Token counts
	ClientTokens     int // Tokens in original request
	CompressedTokens int // Tokens in compressed request

	// Message counts (instead of full arrays to avoid system prompt duplication)
	ClientMsgCount     int // Number of messages from client
	CompressedMsgCount int // Number of messages after compression

	// Compression details
	CompressionEnabled bool
	ToolCompressions   []ToolCompressionEntry // Individual tool compression details

	// LLM response
	ResponseTokens int // Tokens in response
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
			t.lastActive = time.Now()

			// Add compression info if compression was applied
			if data.CompressionEnabled && data.ClientTokens > 0 {
				// tokensSaved := data.ClientTokens - data.CompressedTokens
				ratio := float64(data.CompressedTokens) / float64(data.ClientTokens)

				step.ProxyInteraction.Compression = &ProxyCompressionInfo{
					Enabled:          true,
					OriginalTokens:   data.ClientTokens,
					CompressedTokens: data.CompressedTokens,
					// TokensSaved:      tokensSaved,
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
	Enabled         bool          // Enable trajectory logging
	BaseDir         string        // Base directory for trajectory files (e.g., "logs/session_1/")
	AgentName       string        // Agent name (e.g., "claude-code")
	SessionTTL      time.Duration // Inactive session eviction TTL
	CleanupInterval time.Duration // Inactive session cleanup interval
}

// TrajectoryManager manages multiple TrajectoryTrackers by session ID.
// Each unique session ID gets its own trajectory file: trajectory_<sessionID>.json
type TrajectoryManager struct {
	config       TrajectoryManagerConfig
	trackers     map[string]*TrajectoryTracker
	mainSessions map[string]bool // session IDs marked as main agent
	mu           sync.RWMutex
	stopChan     chan struct{}
	wg           sync.WaitGroup
}

// NewTrajectoryManager creates a new trajectory manager.
func NewTrajectoryManager(cfg TrajectoryManagerConfig) *TrajectoryManager {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = time.Hour
	}
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 5 * time.Minute
	}

	m := &TrajectoryManager{
		config:       cfg,
		trackers:     make(map[string]*TrajectoryTracker),
		mainSessions: make(map[string]bool),
		stopChan:     make(chan struct{}),
	}
	if cfg.Enabled {
		m.wg.Add(1)
		go m.cleanupLoop()
	}
	return m
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

	// Check if this session is marked as main agent — tag filename accordingly
	suffix := sessionID
	if m.mainSessions[sessionID] {
		suffix = sessionID + "_MAIN"
	}
	logPath := filepath.Join(m.config.BaseDir, fmt.Sprintf("trajectory_%s.json", suffix))
	tracker, err := NewTrajectoryTracker(TrajectoryConfig{
		Enabled:   true,
		LogPath:   logPath,
		AgentName: m.config.AgentName,
		SessionID: sessionID,
	})
	if err != nil {
		log.Error().Err(err).Str("session_id", sessionID).Msg("trajectory manager: failed to create tracker")
		return nil
	}

	m.trackers[sessionID] = tracker
	log.Info().
		Str("session_id", sessionID).
		Str("path", logPath).
		Bool("main", m.mainSessions[sessionID]).
		Msg("trajectory manager: created new tracker for session")

	return tracker
}

// MarkMainSession marks a session ID as the main agent session.
// Only the FIRST session to be marked wins — subsequent calls with different
// session IDs are ignored. This prevents multiple trajectory files from being
// tagged _MAIN when several conversations hit the same gateway instance.
//
// Must be called before the first recording for that session to take effect on the filename.
// If the tracker already exists, renames the file on disk.
func (m *TrajectoryManager) MarkMainSession(sessionID string) {
	if !m.config.Enabled || sessionID == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Already marked — check if it's the same session (no-op) or different (reject)
	if len(m.mainSessions) > 0 {
		if m.mainSessions[sessionID] {
			return // same session, already marked
		}
		return // different session — only one MAIN allowed
	}
	m.mainSessions[sessionID] = true

	// If tracker already exists, rename its file to include _MAIN
	if tracker, exists := m.trackers[sessionID]; exists {
		oldPath := tracker.config.LogPath
		newPath := filepath.Join(m.config.BaseDir, fmt.Sprintf("trajectory_%s_MAIN.json", sessionID))
		if oldPath != newPath {
			if err := os.Rename(oldPath, newPath); err == nil {
				tracker.config.LogPath = newPath
				log.Info().Str("old", oldPath).Str("new", newPath).Msg("trajectory: renamed to MAIN")
			}
		}
	}
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

// AccumulateAgentResponse accumulates tool calls into the last agent step for a session.
func (m *TrajectoryManager) AccumulateAgentResponse(sessionID string, response AgentResponseData) {
	if tracker := m.getOrCreateTracker(sessionID); tracker != nil {
		tracker.AccumulateAgentResponse(response)
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
	select {
	case <-m.stopChan:
		// already closed
	default:
		close(m.stopChan)
	}
	m.wg.Wait()

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

func (m *TrajectoryManager) cleanupLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(m.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			m.cleanupInactiveTrackers()
		}
	}
}

func (m *TrajectoryManager) cleanupInactiveTrackers() {
	if !m.config.Enabled || m.config.SessionTTL <= 0 {
		return
	}

	cutoff := time.Now().Add(-m.config.SessionTTL)
	stale := make(map[string]*TrajectoryTracker)

	m.mu.Lock()
	for sessionID, tracker := range m.trackers {
		if tracker == nil {
			delete(m.trackers, sessionID)
			continue
		}
		if tracker.LastActivity().Before(cutoff) {
			stale[sessionID] = tracker
			delete(m.trackers, sessionID)
		}
	}
	m.mu.Unlock()

	for sessionID, tracker := range stale {
		if err := tracker.Close(); err != nil {
			log.Error().Err(err).Str("session_id", sessionID).Msg("trajectory manager: failed to close evicted tracker")
		} else {
			log.Debug().Str("session_id", sessionID).Msg("trajectory manager: evicted inactive tracker")
		}
	}
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
