package adapters

import (
	"encoding/json"
	"strings"
)

// BedrockAdapter handles AWS Bedrock API format requests.
// Bedrock with Anthropic models (Claude) uses the same Messages API format
// as direct Anthropic, so this adapter embeds AnthropicAdapter and delegates
// all Extract/Apply operations to it.
//
// The key differences from direct Anthropic are:
//   - Authentication: AWS SigV4 instead of x-api-key (handled by gateway)
//   - URL pattern: /model/{modelId}/invoke instead of /v1/messages
//   - Model ID format: "anthropic.claude-3-5-sonnet-20241022-v2:0"
type BedrockAdapter struct {
	BaseAdapter
	anthropic *AnthropicAdapter
}

// NewBedrockAdapter creates a new Bedrock adapter.
func NewBedrockAdapter() *BedrockAdapter {
	return &BedrockAdapter{
		BaseAdapter: BaseAdapter{
			name:     "bedrock",
			provider: ProviderBedrock,
		},
		anthropic: NewAnthropicAdapter(),
	}
}

// =============================================================================
// TOOL OUTPUT - Delegate to Anthropic
// =============================================================================

// ExtractToolOutput extracts tool result content from Bedrock format.
// Bedrock with Claude uses the same Anthropic Messages format.
func (a *BedrockAdapter) ExtractToolOutput(body []byte) ([]ExtractedContent, error) {
	return a.anthropic.ExtractToolOutput(body)
}

// ApplyToolOutput applies compressed tool results back to the request.
func (a *BedrockAdapter) ApplyToolOutput(body []byte, results []CompressedResult) ([]byte, error) {
	return a.anthropic.ApplyToolOutput(body, results)
}

// =============================================================================
// TOOL DISCOVERY - Delegate to Anthropic
// =============================================================================

// ExtractToolDiscovery extracts tool definitions for filtering.
func (a *BedrockAdapter) ExtractToolDiscovery(body []byte, opts *ToolDiscoveryOptions) ([]ExtractedContent, error) {
	return a.anthropic.ExtractToolDiscovery(body, opts)
}

// ApplyToolDiscovery applies filtered tools back to the request.
func (a *BedrockAdapter) ApplyToolDiscovery(body []byte, results []CompressedResult) ([]byte, error) {
	return a.anthropic.ApplyToolDiscovery(body, results)
}

// =============================================================================
// QUERY EXTRACTION - Delegate to Anthropic
// =============================================================================

// ExtractUserQuery extracts the last user message content.
func (a *BedrockAdapter) ExtractUserQuery(body []byte) string {
	return a.anthropic.ExtractUserQuery(body)
}

// =============================================================================
// USAGE EXTRACTION
// =============================================================================

// ExtractUsage extracts token usage from Bedrock API response.
// Bedrock with Anthropic models returns the same usage format as direct Anthropic:
// {"usage": {"input_tokens": N, "output_tokens": N}}
func (a *BedrockAdapter) ExtractUsage(responseBody []byte) UsageInfo {
	return a.anthropic.ExtractUsage(responseBody)
}

// =============================================================================
// MODEL EXTRACTION
// =============================================================================

// ExtractModel extracts the model name from Bedrock request body.
// Bedrock requests may include a "model" field in the body (like Anthropic)
// or the model ID may only be in the URL path (/model/{modelId}/invoke).
// This method handles the body case; URL-based extraction is handled by
// ExtractModelFromPath.
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
	// Find "/model/" prefix
	const prefix = "/model/"
	idx := strings.Index(path, prefix)
	if idx == -1 {
		return ""
	}

	// Extract everything after /model/ up to the next /
	rest := path[idx+len(prefix):]
	if slashIdx := strings.Index(rest, "/"); slashIdx != -1 {
		return rest[:slashIdx]
	}

	return rest
}

// Ensure BedrockAdapter implements Adapter
var _ Adapter = (*BedrockAdapter)(nil)
