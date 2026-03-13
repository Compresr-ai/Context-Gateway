// openai.go implements the OpenAI adapter for message transformation and usage parsing.
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

// =============================================================================
// TOOL OUTPUT - Extract/Apply
// =============================================================================

// ExtractToolOutput extracts tool result content from OpenAI format.
// Supports both Responses API and Chat Completions API formats.
func (a *OpenAIAdapter) ExtractToolOutput(body []byte) ([]ExtractedContent, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}

	// Try Responses API format first (input[])
	if input, ok := req["input"]; ok && input != nil {
		return a.extractResponsesAPI(req)
	}

	// Try Chat Completions format (messages[])
	if messages, ok := req["messages"]; ok && messages != nil {
		return a.extractChatCompletions(req)
	}

	return nil, nil
}

// extractResponsesAPI extracts tool outputs from Responses API format.
// Format: input: [ {type:"function_call", call_id:"...", name:"..."}, {type:"function_call_output", call_id:"...", output:"..."} ]
func (a *OpenAIAdapter) extractResponsesAPI(req map[string]any) ([]ExtractedContent, error) {
	input, ok := req["input"]
	if !ok || input == nil {
		return nil, nil
	}
	items, ok := input.([]any)
	if !ok {
		return nil, nil
	}

	// Build tool name lookup
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

	// Extract function call outputs
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
					ToolName:     toolNames[callID],
					MessageIndex: i,
				})
			}
		}
	}

	return extracted, nil
}

// extractChatCompletions extracts tool outputs from Chat Completions API format.
// Format: messages: [ ..., {role:"assistant", tool_calls:[...]}, {role:"tool", tool_call_id:"...", content:"..."} ]
func (a *OpenAIAdapter) extractChatCompletions(req map[string]any) ([]ExtractedContent, error) {
	messages, ok := req["messages"].([]any)
	if !ok {
		return nil, nil
	}

	// Build tool name lookup from assistant tool_calls
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
			fn, ok := tc["function"].(map[string]any)
			if ok {
				name := getString(fn, "name")
				if callID != "" && name != "" {
					toolNames[callID] = name
				}
			}
		}
	}

	// Extract tool messages
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
				ToolName:     toolNames[callID],
				MessageIndex: i,
			})
		}
	}

	return extracted, nil
}

// ApplyToolOutput applies compressed tool results back to the request.
// Uses sjson for byte-level replacement to preserve JSON field ordering and KV-cache prefix.
// Supports both Responses API and Chat Completions API formats.
func (a *OpenAIAdapter) ApplyToolOutput(body []byte, results []CompressedResult) ([]byte, error) {
	if len(results) == 0 {
		return body, nil
	}

	// Detect format: Responses API has "input", Chat Completions has "messages"
	isResponsesAPI := gjson.GetBytes(body, "input").Exists()

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

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func extractStringContent(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if m, ok := v.(map[string]any); ok {
		if s, ok := m["text"].(string); ok {
			return s
		}
	}
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if s, ok := m["text"].(string); ok && s != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(s)
		}
	}
	return b.String()
}

// =============================================================================
// TOOL DISCOVERY - Extract/Apply
// =============================================================================

// ExtractToolDiscovery extracts tool definitions for filtering.
// Supports both formats:
// - Responses API (flat): tools: [{type: "function", name: "...", description: "...", parameters: {...}}]
// - Chat Completions (nested): tools: [{type: "function", function: {name, description, parameters}}]
// Stores full tool JSON in Metadata["raw_json"] for later injection.
func (a *OpenAIAdapter) ExtractToolDiscovery(body []byte, opts *ToolDiscoveryOptions) ([]ExtractedContent, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}

	tools, ok := req["tools"].([]any)
	if !ok || len(tools) == 0 {
		return nil, nil
	}

	// Detect format: Responses API has "input" key, Chat Completions has "messages" key
	_, hasInput := req["input"]
	isResponsesAPI := hasInput

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

	// Detect format: Responses API has "input" key
	isResponsesAPI := gjson.GetBytes(body, "input").Exists()

	var keptRaw []byte
	keptRaw = append(keptRaw, '[')
	first := true
	toolsResult.ForEach(func(_, value gjson.Result) bool {
		var name string
		if isResponsesAPI {
			name = value.Get("name").String()
		} else {
			name = value.Get("function.name").String()
		}
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
// PARSED REQUEST - Single-parse optimization for tool discovery
// =============================================================================

// ParseRequest parses the request body once for reuse.
// This avoids repeated JSON unmarshaling when extracting multiple pieces of data.
func (a *OpenAIAdapter) ParseRequest(body []byte) (*ParsedRequest, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}

	parsed := &ParsedRequest{
		Raw: req,
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
// Supports both formats:
// - Responses API (flat): tools: [{type: "function", name: "...", description: "...", parameters: {...}}]
// - Chat Completions (nested): tools: [{type: "function", function: {name, description, parameters}}]
func (a *OpenAIAdapter) ExtractToolDiscoveryFromParsed(parsed *ParsedRequest, opts *ToolDiscoveryOptions) ([]ExtractedContent, error) {
	if parsed == nil || len(parsed.Tools) == 0 {
		return nil, nil
	}

	// Detect format from parsed.Raw
	req, _ := parsed.Raw.(map[string]any)
	_, hasInput := req["input"]
	isResponsesAPI := hasInput

	extracted := make([]ExtractedContent, 0, len(parsed.Tools))
	for i, toolAny := range parsed.Tools {
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
func (a *OpenAIAdapter) ExtractUserQueryFromParsed(parsed *ParsedRequest) string {
	if parsed == nil || len(parsed.Messages) == 0 {
		return ""
	}

	req, ok := parsed.Raw.(map[string]any)
	if !ok {
		return ""
	}

	// Check if this is Responses API format (has "input" key)
	if _, hasInput := req["input"]; hasInput {
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

	req, ok := parsed.Raw.(map[string]any)
	if !ok {
		return nil, nil
	}

	// Check if this is Responses API format
	if _, hasInput := req["input"]; hasInput {
		return a.extractToolOutputFromParsedResponsesAPI(parsed)
	}

	// Chat Completions format
	return a.extractToolOutputFromParsedChatCompletions(parsed)
}

// extractToolOutputFromParsedResponsesAPI extracts tool outputs from Responses API format.
func (a *OpenAIAdapter) extractToolOutputFromParsedResponsesAPI(parsed *ParsedRequest) ([]ExtractedContent, error) {
	// Build tool name lookup
	toolNames := make(map[string]string)
	for _, item := range parsed.Messages {
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

	// Extract function call outputs
	var extracted []ExtractedContent
	for i, item := range parsed.Messages {
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
					ToolName:     toolNames[callID],
					MessageIndex: i,
				})
			}
		}
	}

	return extracted, nil
}

// extractToolOutputFromParsedChatCompletions extracts tool outputs from Chat Completions format.
func (a *OpenAIAdapter) extractToolOutputFromParsedChatCompletions(parsed *ParsedRequest) ([]ExtractedContent, error) {
	// Build tool name lookup from assistant tool_calls
	toolNames := make(map[string]string)
	for _, msgAny := range parsed.Messages {
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
			fn, ok := tc["function"].(map[string]any)
			if ok {
				name := getString(fn, "name")
				if callID != "" && name != "" {
					toolNames[callID] = name
				}
			}
		}
	}

	// Extract tool messages
	var extracted []ExtractedContent
	for i, msgAny := range parsed.Messages {
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
				ToolName:     toolNames[callID],
				MessageIndex: i,
			})
		}
	}

	return extracted, nil
}

// ApplyToolDiscoveryToParsed filters tools and returns modified body.
// Supports both Responses API (flat) and Chat Completions (nested) formats.
func (a *OpenAIAdapter) ApplyToolDiscoveryToParsed(parsed *ParsedRequest, results []CompressedResult) ([]byte, error) {
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

	// Detect format: Responses API has "input" key
	_, hasInput := req["input"]
	isResponsesAPI := hasInput

	filtered := make([]any, 0, len(keepSet))
	for _, toolAny := range parsed.Tools {
		tool, ok := toolAny.(map[string]any)
		if !ok {
			continue
		}

		var name string
		if isResponsesAPI {
			// Responses API: flat format
			name = getString(tool, "name")
		} else {
			// Chat Completions: nested format
			fn, ok := tool["function"].(map[string]any)
			if !ok {
				continue
			}
			name = getString(fn, "name")
		}

		if keepSet[name] {
			filtered = append(filtered, toolAny)
		}
	}

	req["tools"] = filtered
	return utils.MarshalNoEscape(req)
}

// Ensure OpenAIAdapter implements ParsedRequestAdapter
var _ ParsedRequestAdapter = (*OpenAIAdapter)(nil)

// =============================================================================
// LAST USER CONTENT - Structural extraction for classification
// =============================================================================

// ExtractLastUserContent extracts text blocks from the last user message.
// OpenAI uses separate role="tool" messages for tool results, so hasToolResults is always false.
// Supports both Responses API (input[]) and Chat Completions (messages[]).
func (a *OpenAIAdapter) ExtractLastUserContent(body []byte) ([]string, bool) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, false
	}

	// Try Responses API format first (input can be string or array)
	if input, ok := req["input"]; ok && input != nil {
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

// =============================================================================
// QUERY EXTRACTION
// =============================================================================

// ExtractUserQuery extracts the last user message content.
// Supports both Responses API (input[]) and Chat Completions (messages[]).
func (a *OpenAIAdapter) ExtractUserQuery(body []byte) string {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}

	// Try Responses API format (input[])
	if input, ok := req["input"]; ok && input != nil {
		items, ok := input.([]any)
		if ok {
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

	// Try Responses API format (input[]) — look for reasoning before function_call
	if input, ok := req["input"].([]any); ok {
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

// =============================================================================
// USAGE EXTRACTION - Extract token usage from API response
// =============================================================================

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

	nonCachedInput := int(promptTokens) - int(cachedTokens)
	if nonCachedInput < 0 {
		nonCachedInput = 0
	}

	return UsageInfo{
		InputTokens:          nonCachedInput,
		OutputTokens:         int(completionTokens),
		TotalTokens:          int(totalTokens),
		CacheReadInputTokens: int(cachedTokens),
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

var _ Adapter = (*OpenAIAdapter)(nil)
