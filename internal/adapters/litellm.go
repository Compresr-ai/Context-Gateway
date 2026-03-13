// litellm.go implements the LiteLLM adapter for message transformation and usage parsing.
package adapters

// LiteLLMAdapter handles LiteLLM proxy API format requests.
// LiteLLM exposes an OpenAI-compatible API, so this adapter embeds OpenAIAdapter
// and delegates all methods. Unlike Ollama, LiteLLM returns standard OpenAI usage
// format (prompt_tokens/completion_tokens), so no custom usage parsing is needed.
type LiteLLMAdapter struct {
	BaseAdapter
	*OpenAIAdapter
}

// NewLiteLLMAdapter creates a new LiteLLM adapter.
func NewLiteLLMAdapter() *LiteLLMAdapter {
	return &LiteLLMAdapter{
		BaseAdapter: BaseAdapter{
			name:     "litellm",
			provider: ProviderLiteLLM,
		},
		OpenAIAdapter: NewOpenAIAdapter(),
	}
}

// Name returns the adapter name (overrides embedded OpenAIAdapter.Name).
func (a *LiteLLMAdapter) Name() string {
	return a.BaseAdapter.Name()
}

// Provider returns the provider type (overrides embedded OpenAIAdapter.Provider).
func (a *LiteLLMAdapter) Provider() Provider {
	return a.BaseAdapter.Provider()
}

// ExtractUsage extracts token usage from LiteLLM API response.
// LiteLLM returns standard OpenAI format, so we delegate directly.
func (a *LiteLLMAdapter) ExtractUsage(responseBody []byte) UsageInfo {
	return a.OpenAIAdapter.ExtractUsage(responseBody)
}

// =============================================================================
// PARSED REQUEST ADAPTER - Delegate to OpenAI
// =============================================================================

// ParseRequest parses the request body once for reuse.
func (a *LiteLLMAdapter) ParseRequest(body []byte) (*ParsedRequest, error) {
	return a.OpenAIAdapter.ParseRequest(body)
}

// ExtractToolDiscoveryFromParsed extracts tool definitions from a pre-parsed request.
func (a *LiteLLMAdapter) ExtractToolDiscoveryFromParsed(parsed *ParsedRequest, opts *ToolDiscoveryOptions) ([]ExtractedContent, error) {
	return a.OpenAIAdapter.ExtractToolDiscoveryFromParsed(parsed, opts)
}

// ExtractUserQueryFromParsed extracts the last user message from a pre-parsed request.
func (a *LiteLLMAdapter) ExtractUserQueryFromParsed(parsed *ParsedRequest) string {
	return a.OpenAIAdapter.ExtractUserQueryFromParsed(parsed)
}

// ExtractToolOutputFromParsed extracts tool results from a pre-parsed request.
func (a *LiteLLMAdapter) ExtractToolOutputFromParsed(parsed *ParsedRequest) ([]ExtractedContent, error) {
	return a.OpenAIAdapter.ExtractToolOutputFromParsed(parsed)
}

// ApplyToolDiscoveryToParsed filters tools and returns modified body.
func (a *LiteLLMAdapter) ApplyToolDiscoveryToParsed(parsed *ParsedRequest, results []CompressedResult) ([]byte, error) {
	return a.OpenAIAdapter.ApplyToolDiscoveryToParsed(parsed, results)
}

// ExtractAssistantIntent delegates to OpenAI (resolves ambiguity from dual embedding).
func (a *LiteLLMAdapter) ExtractAssistantIntent(body []byte) string {
	return a.OpenAIAdapter.ExtractAssistantIntent(body)
}

// Ensure LiteLLMAdapter implements Adapter and ParsedRequestAdapter
var _ Adapter = (*LiteLLMAdapter)(nil)
var _ ParsedRequestAdapter = (*LiteLLMAdapter)(nil)
