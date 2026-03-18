// bedrock.go implements the AWS Bedrock adapter for message transformation and usage parsing.
package adapters

import (
	"encoding/json"
	"strings"
)

// BedrockAdapter handles AWS Bedrock API format requests.
// Bedrock with Anthropic models (Claude) uses the same Messages API format
// as direct Anthropic. This adapter embeds *AnthropicAdapter to inherit all
// Extract/Apply operations without delegation boilerplate.
//
// The key differences from direct Anthropic are:
//   - Authentication: AWS SigV4 instead of x-api-key (handled by gateway)
//   - URL pattern: /model/{modelId}/invoke instead of /v1/messages
//   - Model ID format: "anthropic.claude-3-5-sonnet-20241022-v2:0"
//
// METHOD AMBIGUITY: Both BaseAdapter and *AnthropicAdapter (which also embeds
// BaseAdapter) provide Name, Provider, ExtractAssistantIntent, and ExtractTurnSignal.
// Those 4 methods are explicitly delegated below to resolve the ambiguity.
// All other Adapter/ParsedRequestAdapter methods are promoted automatically.
//
// KNOWN LIMITATION — Converse API (/converse, /converse-stream):
// These endpoints use a different camelCase format incompatible with the
// Messages API parsing in AnthropicAdapter (e.g. "toolConfig" vs "tools",
// "inputText" vs text content blocks). Requests via /converse will be routed
// to this adapter but compression pipes will silently produce no-ops for them
// since extraction will find no tool outputs or discoveries to compress.
// Fix requires a dedicated ConverseAdapter with its own Extract/Apply logic.
type BedrockAdapter struct {
	BaseAdapter
	*AnthropicAdapter
}

// NewBedrockAdapter creates a new Bedrock adapter.
func NewBedrockAdapter() *BedrockAdapter {
	return &BedrockAdapter{
		BaseAdapter: BaseAdapter{
			name:     "bedrock",
			provider: ProviderBedrock,
		},
		AnthropicAdapter: NewAnthropicAdapter(),
	}
}

// Name returns the adapter name (overrides embedded AnthropicAdapter.Name via BaseAdapter).
func (a *BedrockAdapter) Name() string { return a.BaseAdapter.Name() }

// Provider returns the provider type (overrides embedded AnthropicAdapter.Provider via BaseAdapter).
func (a *BedrockAdapter) Provider() Provider { return a.BaseAdapter.Provider() }

// ExtractAssistantIntent delegates to AnthropicAdapter (resolves BaseAdapter ambiguity).
func (a *BedrockAdapter) ExtractAssistantIntent(body []byte) string {
	return a.AnthropicAdapter.ExtractAssistantIntent(body)
}

// ExtractTurnSignal delegates to AnthropicAdapter (resolves BaseAdapter ambiguity).
func (a *BedrockAdapter) ExtractTurnSignal(responseBody []byte, streamStopReason string) TurnSignal {
	return a.AnthropicAdapter.ExtractTurnSignal(responseBody, streamStopReason)
}

// MODEL EXTRACTION — Bedrock-specific override

// ExtractModel extracts the model name from Bedrock request body.
// In practice this always returns "" because AWS SDK clients put the model ID
// in the URL path, not the body. Use ExtractModelFromPath for Bedrock requests.
// This method exists to satisfy the Adapter interface; URL-based extraction is
// handled by ExtractModelFromPath.
func (a *BedrockAdapter) ExtractModel(requestBody []byte) string {
	if len(requestBody) == 0 {
		return ""
	}

	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(requestBody, &req); err != nil {
		return ""
	}
	if req.Model != "" {
		return req.Model
	}

	// Bedrock requests from the AWS SDK often don't include a model field
	// in the body since it's in the URL. Try anthropic_version as a signal.
	var bedrockReq struct {
		AnthropicVersion string `json:"anthropic_version"`
	}
	if err := json.Unmarshal(requestBody, &bedrockReq); err == nil && bedrockReq.AnthropicVersion != "" {
		// Body is Anthropic format but no model field — model is in URL
		return ""
	}

	return ""
}

// ExtractModelFromPath extracts the model ID from a Bedrock URL path.
// Path format: /model/{modelId}/invoke or /model/{modelId}/invoke-with-response-stream
// Example: /model/anthropic.claude-3-5-sonnet-20241022-v2:0/invoke
func ExtractModelFromPath(path string) string {
	const prefix = "/model/"
	idx := strings.Index(path, prefix)
	if idx == -1 {
		return ""
	}
	rest := path[idx+len(prefix):]
	if slashIdx := strings.Index(rest, "/"); slashIdx != -1 {
		return rest[:slashIdx]
	}
	return rest
}

// Ensure BedrockAdapter implements Adapter and ParsedRequestAdapter
var _ Adapter = (*BedrockAdapter)(nil)
var _ ParsedRequestAdapter = (*BedrockAdapter)(nil)
