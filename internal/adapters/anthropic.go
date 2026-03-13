// anthropic.go implements the Anthropic Claude adapter for message transformation and usage parsing.
package adapters

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/compresr/context-gateway/internal/utils"
	"github.com/rs/zerolog/log"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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
// Uses sjson for byte-level replacement to preserve JSON field ordering and KV-cache prefix.
func (a *AnthropicAdapter) ApplyToolOutput(body []byte, results []CompressedResult) ([]byte, error) {
	if len(results) == 0 {
		return body, nil
	}

	modified := body
	// Process in reverse order to maintain correct byte offsets
	for i := len(results) - 1; i >= 0; i-- {
		r := results[i]
		// Anthropic: messages[N].content[M].content where M is the tool_result block
		path := fmt.Sprintf("messages.%d.content.%d.content", r.MessageIndex, r.BlockIndex)
		var err error
		modified, err = sjson.SetBytes(modified, path, r.Compressed)
		if err != nil {
			log.Warn().Err(err).Str("path", path).Str("id", r.ID).
				Msg("sjson set failed for tool output, skipping")
			continue
		}
	}
	return modified, nil
}

// =============================================================================
// TOOL DISCOVERY - Extract/Apply
// =============================================================================

// ExtractToolDiscovery extracts tool definitions for filtering.
// Anthropic format: tools: [{name, description, input_schema}]
// Stores full tool JSON in Metadata["raw_json"] for later injection.
func (a *AnthropicAdapter) ExtractToolDiscovery(body []byte, opts *ToolDiscoveryOptions) ([]ExtractedContent, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}

	tools, ok := req["tools"].([]any)
	if !ok || len(tools) == 0 {
		return nil, nil
	}

	extracted := make([]ExtractedContent, 0, len(tools))
	for i, toolAny := range tools {
		tool, ok := toolAny.(map[string]any)
		if !ok {
			continue
		}

		name, _ := tool["name"].(string)
		if name == "" {
			continue
		}
		description, _ := tool["description"].(string)

		// Serialize full tool definition for later injection
		rawJSON, _ := json.Marshal(toolAny)

		extracted = append(extracted, ExtractedContent{
			ID:           name,
			Content:      description,
			ContentType:  "tool_def",
			ToolName:     name,
			MessageIndex: i,
			Metadata: map[string]interface{}{
				"raw_json": string(rawJSON),
			},
		})
	}

	return extracted, nil
}

// ApplyToolDiscovery filters tools based on Keep flag in results.
// Uses gjson/sjson to preserve original JSON byte representation and KV-cache prefix.
func (a *AnthropicAdapter) ApplyToolDiscovery(body []byte, results []CompressedResult) ([]byte, error) {
	if len(results) == 0 {
		return body, nil
	}

	keepSet := make(map[string]bool)
	for _, r := range results {
		if r.Keep {
			keepSet[r.ID] = true
		}
	}

	// Extract each kept tool's raw JSON bytes from the original body
	toolsResult := gjson.GetBytes(body, "tools")
	if !toolsResult.Exists() {
		return body, nil
	}

	var keptRaw []byte
	keptRaw = append(keptRaw, '[')
	first := true
	toolsResult.ForEach(func(_, value gjson.Result) bool {
		name := value.Get("name").String()
		if keepSet[name] {
			if !first {
				keptRaw = append(keptRaw, ',')
			}
			keptRaw = append(keptRaw, value.Raw...)
			first = false
		}
		return true
	})
	keptRaw = append(keptRaw, ']')

	return sjson.SetRawBytes(body, "tools", keptRaw)
}

// =============================================================================
// QUERY EXTRACTION
// =============================================================================

// ExtractUserQuery extracts the last real user question from Anthropic format.
// Skips tool_result messages and system-reminder injections to find the actual
// user intent that triggered the tool calls being compressed.
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

	// Iterate backwards to find the last real user message.
	// Skip messages that are only tool_result blocks (not actual user text).
	for i := len(msgArray) - 1; i >= 0; i-- {
		m, ok := msgArray[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role == "user" {
			content := a.extractUserTextContent(m["content"])
			if content != "" {
				return content
			}
		}
	}
	return ""
}

// ExtractAssistantIntent extracts the LLM's reasoning from the last assistant
// message that contains tool_use calls. In Anthropic format, assistant messages
// have content blocks: [{type:"text", text:"reasoning..."}, {type:"tool_use", ...}].
// The text blocks contain the LLM's justification for calling the tool.
func (a *AnthropicAdapter) ExtractAssistantIntent(body []byte) string {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	msgArray, ok := req["messages"].([]any)
	if !ok {
		return ""
	}

	// Iterate backwards to find the last assistant message with tool_use
	for i := len(msgArray) - 1; i >= 0; i-- {
		m, ok := msgArray[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role != "assistant" {
			continue
		}

		arr, ok := m["content"].([]any)
		if !ok {
			continue
		}

		// Check if this assistant message has tool_use blocks
		hasToolUse := false
		var intentText string
		for _, item := range arr {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := block["type"].(string)
			if blockType == "tool_use" {
				hasToolUse = true
			}
			if blockType == "text" {
				if t, ok := block["text"].(string); ok && !isSystemReminder(t) {
					if intentText != "" {
						intentText += " "
					}
					intentText += t
				}
			}
		}

		if hasToolUse && intentText != "" {
			return intentText
		}
	}
	return ""
}

// extractUserTextContent extracts only real user text from a message,
// filtering out tool_result blocks and system-reminder injections.
// Returns empty string if the message has no genuine user text.
func (a *AnthropicAdapter) extractUserTextContent(content any) string {
	if content == nil {
		return ""
	}

	// String content — check for system-reminder
	if str, ok := content.(string); ok {
		if isSystemReminder(str) {
			return ""
		}
		return str
	}

	// Array content — skip tool_result blocks, filter system reminders from text blocks
	arr, ok := content.([]any)
	if !ok {
		return ""
	}

	var text string
	hasToolResult := false
	for _, item := range arr {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := itemMap["type"].(string)
		if itemType == "tool_result" {
			hasToolResult = true
			continue
		}
		if itemType == "text" {
			if t, ok := itemMap["text"].(string); ok && !isSystemReminder(t) {
				text += t
			}
		}
	}

	// If this message is purely tool results (no real user text), return empty
	// so the caller keeps searching backward for the actual user question
	if text == "" && hasToolResult {
		return ""
	}
	return text
}

// isSystemReminder checks if text is a system-reminder injection from the client.
func isSystemReminder(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "<system-reminder>")
}

// =============================================================================
// LAST USER CONTENT - Structural extraction for classification
// =============================================================================

// ExtractLastUserContent extracts text blocks and tool_result flag from the last user message.
// Returns individual text blocks (not concatenated) and whether tool_result blocks exist.
// This fixes Bug D: human text in mixed tool_result + text messages is no longer lost.
func (a *AnthropicAdapter) ExtractLastUserContent(body []byte) ([]string, bool) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, false
	}

	messages, ok := req["messages"].([]any)
	if !ok || len(messages) == 0 {
		return nil, false
	}

	// Find last user message
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "user" {
			continue
		}

		content := msg["content"]
		if content == nil {
			return nil, false
		}

		// String content — always a single text block, never has tool_results
		if str, isStr := content.(string); isStr {
			return []string{str}, false
		}

		// Array content — iterate blocks
		arr, isArr := content.([]any)
		if !isArr {
			return nil, false
		}

		var textBlocks []string
		hasToolResults := false
		for _, item := range arr {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := block["type"].(string)
			switch blockType {
			case "text":
				if text, ok := block["text"].(string); ok && text != "" {
					textBlocks = append(textBlocks, text)
				}
			case "tool_result":
				hasToolResults = true
			}
		}
		return textBlocks, hasToolResults
	}
	return nil, false
}

// =============================================================================
// PARSED REQUEST - Single-parse optimization
// =============================================================================

// ParseRequest parses the request body once for reuse.
// This avoids repeated JSON unmarshaling when extracting multiple pieces of data.
func (a *AnthropicAdapter) ParseRequest(body []byte) (*ParsedRequest, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}

	parsed := &ParsedRequest{
		Raw: req,
	}

	// Extract messages
	if messages, ok := req["messages"].([]any); ok {
		parsed.Messages = messages
	}

	// Extract tools
	if tools, ok := req["tools"].([]any); ok {
		parsed.Tools = tools
	}

	return parsed, nil
}

// ExtractToolDiscoveryFromParsed extracts tool definitions from a pre-parsed request.
// Stores full tool JSON in Metadata["raw_json"] for later injection.
func (a *AnthropicAdapter) ExtractToolDiscoveryFromParsed(parsed *ParsedRequest, opts *ToolDiscoveryOptions) ([]ExtractedContent, error) {
	if parsed == nil || len(parsed.Tools) == 0 {
		return nil, nil
	}

	extracted := make([]ExtractedContent, 0, len(parsed.Tools))
	for i, toolAny := range parsed.Tools {
		tool, ok := toolAny.(map[string]any)
		if !ok {
			continue
		}

		name, _ := tool["name"].(string)
		if name == "" {
			continue
		}
		description, _ := tool["description"].(string)

		// Serialize full tool definition for later injection
		rawJSON, _ := json.Marshal(toolAny)

		extracted = append(extracted, ExtractedContent{
			ID:           name,
			Content:      description,
			ContentType:  "tool_def",
			ToolName:     name,
			MessageIndex: i,
			Metadata: map[string]interface{}{
				"raw_json": string(rawJSON),
			},
		})
	}

	return extracted, nil
}

// ExtractUserQueryFromParsed extracts the last user message from a pre-parsed request.
func (a *AnthropicAdapter) ExtractUserQueryFromParsed(parsed *ParsedRequest) string {
	if parsed == nil || len(parsed.Messages) == 0 {
		return ""
	}

	// Iterate backwards to find the last user message
	for i := len(parsed.Messages) - 1; i >= 0; i-- {
		m, ok := parsed.Messages[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role == "user" {
			content := a.extractUserTextContent(m["content"])
			if content != "" {
				return content
			}
		}
	}
	return ""
}

// ExtractToolOutputFromParsed extracts tool results from a pre-parsed request.
func (a *AnthropicAdapter) ExtractToolOutputFromParsed(parsed *ParsedRequest) ([]ExtractedContent, error) {
	if parsed == nil || len(parsed.Messages) == 0 {
		return nil, nil
	}

	// Build tool name lookup map once (avoids O(n²) re-parsing)
	toolNames := make(map[string]string)
	for _, msgAny := range parsed.Messages {
		msg, ok := msgAny.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "assistant" {
			continue
		}
		contentArr, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range contentArr {
			blockMap, ok := block.(map[string]any)
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
	for msgIdx, msgAny := range parsed.Messages {
		msg, ok := msgAny.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "user" {
			continue
		}

		// Content can be string (skip) or array of blocks
		contentArr, ok := msg["content"].([]any)
		if !ok {
			continue
		}

		for blockIdx, block := range contentArr {
			blockMap, ok := block.(map[string]any)
			if !ok {
				continue
			}

			blockType, _ := blockMap["type"].(string)
			if blockType != "tool_result" {
				continue
			}

			toolUseID, _ := blockMap["tool_use_id"].(string)
			// Convert to map[string]interface{} for extractBlockContent
			blockMapInterface := make(map[string]interface{}, len(blockMap))
			for k, v := range blockMap {
				blockMapInterface[k] = v
			}
			content := a.extractBlockContent(blockMapInterface)

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

// ApplyToolDiscoveryToParsed filters tools and returns modified body.
// Note: ParsedRequest doesn't preserve original bytes, so we use MarshalNoEscape here.
// For KV-cache preservation, prefer the byte-level ApplyToolDiscovery when possible.
func (a *AnthropicAdapter) ApplyToolDiscoveryToParsed(parsed *ParsedRequest, results []CompressedResult) ([]byte, error) {
	if len(results) == 0 || parsed == nil {
		return utils.MarshalNoEscape(parsed.Raw)
	}

	keepSet := make(map[string]bool)
	for _, r := range results {
		if r.Keep {
			keepSet[r.ID] = true
		}
	}

	req, ok := parsed.Raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid parsed request type")
	}

	filtered := make([]any, 0, len(keepSet))
	for _, toolAny := range parsed.Tools {
		tool, ok := toolAny.(map[string]any)
		if !ok {
			continue
		}
		name, _ := tool["name"].(string)
		if keepSet[name] {
			filtered = append(filtered, toolAny)
		}
	}

	req["tools"] = filtered
	return utils.MarshalNoEscape(req)
}

// Ensure AnthropicAdapter implements ParsedRequestAdapter
var _ ParsedRequestAdapter = (*AnthropicAdapter)(nil)

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
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(responseBody, &resp); err != nil {
		return UsageInfo{}
	}

	// Anthropic's input_tokens includes cache_read tokens and cache_creation tokens.
	// Subtract them so InputTokens represents only non-cached input (avoids double-counting in cost calculation).
	nonCachedInput := resp.Usage.InputTokens - resp.Usage.CacheCreationInputTokens - resp.Usage.CacheReadInputTokens
	if nonCachedInput < 0 {
		nonCachedInput = 0
	}

	return UsageInfo{
		InputTokens:              nonCachedInput,
		OutputTokens:             resp.Usage.OutputTokens,
		TotalTokens:              resp.Usage.InputTokens + resp.Usage.OutputTokens,
		CacheCreationInputTokens: resp.Usage.CacheCreationInputTokens,
		CacheReadInputTokens:     resp.Usage.CacheReadInputTokens,
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
