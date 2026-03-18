// Package adapters types - unified types for provider-specific request handling.
package adapters

import "encoding/json"

// DeferredStubDescription is the description embedded in all deferred tool stubs.
// It is a single constant string so all stubs are byte-identical regardless of tool name,
// preserving KV-cache prefix stability across requests.
// The short "[deferred]" marker is also used by tests and helper functions to
// identify stubs when counting effective (non-deferred) tools.
const DeferredStubDescription = "[deferred]"

// EXTRACTION TYPES - Output from adapter.Extract*()

// ExtractedContent is the unified extraction result from any target type.
// Pipes receive this from adapters and process it (compress/filter).
type ExtractedContent struct {
	// ID uniquely identifies this content (tool_call_id, message index, or tool name)
	ID string

	// Content is the raw content to compress/filter
	Content string

	// ContentType provides context (e.g., "tool_result", "user_message", "tool_def")
	ContentType string

	// Format is the detected content format (json, markdown, text, unknown).
	// Set by adapters during extraction via DetectContentFormat.
	// Used by the tool_output pipe to gate compression by format.
	Format ContentFormat

	// ToolName is the name of the tool (for tool_output and tool_discovery)
	ToolName string

	// MessageIndex is the position in messages array
	MessageIndex int

	// BlockIndex is the position within content blocks (Anthropic format)
	BlockIndex int

	// Metadata holds provider-specific data needed for Apply
	Metadata map[string]any
}

// COMPRESSION RESULT - Input to adapter.Apply*()

// CompressedResult is what pipes return after compression/filtering.
// Adapters use this to patch the modified content back into the request.
type CompressedResult struct {
	// ID matches ExtractedContent.ID
	ID string

	// Compressed is the compressed/filtered content
	Compressed string

	// ShadowRef is the reference ID for expand_context (tool_output only)
	ShadowRef string

	// Keep indicates whether to keep this item (tool_discovery filtering)
	Keep bool

	// MessageIndex is the position in messages array (from ExtractedContent).
	// Used by sjson-based ApplyToolOutput to replace content at exact byte path
	// without re-serializing the entire JSON (preserves KV-cache prefix).
	MessageIndex int

	// BlockIndex is the position within content blocks (Anthropic format).
	// Used together with MessageIndex for precise sjson path targeting.
	BlockIndex int
}

// EXTRACT OPTIONS - Configuration for extraction

// ToolDiscoveryOptions provides context for tool filtering.
type ToolDiscoveryOptions struct {
	// Query is the user's current query (for relevance filtering)
	Query string
}

// TOOL TYPES - Unified tool representation

// Tool represents a tool definition available to the LLM.
type Tool struct {
	Type     string       `json:"type"` // Always "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction contains the function schema.
type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"` // JSON Schema
}

// PROVIDER TYPES - Used for identification and routing

// Provider identifies which LLM provider format is being used.
type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
	ProviderGemini    Provider = "gemini"
	ProviderBedrock   Provider = "bedrock"
	ProviderOllama    Provider = "ollama"
	ProviderLiteLLM   Provider = "litellm"
	ProviderMiniMax   Provider = "minimax"
	ProviderUnknown   Provider = "unknown"
)

// String returns the provider name.
func (p Provider) String() string {
	return string(p)
}

// ProviderFromString converts a string to a Provider type.
func ProviderFromString(s string) Provider {
	switch s {
	case "anthropic":
		return ProviderAnthropic
	case "openai":
		return ProviderOpenAI
	case "gemini":
		return ProviderGemini
	case "bedrock":
		return ProviderBedrock
	case "ollama":
		return ProviderOllama
	case "litellm":
		return ProviderLiteLLM
	case "minimax":
		return ProviderMiniMax
	default:
		return ProviderUnknown
	}
}

// USAGE TYPES - Token usage extracted from API response

// UsageInfo holds token usage extracted from API response.
type UsageInfo struct {
	InputTokens              int
	OutputTokens             int
	TotalTokens              int
	CacheCreationInputTokens int // Tokens written to cache (Anthropic: 1.25x input price)
	CacheReadInputTokens     int // Tokens read from cache (Anthropic: 0.1x, OpenAI: 0.5x)
}

// PARSED REQUEST - Single-parse optimization for tool discovery

// ParsedRequest holds a pre-parsed request body to avoid repeated JSON parsing.
// This is an optimization for tool discovery which needs to extract multiple
// pieces of information (tools, user query, tool outputs) from the same body.
type ParsedRequest struct {
	// Raw is the underlying parsed structure (provider-specific type)
	Raw any

	// Messages is the parsed messages array (provider-specific format)
	Messages []any

	// Tools is the parsed tools array (provider-specific format)
	Tools []any

	// OriginalBody preserves the original request bytes so ApplyToolDiscoveryToParsed
	// can delegate to the sjson-based ApplyToolDiscovery instead of re-marshaling
	// the Raw map (which reorders keys and breaks KV-cache prefix stability).
	OriginalBody []byte
}

// PHANTOM TOOL TYPES - Used by phantom loop for provider-agnostic tool call handling

// ToolCall represents a tool call extracted from an LLM response.
// Used by phantom loop to detect and handle phantom tool calls.
type ToolCall struct {
	// ToolUseID is the unique ID for this tool call (tool_use_id, call_id, or tool_call_id)
	ToolUseID string

	// ToolName is the name of the tool being called
	ToolName string

	// Input contains the tool call arguments
	Input map[string]any
}

// TURN SIGNAL - Normalized turn state for dashboard status transitions

// TurnSignal is a provider-agnostic classification of the LLM's stop reason.
// Each adapter maps its native stop reason strings to one of these values.
type TurnSignal int

const (
	// TurnSignalUnknown means the stop reason could not be determined.
	// The caller should not change the current session status.
	TurnSignalUnknown TurnSignal = iota

	// TurnSignalHumanTurn means the agent finished its turn and is waiting
	// for user input. Maps to dashboard.StatusWaitingForHuman.
	//   Anthropic: end_turn, stop_sequence, refusal
	//   OpenAI:    stop, content_filter
	//   Gemini:    STOP (only when no functionCall part present)
	TurnSignalHumanTurn

	// TurnSignalAgentWorking means the agent is mid-loop (called a tool or
	// is using server-side tools). Session stays active.
	//   Anthropic: tool_use, pause_turn
	//   OpenAI:    tool_calls, function_call
	//   Gemini:    STOP with functionCall part, or PROHIBITED_CONTENT (tool loop)
	TurnSignalAgentWorking

	// TurnSignalTruncated means the response was cut off due to token limits.
	// Not a clean turn boundary — the session stays active (same as AgentWorking).
	// The agent did not finish its turn; retrying with a larger context is usually needed.
	//   Anthropic: max_tokens, model_context_window_exceeded
	//   OpenAI:    length
	//   Gemini:    MAX_TOKENS
	TurnSignalTruncated
)

// ParsedRequestAdapter is an optional interface for adapters that support
// single-parse optimization. Adapters implementing this can parse once and
// extract multiple times, avoiding repeated JSON unmarshaling.
type ParsedRequestAdapter interface {
	// ParseRequest parses the request body once for reuse.
	ParseRequest(body []byte) (*ParsedRequest, error)

	// ExtractToolDiscoveryFromParsed extracts tool definitions from a pre-parsed request.
	ExtractToolDiscoveryFromParsed(parsed *ParsedRequest, opts *ToolDiscoveryOptions) ([]ExtractedContent, error)

	// ExtractUserQueryFromParsed extracts the last user message from a pre-parsed request.
	ExtractUserQueryFromParsed(parsed *ParsedRequest) string

	// ExtractToolOutputFromParsed extracts tool results from a pre-parsed request.
	ExtractToolOutputFromParsed(parsed *ParsedRequest) ([]ExtractedContent, error)

	// ApplyToolDiscoveryToParsed filters tools and returns modified body.
	ApplyToolDiscoveryToParsed(parsed *ParsedRequest, results []CompressedResult) ([]byte, error)
}
