// litellm.go implements the LiteLLM adapter for message transformation and usage parsing.
package adapters

// LiteLLMAdapter handles LiteLLM proxy API format requests.
// LiteLLM exposes an OpenAI-compatible API, so this adapter embeds OpenAIAdapter
// and delegates all methods.
//
// Usage note: when LiteLLM fronts an Anthropic backend, responses may include
// Anthropic-style cache fields (cache_creation_input_tokens, cache_read_input_tokens)
// alongside standard OpenAI usage fields. OpenAIAdapter.ExtractUsage handles both
// formats, so no custom usage parsing is needed in this adapter.
// LiteLLMAdapter embeds both BaseAdapter and *OpenAIAdapter, which creates ambiguous
// selectors for methods implemented on both. Any method that exists on both embedded
// types MUST be explicitly delegated below (e.g. Name, Provider, ExtractAssistantIntent,
// ExtractTurnSignal). Do not remove those delegation stubs without resolving the ambiguity.
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

// ExtractAssistantIntent delegates to OpenAI (resolves ambiguity from dual embedding).
func (a *LiteLLMAdapter) ExtractAssistantIntent(body []byte) string {
	return a.OpenAIAdapter.ExtractAssistantIntent(body)
}

// ExtractTurnSignal delegates to OpenAI (resolves ambiguity from dual embedding).
func (a *LiteLLMAdapter) ExtractTurnSignal(responseBody []byte, streamStopReason string) TurnSignal {
	return a.OpenAIAdapter.ExtractTurnSignal(responseBody, streamStopReason)
}

// Ensure LiteLLMAdapter implements Adapter and ParsedRequestAdapter
var _ Adapter = (*LiteLLMAdapter)(nil)
var _ ParsedRequestAdapter = (*LiteLLMAdapter)(nil)
