package adapters

import (
	"encoding/json"
	"fmt"
	"strings"
)

// GeminiAdapter handles Google Gemini API format requests.
// Gemini uses a unique format with contents[]/parts[] and functionCall/functionResponse
// objects, distinct from both OpenAI and Anthropic formats.
//
// Key format differences:
//   - Tool calls: parts[].functionCall with name/args
//   - Tool responses: parts[].functionResponse with name/response (object, not string)
//   - Usage: usageMetadata.promptTokenCount/candidatesTokenCount/totalTokenCount
//   - Model: in URL path (/models/{model}:generateContent), not request body
type GeminiAdapter struct {
	BaseAdapter
}

// NewGeminiAdapter creates a new Gemini adapter.
func NewGeminiAdapter() *GeminiAdapter {
	return &GeminiAdapter{
		BaseAdapter: BaseAdapter{
			name:     "gemini",
			provider: ProviderGemini,
		},
	}
}

// =============================================================================
// TOOL OUTPUT - Extract/Apply
// =============================================================================

// ExtractToolOutput extracts tool result content from Gemini format.
// Gemini format: contents[] with parts containing functionResponse objects.
//
//	{"contents": [
//	  {"role": "user", "parts": [{"functionResponse": {"name": "read_file", "response": {"content": "..."}}}]}
//	]}
func (a *GeminiAdapter) ExtractToolOutput(body []byte) ([]ExtractedContent, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}

	contents, ok := req["contents"].([]any)
	if !ok {
		return nil, nil
	}

	// Build tool name lookup from model's functionCall parts
	// (not strictly needed since functionResponse already has name, but kept for consistency)
	var extracted []ExtractedContent
	for msgIdx, contentAny := range contents {
		content, ok := contentAny.(map[string]any)
		if !ok {
			continue
		}

		parts, ok := content["parts"].([]any)
		if !ok {
			continue
		}

		for partIdx, partAny := range parts {
			part, ok := partAny.(map[string]any)
			if !ok {
				continue
			}

			fnResp, ok := part["functionResponse"].(map[string]any)
			if !ok {
				continue
			}

			name := getString(fnResp, "name")
			respContent := a.extractResponseContent(fnResp["response"])

			if respContent != "" {
				extracted = append(extracted, ExtractedContent{
					ID:           fmt.Sprintf("%d_%d", msgIdx, partIdx),
					Content:      respContent,
					ContentType:  "tool_result",
					ToolName:     name,
					MessageIndex: msgIdx,
					BlockIndex:   partIdx,
				})
			}
		}
	}

	return extracted, nil
}

// ApplyToolOutput applies compressed tool results back to the Gemini format request.
func (a *GeminiAdapter) ApplyToolOutput(body []byte, results []CompressedResult) ([]byte, error) {
	if len(results) == 0 {
		return body, nil
	}

	resultMap := make(map[string]string)
	for _, r := range results {
		resultMap[r.ID] = r.Compressed
	}

	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}

	contents, ok := req["contents"].([]any)
	if !ok {
		return body, nil
	}

	for msgIdx, contentAny := range contents {
		content, ok := contentAny.(map[string]any)
		if !ok {
			continue
		}

		parts, ok := content["parts"].([]any)
		if !ok {
			continue
		}

		for partIdx, partAny := range parts {
			part, ok := partAny.(map[string]any)
			if !ok {
				continue
			}

			fnResp, ok := part["functionResponse"].(map[string]any)
			if !ok {
				continue
			}

			id := fmt.Sprintf("%d_%d", msgIdx, partIdx)
			if compressed, found := resultMap[id]; found {
				fnResp["response"] = map[string]any{"result": compressed}
				part["functionResponse"] = fnResp
				parts[partIdx] = part
			}
		}

		content["parts"] = parts
		contents[msgIdx] = content
	}

	req["contents"] = contents
	return json.Marshal(req)
}

// =============================================================================
// TOOL DISCOVERY - Extract/Apply (stub)
// =============================================================================

// ExtractToolDiscovery extracts tool definitions for filtering.
func (a *GeminiAdapter) ExtractToolDiscovery(body []byte, opts *ToolDiscoveryOptions) ([]ExtractedContent, error) {
	return nil, nil
}

// ApplyToolDiscovery applies filtered tools back to the request.
func (a *GeminiAdapter) ApplyToolDiscovery(body []byte, results []CompressedResult) ([]byte, error) {
	return body, nil
}

// =============================================================================
// QUERY EXTRACTION
// =============================================================================

// ExtractUserQuery extracts the last user message content from Gemini format.
// Looks for contents[] with role:"user" and text parts.
func (a *GeminiAdapter) ExtractUserQuery(body []byte) string {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}

	contents, ok := req["contents"].([]any)
	if !ok {
		return ""
	}

	// Iterate backwards to find the last user message with text content
	for i := len(contents) - 1; i >= 0; i-- {
		content, ok := contents[i].(map[string]any)
		if !ok {
			continue
		}

		if getString(content, "role") != "user" {
			continue
		}

		parts, ok := content["parts"].([]any)
		if !ok {
			continue
		}

		// Look for text parts (skip functionResponse parts)
		var texts []string
		for _, partAny := range parts {
			part, ok := partAny.(map[string]any)
			if !ok {
				continue
			}
			if text := getString(part, "text"); text != "" {
				texts = append(texts, text)
			}
		}

		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}

	return ""
}

// =============================================================================
// USAGE EXTRACTION
// =============================================================================

// ExtractUsage extracts token usage from Gemini API response.
// Gemini format: {"usageMetadata": {"promptTokenCount": N, "candidatesTokenCount": N, "totalTokenCount": N}}
func (a *GeminiAdapter) ExtractUsage(responseBody []byte) UsageInfo {
	if len(responseBody) == 0 {
		return UsageInfo{}
	}

	var resp struct {
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(responseBody, &resp); err != nil {
		return UsageInfo{}
	}

	return UsageInfo{
		InputTokens:  resp.UsageMetadata.PromptTokenCount,
		OutputTokens: resp.UsageMetadata.CandidatesTokenCount,
		TotalTokens:  resp.UsageMetadata.TotalTokenCount,
	}
}

// =============================================================================
// MODEL EXTRACTION
// =============================================================================

// ExtractModel extracts the model name from Gemini request body.
// Gemini typically puts the model in the URL path (/models/{model}:generateContent),
// not in the request body. Some clients may include a "model" field in the body.
func (a *GeminiAdapter) ExtractModel(requestBody []byte) string {
	if len(requestBody) == 0 {
		return ""
	}

	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(requestBody, &req); err != nil {
		return ""
	}

	// Strip "models/" prefix if present (e.g., "models/gemini-3-flash" -> "gemini-3-flash")
	if strings.HasPrefix(req.Model, "models/") {
		return req.Model[len("models/"):]
	}
	return req.Model
}

// =============================================================================
// HELPERS
// =============================================================================

// extractResponseContent extracts a string from a Gemini functionResponse.response value.
// The response field is typically a JSON object, so we serialize it.
func (a *GeminiAdapter) extractResponseContent(v any) string {
	if v == nil {
		return ""
	}

	// Direct string (unlikely but handle it)
	if s, ok := v.(string); ok {
		return s
	}

	// Object — serialize to JSON string for compression
	if m, ok := v.(map[string]any); ok {
		// If there's a single "result" or "content" or "output" key with a string value, use it directly
		for _, key := range []string{"result", "content", "output"} {
			if s, ok := m[key].(string); ok && len(m) == 1 {
				return s
			}
		}
		// Otherwise serialize the entire object
		b, err := json.Marshal(m)
		if err != nil {
			return ""
		}
		return string(b)
	}

	// Array or other — serialize
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// Ensure GeminiAdapter implements Adapter
var _ Adapter = (*GeminiAdapter)(nil)
