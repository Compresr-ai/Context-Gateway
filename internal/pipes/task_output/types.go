// Package taskoutput handles task/subagent output in the compression pipeline.
package taskoutput

import "time"

// PipeName is the identifier for the task output pipe.
const PipeName = "task_output"

// DefaultMinTokens is the minimum token count below which content is not compressed.
const DefaultMinTokens = 256

// maxConcurrentCompressions limits parallel LLM calls per request.
const maxConcurrentCompressions = 10

// defaultExternalTimeout is the fallback timeout for external LLM calls.
const defaultExternalTimeout = 30 * time.Second

// maxResponseTokens is the maximum tokens requested from the compression LLM.
const maxResponseTokens = 2048

// systemPrompt is the instruction sent to the compression LLM.
const systemPrompt = `You are a concise technical summarizer.
Summarize the following task output, preserving:
- Key results, return values, and status codes
- Error messages and failure reasons
- File paths, identifiers, and structured data keys
- Numerical metrics and counts

Omit: verbose stack traces, repetitive lines, raw binary data, and decorative formatting.
Output only the summary — no preamble or meta-commentary.`

// userPromptFmt is the format string for the user prompt (tool name + content).
const userPromptFmt = "Task output from tool %q:\n\n%s"

// CLIENT AGENT

// ClientAgent identifies which AI client is in use.
// Detected at request time from User-Agent headers and stored in PipeContext.
type ClientAgent string

const (
	// ClientUnknown means the client could not be identified.
	ClientUnknown ClientAgent = ""

	// ClientClaudeCode is the Anthropic Claude Code CLI (uses Agent/Task tools).
	ClientClaudeCode ClientAgent = "claude_code"

	// ClientCodex is the OpenAI Codex CLI (uses exec_command/shell_command/wait_agent).
	ClientCodex ClientAgent = "codex"

	// ClientGeneric is a fallback for unknown clients; matches no tools.
	ClientGeneric ClientAgent = "generic"
)

// TASK OUTPUT UNIFIED STRUCT

// TaskOutput is the normalized representation of a task/subagent tool result.
// Provider-specific formatting is parsed by the ClientSchema and stored here
// so downstream processing (compression, logging) is provider-agnostic.
type TaskOutput struct {
	// PrimaryContent is the main output text from the subagent.
	// This is what gets compressed or passed through.
	PrimaryContent string

	// Metadata holds structured metadata extracted from provider-specific formats.
	Metadata TaskOutputMetadata

	// Source is the raw ExtractedContent this was parsed from.
	Source any // adapters.ExtractedContent — kept as any to avoid import cycle
}

// TaskOutputMetadata holds structured info extracted from subagent outputs.
type TaskOutputMetadata struct {
	// AgentID is the subagent identifier (Claude Code: agentId hex string).
	AgentID string

	// ExitCode is the process exit code (Codex exec_command: exit_code).
	ExitCode int

	// TotalTokens is the token usage (Claude Code: total_tokens from <usage> block).
	TotalTokens int

	// ToolUses is the number of tool calls made (Claude Code: tool_uses).
	ToolUses int

	// DurationMS is the execution duration in milliseconds.
	DurationMS int64

	// HasMetadata is true when any structured metadata was successfully parsed.
	HasMetadata bool

	// RawMetadataBlock is the original metadata text (preserved on reconstruct).
	RawMetadataBlock string
}

// EVENT TYPES

// TaskOutputEvent is written to the per-provider JSONL log file.
type TaskOutputEvent struct {
	RequestID        string    `json:"request_id"`
	Timestamp        time.Time `json:"timestamp"`
	Provider         string    `json:"provider"`
	ClientAgent      string    `json:"client_agent,omitempty"`
	ToolName         string    `json:"tool_name"`
	ToolCallID       string    `json:"tool_call_id,omitempty"`
	Strategy         string    `json:"strategy"`
	OriginalTokens   int       `json:"original_tokens"`
	CompressedTokens int       `json:"compressed_tokens,omitempty"`
	// Status is one of: "passthrough", "compressed", "passthrough_small", "error"
	Status   string `json:"status"`
	ErrorMsg string `json:"error,omitempty"`
}
