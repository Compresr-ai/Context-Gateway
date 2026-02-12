// Package monitoring - trajectory_types.go defines ATIF (Agent Trajectory Interchange Format) types.
//
// DESIGN: These types follow the ATIF v1.6 specification from Harbor:
// https://github.com/laude-institute/harbor/blob/main/docs/rfcs/0001-trajectory-format.md
//
// ATIF provides a standardized JSON format for logging agent interactions,
// including user messages, assistant responses, tool calls, and observations.
//
// TYPES:
//   - Trajectory:     Root object containing complete interaction history
//   - Step:           Single interaction turn (user message, agent response, or system)
//   - ToolCall:       Function/tool invocation by the agent
//   - Observation:    Environment feedback after actions
//   - Metrics:        Token usage and cost data per step
//   - FinalMetrics:   Aggregate statistics for the trajectory
//   - Agent:          Agent configuration metadata
package monitoring

import (
	"encoding/json"
	"time"
)

// =============================================================================
// TRAJECTORY - Root object
// =============================================================================

// Trajectory represents a complete agent interaction session in ATIF format.
type Trajectory struct {
	SchemaVersion          string        `json:"schema_version"`                     // ATIF version, e.g. "ATIF-v1.6"
	SessionID              string        `json:"session_id"`                         // Unique identifier for this session
	Agent                  Agent         `json:"agent"`                              // Agent configuration
	Steps                  []Step        `json:"steps"`                              // Interaction history
	Notes                  string        `json:"notes,omitempty"`                    // Custom notes or explanations
	FinalMetrics           *FinalMetrics `json:"final_metrics,omitempty"`            // Aggregate statistics
	ContinuedTrajectoryRef string        `json:"continued_trajectory_ref,omitempty"` // Reference to continuation file
	Extra                  any           `json:"extra,omitempty"`                    // Custom metadata
}

// NewTrajectory creates a new trajectory with sensible defaults.
func NewTrajectory(sessionID string, agentName string, agentVersion string) *Trajectory {
	return &Trajectory{
		SchemaVersion: "ATIF-v1.6",
		SessionID:     sessionID,
		Agent: Agent{
			Name:    agentName,
			Version: agentVersion,
		},
		Steps: make([]Step, 0),
	}
}

// AddStep appends a step to the trajectory with automatic step_id.
func (t *Trajectory) AddStep(step Step) {
	step.StepID = len(t.Steps) + 1
	if step.Timestamp == "" {
		step.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	t.Steps = append(t.Steps, step)
}

// ToJSON serializes the trajectory to JSON bytes.
func (t *Trajectory) ToJSON() ([]byte, error) {
	return json.MarshalIndent(t, "", "  ")
}

// =============================================================================
// AGENT - Agent configuration metadata
// =============================================================================

// Agent identifies the agent system used for the trajectory.
type Agent struct {
	Name            string           `json:"name"`                       // Agent name, e.g. "claude-code", "openclaw"
	Version         string           `json:"version"`                    // Agent version
	ModelName       string           `json:"model_name,omitempty"`       // Default LLM model
	ToolDefinitions []ToolDefinition `json:"tool_definitions,omitempty"` // Available tools
	Extra           any              `json:"extra,omitempty"`            // Custom agent config
}

// ToolDefinition represents a tool/function available to the agent.
// Follows OpenAI's function calling schema.
type ToolDefinition struct {
	Type     string         `json:"type"`     // "function"
	Function FunctionSchema `json:"function"` // Function details
}

// FunctionSchema describes a function's signature.
type FunctionSchema struct {
	Name        string `json:"name"`                  // Function name
	Description string `json:"description,omitempty"` // What the function does
	Parameters  any    `json:"parameters,omitempty"`  // JSON Schema for parameters
}

// =============================================================================
// STEP - Single interaction turn
// =============================================================================

// StepSource identifies who originated the step.
type StepSource string

const (
	StepSourceSystem StepSource = "system" // System prompts
	StepSourceUser   StepSource = "user"   // User messages
	StepSourceAgent  StepSource = "agent"  // Agent responses
)

// Step represents a single turn in the trajectory.
type Step struct {
	StepID           int               `json:"step_id"`                     // Ordinal index (1-based)
	Timestamp        string            `json:"timestamp,omitempty"`         // ISO 8601 timestamp
	Source           StepSource        `json:"source"`                      // Who originated: system, user, agent
	ModelName        string            `json:"model_name,omitempty"`        // LLM model for this turn (agent only)
	ReasoningEffort  string            `json:"reasoning_effort,omitempty"`  // low, medium, high (agent only)
	Message          string            `json:"message"`                     // The dialogue message
	ReasoningContent string            `json:"reasoning_content,omitempty"` // Agent's explicit reasoning (agent only)
	ToolCalls        []ToolCall        `json:"tool_calls,omitempty"`        // Tool invocations (agent only)
	Observation      *Observation      `json:"observation,omitempty"`       // Environment feedback
	Metrics          *Metrics          `json:"metrics,omitempty"`           // Token usage (agent only)
	ProxyInteraction *ProxyInteraction `json:"proxy_interaction,omitempty"` // Proxy flow: client→proxy→LLM→client
	IsCopiedContext  bool              `json:"is_copied_context,omitempty"` // Copied from previous trajectory
	Extra            any               `json:"extra,omitempty"`             // Custom step metadata
}

// NewUserStep creates a step for a user message.
func NewUserStep(message string) Step {
	return Step{
		Source:    StepSourceUser,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Message:   message,
	}
}

// NewAgentStep creates a step for an agent response.
func NewAgentStep(message string, model string) Step {
	return Step{
		Source:    StepSourceAgent,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Message:   message,
		ModelName: model,
	}
}

// NewSystemStep creates a step for a system message.
func NewSystemStep(message string) Step {
	return Step{
		Source:    StepSourceSystem,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Message:   message,
	}
}

// =============================================================================
// TOOL CALLS - Agent actions
// =============================================================================

// ToolCall represents a function/tool invocation by the agent.
type ToolCall struct {
	ToolCallID   string `json:"tool_call_id"`  // Unique identifier for this call
	FunctionName string `json:"function_name"` // Name of the function
	Arguments    any    `json:"arguments"`     // Arguments passed (JSON object)
}

// =============================================================================
// OBSERVATION - Environment feedback
// =============================================================================

// Observation records results from tool executions or system events.
type Observation struct {
	Results []ObservationResult `json:"results"` // Array of result objects
}

// ObservationResult is the result from a single tool call or action.
type ObservationResult struct {
	SourceCallID          string                  `json:"source_call_id,omitempty"`          // Corresponding tool_call_id
	Content               string                  `json:"content,omitempty"`                 // Output text
	SubagentTrajectoryRef []SubagentTrajectoryRef `json:"subagent_trajectory_ref,omitempty"` // Subagent references
}

// SubagentTrajectoryRef references a delegated subagent trajectory.
type SubagentTrajectoryRef struct {
	SessionID      string `json:"session_id"`                // Subagent's session ID
	TrajectoryPath string `json:"trajectory_path,omitempty"` // File path or URL
	Extra          any    `json:"extra,omitempty"`           // Custom metadata
}

// =============================================================================
// METRICS - Token usage and costs
// =============================================================================

// Metrics contains LLM operational data for a single step.
type Metrics struct {
	PromptTokens       int       `json:"prompt_tokens,omitempty"`        // Total input tokens
	CompletionTokens   int       `json:"completion_tokens,omitempty"`    // Generated tokens
	CachedTokens       int       `json:"cached_tokens,omitempty"`        // Cache hits (subset of prompt)
	CostUSD            float64   `json:"cost_usd,omitempty"`             // Cost in USD
	PromptTokenIDs     []int     `json:"prompt_token_ids,omitempty"`     // Token IDs for prompt
	CompletionTokenIDs []int     `json:"completion_token_ids,omitempty"` // Token IDs for completion
	Logprobs           []float64 `json:"logprobs,omitempty"`             // Log probabilities
	Extra              any       `json:"extra,omitempty"`                // Provider-specific metrics
}

// =============================================================================
// PROXY INTERACTION - Tracks message flow through the gateway proxy
// =============================================================================

// ProxyInteraction captures the full request/response flow through the proxy.
// For each step, this shows: Client → Proxy → LLM → Proxy → Client
type ProxyInteraction struct {
	// Pipeline info
	PipeType     string `json:"pipe_type"`               // Which pipe was used: passthrough, tool_output, tool_discovery
	PipeStrategy string `json:"pipe_strategy,omitempty"` // How it was processed: passthrough, api, llm

	// Request flow
	ClientToProxy *ProxyMessage `json:"client_to_proxy,omitempty"` // Original request from client
	ProxyToLLM    *ProxyMessage `json:"proxy_to_llm,omitempty"`    // Request sent to LLM (after compression)

	// Response flow
	LLMToProxy    *ProxyMessage `json:"llm_to_proxy,omitempty"`    // Response from LLM
	ProxyToClient *ProxyMessage `json:"proxy_to_client,omitempty"` // Response forwarded to client

	// Compression details
	Compression *ProxyCompressionInfo `json:"compression,omitempty"` // What was compressed
}

// ProxyMessage represents a message at any point in the proxy flow.
type ProxyMessage struct {
	Timestamp   string `json:"timestamp"`              // ISO 8601 timestamp
	Messages    []any  `json:"messages,omitempty"`     // Chat messages array
	TokenCount  int    `json:"token_count,omitempty"`  // Estimated token count
	ContentHash string `json:"content_hash,omitempty"` // SHA256 of content (for comparison)
}

// ProxyCompressionInfo captures details about proxy compression.
type ProxyCompressionInfo struct {
	Enabled          bool                   `json:"enabled"`                     // Was compression applied
	OriginalTokens   int                    `json:"original_tokens,omitempty"`   // Tokens before compression
	CompressedTokens int                    `json:"compressed_tokens,omitempty"` // Tokens after compression
	TokensSaved      int                    `json:"tokens_saved,omitempty"`      // Tokens removed
	CompressionRatio float64                `json:"compression_ratio,omitempty"` // Ratio (compressed/original)
	ToolCompressions []ToolCompressionEntry `json:"tool_compressions,omitempty"` // Individual tool compression details
}

// ToolCompressionEntry captures compression details for a single tool output.
type ToolCompressionEntry struct {
	ToolName          string  `json:"tool_name"`                    // Name of the tool
	ToolCallID        string  `json:"tool_call_id,omitempty"`       // Tool call ID for correlation
	Status            string  `json:"status"`                       // compressed, passthrough_small, passthrough_large, cache_hit
	ShadowID          string  `json:"shadow_id,omitempty"`          // Shadow reference ID
	OriginalBytes     int     `json:"original_bytes"`               // Size before compression
	CompressedBytes   int     `json:"compressed_bytes"`             // Size after compression
	CompressionRatio  float64 `json:"compression_ratio"`            // Ratio for this tool
	OriginalContent   string  `json:"original_content,omitempty"`   // Original content
	CompressedContent string  `json:"compressed_content,omitempty"` // Compressed content
	CacheHit          bool    `json:"cache_hit"`                    // Was this a cache hit
}

// =============================================================================
// FINAL METRICS - Aggregate statistics
// =============================================================================

// FinalMetrics provides aggregate statistics for the entire trajectory.
type FinalMetrics struct {
	TotalPromptTokens     int     `json:"total_prompt_tokens,omitempty"`     // Sum of all prompt tokens
	TotalCompletionTokens int     `json:"total_completion_tokens,omitempty"` // Sum of all completion tokens
	TotalCachedTokens     int     `json:"total_cached_tokens,omitempty"`     // Sum of all cached tokens
	TotalCostUSD          float64 `json:"total_cost_usd,omitempty"`          // Total cost
	TotalSteps            int     `json:"total_steps,omitempty"`             // Number of steps
	Extra                 any     `json:"extra,omitempty"`                   // Custom aggregate metrics
}

// ComputeFinalMetrics calculates aggregate metrics from all steps.
func (t *Trajectory) ComputeFinalMetrics() {
	var fm FinalMetrics
	fm.TotalSteps = len(t.Steps)

	for _, step := range t.Steps {
		if step.Metrics != nil {
			fm.TotalPromptTokens += step.Metrics.PromptTokens
			fm.TotalCompletionTokens += step.Metrics.CompletionTokens
			fm.TotalCachedTokens += step.Metrics.CachedTokens
			fm.TotalCostUSD += step.Metrics.CostUSD
		}
	}

	t.FinalMetrics = &fm
}
