// minimax.go implements the MiniMax adapter for message transformation and usage parsing.
package adapters

// MiniMaxAdapter handles MiniMax API format requests.
// MiniMax exposes an OpenAI-compatible API (https://api.minimax.io/v1/chat/completions),
// so this adapter embeds OpenAIAdapter and delegates all methods.
// MiniMax returns standard OpenAI usage format (prompt_tokens/completion_tokens),
// so no custom usage parsing is needed.
type MiniMaxAdapter struct {
	BaseAdapter
	*OpenAIAdapter
}

// NewMiniMaxAdapter creates a new MiniMax adapter.
func NewMiniMaxAdapter() *MiniMaxAdapter {
	return &MiniMaxAdapter{
		BaseAdapter: BaseAdapter{
			name:     "minimax",
			provider: ProviderMiniMax,
		},
		OpenAIAdapter: NewOpenAIAdapter(),
	}
}

// Name returns the adapter name (overrides embedded OpenAIAdapter.Name).
func (a *MiniMaxAdapter) Name() string {
	return a.BaseAdapter.Name()
}

// Provider returns the provider type (overrides embedded OpenAIAdapter.Provider).
func (a *MiniMaxAdapter) Provider() Provider {
	return a.BaseAdapter.Provider()
}

// ExtractUsage extracts token usage from MiniMax API response.
// MiniMax returns standard OpenAI format, so we delegate directly.
func (a *MiniMaxAdapter) ExtractUsage(responseBody []byte) UsageInfo {
	return a.OpenAIAdapter.ExtractUsage(responseBody)
}

// =============================================================================
// PARSED REQUEST ADAPTER - Delegate to OpenAI
// =============================================================================

// ParseRequest parses the request body once for reuse.
func (a *MiniMaxAdapter) ParseRequest(body []byte) (*ParsedRequest, error) {
	return a.OpenAIAdapter.ParseRequest(body)
}

// ExtractToolDiscoveryFromParsed extracts tool definitions from a pre-parsed request.
func (a *MiniMaxAdapter) ExtractToolDiscoveryFromParsed(parsed *ParsedRequest, opts *ToolDiscoveryOptions) ([]ExtractedContent, error) {
	return a.OpenAIAdapter.ExtractToolDiscoveryFromParsed(parsed, opts)
}

// ExtractUserQueryFromParsed extracts the last user message from a pre-parsed request.
func (a *MiniMaxAdapter) ExtractUserQueryFromParsed(parsed *ParsedRequest) string {
	return a.OpenAIAdapter.ExtractUserQueryFromParsed(parsed)
}

// ExtractToolOutputFromParsed extracts tool results from a pre-parsed request.
func (a *MiniMaxAdapter) ExtractToolOutputFromParsed(parsed *ParsedRequest) ([]ExtractedContent, error) {
	return a.OpenAIAdapter.ExtractToolOutputFromParsed(parsed)
}

// ApplyToolDiscoveryToParsed filters tools and returns modified body.
func (a *MiniMaxAdapter) ApplyToolDiscoveryToParsed(parsed *ParsedRequest, results []CompressedResult) ([]byte, error) {
	return a.OpenAIAdapter.ApplyToolDiscoveryToParsed(parsed, results)
}

// ExtractAssistantIntent delegates to OpenAI (resolves ambiguity from dual embedding).
func (a *MiniMaxAdapter) ExtractAssistantIntent(body []byte) string {
	return a.OpenAIAdapter.ExtractAssistantIntent(body)
}

// Ensure MiniMaxAdapter implements Adapter and ParsedRequestAdapter
var _ Adapter = (*MiniMaxAdapter)(nil)
var _ ParsedRequestAdapter = (*MiniMaxAdapter)(nil)
