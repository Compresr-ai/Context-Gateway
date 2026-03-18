// anthropic.go implements the Anthropic Claude adapter for message transformation and usage parsing.
package adapters

import (
	"encoding/json"
	"fmt"
	"strings"

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

// TOOL OUTPUT - Extract/Apply

// ExtractToolOutput extracts tool result content from Anthropic format.
// Anthropic format: {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "xxx", "content": "..."}]}
// Note: content can be string or array of blocks
func (a *AnthropicAdapter) ExtractToolOutput(body []byte) ([]ExtractedContent, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}
	messages, _ := req["messages"].([]any)
	return a.extractToolOutputsFromMessages(messages), nil
}

// extractToolOutputsFromMessages extracts all tool_result blocks from a messages []any slice.
// Shared by ExtractToolOutput (parses from body) and ExtractToolOutputFromParsed (uses pre-parsed).
func (a *AnthropicAdapter) extractToolOutputsFromMessages(messages []any) []ExtractedContent {
	// Step 1: Build tool name lookup from assistant messages (avoids O(n²) re-parsing)
	toolNames := make(map[string]string)
	for _, msgAny := range messages {
		msg, ok := msgAny.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := msg["role"].(string); role != "assistant" {
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

	// Step 2: Extract tool_result blocks from user messages
	var extracted []ExtractedContent
	for msgIdx, msgAny := range messages {
		msg, ok := msgAny.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := msg["role"].(string); role != "user" {
			continue
		}
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
			content := a.extractBlockContent(blockMap)
			if content != "" {
				extracted = append(extracted, ExtractedContent{
					ID:           toolUseID,
					Content:      content,
					ContentType:  "tool_result",
					Format:       DetectContentFormat(content),
					ToolName:     toolNames[toolUseID],
					MessageIndex: msgIdx,
					BlockIndex:   blockIdx,
				})
			}
		}
	}
	return extracted
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

// TOOL DISCOVERY - Extract/Apply

// ExtractToolDiscovery extracts tool definitions for filtering.
// Anthropic format: tools: [{name, description, input_schema}]
func (a *AnthropicAdapter) ExtractToolDiscovery(body []byte, opts *ToolDiscoveryOptions) ([]ExtractedContent, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}
	tools, ok := req["tools"].([]any)
	if !ok || len(tools) == 0 {
		return nil, nil
	}
	return a.extractToolsToContent(tools), nil
}

// extractToolsToContent converts a tools []any slice into []ExtractedContent.
// Shared by ExtractToolDiscovery (parses from body) and ExtractToolDiscoveryFromParsed (uses pre-parsed).
// Stores full tool JSON in Metadata["raw_json"] for later injection.
func (a *AnthropicAdapter) extractToolsToContent(tools []any) []ExtractedContent {
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
		rawJSON, _ := json.Marshal(toolAny)
		extracted = append(extracted, ExtractedContent{
			ID:           name,
			Content:      description,
			ContentType:  "tool_def",
			ToolName:     name,
			MessageIndex: i,
			Metadata: map[string]any{
				"raw_json": string(rawJSON),
			},
		})
	}
	return extracted
}

// buildDeferredStub returns minimal Anthropic-format stub bytes for a deferred tool.
// Deferred tools keep their name so the LLM knows they exist, but have a stripped
// description and empty schema to save tokens.
// Output is deterministic: same toolName → identical bytes every call.
// The description is a constant (DeferredStubDescription) so all stubs are byte-identical
// except for the tool name, preserving KV-cache prefix stability.
func buildDeferredStub(toolName string) []byte {
	nameJSON, _ := json.Marshal(toolName)
	descJSON, _ := json.Marshal(DeferredStubDescription)
	b := make([]byte, 0, 8+len(nameJSON)+16+len(descJSON)+45)
	b = append(b, `{"name":`...)
	b = append(b, nameJSON...)
	b = append(b, `,"description":`...)
	b = append(b, descJSON...)
	b = append(b, `,"input_schema":{"type":"object","properties":{}}}`...)
	return b
}

// ApplyToolDiscovery filters tools based on Keep flag in results.
// Kept tools (Keep=true) are forwarded with their original definition.
// Deferred tools (Keep=false) are replaced with minimal stubs so the tools[]
// array length stays constant across requests — preserving KV-cache prefix.
//
// Server tools (type != "custom", e.g. "web_search_20260209", "bash_20250124") are
// always forwarded as-is regardless of Keep flag. Stubbing them would strip the
// type field, breaking the server-side execution hook keyed on that type.
//
// Uses gjson/sjson to preserve original JSON byte representation outside tools[].
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

	toolsResult := gjson.GetBytes(body, "tools")
	if !toolsResult.Exists() {
		return body, nil
	}

	var newRaw []byte
	newRaw = append(newRaw, '[')
	first := true
	toolsResult.ForEach(func(_, value gjson.Result) bool {
		name := value.Get("name").String()
		if name == "" {
			return true // skip malformed entries
		}
		if !first {
			newRaw = append(newRaw, ',')
		}
		// Server tools have a type field that is not "custom" (e.g. "web_search_20260209").
		// Their type drives server-side execution — always preserve them verbatim.
		toolType := value.Get("type").String()
		isServerTool := toolType != "" && toolType != "custom"
		if keepSet[name] || isServerTool {
			newRaw = append(newRaw, value.Raw...) // full definition
		} else {
			newRaw = append(newRaw, buildDeferredStub(name)...) // minimal stub
		}
		first = false
		return true
	})
	newRaw = append(newRaw, ']')

	return sjson.SetRawBytes(body, "tools", newRaw)
}

// QUERY EXTRACTION

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

// LAST USER CONTENT - Structural extraction for classification

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

// PARSED REQUEST - Single-parse optimization

// ParseRequest parses the request body once for reuse.
// This avoids repeated JSON unmarshaling when extracting multiple pieces of data.
func (a *AnthropicAdapter) ParseRequest(body []byte) (*ParsedRequest, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}

	parsed := &ParsedRequest{
		Raw:          req,
		OriginalBody: body,
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
func (a *AnthropicAdapter) ExtractToolDiscoveryFromParsed(parsed *ParsedRequest, opts *ToolDiscoveryOptions) ([]ExtractedContent, error) {
	if parsed == nil || len(parsed.Tools) == 0 {
		return nil, nil
	}
	return a.extractToolsToContent(parsed.Tools), nil
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
	if parsed == nil {
		return nil, nil
	}
	return a.extractToolOutputsFromMessages(parsed.Messages), nil
}

// ApplyToolDiscoveryToParsed filters tools and returns modified body.
// Delegates to the sjson-based ApplyToolDiscovery via OriginalBody to preserve
// key ordering and KV-cache prefix stability.
func (a *AnthropicAdapter) ApplyToolDiscoveryToParsed(parsed *ParsedRequest, results []CompressedResult) ([]byte, error) {
	if parsed == nil {
		return nil, fmt.Errorf("nil parsed request")
	}
	if len(results) == 0 {
		return parsed.OriginalBody, nil
	}
	return a.ApplyToolDiscovery(parsed.OriginalBody, results)
}

// Ensure AnthropicAdapter implements ParsedRequestAdapter
var _ ParsedRequestAdapter = (*AnthropicAdapter)(nil)

// HELPERS

// extractBlockContent gets the content string from a tool_result block.
// Content can be a string or an array of content blocks.
func (a *AnthropicAdapter) extractBlockContent(block map[string]any) string {
	content := block["content"]
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

// USAGE EXTRACTION - Extract token usage from API response

// ExtractUsage extracts token usage from Anthropic API response.
// Anthropic format: {"usage": {"input_tokens": N, "output_tokens": N, "cache_creation_input_tokens": N, "cache_read_input_tokens": N}}
//
// IMPORTANT: Anthropic's input_tokens represents ONLY the non-cached portion of the input
// (tokens processed after the last cache breakpoint). It does NOT include cache hits.
// Total input = input_tokens + cache_creation_input_tokens + cache_read_input_tokens
// See: https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching
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

	// input_tokens is already the non-cached suffix — no subtraction needed.
	// TotalTokens must include all three input categories.
	return UsageInfo{
		InputTokens:              resp.Usage.InputTokens,
		OutputTokens:             resp.Usage.OutputTokens,
		TotalTokens:              resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens + resp.Usage.OutputTokens,
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

// PHANTOM TOOL OPERATIONS - Response parsing and message construction

// ExtractToolCallsFromResponse extracts tool_use blocks from Anthropic response.
func (a *AnthropicAdapter) ExtractToolCallsFromResponse(responseBody []byte) ([]ToolCall, error) {
	var response map[string]any
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	content, ok := response["content"].([]any)
	if !ok {
		return nil, nil
	}
	var calls []ToolCall
	for _, block := range content {
		blockMap, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if blockMap["type"] != "tool_use" {
			continue
		}
		name, _ := blockMap["name"].(string)
		toolUseID, _ := blockMap["id"].(string)
		input, _ := blockMap["input"].(map[string]any)
		if toolUseID != "" {
			calls = append(calls, ToolCall{ToolUseID: toolUseID, ToolName: name, Input: input})
		}
	}
	return calls, nil
}

// FilterToolCallFromResponse removes a named tool_use block from an Anthropic response.
// Also fixes stop_reason when all tool_use blocks are removed.
func (a *AnthropicAdapter) FilterToolCallFromResponse(responseBody []byte, toolName string) ([]byte, bool) {
	contentRaw := gjson.GetBytes(responseBody, "content")
	if !contentRaw.Exists() {
		return responseBody, false
	}

	var blocks []json.RawMessage
	if err := json.Unmarshal([]byte(contentRaw.Raw), &blocks); err != nil {
		return responseBody, false
	}

	filtered := make([]json.RawMessage, 0, len(blocks))
	modified := false
	hasRemainingToolUse := false

	for _, block := range blocks {
		blockType := gjson.GetBytes(block, "type").String()
		if blockType == "tool_use" && gjson.GetBytes(block, "name").String() == toolName {
			modified = true
			continue
		}
		if blockType == "tool_use" {
			hasRemainingToolUse = true
		}
		filtered = append(filtered, block)
	}

	if !modified {
		return responseBody, false
	}

	filteredJSON, err := json.Marshal(filtered)
	if err != nil {
		return responseBody, false
	}

	result, err := sjson.SetRawBytes(responseBody, "content", filteredJSON)
	if err != nil {
		return responseBody, false
	}

	// Fix stop_reason only if all tool_use blocks were removed
	if !hasRemainingToolUse {
		if gjson.GetBytes(result, "stop_reason").String() == "tool_use" {
			result, err = sjson.SetBytes(result, "stop_reason", "end_turn")
			if err != nil {
				return responseBody, false
			}
		}
	}

	return result, true
}

// AppendMessages appends an assistant response and tool results to an Anthropic request.
// Uses sjson for byte-level modification to preserve KV-cache prefix.
func (a *AnthropicAdapter) AppendMessages(body []byte, assistantResponse []byte, toolResults []map[string]any) ([]byte, error) {
	out := body

	// Append assistant message using raw content bytes
	contentRaw := gjson.GetBytes(assistantResponse, "content")
	if contentRaw.Exists() {
		assistantMsg := []byte(`{"role":"assistant","content":` + contentRaw.Raw + `}`)
		var err error
		out, err = sjson.SetRawBytes(out, "messages.-1", assistantMsg)
		if err != nil {
			return nil, fmt.Errorf("AppendMessages: append assistant message: %w", err)
		}
	}

	// Append tool result messages
	for _, tr := range toolResults {
		trJSON, marshalErr := json.Marshal(tr)
		if marshalErr != nil {
			return nil, fmt.Errorf("AppendMessages: marshal tool result: %w", marshalErr)
		}
		var err error
		out, err = sjson.SetRawBytes(out, "messages.-1", trJSON)
		if err != nil {
			return nil, fmt.Errorf("AppendMessages: append tool result: %w", err)
		}
	}

	return out, nil
}

// BuildToolResultMessages constructs the Anthropic tool result message format.
// Anthropic groups all tool results in ONE user message with tool_result content blocks.
// The requestBody parameter is unused for Anthropic (format is always the same).
func (a *AnthropicAdapter) BuildToolResultMessages(calls []ToolCall, contentPerCall []string, _ []byte) []map[string]any {
	contentBlocks := make([]any, 0, len(calls))
	for i, call := range calls {
		var text string
		if i < len(contentPerCall) {
			text = contentPerCall[i]
		}
		contentBlocks = append(contentBlocks, map[string]any{
			"type":        "tool_result",
			"tool_use_id": call.ToolUseID,
			"content":     text,
		})
	}
	return []map[string]any{{
		"role":    "user",
		"content": contentBlocks,
	}}
}

// ExtractTurnSignal classifies the Anthropic stop reason into a normalized TurnSignal.
//
//	end_turn, stop_sequence, refusal → HumanTurn (agent finished, waiting for user)
//	tool_use, pause_turn             → AgentWorking (tool loop or server-side tool)
//	max_tokens, model_context_window_exceeded → Truncated
func (a *AnthropicAdapter) ExtractTurnSignal(responseBody []byte, streamStopReason string) TurnSignal {
	reason := streamStopReason
	if reason == "" {
		reason = gjson.GetBytes(responseBody, "stop_reason").String()
	}
	switch reason {
	case "tool_use", "pause_turn":
		return TurnSignalAgentWorking
	case "max_tokens", "model_context_window_exceeded":
		return TurnSignalTruncated
	case "end_turn", "stop_sequence", "refusal":
		return TurnSignalHumanTurn
	case "":
		return TurnSignalUnknown
	default:
		return TurnSignalHumanTurn
	}
}

// Ensure AnthropicAdapter implements Adapter
var _ Adapter = (*AnthropicAdapter)(nil)
