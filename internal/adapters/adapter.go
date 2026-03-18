// Package adapters provides provider-specific request handling.
package adapters

// Adapter defines the unified interface for provider-specific request handling.
// Each adapter implements 2×2 Apply/Extract pairs for the two pipe types.
// Adapters are stateless and thread-safe.
type Adapter interface {
	// Name returns the adapter identifier (e.g., "openai", "anthropic")
	Name() string

	// Provider returns the provider type for this adapter
	Provider() Provider

	// TOOL OUTPUT - Extract/Apply tool results for compression

	// ExtractToolOutput extracts tool result content from messages.
	// OpenAI: messages where role="tool"
	// Anthropic: content blocks where type="tool_result"
	ExtractToolOutput(body []byte) ([]ExtractedContent, error)

	// ApplyToolOutput patches compressed tool results back to the request.
	ApplyToolOutput(body []byte, results []CompressedResult) ([]byte, error)

	// TOOL DISCOVERY - Extract/Apply tool definitions for filtering

	// ExtractToolDiscovery extracts tool definitions for relevance filtering.
	ExtractToolDiscovery(body []byte, opts *ToolDiscoveryOptions) ([]ExtractedContent, error)

	// ApplyToolDiscovery patches filtered tools back to the request.
	ApplyToolDiscovery(body []byte, results []CompressedResult) ([]byte, error)

	// QUERY EXTRACTION - Get user query for compression context

	// ExtractUserQuery extracts the last user message content for compression context.
	// Used by tool_output pipe to provide query context to compression API.
	ExtractUserQuery(body []byte) string

	// ExtractAssistantIntent extracts the LLM's reasoning text from the last
	// assistant message that contains tool calls. This captures WHY the LLM
	// called the tool (e.g., "I'll read the file to understand the code..."),
	// which is more relevant for compression than the original user question.
	ExtractAssistantIntent(body []byte) string

	// ExtractLastUserContent extracts text blocks and tool_result flag from the last user message.
	// Pure structural extraction — returns raw text blocks with no semantic filtering.
	// Anthropic: iterates content blocks, returns type="text" blocks, detects type="tool_result".
	// OpenAI: returns content string from last role="user" message. hasToolResults=false (separate role="tool").
	// Gemini: returns text from parts[] of last user contents entry.
	ExtractLastUserContent(body []byte) (textBlocks []string, hasToolResults bool)

	// USAGE EXTRACTION - Get token usage from API response

	// ExtractUsage extracts token usage from API response body.
	// OpenAI: {"usage": {"prompt_tokens": N, "completion_tokens": N, "total_tokens": N}}
	// Anthropic: {"usage": {"input_tokens": N, "output_tokens": N}}
	ExtractUsage(responseBody []byte) UsageInfo

	// ExtractModel extracts the model name from request body.
	ExtractModel(requestBody []byte) string

	// PHANTOM TOOL OPERATIONS - Response parsing and message construction

	// ExtractToolCallsFromResponse extracts all tool_use/function_call blocks from
	// an LLM response body. Used by the phantom loop to detect phantom tool calls.
	// Anthropic: reads content[] for type:"tool_use" blocks
	// OpenAI Chat: reads choices[0].message.tool_calls[]
	// OpenAI Responses API: reads output[] for type:"function_call" items
	ExtractToolCallsFromResponse(responseBody []byte) ([]ToolCall, error)

	// FilterToolCallFromResponse removes all occurrences of the named tool from the
	// LLM response. Returns the modified response and whether any modification was made.
	// Called before returning the final response to the client to strip phantom tools.
	FilterToolCallFromResponse(responseBody []byte, toolName string) ([]byte, bool)

	// AppendMessages appends an assistant response and tool result messages to a
	// request body. Used by the phantom loop to build the next iteration's request.
	// Anthropic/OpenAI Chat: appends to messages[]
	// OpenAI Responses API: appends function_call + function_call_output items to input[]
	AppendMessages(body []byte, assistantResponse []byte, toolResults []map[string]any) ([]byte, error)

	// BuildToolResultMessages constructs tool result message(s) in this adapter's
	// native format. The requestBody is used to detect Responses API vs Chat format.
	// Anthropic: ONE user message with grouped tool_result content blocks
	// OpenAI Chat: one role:"tool" message per call
	// OpenAI Responses API: one type:"function_call_output" item per call
	BuildToolResultMessages(calls []ToolCall, contentPerCall []string, requestBody []byte) []map[string]any

	// TURN SIGNAL - Classify the LLM's stop reason

	// ExtractTurnSignal returns the normalized TurnSignal for the completed response.
	// streamStopReason is pre-extracted from the SSE stream (empty for non-streaming).
	// responseBody is the full response body (used for non-streaming extraction).
	// Adapters map their native stop reason strings to the TurnSignal enum.
	ExtractTurnSignal(responseBody []byte, streamStopReason string) TurnSignal
}

// BaseAdapter provides common functionality for all adapters.
type BaseAdapter struct {
	name     string
	provider Provider
}

// Name returns the adapter name.
func (a *BaseAdapter) Name() string {
	return a.name
}

// Provider returns the provider type.
func (a *BaseAdapter) Provider() Provider {
	return a.provider
}

// ExtractAssistantIntent default implementation returns empty string.
// Overridden by Anthropic and OpenAI adapters.
func (a *BaseAdapter) ExtractAssistantIntent(_ []byte) string {
	return ""
}

// ExtractTurnSignal default implementation returns TurnSignalUnknown.
// Anthropic, OpenAI, and Gemini adapters provide concrete implementations.
func (a *BaseAdapter) ExtractTurnSignal(_ []byte, _ string) TurnSignal {
	return TurnSignalUnknown
}
