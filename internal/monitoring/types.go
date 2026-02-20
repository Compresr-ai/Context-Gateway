// Package monitoring - types.go defines shared types.
//
// DESIGN: These types are used by both gateway/ and monitoring/ packages.
// Defined here ONCE to avoid duplication and circular imports.
//
// TYPES:
//   - PipeType:      Identifies which pipe handled a request
//   - RequestEvent:  Telemetry data for each request
//   - Config types:  TelemetryConfig, LoggerConfig, AlertConfig
package monitoring

import "time"

// =============================================================================
// PIPE TYPES - Used by router and telemetry
// =============================================================================

// PipeType identifies which compression pipe handles the request.
type PipeType string

const (
	PipeNone          PipeType = "none"
	PipePassthrough   PipeType = "passthrough"
	PipeToolOutput    PipeType = "tool_output"
	PipeToolDiscovery PipeType = "tool_discovery"
)

// =============================================================================
// EVENT TYPES - Structured data for telemetry recording
// =============================================================================

// RequestEvent captures a request through the gateway.
type RequestEvent struct {
	RequestID            string    `json:"request_id"`
	Timestamp            time.Time `json:"timestamp"`
	Method               string    `json:"method"`
	Path                 string    `json:"path"`
	ClientIP             string    `json:"client_ip"`
	Provider             string    `json:"provider"`
	Model                string    `json:"model,omitempty"`
	RequestBodySize      int       `json:"request_body_size"`
	ResponseBodySize     int       `json:"response_body_size"`
	StatusCode           int       `json:"status_code"`
	OriginalTokens       int       `json:"original_tokens"`
	CompressedTokens     int       `json:"compressed_tokens"`
	TokensSaved          int       `json:"tokens_saved"`
	CompressionRatio     float64   `json:"compression_ratio"`
	CompressionUsed      bool      `json:"compression_used"`
	PipeType             PipeType  `json:"pipe_type"`
	PipeStrategy         string    `json:"pipe_strategy"`
	ShadowRefsCreated    int       `json:"shadow_refs_created"`
	ExpandLoops          int       `json:"expand_loops"`
	ExpandCallsFound     int       `json:"expand_calls_found"`
	ExpandCallsNotFound  int       `json:"expand_calls_not_found"`
	Success              bool      `json:"success"`
	Error                string    `json:"error,omitempty"`
	CompressionLatencyMs int64     `json:"compression_latency_ms"`
	ForwardLatencyMs     int64     `json:"forward_latency_ms"`
	TotalLatencyMs       int64     `json:"total_latency_ms"`
	AuthModeInitial      string    `json:"auth_mode_initial,omitempty"`   // subscription, api_key, bearer, oauth, none, unknown
	AuthModeEffective    string    `json:"auth_mode_effective,omitempty"` // Actual auth sent upstream
	AuthFallbackUsed     bool      `json:"auth_fallback_used,omitempty"`  // True when subscription->api_key fallback happened
	// Usage from API response (extracted by adapter)
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	TotalTokens  int `json:"total_tokens,omitempty"`
}

// InitEvent captures gateway startup configuration and agent flags.
type InitEvent struct {
	Timestamp             time.Time      `json:"timestamp"`
	Event                 string         `json:"event"`
	AgentName             string         `json:"agent_name,omitempty"`
	AgentFlags            []string       `json:"agent_flags,omitempty"`
	AutoApproveMode       bool           `json:"auto_approve_mode"`
	ServerPort            int            `json:"server_port"`
	ServerReadTimeoutMs   int64          `json:"server_read_timeout_ms"`
	ServerWriteTimeoutMs  int64          `json:"server_write_timeout_ms"`
	ToolOutputEnabled     bool           `json:"tool_output_enabled"`
	ToolOutputStrategy    string         `json:"tool_output_strategy,omitempty"`
	ToolDiscoveryEnabled  bool           `json:"tool_discovery_enabled"`
	ToolDiscoveryStrategy string         `json:"tool_discovery_strategy,omitempty"`
	PreemptiveEnabled     bool           `json:"preemptive_enabled"`
	PreemptiveTrigger     float64        `json:"preemptive_trigger_threshold"`
	Providers             []InitProvider `json:"providers,omitempty"`
	TelemetryPath         string         `json:"telemetry_path,omitempty"`
	CompressionLogPath    string         `json:"compression_log_path,omitempty"`
	ToolDiscoveryLogPath  string         `json:"tool_discovery_log_path,omitempty"`
	TrajectoryEnabled     bool           `json:"trajectory_enabled"`
	Extra                 map[string]any `json:"extra,omitempty"`
}

// InitProvider summarizes a provider config without leaking secrets.
type InitProvider struct {
	Name          string `json:"name"`
	Auth          string `json:"auth,omitempty"`
	Model         string `json:"model,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	HasAPIKey     bool   `json:"has_api_key"`
	APIKeyEnvLike bool   `json:"api_key_env_like,omitempty"`
}

// ExpandEvent captures an expand_context call.
type ExpandEvent struct {
	Timestamp   time.Time `json:"timestamp"`
	RequestID   string    `json:"request_id,omitempty"`
	ShadowRefID string    `json:"shadow_ref_id"`
	Found       bool      `json:"found"`
	Success     bool      `json:"success"`
}

// CompressionComparison captures before/after compression comparison.
// StepID links to trajectory step for correlation.
type CompressionComparison struct {
	RequestID         string   `json:"request_id"`
	StepID            int      `json:"step_id,omitempty"`
	Timestamp         string   `json:"timestamp,omitempty"`
	PipeType          string   `json:"pipe_type"`
	ToolName          string   `json:"tool_name,omitempty"`
	ShadowID          string   `json:"shadow_id,omitempty"`
	OriginalBytes     int      `json:"original_bytes"`
	CompressedBytes   int      `json:"compressed_bytes"`
	CompressionRatio  float64  `json:"compression_ratio"`
	OriginalContent   string   `json:"original_content,omitempty"`
	CompressedContent string   `json:"compressed_content,omitempty"`
	AllTools          []string `json:"all_tools,omitempty"`
	SelectedTools     []string `json:"selected_tools,omitempty"`
	CacheHit          bool     `json:"cache_hit"`
	IsLastTool        bool     `json:"is_last_tool,omitempty"`
	Status            string   `json:"status"`                  // compressed, passthrough_small, passthrough_large, cache_hit
	MinThreshold      int      `json:"min_threshold,omitempty"` // Min byte threshold used
	MaxThreshold      int      `json:"max_threshold,omitempty"` // Max byte threshold used
}

// =============================================================================
// CONFIG TYPES
// =============================================================================

// TelemetryConfig contains telemetry configuration.
type TelemetryConfig struct {
	Enabled              bool   `yaml:"enabled"`
	LogPath              string `yaml:"log_path"`
	LogToStdout          bool   `yaml:"log_to_stdout"`
	CompressionLogPath   string `yaml:"compression_log_path"`
	ToolDiscoveryLogPath string `yaml:"tool_discovery_log_path"`
}

// LoggerConfig contains logging configuration.
type LoggerConfig struct {
	Level  string `yaml:"level"`  // debug, info, warn, error
	Format string `yaml:"format"` // json, console
	Output string `yaml:"output"` // stdout, stderr, or file path
}

// AlertConfig contains alert thresholds.
type AlertConfig struct {
	HighLatencyThreshold time.Duration `yaml:"high_latency_threshold"`
}
