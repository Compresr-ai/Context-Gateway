// openai.go implements the OpenAI adapter for message transformation and usage parsing.
package adapters

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenAIAdapter handles OpenAI API format requests.
// Supports both:
//   - Responses API: input[] array with function_call/function_call_output items
//   - Chat Completions API: messages[] with role="tool" items
type OpenAIAdapter struct {
	BaseAdapter
}

// NewOpenAIAdapter creates a new OpenAI adapter.
func NewOpenAIAdapter() *OpenAIAdapter {
	return &OpenAIAdapter{
		BaseAdapter: BaseAdapter{
			name:     "openai",
			provider: ProviderOpenAI,
		},
	}
}

// TOOL OUTPUT - Extract/Apply

// ExtractToolOutput extracts tool result content from OpenAI format.
// Supports both Responses API and Chat Completions API formats.
func (a *OpenAIAdapter) ExtractToolOutput(body []byte) ([]ExtractedContent, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}
	if input, ok := req["input"]; ok && input != nil {
		items, _ := input.([]any)
		return a.extractResponsesAPIItems(items), nil
	}
	if messages, ok := req["messages"].([]any); ok {
		return a.extractChatCompletionsMessages(messages), nil
	}
	return nil, nil
}

// extractResponsesAPIItems extracts tool outputs from a Responses API input[] slice.
// Shared by ExtractToolOutput and ExtractToolOutputFromParsed.
// Format: [ {type:"function_call", call_id, name}, {type:"function_call_output", call_id, output} ]
func (a *OpenAIAdapter) extractResponsesAPIItems(items []any) []ExtractedContent {
	toolNames := make(map[string]string)
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if typ := getString(m, "type"); typ == "function_call" {
			callID := getString(m, "call_id")
			name := getString(m, "name")
			if callID != "" && name != "" {
				toolNames[callID] = name
			}
		}
	}
	var extracted []ExtractedContent
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if typ := getString(m, "type"); typ == "function_call_output" {
			callID := getString(m, "call_id")
			content := extractStringContent(m["output"])
			if callID != "" && content != "" {
				extracted = append(extracted, ExtractedContent{
					ID:           callID,
					Content:      content,
					ContentType:  "tool_result",
					Format:       DetectContentFormat(content),
					ToolName:     toolNames[callID],
					MessageIndex: i,
				})
			}
		}
	}
	return extracted
}

// extractChatCompletionsMessages extracts tool outputs from a Chat Completions messages[] slice.
// Shared by ExtractToolOutput and ExtractToolOutputFromParsed.
// Format: [ ..., {role:"assistant", tool_calls:[...]}, {role:"tool", tool_call_id, content} ]
func (a *OpenAIAdapter) extractChatCompletionsMessages(messages []any) []ExtractedContent {
	toolNames := make(map[string]string)
	for _, msgAny := range messages {
		msg, ok := msgAny.(map[string]any)
		if !ok {
			continue
		}
		if getString(msg, "role") != "assistant" {
			continue
		}
		toolCalls, ok := msg["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, tcAny := range toolCalls {
			tc, ok := tcAny.(map[string]any)
			if !ok {
				continue
			}
			callID := getString(tc, "id")
			if fn, ok := tc["function"].(map[string]any); ok {
				name := getString(fn, "name")
				if callID != "" && name != "" {
					toolNames[callID] = name
				}
			}
		}
	}
	var extracted []ExtractedContent
	for i, msgAny := range messages {
		msg, ok := msgAny.(map[string]any)
		if !ok {
			continue
		}
		if getString(msg, "role") != "tool" {
			continue
		}
		callID := getString(msg, "tool_call_id")
		content := extractStringContent(msg["content"])
		if callID != "" && content != "" {
			extracted = append(extracted, ExtractedContent{
				ID:           callID,
				Content:      content,
				ContentType:  "tool_result",
				Format:       DetectContentFormat(content),
				ToolName:     toolNames[callID],
				MessageIndex: i,
			})
		}
	}
	return extracted
}

// ApplyToolOutput applies compressed tool results back to the request.
// Uses sjson for byte-level replacement to preserve JSON field ordering and KV-cache prefix.
// Supports both Responses API and Chat Completions API formats.
func (a *OpenAIAdapter) ApplyToolOutput(body []byte, results []CompressedResult) ([]byte, error) {
	if len(results) == 0 {
		return body, nil
	}

	// Detect format: Responses API has "input" but not "messages"
	isResponsesAPI := gjson.GetBytes(body, "input").Exists() && !gjson.GetBytes(body, "messages").Exists()

	modified := body
	// Process in reverse order to maintain correct byte offsets
	for i := len(results) - 1; i >= 0; i-- {
		r := results[i]
		var path string
		if isResponsesAPI {
			// Responses API: input[N].output
			path = fmt.Sprintf("input.%d.output", r.MessageIndex)
		} else {
			// Chat Completions: messages[N].content
			path = fmt.Sprintf("messages.%d.content", r.MessageIndex)
		}
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
// Supports both formats:
// - Responses API (flat): tools: [{type: "function", name: "...", description: "...", parameters: {...}}]
// - Chat Completions (nested): tools: [{type: "function", function: {name, description, parameters}}]
func (a *OpenAIAdapter) ExtractToolDiscovery(body []byte, opts *ToolDiscoveryOptions) ([]ExtractedContent, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}
	tools, ok := req["tools"].([]any)
	if !ok || len(tools) == 0 {
		return nil, nil
	}
	_, hasInput := req["input"]
	_, hasMessages := req["messages"]
	return a.extractToolsToContent(tools, hasInput && !hasMessages), nil
}

// extractToolsToContent converts a tools []any slice into []ExtractedContent.
// Shared by ExtractToolDiscovery (parses from body) and ExtractToolDiscoveryFromParsed (uses pre-parsed).
// Stores full tool JSON in Metadata["raw_json"] for later injection.
func (a *OpenAIAdapter) extractToolsToContent(tools []any, isResponsesAPI bool) []ExtractedContent {
	extracted := make([]ExtractedContent, 0, len(tools))
	for i, toolAny := range tools {
		tool, ok := toolAny.(map[string]any)
		if !ok {
			continue
		}
		var name, description string
		if isResponsesAPI {
			// Responses API: flat format {"type": "function", "name": "...", "description": "..."}
			name = getString(tool, "name")
			description = getString(tool, "description")
			// Fallback: some Codex versions send nested Chat Completions format even with input[]
			if name == "" {
				if fn, ok := tool["function"].(map[string]any); ok {
					name = getString(fn, "name")
					description = getString(fn, "description")
				}
			}
		} else {
			// Chat Completions: nested format {"type": "function", "function": {"name": "..."}}
			fn, ok := tool["function"].(map[string]any)
			if !ok {
				continue
			}
			name = getString(fn, "name")
			description = getString(fn, "description")
		}
		if name == "" {
			continue
		}
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

// buildDeferredStubChat returns minimal Chat Completions format stub for a deferred tool.
// Output is deterministic: same toolName + shortDesc → identical bytes every call.
func buildDeferredStubChat(toolName, shortDesc string) []byte {
	nameJSON, _ := json.Marshal(toolName)
	descJSON, _ := json.Marshal(shortDesc)
	b := make([]byte, 0, 38+len(nameJSON)+len(descJSON)+60)
	b = append(b, `{"type":"function","function":{"name":`...)
	b = append(b, nameJSON...)
	b = append(b, `,"description":`...)
	b = append(b, descJSON...)
	b = append(b, `,"parameters":{"type":"object","properties":{}}}}`...)
	return b
}

// buildDeferredStubResponses returns minimal Responses API format stub for a deferred tool.
// Output is deterministic: same toolName + shortDesc → identical bytes every call.
func buildDeferredStubResponses(toolName, shortDesc string) []byte {
	nameJSON, _ := json.Marshal(toolName)
	descJSON, _ := json.Marshal(shortDesc)
	b := make([]byte, 0, 26+len(nameJSON)+len(descJSON)+50)
	b = append(b, `{"type":"function","name":`...)
	b = append(b, nameJSON...)
	b = append(b, `,"description":`...)
	b = append(b, descJSON...)
	b = append(b, `,"parameters":{"type":"object","properties":{}}}`...)
	return b
}

// ApplyToolDiscovery filters tools based on Keep flag in results.
// Kept tools (Keep=true) are forwarded with their original definition.
// Deferred tools (Keep=false) are replaced with minimal stubs so the tools[]
// array length stays constant across requests — preserving KV-cache prefix.
// Supports both Responses API (flat) and Chat Completions (nested) formats.
func (a *OpenAIAdapter) ApplyToolDiscovery(body []byte, results []CompressedResult) ([]byte, error) {
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

	// Detect format: Responses API has "input" but not "messages"
	isResponsesAPI := gjson.GetBytes(body, "input").Exists() && !gjson.GetBytes(body, "messages").Exists()

	var newRaw []byte
	newRaw = append(newRaw, '[')
	first := true
	toolsResult.ForEach(func(_, value gjson.Result) bool {
		var name string
		if isResponsesAPI {
			name = value.Get("name").String()
		} else {
			name = value.Get("function.name").String()
		}
		if name == "" {
			return true // skip malformed entries
		}
		if !first {
			newRaw = append(newRaw, ',')
		}
		if keepSet[name] {
			newRaw = append(newRaw, value.Raw...) // full definition
		} else {
			if isResponsesAPI {
				newRaw = append(newRaw, buildDeferredStubResponses(name, DeferredStubDescription)...)
			} else {
				newRaw = append(newRaw, buildDeferredStubChat(name, DeferredStubDescription)...)
			}
		}
		first = false
		return true
	})
	newRaw = append(newRaw, ']')

	return sjson.SetRawBytes(body, "tools", newRaw)
}

// PARSED REQUEST - Single-parse optimization for tool discovery

// ParseRequest parses the request body once for reuse.
// This avoids repeated JSON unmarshaling when extracting multiple pieces of data.
func (a *OpenAIAdapter) ParseRequest(body []byte) (*ParsedRequest, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}

	parsed := &ParsedRequest{
		Raw:          req,
		OriginalBody: body,
	}

	// Extract messages (Chat Completions format)
	if messages, ok := req["messages"].([]any); ok {
		parsed.Messages = messages
	}

	// Extract input (Responses API format) - store in Messages for unified access
	if input, ok := req["input"].([]any); ok && parsed.Messages == nil {
		parsed.Messages = input
	}

	// Extract tools
	if tools, ok := req["tools"].([]any); ok {
		parsed.Tools = tools
	}

	return parsed, nil
}

// ExtractToolDiscoveryFromParsed extracts tool definitions from a pre-parsed request.
func (a *OpenAIAdapter) ExtractToolDiscoveryFromParsed(parsed *ParsedRequest, opts *ToolDiscoveryOptions) ([]ExtractedContent, error) {
	if parsed == nil || len(parsed.Tools) == 0 {
		return nil, nil
	}
	req, _ := parsed.Raw.(map[string]any)
	_, hasInput := req["input"]
	_, hasMessages := req["messages"]
	return a.extractToolsToContent(parsed.Tools, hasInput && !hasMessages), nil
}

// ExtractUserQueryFromParsed extracts the last user message from a pre-parsed request.
func (a *OpenAIAdapter) ExtractUserQueryFromParsed(parsed *ParsedRequest) string {
	if parsed == nil || len(parsed.Messages) == 0 {
		return ""
	}

	req, ok := parsed.Raw.(map[string]any)
	if !ok {
		return ""
	}

	// Check if this is Responses API format (has "input" but not "messages")
	_, hasInput := req["input"]
	_, hasMessages := req["messages"]
	if hasInput && !hasMessages {
		// Responses API: look for type="message" && role="user"
		for i := len(parsed.Messages) - 1; i >= 0; i-- {
			m, ok := parsed.Messages[i].(map[string]any)
			if !ok {
				continue
			}
			typ := getString(m, "type")
			role := getString(m, "role")
			if typ == "message" && role == "user" {
				content := extractStringContent(m["content"])
				if content != "" {
					return content
				}
			}
		}
		return ""
	}

	// Chat Completions format: look for role="user"
	for i := len(parsed.Messages) - 1; i >= 0; i-- {
		msg, ok := parsed.Messages[i].(map[string]any)
		if !ok {
			continue
		}
		if getString(msg, "role") == "user" {
			content := extractStringContent(msg["content"])
			if content != "" {
				return content
			}
		}
	}
	return ""
}

// ExtractToolOutputFromParsed extracts tool results from a pre-parsed request.
func (a *OpenAIAdapter) ExtractToolOutputFromParsed(parsed *ParsedRequest) ([]ExtractedContent, error) {
	if parsed == nil || len(parsed.Messages) == 0 {
		return nil, nil
	}
	req, _ := parsed.Raw.(map[string]any)
	_, hasInput := req["input"]
	_, hasMessages := req["messages"]
	if hasInput && !hasMessages {
		return a.extractResponsesAPIItems(parsed.Messages), nil
	}
	return a.extractChatCompletionsMessages(parsed.Messages), nil
}

// ApplyToolDiscoveryToParsed filters tools and returns modified body.
// Delegates to the sjson-based ApplyToolDiscovery via OriginalBody to preserve
// key ordering and KV-cache prefix stability.
func (a *OpenAIAdapter) ApplyToolDiscoveryToParsed(parsed *ParsedRequest, results []CompressedResult) ([]byte, error) {
	if parsed == nil {
		return nil, fmt.Errorf("nil parsed request")
	}
	if len(results) == 0 {
		return parsed.OriginalBody, nil
	}
	return a.ApplyToolDiscovery(parsed.OriginalBody, results)
}

// Ensure OpenAIAdapter implements ParsedRequestAdapter
var _ ParsedRequestAdapter = (*OpenAIAdapter)(nil)

// LAST USER CONTENT - Structural extraction for classification

// ExtractLastUserContent extracts text blocks from the last user message.
// OpenAI uses separate role="tool" messages for tool results, so hasToolResults is always false.
// Supports both Responses API (input[]) and Chat Completions (messages[]).
func (a *OpenAIAdapter) ExtractLastUserContent(body []byte) ([]string, bool) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, false
	}

	// Use canonical format detection: Responses API only when input exists and messages does not.
	_, hasMessages := req["messages"]

	// Responses API format (input can be string or array)
	if input, ok := req["input"]; ok && input != nil && !hasMessages {
		// String input: "input": "Say hello briefly."
		if s, ok := input.(string); ok && s != "" {
			return []string{s}, false
		}
		// Array input: "input": [{type: "message", role: "user", content: "..."}]
		if items, ok := input.([]any); ok {
			for i := len(items) - 1; i >= 0; i-- {
				m, ok := items[i].(map[string]any)
				if !ok {
					continue
				}
				if getString(m, "type") == "message" && getString(m, "role") == "user" {
					content := extractStringContent(m["content"])
					if content != "" {
						return []string{content}, false
					}
				}
			}
		}
	}

	// Chat Completions format (messages[])
	if messages, ok := req["messages"].([]any); ok {
		for i := len(messages) - 1; i >= 0; i-- {
			msg, ok := messages[i].(map[string]any)
			if !ok {
				continue
			}
			if getString(msg, "role") == "user" {
				content := extractStringContent(msg["content"])
				if content != "" {
					return []string{content}, false
				}
			}
		}
	}

	return nil, false
}

// QUERY EXTRACTION

// ExtractUserQuery extracts the last user message content.
// Supports both Responses API (input[]) and Chat Completions (messages[]).
func (a *OpenAIAdapter) ExtractUserQuery(body []byte) string {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}

	// Use canonical format detection: Responses API only when input exists and messages does not.
	_, hasMessages := req["messages"]

	// Responses API format (input can be a string or an array)
	if input, ok := req["input"]; ok && input != nil && !hasMessages {
		// String input: "input": "Say hello briefly."
		if s, ok := input.(string); ok {
			text := extractUserText(s)
			if text != "" {
				return text
			}
		}
		// Array input: "input": [{type: "message", role: "user", ...}, ...]
		if items, ok := input.([]any); ok {
			// Iterate backwards to find the last real user message
			for i := len(items) - 1; i >= 0; i-- {
				m, ok := items[i].(map[string]any)
				if !ok {
					continue
				}
				typ := getString(m, "type")
				role := getString(m, "role")
				if typ == "message" && role == "user" {
					content := extractUserText(m["content"])
					if content != "" {
						return content
					}
				}
			}
		}
	}

	// Try Chat Completions format (messages[])
	if messages, ok := req["messages"].([]any); ok {
		// Iterate backwards to find the last real user message
		// Skip tool-role messages and system-reminder injections
		for i := len(messages) - 1; i >= 0; i-- {
			msg, ok := messages[i].(map[string]any)
			if !ok {
				continue
			}
			role := getString(msg, "role")
			// Skip tool results (role=tool in OpenAI format)
			if role == "tool" {
				continue
			}
			if role == "user" {
				content := extractUserText(msg["content"])
				if content != "" {
					return content
				}
			}
		}
	}

	return ""
}

// extractUserText extracts genuine user text, filtering out system reminders.
func extractUserText(content any) string {
	text := extractStringContent(content)
	if text != "" && !strings.HasPrefix(strings.TrimSpace(text), "<system-reminder>") {
		return text
	}
	return ""
}

// ExtractAssistantIntent extracts the LLM's reasoning from the last assistant
// message that contains tool_calls. In OpenAI Chat Completions format, the
// assistant's reasoning is in the content field of the message with tool_calls.
func (a *OpenAIAdapter) ExtractAssistantIntent(body []byte) string {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}

	// Use canonical format detection: Responses API only when input exists and messages does not.
	_, hasMessages := req["messages"]

	// Responses API format (input[]) — look for reasoning before function_call
	if input, ok := req["input"].([]any); ok && !hasMessages {
		for i := len(input) - 1; i >= 0; i-- {
			item, ok := input[i].(map[string]any)
			if !ok {
				continue
			}
			typ := getString(item, "type")
			// function_call items don't have reasoning text — look for preceding message
			if typ == "message" && getString(item, "role") == "assistant" {
				content := extractStringContent(item["content"])
				if content != "" && !strings.HasPrefix(strings.TrimSpace(content), "<system-reminder>") {
					return content
				}
			}
		}
	}

	// Chat Completions format (messages[])
	if messages, ok := req["messages"].([]any); ok {
		for i := len(messages) - 1; i >= 0; i-- {
			msg, ok := messages[i].(map[string]any)
			if !ok {
				continue
			}
			if getString(msg, "role") != "assistant" {
				continue
			}
			if _, hasTools := msg["tool_calls"]; !hasTools {
				continue
			}
			content := extractStringContent(msg["content"])
			if content != "" && !strings.HasPrefix(strings.TrimSpace(content), "<system-reminder>") {
				return content
			}
		}
	}
	return ""
}

// USAGE EXTRACTION - Extract token usage from API response

// ExtractUsage extracts token usage from OpenAI API response.
// Supports both Chat Completions and Responses API field names:
//   - Chat Completions: prompt_tokens, completion_tokens, prompt_tokens_details.cached_tokens
//   - Responses API:    input_tokens, output_tokens, input_tokens_details.cached_tokens
//
// Note: OpenAI's prompt_tokens/input_tokens INCLUDES cached tokens, so we normalize by
// subtracting cached_tokens from InputTokens to match the convention that InputTokens = non-cached only.
func (a *OpenAIAdapter) ExtractUsage(responseBody []byte) UsageInfo {
	if len(responseBody) == 0 {
		return UsageInfo{}
	}

	usage := gjson.GetBytes(responseBody, "usage")
	if !usage.Exists() {
		return UsageInfo{}
	}

	// Try Chat Completions fields first, then Responses API fields
	promptTokens := usage.Get("prompt_tokens").Int()
	if promptTokens == 0 {
		promptTokens = usage.Get("input_tokens").Int()
	}

	completionTokens := usage.Get("completion_tokens").Int()
	if completionTokens == 0 {
		completionTokens = usage.Get("output_tokens").Int()
	}

	totalTokens := usage.Get("total_tokens").Int()

	// Try Chat Completions cached path first, then Responses API cached path
	cachedTokens := usage.Get("prompt_tokens_details.cached_tokens").Int()
	if cachedTokens == 0 {
		cachedTokens = usage.Get("input_tokens_details.cached_tokens").Int()
	}

	// cache_creation_input_tokens: present when OpenAI/LiteLLM proxies an Anthropic backend
	// that uses prompt caching. Populated at the top level of the usage object.
	cacheCreationTokens := usage.Get("cache_creation_input_tokens").Int()

	nonCachedInput := int(promptTokens) - int(cachedTokens)
	if nonCachedInput < 0 {
		nonCachedInput = 0
	}

	return UsageInfo{
		InputTokens:              nonCachedInput,
		OutputTokens:             int(completionTokens),
		TotalTokens:              int(totalTokens),
		CacheReadInputTokens:     int(cachedTokens),
		CacheCreationInputTokens: int(cacheCreationTokens),
	}
}

// ExtractModel extracts the model name from OpenAI request body.
func (a *OpenAIAdapter) ExtractModel(requestBody []byte) string {
	if len(requestBody) == 0 {
		return ""
	}

	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(requestBody, &req); err != nil {
		return ""
	}

	// Strip provider prefix if present (e.g., "openai/gpt-4o" -> "gpt-4o")
	if idx := strings.Index(req.Model, "/"); idx != -1 {
		return req.Model[idx+1:]
	}
	return req.Model
}

// PHANTOM TOOL OPERATIONS - Response parsing and message construction

// ExtractToolCallsFromResponse extracts tool calls from an OpenAI response.
// Handles both Responses API (output[]) and Chat Completions (choices[].message.tool_calls).
func (a *OpenAIAdapter) ExtractToolCallsFromResponse(responseBody []byte) ([]ToolCall, error) {
	var response map[string]any
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// OpenAI Responses API format: output[] with type:"function_call"
	if output, ok := response["output"].([]any); ok {
		var calls []ToolCall
		for _, item := range output {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if itemMap["type"] != "function_call" {
				continue
			}
			name, _ := itemMap["name"].(string)
			callID, _ := itemMap["call_id"].(string)
			argsStr, _ := itemMap["arguments"].(string)
			var input map[string]any
			if err := json.Unmarshal([]byte(argsStr), &input); err != nil {
				input = make(map[string]any)
			}
			if callID != "" {
				calls = append(calls, ToolCall{ToolUseID: callID, ToolName: name, Input: input})
			}
		}
		if len(calls) > 0 {
			return calls, nil
		}
	}

	// Chat Completions format: choices[0].message.tool_calls
	choices, ok := response["choices"].([]any)
	if !ok || len(choices) == 0 {
		return nil, nil
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return nil, nil
	}
	message, ok := choice["message"].(map[string]any)
	if !ok {
		return nil, nil
	}
	toolCalls, ok := message["tool_calls"].([]any)
	if !ok {
		return nil, nil
	}

	var calls []ToolCall
	for _, tc := range toolCalls {
		tcMap, ok := tc.(map[string]any)
		if !ok {
			continue
		}
		function, ok := tcMap["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := function["name"].(string)
		toolCallID, _ := tcMap["id"].(string)
		argsStr, _ := function["arguments"].(string)
		var input map[string]any
		if err := json.Unmarshal([]byte(argsStr), &input); err != nil {
			input = make(map[string]any)
		}
		if toolCallID != "" {
			calls = append(calls, ToolCall{ToolUseID: toolCallID, ToolName: name, Input: input})
		}
	}
	return calls, nil
}

// FilterToolCallFromResponse removes a named tool call from an OpenAI response.
// Handles both Responses API (output[]) and Chat Completions (choices[].message.tool_calls).
func (a *OpenAIAdapter) FilterToolCallFromResponse(responseBody []byte, toolName string) ([]byte, bool) {
	// Try Responses API format first: filter output[] items
	outputRaw := gjson.GetBytes(responseBody, "output")
	if outputRaw.Exists() {
		var items []json.RawMessage
		if err := json.Unmarshal([]byte(outputRaw.Raw), &items); err == nil {
			filtered := make([]json.RawMessage, 0, len(items))
			modified := false
			for _, item := range items {
				if gjson.GetBytes(item, "type").String() == "function_call" &&
					gjson.GetBytes(item, "name").String() == toolName {
					modified = true
					continue
				}
				filtered = append(filtered, item)
			}
			if modified {
				filteredJSON, err := json.Marshal(filtered)
				if err != nil {
					return responseBody, false
				}
				result, err := sjson.SetRawBytes(responseBody, "output", filteredJSON)
				if err != nil {
					return responseBody, false
				}
				return result, true
			}
		}
	}

	// Chat Completions format: filter choices[].message.tool_calls
	choicesRaw := gjson.GetBytes(responseBody, "choices")
	if !choicesRaw.Exists() {
		return responseBody, false
	}

	var choices []json.RawMessage
	if err := json.Unmarshal([]byte(choicesRaw.Raw), &choices); err != nil {
		return responseBody, false
	}

	modified := false
	for i, choiceRaw := range choices {
		toolCallsRaw := gjson.GetBytes(choiceRaw, "message.tool_calls")
		if !toolCallsRaw.Exists() {
			continue
		}

		var calls []json.RawMessage
		if err := json.Unmarshal([]byte(toolCallsRaw.Raw), &calls); err != nil {
			continue
		}

		filteredCalls := make([]json.RawMessage, 0, len(calls))
		wasModified := false
		for _, call := range calls {
			if gjson.GetBytes(call, "function.name").String() == toolName {
				wasModified = true
				continue
			}
			filteredCalls = append(filteredCalls, call)
		}

		if !wasModified {
			continue
		}
		modified = true

		updated := choiceRaw
		var err error
		if len(filteredCalls) == 0 {
			updated, err = sjson.DeleteBytes(updated, "message.tool_calls")
			if err != nil {
				return responseBody, false
			}
			if gjson.GetBytes(updated, "finish_reason").String() == "tool_calls" {
				updated, err = sjson.SetBytes(updated, "finish_reason", "stop")
				if err != nil {
					return responseBody, false
				}
			}
		} else {
			filteredJSON, marshalErr := json.Marshal(filteredCalls)
			if marshalErr != nil {
				return responseBody, false
			}
			updated, err = sjson.SetRawBytes(updated, "message.tool_calls", filteredJSON)
			if err != nil {
				return responseBody, false
			}
		}
		choices[i] = updated
	}

	if !modified {
		return responseBody, false
	}

	choicesJSON, err := json.Marshal(choices)
	if err != nil {
		return responseBody, false
	}
	result, err := sjson.SetRawBytes(responseBody, "choices", choicesJSON)
	if err != nil {
		return responseBody, false
	}
	return result, true
}

// AppendMessages appends an assistant response and tool results to an OpenAI request.
// Detects Responses API vs Chat Completions from the request body format.
func (a *OpenAIAdapter) AppendMessages(body []byte, assistantResponse []byte, toolResults []map[string]any) ([]byte, error) {
	// Detect format from request body
	if gjson.GetBytes(body, "input").Exists() && !gjson.GetBytes(body, "messages").Exists() {
		return a.appendMessagesResponsesAPI(body, assistantResponse, toolResults)
	}
	return a.appendMessagesChatCompletions(body, assistantResponse, toolResults)
}

// appendMessagesChatCompletions appends messages to a Chat Completions request.
func (a *OpenAIAdapter) appendMessagesChatCompletions(body []byte, assistantResponse []byte, toolResults []map[string]any) ([]byte, error) {
	out := body

	// Append assistant message (choices[0].message used as-is)
	messageRaw := gjson.GetBytes(assistantResponse, "choices.0.message")
	if messageRaw.Exists() {
		var err error
		out, err = sjson.SetRawBytes(out, "messages.-1", []byte(messageRaw.Raw))
		if err != nil {
			return nil, fmt.Errorf("AppendMessages: append assistant message: %w", err)
		}
	}

	// Append tool results
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

// appendMessagesResponsesAPI appends messages to a Responses API request.
func (a *OpenAIAdapter) appendMessagesResponsesAPI(body []byte, assistantResponse []byte, toolResults []map[string]any) ([]byte, error) {
	var response map[string]any
	if err := json.Unmarshal(assistantResponse, &response); err != nil {
		return nil, err
	}

	result := body

	// Extract function_call items from output[] and append to input[]
	if output, ok := response["output"].([]any); ok {
		for _, item := range output {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if itemMap["type"] != "function_call" {
				continue
			}
			itemJSON, err := json.Marshal(itemMap)
			if err != nil {
				continue
			}
			result, err = sjson.SetRawBytes(result, "input.-1", itemJSON)
			if err != nil {
				return nil, fmt.Errorf("AppendMessages: append function_call to input: %w", err)
			}
		}
	}

	// Append tool results directly to input[]
	for _, tr := range toolResults {
		trJSON, err := json.Marshal(tr)
		if err != nil {
			continue
		}
		result, err = sjson.SetRawBytes(result, "input.-1", trJSON)
		if err != nil {
			return nil, fmt.Errorf("AppendMessages: append tool result to input: %w", err)
		}
	}

	return result, nil
}

// BuildToolResultMessages constructs OpenAI tool result messages.
// Detects Responses API vs Chat Completions from requestBody.
// Responses API: returns [{type:"function_call_output", call_id:..., output:...}] (separate items)
// Chat Completions: returns [{role:"tool", tool_call_id:..., content:...}] (separate messages)
func (a *OpenAIAdapter) BuildToolResultMessages(calls []ToolCall, contentPerCall []string, requestBody []byte) []map[string]any {
	isResponses := gjson.GetBytes(requestBody, "input").Exists() && !gjson.GetBytes(requestBody, "messages").Exists()

	results := make([]map[string]any, 0, len(calls))
	for i, call := range calls {
		var text string
		if i < len(contentPerCall) {
			text = contentPerCall[i]
		}
		if isResponses {
			results = append(results, map[string]any{
				"type":    "function_call_output",
				"call_id": call.ToolUseID,
				"output":  text,
			})
		} else {
			results = append(results, map[string]any{
				"role":         "tool",
				"tool_call_id": call.ToolUseID,
				"content":      text,
			})
		}
	}
	return results
}

// ExtractTurnSignal classifies the OpenAI finish_reason into a normalized TurnSignal.
//
//	stop, content_filter         → HumanTurn
//	tool_calls, function_call    → AgentWorking
//	length                       → Truncated
func (a *OpenAIAdapter) ExtractTurnSignal(responseBody []byte, streamStopReason string) TurnSignal {
	reason := streamStopReason
	if reason == "" {
		reason = gjson.GetBytes(responseBody, "choices.0.finish_reason").String()
	}
	switch reason {
	case "tool_calls", "function_call":
		return TurnSignalAgentWorking
	case "length":
		return TurnSignalTruncated
	case "stop", "content_filter":
		return TurnSignalHumanTurn
	case "":
		return TurnSignalUnknown
	default:
		return TurnSignalHumanTurn
	}
}

var _ Adapter = (*OpenAIAdapter)(nil)
