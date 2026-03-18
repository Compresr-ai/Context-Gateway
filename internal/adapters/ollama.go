// ollama.go implements the Ollama adapter for message transformation and usage parsing.
package adapters

import (
	"encoding/json"
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OllamaAdapter handles Ollama API format requests.
// Ollama uses the OpenAI Chat Completions format for requests (messages[], tool_calls[], role: tool),
// so this adapter embeds OpenAIAdapter and delegates all request-side methods.
// The only difference is the response format — Ollama uses prompt_eval_count/eval_count
// instead of OpenAI's prompt_tokens/completion_tokens, and a flat message structure.
//
// Response format differences from OpenAI (native /api/chat endpoint):
//   - Tool calls: top-level "message.tool_calls[].function.{name, arguments}" (not choices[0])
//   - arguments: JSON object directly (not a JSON-encoded string)
//   - No "id" field on tool calls
//
// KNOWN LIMITATIONS (native /api/chat endpoint):
//
//  1. Streaming usage is never captured.
//     Ollama native streaming uses NDJSON (single "\n" delimited) not SSE ("data: ...\n\n").
//     The gateway's SSE parser never fires for native Ollama streams, so all streaming
//     sessions report 0 tokens. Non-streaming sessions work correctly via ExtractUsage.
//     Fix requires a custom NDJSON streaming handler for the Ollama path.
//
// OllamaAdapter embeds both BaseAdapter and *OpenAIAdapter, which creates ambiguous
// selectors for methods implemented on both. Any method that exists on both embedded
// types MUST be explicitly delegated below (e.g. Name, Provider, ExtractAssistantIntent,
// ExtractTurnSignal). Do not remove those delegation stubs without resolving the ambiguity.
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

// ExtractAssistantIntent delegates to OpenAI (resolves ambiguity from dual embedding).
func (a *OllamaAdapter) ExtractAssistantIntent(body []byte) string {
	return a.OpenAIAdapter.ExtractAssistantIntent(body)
}

// ExtractTurnSignal delegates to OpenAI (resolves ambiguity from dual embedding).
func (a *OllamaAdapter) ExtractTurnSignal(responseBody []byte, streamStopReason string) TurnSignal {
	return a.OpenAIAdapter.ExtractTurnSignal(responseBody, streamStopReason)
}

// PHANTOM TOOL OPERATIONS - Ollama-native overrides
//
// Ollama /api/chat response format:
//
//	{
//	  "message": {
//	    "role": "assistant",
//	    "tool_calls": [
//	      {"function": {"name": "...", "arguments": {...}}}
//	    ]
//	  }
//	}
//
// Key differences from OpenAI Chat Completions:
//   - tool_calls lives under top-level "message" (not "choices[0].message")
//   - "arguments" is a JSON object, NOT a JSON-encoded string
//   - Tool calls have no "id" field

// ExtractToolCallsFromResponse extracts tool calls from an Ollama native response.
// Reads from top-level "message.tool_calls[]" with object arguments.
func (a *OllamaAdapter) ExtractToolCallsFromResponse(responseBody []byte) ([]ToolCall, error) {
	toolCallsRaw := gjson.GetBytes(responseBody, "message.tool_calls")
	if !toolCallsRaw.Exists() {
		// Fall back to OpenAI format for OpenAI-compatible Ollama endpoints
		return a.OpenAIAdapter.ExtractToolCallsFromResponse(responseBody)
	}

	var rawCalls []json.RawMessage
	if err := json.Unmarshal([]byte(toolCallsRaw.Raw), &rawCalls); err != nil {
		return nil, fmt.Errorf("ollama: failed to parse tool_calls: %w", err)
	}

	calls := make([]ToolCall, 0, len(rawCalls))
	for i, raw := range rawCalls {
		name := gjson.GetBytes(raw, "function.name").String()
		if name == "" {
			continue
		}
		// arguments is a JSON object (not a string) in Ollama native format
		var input map[string]any
		argsResult := gjson.GetBytes(raw, "function.arguments")
		switch {
		case argsResult.IsObject():
			if err := json.Unmarshal([]byte(argsResult.Raw), &input); err != nil {
				input = make(map[string]any)
			}
		case argsResult.Type == gjson.String:
			// Some Ollama builds may encode arguments as a string — handle gracefully
			if err := json.Unmarshal([]byte(argsResult.String()), &input); err != nil {
				input = make(map[string]any)
			}
		default:
			input = make(map[string]any)
		}
		// Ollama has no tool call id; use a positional placeholder
		id := fmt.Sprintf("ollama_call_%d", i)
		calls = append(calls, ToolCall{ToolUseID: id, ToolName: name, Input: input})
	}
	return calls, nil
}

// FilterToolCallFromResponse removes a named tool call from an Ollama native response.
// Filters from top-level "message.tool_calls[]".
func (a *OllamaAdapter) FilterToolCallFromResponse(responseBody []byte, toolName string) ([]byte, bool) {
	toolCallsRaw := gjson.GetBytes(responseBody, "message.tool_calls")
	if !toolCallsRaw.Exists() {
		// Fall back to OpenAI format for OpenAI-compatible Ollama endpoints
		return a.OpenAIAdapter.FilterToolCallFromResponse(responseBody, toolName)
	}

	var calls []json.RawMessage
	if err := json.Unmarshal([]byte(toolCallsRaw.Raw), &calls); err != nil {
		return responseBody, false
	}

	filtered := make([]json.RawMessage, 0, len(calls))
	modified := false
	for _, call := range calls {
		if gjson.GetBytes(call, "function.name").String() == toolName {
			modified = true
			continue
		}
		filtered = append(filtered, call)
	}
	if !modified {
		return responseBody, false
	}

	filteredJSON, err := json.Marshal(filtered)
	if err != nil {
		return responseBody, false
	}
	result, err := sjson.SetRawBytes(responseBody, "message.tool_calls", filteredJSON)
	if err != nil {
		return responseBody, false
	}
	return result, true
}

// AppendMessages appends an assistant response and tool results to an Ollama request.
// Ollama uses messages[] like OpenAI Chat Completions. The assistant response body
// uses top-level "message" (not "choices[0].message"), so we extract from there first.
func (a *OllamaAdapter) AppendMessages(body []byte, assistantResponse []byte, toolResults []map[string]any) ([]byte, error) {
	out := body

	// Extract assistant message: Ollama uses top-level "message", OpenAI uses "choices[0].message"
	var assistantMsg json.RawMessage
	if msgRaw := gjson.GetBytes(assistantResponse, "message"); msgRaw.Exists() {
		assistantMsg = json.RawMessage(msgRaw.Raw)
	} else if msgRaw := gjson.GetBytes(assistantResponse, "choices.0.message"); msgRaw.Exists() {
		assistantMsg = json.RawMessage(msgRaw.Raw)
	}

	if assistantMsg != nil {
		var err error
		out, err = sjson.SetRawBytes(out, "messages.-1", assistantMsg)
		if err != nil {
			return nil, fmt.Errorf("ollama AppendMessages: append assistant: %w", err)
		}
	}

	for _, tr := range toolResults {
		trJSON, err := json.Marshal(tr)
		if err != nil {
			return nil, fmt.Errorf("ollama AppendMessages: marshal tool result: %w", err)
		}
		var setErr error
		out, setErr = sjson.SetRawBytes(out, "messages.-1", trJSON)
		if setErr != nil {
			return nil, fmt.Errorf("ollama AppendMessages: append tool result: %w", setErr)
		}
	}

	return out, nil
}

// BuildToolResultMessages constructs Ollama tool result messages.
// Ollama expects {"role":"tool","content":"..."} without a tool_call_id.
func (a *OllamaAdapter) BuildToolResultMessages(calls []ToolCall, contentPerCall []string, _ []byte) []map[string]any {
	results := make([]map[string]any, 0, len(calls))
	for i, call := range calls {
		var text string
		if i < len(contentPerCall) {
			text = contentPerCall[i]
		}
		results = append(results, map[string]any{
			"role":    "tool",
			"name":    call.ToolName,
			"content": text,
		})
	}
	return results
}

// Ensure OllamaAdapter implements Adapter and ParsedRequestAdapter
var _ Adapter = (*OllamaAdapter)(nil)
var _ ParsedRequestAdapter = (*OllamaAdapter)(nil)
