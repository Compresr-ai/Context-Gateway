// Package monitoring - types.go defines shared types.
package monitoring

import (
	"encoding/json"
	"time"
)

// PIPE TYPES - Used by router and telemetry

// PipeType identifies which compression pipe handles the request.
type PipeType string

const (
	PipeNone          PipeType = "none"
	PipePassthrough   PipeType = "passthrough"
	PipeToolOutput    PipeType = "tool_output"
	PipeToolDiscovery PipeType = "tool_discovery"
	PipeTaskOutput    PipeType = "task_output"
)

// EVENT TYPES - Structured data for telemetry recording

// RequestEvent captures a request through the gateway.
type RequestEvent struct {
	RequestID        string    `json:"request_id"`
	Timestamp        time.Time `json:"timestamp"`
	Method           string    `json:"method"`
	Path             string    `json:"path"`
	ClientIP         string    `json:"client_ip"`
	Provider         string    `json:"provider"`
	Model            string    `json:"model,omitempty"`
	RequestBodySize  int       `json:"request_body_size"`
	ResponseBodySize int       `json:"response_body_size"`
	StatusCode       int       `json:"status_code"`

	// Pipe-specific counts (grouped together for easy analysis)
	ToolOutputCount       int `json:"tool_output_count"`                 // Number of tool outputs compressed
	ToolDiscoveryOriginal int `json:"tool_discovery_original,omitempty"` // Tools before filtering
	ToolDiscoveryFiltered int `json:"tool_discovery_filtered,omitempty"` // Tools after filtering
	TaskOutputCount       int `json:"task_output_count,omitempty"`       // Number of task outputs handled

	// Token metrics
	OriginalTokens   int     `json:"original_tokens"`
	CompressedTokens int     `json:"compressed_tokens"`
	TokensSaved      int     `json:"tokens_saved"`
	CompressionRatio float64 `json:"compression_ratio"` // Removed fraction: 1 - compressed/original (0.9 = 90% removed; higher = more aggressive)
	CompressionUsed  bool    `json:"compression_used"`

	// Pipe routing
	PipeType     PipeType `json:"pipe_type"`
	PipeStrategy string   `json:"pipe_strategy"`

	// Expand context tracking
	ShadowRefsCreated   int `json:"shadow_refs_created"`
	ExpandLoops         int `json:"expand_loops"`
	ExpandCallsFound    int `json:"expand_calls_found"`
	ExpandCallsNotFound int `json:"expand_calls_not_found"`
	ExpandPenaltyTokens int `json:"expand_penalty_tokens,omitempty"`

	// Request result
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`

	// Latency
	CompressionLatencyMs int64 `json:"compression_latency_ms"`
	ForwardLatencyMs     int64 `json:"forward_latency_ms"`
	TotalLatencyMs       int64 `json:"total_latency_ms"`

	// Auth
	AuthModeInitial   string `json:"auth_mode_initial,omitempty"`   // subscription, api_key, bearer, oauth, none, unknown
	AuthModeEffective string `json:"auth_mode_effective,omitempty"` // Actual auth sent upstream
	AuthFallbackUsed  bool   `json:"auth_fallback_used,omitempty"`  // True when subscription->api_key fallback happened

	// Preemptive summarization
	HistoryCompactionTriggered bool `json:"history_compaction_triggered,omitempty"` // Whether preemptive summarization ran

	// Agent classification
	IsMainAgent bool `json:"is_main_agent"` // True for main Claude Code agent, false for subagents

	// Usage from API response (extracted by adapter)
	InputTokens              int     `json:"input_tokens,omitempty"`
	OutputTokens             int     `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens,omitempty"`
	TotalTokens              int     `json:"total_tokens,omitempty"`
	CostUSD                  float64 `json:"cost_usd,omitempty"` // Computed cost for this request

	// VERBOSE PAYLOADS (populated when monitoring.verbose_payloads=true)
	RequestHeaders      map[string]string `json:"request_headers,omitempty"`       // Sanitized headers (no secrets)
	ResponseHeaders     map[string]string `json:"response_headers,omitempty"`      // Response headers
	RequestBodyPreview  string            `json:"request_body_preview,omitempty"`  // First 500 chars
	ResponseBodyPreview string            `json:"response_body_preview,omitempty"` // First 500 chars
	AuthHeaderSent      string            `json:"auth_header_sent,omitempty"`      // Masked: "Bearer xxx", "sk-..."
	UpstreamURL         string            `json:"upstream_url,omitempty"`          // Actual endpoint hit
	FallbackReason      string            `json:"fallback_reason,omitempty"`       // "401 Unauthorized", etc.
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
	RequestID        string  `json:"request_id"`
	EventType        string  `json:"event_type,omitempty"` // mandatory second key: "lazy_loading", "tool_output", etc.
	SessionID        string  `json:"session_id,omitempty"` // cost session ID for cross-request correlation
	Timestamp        string  `json:"timestamp,omitempty"`  // RFC3339 UTC timestamp; auto-filled by log functions if empty
	IsMainAgent      bool    `json:"is_main_agent"`        // True for main agent, false for subagents
	ProviderModel    string  `json:"model,omitempty"`      // LLM model (e.g., "claude-sonnet-4-5", "gpt-5.1-codex")
	StepID           int     `json:"step_id,omitempty"`
	ToolName         string  `json:"tool_name,omitempty"`
	ShadowID         string  `json:"shadow_id,omitempty"`
	OriginalTokens   int     `json:"original_tokens,omitempty"`   // Tiktoken-based count
	CompressedTokens int     `json:"compressed_tokens,omitempty"` // Tiktoken-based count
	CompressionRatio float64 `json:"compression_ratio"`           // Removed fraction: 1 - compressed/original (0.9 = 90% removed; higher = more aggressive)
	CacheHit         bool    `json:"cache_hit"`
	IsLastTool       bool    `json:"is_last_tool,omitempty"`
	Status           string  `json:"status"` // compressed, passthrough_small, passthrough_large, cache_hit
	MinThreshold     int     `json:"min_threshold,omitempty"`
	MaxThreshold     int     `json:"max_threshold,omitempty"`
	CompressionModel string  `json:"compression_model,omitempty"` // e.g., "toc_latte_v1"
	Query            string  `json:"query,omitempty"`
	QueryAgnostic    bool    `json:"query_agnostic,omitempty"`
	ToolCount        int     `json:"tool_count,omitempty"`    // total tools in original request
	StubCount        int     `json:"stub_count,omitempty"`    // tools replaced with stubs (deferred)
	PhantomCount     int     `json:"phantom_count,omitempty"` // phantom tools injected
	// Large/variable fields — used internally, not written to tool_discovery.jsonl
	AllTools          []string `json:"-"`
	SelectedTools     []string `json:"-"`
	OriginalContent   string   `json:"original_content,omitempty"`
	CompressedContent string   `json:"compressed_content,omitempty"`
}

// Tool discovery event types for tool_discovery.jsonl
const (
	EventTypeLazyLoading             = "lazy_loading"              // initial request tool filtering (lazy loading)
	EventTypeToolSearchResult        = "tool_search_result"        // search results returned by gateway_search_tools
	EventTypeToolSearchSelect        = EventTypeToolSearchResult   // alias kept for internal use
	EventTypeToolDescriptionCompress = "tool_description_compress" // stage 2 description compression
)

// Tool output event types for tool_output_compression.jsonl
const (
	EventTypeToolOutput = "tool_output" // tool execution result that was compressed (also a tool discovery signal)
)

// Task output event types for task_output_compression.jsonl
const (
	EventTypeTaskOutput = "task_output" // subagent result identified and processed by the task_output pipe
)

// TYPED LOG ENTRY STRUCTS — one per pipe, all sharing LogEntryBase
//
// Each of the 4 pipes writes a distinct typed struct to its JSONL log.
// LogEntryBase guarantees all entries share the same first 4 keys in the same
// order: request_id, event_type, session_id, timestamp.

// LogEntryBase is the shared header for every JSONL log entry across all 4 pipes.
// Embedded as the first field in each typed struct so JSON marshalling always
// produces the same leading keys in the same order.
type LogEntryBase struct {
	RequestID string `json:"request_id"`
	EventType string `json:"event_type"`
	SessionID string `json:"session_id,omitempty"`
	Timestamp string `json:"timestamp"`
}

// LazyLoadingEntry is written to tool_discovery.jsonl for the initial tool-filtering
// step (event_type = "lazy_loading").
type LazyLoadingEntry struct {
	LogEntryBase
	IsMainAgent      bool    `json:"is_main_agent"`
	OriginalTokens   int     `json:"original_tokens,omitempty"`
	CompressedTokens int     `json:"compressed_tokens,omitempty"`
	CompressionRatio float64 `json:"compression_ratio"`
	CacheHit         bool    `json:"cache_hit"`
	Status           string  `json:"status"`
	ProviderModel    string  `json:"model,omitempty"`
	CompressionModel string  `json:"compression_model,omitempty"`
	ToolCount        int     `json:"tool_count,omitempty"`
	StubCount        int     `json:"stub_count,omitempty"`
	PhantomCount     int     `json:"phantom_count,omitempty"`
}

// ToolSearchResult is written to tool_discovery.jsonl for gateway_search_tools
// calls, both successful searches and API fallback errors
// (event_type = "tool_search_result").
//
// Two-stage compression metrics:
// - Stage 1 (Tool Selection): Reduces tools from full pool to relevant subset
// - Stage 2 (Schema Compression): Compresses selected tool schemas via Compresr API
type ToolSearchResult struct {
	LogEntryBase
	IsMainAgent      bool     `json:"is_main_agent"`
	Query            string   `json:"query,omitempty"`
	DeferredCount    int      `json:"deferred_count,omitempty"` // Deprecated: use OriginalToolCount
	ResultsCount     int      `json:"results_count,omitempty"`  // Deprecated: use SelectedToolCount
	ToolsProvided    []string `json:"tools_provided,omitempty"` // Deprecated: use SelectedTools
	Status           string   `json:"status,omitempty"`
	ProviderModel    string   `json:"model,omitempty"`
	CompressionModel string   `json:"compression_model,omitempty"`
	ErrorDetail      string   `json:"error_detail,omitempty"` // set on API fallback errors

	// Stage 1: Tool Selection (pool → selected)
	OriginalToolCount int      `json:"original_tool_count,omitempty"` // tools in deferred pool
	SelectedToolCount int      `json:"selected_tool_count,omitempty"` // tools after selection
	SelectedTools     []string `json:"selected_tools,omitempty"`      // list of selected tool names

	// Compression metrics (deprecated - use Stage fields)
	Strategy         string  `json:"strategy,omitempty"`
	OriginalTokens   int     `json:"original_tokens,omitempty"`   // Deprecated: use Stage1OriginalTokens
	CompressedTokens int     `json:"compressed_tokens,omitempty"` // Deprecated: use Stage2CompressedTokens
	CompressionRatio float64 `json:"compression_ratio,omitempty"` // Deprecated: use EndToEndCompressionRatio

	// Stage 1: Tool Selection tokens (full schemas → selected schemas)
	Stage1OriginalTokens   int     `json:"stage1_original_tokens,omitempty"`   // full pool schema tokens
	Stage1CompressedTokens int     `json:"stage1_compressed_tokens,omitempty"` // selected tools schema tokens
	Stage1CompressionRatio float64 `json:"stage1_compression_ratio,omitempty"` // selection compression ratio

	// Stage 2: Schema Compression tokens (formatted → compressed)
	Stage2OriginalTokens   int     `json:"stage2_original_tokens,omitempty"`   // formatted search results tokens
	Stage2CompressedTokens int     `json:"stage2_compressed_tokens,omitempty"` // after schema compression
	Stage2CompressionRatio float64 `json:"stage2_compression_ratio,omitempty"` // schema compression ratio
	Stage2Strategy         string  `json:"stage2_strategy,omitempty"`          // compresr | external_provider | passthrough

	// End-to-End metrics (pool → final output)
	EndToEndOriginalTokens   int     `json:"end_to_end_original_tokens,omitempty"`   // same as Stage1OriginalTokens
	EndToEndCompressedTokens int     `json:"end_to_end_compressed_tokens,omitempty"` // same as Stage2CompressedTokens
	EndToEndCompressionRatio float64 `json:"end_to_end_compression_ratio,omitempty"` // overall compression ratio
}

// ToolOutputEntry is written to tool_output_compression.jsonl for each tool
// execution result that passed through the tool-output pipe
// (event_type = "tool_output").
type ToolOutputEntry struct {
	LogEntryBase
	IsMainAgent       bool    `json:"is_main_agent"`
	ProviderModel     string  `json:"model,omitempty"`
	ToolName          string  `json:"tool_name,omitempty"`
	ShadowID          string  `json:"shadow_id,omitempty"`
	StepID            int     `json:"step_id,omitempty"`
	OriginalTokens    int     `json:"original_tokens,omitempty"`
	CompressedTokens  int     `json:"compressed_tokens,omitempty"`
	CompressionRatio  float64 `json:"compression_ratio"`
	CacheHit          bool    `json:"cache_hit"`
	IsLastTool        bool    `json:"is_last_tool,omitempty"`
	Status            string  `json:"status"`
	MinThreshold      int     `json:"min_threshold,omitempty"`
	MaxThreshold      int     `json:"max_threshold,omitempty"`
	CompressionModel  string  `json:"compression_model,omitempty"`
	Query             string  `json:"query,omitempty"`
	QueryAgnostic     bool    `json:"query_agnostic,omitempty"`
	OriginalContent   string  `json:"original_content,omitempty"`
	CompressedContent string  `json:"compressed_content,omitempty"`
}

// TaskOutputEntry is written to task_output_compression.jsonl for subagent results
// processed by the task-output pipe (event_type = "task_output").
type TaskOutputEntry struct {
	LogEntryBase
	IsMainAgent       bool    `json:"is_main_agent"`
	ProviderModel     string  `json:"model,omitempty"`
	ToolName          string  `json:"tool_name,omitempty"`
	OriginalTokens    int     `json:"original_tokens,omitempty"`
	CompressedTokens  int     `json:"compressed_tokens,omitempty"`
	CompressionRatio  float64 `json:"compression_ratio"`
	Status            string  `json:"status"`
	CompressionModel  string  `json:"compression_model,omitempty"`
	OriginalContent   string  `json:"original_content,omitempty"`
	CompressedContent string  `json:"compressed_content,omitempty"`
}

// SessionToolEntry holds name, token count, and full schema for a single tool
// in the per-session catalog.
type SessionToolEntry struct {
	ToolName       string          `json:"tool_name"`
	OriginalTokens int             `json:"original_tokens"`
	Schema         json.RawMessage `json:"schema"`
}

// SessionToolsCatalogFile is the structure written to session_tools.json —
// a human-readable, pretty-printed (indent 4) catalog of every tool seen during
// the session. Unlike the JSONL entry above this is a standalone JSON file that
// is rewritten each time new tools are discovered (e.g. from a subagent).
type SessionToolsCatalogFile struct {
	SessionID string             `json:"session_id"`
	UpdatedAt string             `json:"updated_at"`
	Tools     []SessionToolEntry `json:"tools"`
}

// CONFIG TYPES

// TelemetryConfig contains telemetry configuration.
type TelemetryConfig struct {
	Enabled              bool   `yaml:"enabled"`
	LogPath              string `yaml:"log_path"`
	LogToStdout          bool   `yaml:"log_to_stdout"`
	VerbosePayloads      bool   `yaml:"verbose_payloads"` // Log request/response bodies and headers
	CompressionLogPath   string `yaml:"compression_log_path"`
	ToolDiscoveryLogPath string `yaml:"tool_discovery_log_path"`
	// TaskOutputLogPath is the base path for task output logs.
	// Unified compression log: {base}_compression.jsonl (written by telemetry tracker)
	// Per-provider logs:       {base}_{provider}.jsonl  (written by taskoutput.Logger inside pipe)
	TaskOutputLogPath string `yaml:"task_output_log_path"`
	// SessionToolsPath is the path for the human-readable session_tools.json catalog.
	// Written once per session, updated when subagents introduce new tools.
	SessionToolsPath string `yaml:"session_tools_path"`
	// SessionStatsPath is the path for the live session_stats.json snapshot.
	// Rewritten atomically every ~3 seconds with cumulative session activity counters.
	SessionStatsPath string `yaml:"session_stats_path"`
	// ExpandContextCallsPath is the JSONL log of every expand_context invocation.
	// Each entry contains the original + compressed content that triggered the call —
	// a training signal for compressions the model found too aggressive.
	ExpandContextCallsPath string `yaml:"expand_context_calls_path"`
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
