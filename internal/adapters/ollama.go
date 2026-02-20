package adapters

import (
	"encoding/json"
)

// OllamaAdapter handles Ollama API format requests.
// Ollama uses the OpenAI Chat Completions format for requests (messages[], tool_calls[], role: tool),
// so this adapter embeds OpenAIAdapter and delegates all request-side methods.
// The only difference is the response format — Ollama uses prompt_eval_count/eval_count
// instead of OpenAI's prompt_tokens/completion_tokens.
type OllamaAdapter struct {
	BaseAdapter
	*OpenAIAdapter
}

// NewOllamaAdapter creates a new Ollama adapter.
func NewOllamaAdapter() *OllamaAdapter {
	return &OllamaAdapter{
		BaseAdapter: BaseAdapter{
			name:     "ollama",
			provider: ProviderOllama,
		},
		OpenAIAdapter: NewOpenAIAdapter(),
	}
}

// Name returns the adapter name (overrides embedded OpenAIAdapter.Name).
func (a *OllamaAdapter) Name() string {
	return a.BaseAdapter.Name()
}

// Provider returns the provider type (overrides embedded OpenAIAdapter.Provider).
func (a *OllamaAdapter) Provider() Provider {
	return a.BaseAdapter.Provider()
}

// ExtractUsage extracts token usage from Ollama API response.
// Ollama format: {"prompt_eval_count": N, "eval_count": N}
// Also supports OpenAI format as fallback (some Ollama versions return it).
func (a *OllamaAdapter) ExtractUsage(responseBody []byte) UsageInfo {
	if len(responseBody) == 0 {
		return UsageInfo{}
	}

	// Try Ollama-native format first
	var resp struct {
		PromptEvalCount int `json:"prompt_eval_count"`
		EvalCount       int `json:"eval_count"`
	}
	if err := json.Unmarshal(responseBody, &resp); err != nil {
		return UsageInfo{}
	}

	if resp.PromptEvalCount > 0 || resp.EvalCount > 0 {
		return UsageInfo{
			InputTokens:  resp.PromptEvalCount,
			OutputTokens: resp.EvalCount,
			TotalTokens:  resp.PromptEvalCount + resp.EvalCount,
		}
	}

	// Fallback to OpenAI format (some Ollama versions return it)
	return a.OpenAIAdapter.ExtractUsage(responseBody)
}

// =============================================================================
// PARSED REQUEST ADAPTER - Delegate to OpenAI
// =============================================================================

// ParseRequest parses the request body once for reuse.
func (a *OllamaAdapter) ParseRequest(body []byte) (*ParsedRequest, error) {
	return a.OpenAIAdapter.ParseRequest(body)
}

// ExtractToolDiscoveryFromParsed extracts tool definitions from a pre-parsed request.
func (a *OllamaAdapter) ExtractToolDiscoveryFromParsed(parsed *ParsedRequest, opts *ToolDiscoveryOptions) ([]ExtractedContent, error) {
	return a.OpenAIAdapter.ExtractToolDiscoveryFromParsed(parsed, opts)
}

// ExtractUserQueryFromParsed extracts the last user message from a pre-parsed request.
func (a *OllamaAdapter) ExtractUserQueryFromParsed(parsed *ParsedRequest) string {
	return a.OpenAIAdapter.ExtractUserQueryFromParsed(parsed)
}

// ExtractToolOutputFromParsed extracts tool results from a pre-parsed request.
func (a *OllamaAdapter) ExtractToolOutputFromParsed(parsed *ParsedRequest) ([]ExtractedContent, error) {
	return a.OpenAIAdapter.ExtractToolOutputFromParsed(parsed)
}

// ApplyToolDiscoveryToParsed filters tools and returns modified body.
func (a *OllamaAdapter) ApplyToolDiscoveryToParsed(parsed *ParsedRequest, results []CompressedResult) ([]byte, error) {
	return a.OpenAIAdapter.ApplyToolDiscoveryToParsed(parsed, results)
}

// Ensure OllamaAdapter implements Adapter and ParsedRequestAdapter
var _ Adapter = (*OllamaAdapter)(nil)
var _ ParsedRequestAdapter = (*OllamaAdapter)(nil)
