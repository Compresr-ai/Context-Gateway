// Package adapters types - unified types for provider-specific request handling.
//
// DESIGN: Adapters provide 3×2 Apply/Extract pairs for the three pipe types:
//   - ToolOutput:    Extract/Apply tool results for compression
//   - History:       Extract/Apply conversation history for compression
//   - ToolDiscovery: Extract/Apply tool definitions for filtering
//
// All types needed by adapters, pipes, and gateway are defined here.
// This eliminates circular imports and provides clear contracts.
package adapters

import "encoding/json"

// =============================================================================
// EXTRACTION TYPES - Output from adapter.Extract*()
// =============================================================================

// ExtractedContent is the unified extraction result from any target type.
// Pipes receive this from adapters and process it (compress/filter).
type ExtractedContent struct {
	// ID uniquely identifies this content (tool_call_id, message index, or tool name)
	ID string

	// Content is the raw content to compress/filter
	Content string

	// ContentType provides context (e.g., "tool_result", "user_message", "tool_def")
	ContentType string

	// ToolName is the name of the tool (for tool_output and tool_discovery)
	ToolName string

	// MessageIndex is the position in messages array
	MessageIndex int

	// BlockIndex is the position within content blocks (Anthropic format)
	BlockIndex int

	// Metadata holds provider-specific data needed for Apply
	Metadata map[string]interface{}
}

// =============================================================================
// COMPRESSION RESULT - Input to adapter.Apply*()
// =============================================================================

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
}

// =============================================================================
// EXTRACT OPTIONS - Configuration for extraction
// =============================================================================

// HistoryOptions provides additional context for history extraction.
type HistoryOptions struct {
	// KeepRecent is the number of recent messages to exclude from extraction
	KeepRecent int
}

// ToolDiscoveryOptions provides context for tool filtering.
type ToolDiscoveryOptions struct {
	// Query is the user's current query (for relevance filtering)
	Query string
}

// =============================================================================
// TOOL TYPES - Unified tool representation
// =============================================================================

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

// =============================================================================
// PROVIDER TYPES - Used for identification and routing
// =============================================================================

// Provider identifies which LLM provider format is being used.
type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
	ProviderGemini    Provider = "gemini"
	ProviderBedrock   Provider = "bedrock"
	ProviderOllama    Provider = "ollama"
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
	default:
		return ProviderUnknown
	}
}

// =============================================================================
// USAGE TYPES - Token usage extracted from API response
// =============================================================================

// UsageInfo holds token usage extracted from API response.
type UsageInfo struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}
