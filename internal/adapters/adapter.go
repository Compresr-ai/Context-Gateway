// Package adapters provides provider-specific request handling.
//
// DESIGN: The gateway supports multiple LLM providers (OpenAI, Anthropic).
// Each has different request formats. Adapters abstract the differences for
// compression pipes using 2×2 Apply/Extract pairs:
//
//   - ToolOutput:    ExtractToolOutput / ApplyToolOutput
//   - ToolDiscovery: ExtractToolDiscovery / ApplyToolDiscovery
//
// FLOW:
//  1. Router identifies provider and gets adapter from registry
//  2. Pipe calls Extract*(body) to get content for processing
//  3. Pipe processes content (compress/filter)
//  4. Pipe calls Apply*(body, results) to patch results back
//
// To add a new provider: implement Adapter interface and register in Registry.
package adapters

// Adapter defines the unified interface for provider-specific request handling.
// Each adapter implements 2×2 Apply/Extract pairs for the two pipe types.
// Adapters are stateless and thread-safe.
type Adapter interface {
	// Name returns the adapter identifier (e.g., "openai", "anthropic")
	Name() string

	// Provider returns the provider type for this adapter
	Provider() Provider

	// =========================================================================
	// TOOL OUTPUT - Extract/Apply tool results for compression
	// =========================================================================

	// ExtractToolOutput extracts tool result content from messages.
	// OpenAI: messages where role="tool"
	// Anthropic: content blocks where type="tool_result"
	ExtractToolOutput(body []byte) ([]ExtractedContent, error)

	// ApplyToolOutput patches compressed tool results back to the request.
	ApplyToolOutput(body []byte, results []CompressedResult) ([]byte, error)

	// =========================================================================
	// TOOL DISCOVERY - Extract/Apply tool definitions for filtering
	// =========================================================================

	// ExtractToolDiscovery extracts tool definitions for relevance filtering.
	ExtractToolDiscovery(body []byte, opts *ToolDiscoveryOptions) ([]ExtractedContent, error)

	// ApplyToolDiscovery patches filtered tools back to the request.
	ApplyToolDiscovery(body []byte, results []CompressedResult) ([]byte, error)

	// =========================================================================
	// QUERY EXTRACTION - Get user query for compression context
	// =========================================================================

	// ExtractUserQuery extracts the last user message content for compression context.
	// Used by tool_output pipe to provide query context to compression API.
	ExtractUserQuery(body []byte) string

	// =========================================================================
	// USAGE EXTRACTION - Get token usage from API response
	// =========================================================================

	// ExtractUsage extracts token usage from API response body.
	// OpenAI: {"usage": {"prompt_tokens": N, "completion_tokens": N, "total_tokens": N}}
	// Anthropic: {"usage": {"input_tokens": N, "output_tokens": N}}
	ExtractUsage(responseBody []byte) UsageInfo

	// ExtractModel extracts the model name from request body.
	ExtractModel(requestBody []byte) string
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
