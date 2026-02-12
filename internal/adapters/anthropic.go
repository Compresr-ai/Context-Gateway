package adapters

import (
	"encoding/json"
	"fmt"
)

// AnthropicAdapter handles Anthropic API format requests.
// Anthropic uses content blocks with type:"tool_result" for tool results.
type AnthropicAdapter struct {
	BaseAdapter
}

// NewAnthropicAdapter creates a new Anthropic adapter.
func NewAnthropicAdapter() *AnthropicAdapter {
	return &AnthropicAdapter{
		BaseAdapter: BaseAdapter{
			name:     "anthropic",
			provider: ProviderAnthropic,
		},
	}
}

// =============================================================================
// TOOL OUTPUT - Extract/Apply
// =============================================================================

// ExtractToolOutput extracts tool result content from Anthropic format.
// Anthropic format: {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "xxx", "content": "..."}]}
// Note: content can be string or array of blocks
func (a *AnthropicAdapter) ExtractToolOutput(body []byte) ([]ExtractedContent, error) {
	var req struct {
		Messages []struct {
			Role    string      `json:"role"`
			Content interface{} `json:"content"` // Can be string or array
		} `json:"messages"`
	}

	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}

	// Build tool name lookup map once (avoids O(n²) re-parsing)
	toolNames := make(map[string]string)
	for _, msg := range req.Messages {
		if msg.Role != "assistant" {
			continue
		}
		contentArr, ok := msg.Content.([]interface{})
		if !ok {
			continue
		}
		for _, block := range contentArr {
			blockMap, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			if blockMap["type"] == "tool_use" {
				id, _ := blockMap["id"].(string)
				name, _ := blockMap["name"].(string)
				if id != "" && name != "" {
					toolNames[id] = name
				}
			}
		}
	}

	var extracted []ExtractedContent
	for msgIdx, msg := range req.Messages {
		if msg.Role != "user" {
			continue
		}

		// Content can be string (skip) or array of blocks
		contentArr, ok := msg.Content.([]interface{})
		if !ok {
			continue
		}

		for blockIdx, block := range contentArr {
			blockMap, ok := block.(map[string]interface{})
			if !ok {
				continue
			}

			blockType, _ := blockMap["type"].(string)
			if blockType != "tool_result" {
				continue
			}

			toolUseID, _ := blockMap["tool_use_id"].(string)
			content := a.extractBlockContent(blockMap)

			if content != "" {
				extracted = append(extracted, ExtractedContent{
					ID:           toolUseID,
					Content:      content,
					ContentType:  "tool_result",
					ToolName:     toolNames[toolUseID],
					MessageIndex: msgIdx,
					BlockIndex:   blockIdx,
				})
			}
		}
	}

	return extracted, nil
}

// ApplyToolOutput applies compressed tool results back to the Anthropic format request.
func (a *AnthropicAdapter) ApplyToolOutput(body []byte, results []CompressedResult) ([]byte, error) {
	if len(results) == 0 {
		return body, nil
	}

	// Build lookup map
	resultMap := make(map[string]string)
	for _, r := range results {
		resultMap[r.ID] = r.Compressed
	}

	// Parse and modify
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}

	messages, ok := req["messages"].([]interface{})
	if !ok {
		return body, nil
	}

	for i, msgInterface := range messages {
		msg, ok := msgInterface.(map[string]interface{})
		if !ok {
			continue
		}

		role, _ := msg["role"].(string)
		if role != "user" {
			continue
		}

		content, ok := msg["content"].([]interface{})
		if !ok {
			continue
		}

		for j, blockInterface := range content {
			block, ok := blockInterface.(map[string]interface{})
			if !ok {
				continue
			}

			blockType, _ := block["type"].(string)
			if blockType != "tool_result" {
				continue
			}

			toolUseID, _ := block["tool_use_id"].(string)
			if compressed, found := resultMap[toolUseID]; found {
				block["content"] = compressed
				content[j] = block
			}
		}

		msg["content"] = content
		messages[i] = msg
	}

	req["messages"] = messages
	return json.Marshal(req)
}

// =============================================================================
// TOOL DISCOVERY - Extract/Apply
// =============================================================================

// ExtractToolDiscovery extracts tool definitions for filtering.
// Should extract tools[] array with name, description, and input_schema.
func (a *AnthropicAdapter) ExtractToolDiscovery(body []byte, opts *ToolDiscoveryOptions) ([]ExtractedContent, error) {
	// TODO: Implement when tool discovery pipe is ready
	return nil, nil
}

// ApplyToolDiscovery applies filtered tools back to the request.
// Should filter tools[] array based on Keep flag in results.
func (a *AnthropicAdapter) ApplyToolDiscovery(body []byte, results []CompressedResult) ([]byte, error) {
	// TODO: Implement when tool discovery pipe is ready
	return body, nil
}

// =============================================================================
// QUERY EXTRACTION
// =============================================================================

// ExtractUserQuery extracts the last user message content from Anthropic format.
// Looks for messages[] with role:"user"
func (a *AnthropicAdapter) ExtractUserQuery(body []byte) string {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}

	messages, ok := req["messages"]
	if !ok || messages == nil {
		return ""
	}
	msgArray, ok := messages.([]any)
	if !ok {
		return ""
	}

	// Iterate backwards to find the last user message
	for i := len(msgArray) - 1; i >= 0; i-- {
		m, ok := msgArray[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role == "user" {
			content := a.extractMessageContent(m["content"])
			if content != "" {
				return content
			}
		}
	}
	return ""
}

// extractMessageContent extracts text from Anthropic message content.
// Content can be a string or an array of content blocks.
func (a *AnthropicAdapter) extractMessageContent(content any) string {
	if content == nil {
		return ""
	}

	// String content
	if str, ok := content.(string); ok {
		return str
	}

	// Array content - extract text blocks
	if arr, ok := content.([]any); ok {
		var text string
		for _, item := range arr {
			if itemMap, ok := item.(map[string]any); ok {
				if itemMap["type"] == "text" {
					if t, ok := itemMap["text"].(string); ok {
						text += t
					}
				}
			}
		}
		return text
	}

	return ""
}

// =============================================================================
// HELPERS
// =============================================================================

// extractBlockContent gets the content string from a tool_result block.
// Content can be a string or an array of content blocks.
func (a *AnthropicAdapter) extractBlockContent(block map[string]interface{}) string {
	content := block["content"]
	if content == nil {
		return ""
	}

	// String content
	if str, ok := content.(string); ok {
		return str
	}

	// Array content - extract text blocks
	if arr, ok := content.([]interface{}); ok {
		var text string
		for _, item := range arr {
			if itemMap, ok := item.(map[string]interface{}); ok {
				if itemMap["type"] == "text" {
					if t, ok := itemMap["text"].(string); ok {
						text += t
					}
				}
			}
		}
		return text
	}

	return ""
}

// =============================================================================
// USAGE EXTRACTION - Extract token usage from API response
// =============================================================================

// ExtractUsage extracts token usage from Anthropic API response.
// Anthropic format: {"usage": {"input_tokens": N, "output_tokens": N}}
func (a *AnthropicAdapter) ExtractUsage(responseBody []byte) UsageInfo {
	if len(responseBody) == 0 {
		return UsageInfo{}
	}

	var resp struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(responseBody, &resp); err != nil {
		return UsageInfo{}
	}

	return UsageInfo{
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
		TotalTokens:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
	}
}

// ExtractModel extracts the model name from Anthropic request body.
func (a *AnthropicAdapter) ExtractModel(requestBody []byte) string {
	if len(requestBody) == 0 {
		return ""
	}

	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(requestBody, &req); err != nil {
		return ""
	}

	// Strip provider prefix if present (e.g., "anthropic/claude-3-5-sonnet" -> "claude-3-5-sonnet")
	if idx := len("anthropic/"); len(req.Model) > idx && req.Model[:idx] == "anthropic/" {
		return req.Model[idx:]
	}
	return req.Model
}

// Ensure AnthropicAdapter implements Adapter
var _ Adapter = (*AnthropicAdapter)(nil)
