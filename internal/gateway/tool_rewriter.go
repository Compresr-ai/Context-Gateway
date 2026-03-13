// Bidirectional rewriting for the universal dispatcher (gateway_search_tool).
//
// DESIGN: The proxy rewrites tool calls in two directions:
//   - Outbound (LLM -> Client): gateway_search_tool call-mode -> real tool_use
//   - Inbound  (Client -> LLM): real tool_use/tool_result in history -> gateway_search_tool
//
// This keeps the LLM's view consistent (tools=[gateway_search_tool]) while
// the client sees normal tool_use/tool_result exchanges.
package gateway

import (
	"encoding/json"

	"github.com/compresr/context-gateway/internal/utils"
)

// =============================================================================
// OUTBOUND: Rewrite LLM response for client (gateway_search_tool -> real tool)
// =============================================================================

// rewriteResponseForClient transforms gateway_search_tool call-mode blocks
// into real tool_use blocks before sending to the client.
func rewriteResponseForClient(responseBody []byte, mappings []*ToolCallMapping, isAnthropic bool) ([]byte, error) {
	var response map[string]any
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return responseBody, err
	}

	lookup := make(map[string]*ToolCallMapping, len(mappings))
	for _, m := range mappings {
		lookup[m.ProxyToolUseID] = m
	}

	if isAnthropic {
		rewriteAnthropicResponse(response, lookup)
	} else {
		rewriteOpenAIResponse(response, lookup)
	}

	return utils.MarshalNoEscape(response)
}

func rewriteAnthropicResponse(response map[string]any, lookup map[string]*ToolCallMapping) {
	content, ok := response["content"].([]any)
	if !ok {
		return
	}

	for i, block := range content {
		blockMap, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if blockMap["type"] != "tool_use" {
			continue
		}

		id, _ := blockMap["id"].(string)
		mapping, found := lookup[id]
		if !found {
			continue
		}

		blockMap["name"] = mapping.ClientToolName
		blockMap["id"] = mapping.ClientToolUseID
		blockMap["input"] = mapping.OriginalInput
		content[i] = blockMap
	}
	response["content"] = content
}

func rewriteOpenAIResponse(response map[string]any, lookup map[string]*ToolCallMapping) {
	choices, ok := response["choices"].([]any)
	if !ok || len(choices) == 0 {
		return
	}

	choice, ok := choices[0].(map[string]any)
	if !ok {
		return
	}
	message, ok := choice["message"].(map[string]any)
	if !ok {
		return
	}
	toolCalls, ok := message["tool_calls"].([]any)
	if !ok {
		return
	}

	for i, tc := range toolCalls {
		tcMap, ok := tc.(map[string]any)
		if !ok {
			continue
		}
		function, ok := tcMap["function"].(map[string]any)
		if !ok {
			continue
		}

		tcID, _ := tcMap["id"].(string)
		mapping, found := lookup[tcID]
		if !found {
			continue
		}

		function["name"] = mapping.ClientToolName
		argsJSON, _ := json.Marshal(mapping.OriginalInput)
		function["arguments"] = string(argsJSON)
		tcMap["function"] = function
		tcMap["id"] = mapping.ClientToolUseID
		toolCalls[i] = tcMap
	}

	message["tool_calls"] = toolCalls
	choice["message"] = message
	choices[0] = choice
	response["choices"] = choices
}

// =============================================================================
// INBOUND: Rewrite client request for LLM (real tool -> gateway_search_tool)
// =============================================================================

// rewriteInboundMessages transforms all tool_use/tool_result references in the
// message history from client-facing names back to gateway_search_tool.
// Must be called on every inbound request before forwarding to LLM.
func rewriteInboundMessages(body []byte, mappings map[string]*ToolCallMapping, isAnthropic bool, searchToolName string) ([]byte, error) {
	if len(mappings) == 0 {
		return body, nil
	}

	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return body, err
	}

	messages, ok := request["messages"].([]any)
	if !ok {
		// Responses API: check for input[] instead of messages[]
		input, inputOk := request["input"].([]any)
		if !inputOk {
			return body, nil
		}
		return rewriteInboundInputItems(request, input, mappings, searchToolName)
	}

	modified := false

	for i, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}

		role, _ := msgMap["role"].(string)

		var changed bool
		if isAnthropic {
			changed = rewriteAnthropicMessage(msgMap, role, mappings, searchToolName)
		} else {
			changed = rewriteOpenAIMessage(msgMap, role, mappings, searchToolName)
		}

		if changed {
			messages[i] = msgMap
			modified = true
		}
	}

	if !modified {
		return body, nil
	}

	request["messages"] = messages
	return utils.MarshalNoEscape(request)
}

func rewriteAnthropicMessage(msg map[string]any, role string, mappings map[string]*ToolCallMapping, searchToolName string) bool {
	content, ok := msg["content"].([]any)
	if !ok {
		return false
	}

	changed := false

	for i, block := range content {
		blockMap, ok := block.(map[string]any)
		if !ok {
			continue
		}

		blockType, _ := blockMap["type"].(string)

		switch blockType {
		case "tool_use":
			id, _ := blockMap["id"].(string)
			mapping := mappings[id]
			if mapping != nil {
				blockMap["name"] = searchToolName
				blockMap["id"] = mapping.ProxyToolUseID
				blockMap["input"] = map[string]any{
					"tool_name":  mapping.ClientToolName,
					"tool_input": mapping.OriginalInput,
				}
				content[i] = blockMap
				changed = true
			}

		case "tool_result":
			toolUseID, _ := blockMap["tool_use_id"].(string)
			mapping := mappings[toolUseID]
			if mapping != nil {
				blockMap["tool_use_id"] = mapping.ProxyToolUseID
				content[i] = blockMap
				changed = true
			}
		}
	}

	if changed {
		msg["content"] = content
	}
	return changed
}

func rewriteOpenAIMessage(msg map[string]any, role string, mappings map[string]*ToolCallMapping, searchToolName string) bool {
	changed := false

	switch role {
	case "assistant":
		toolCalls, ok := msg["tool_calls"].([]any)
		if !ok {
			return false
		}
		for i, tc := range toolCalls {
			tcMap, ok := tc.(map[string]any)
			if !ok {
				continue
			}
			tcID, _ := tcMap["id"].(string)
			mapping := mappings[tcID]
			if mapping == nil {
				continue
			}

			function, ok := tcMap["function"].(map[string]any)
			if !ok {
				continue
			}

			function["name"] = searchToolName
			wrappedInput := map[string]any{
				"tool_name":  mapping.ClientToolName,
				"tool_input": mapping.OriginalInput,
			}
			argsJSON, _ := json.Marshal(wrappedInput)
			function["arguments"] = string(argsJSON)
			tcMap["function"] = function
			tcMap["id"] = mapping.ProxyToolUseID
			toolCalls[i] = tcMap
			changed = true
		}
		if changed {
			msg["tool_calls"] = toolCalls
		}

	case "tool":
		toolCallID, _ := msg["tool_call_id"].(string)
		mapping := mappings[toolCallID]
		if mapping != nil {
			msg["tool_call_id"] = mapping.ProxyToolUseID
			changed = true
		}
	}

	return changed
}

// rewriteInboundInputItems rewrites function_call/function_call_output references
// in Responses API input[] items from client-facing names back to gateway_search_tool.
func rewriteInboundInputItems(request map[string]any, input []any, mappings map[string]*ToolCallMapping, searchToolName string) ([]byte, error) {
	modified := false

	for i, item := range input {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}

		itemType, _ := itemMap["type"].(string)

		switch itemType {
		case "function_call":
			callID, _ := itemMap["call_id"].(string)
			mapping := mappings[callID]
			if mapping != nil {
				itemMap["name"] = searchToolName
				itemMap["call_id"] = mapping.ProxyToolUseID
				// Rewrite arguments to wrap with tool_name/tool_input
				wrappedInput := map[string]any{
					"tool_name":  mapping.ClientToolName,
					"tool_input": mapping.OriginalInput,
				}
				argsJSON, _ := json.Marshal(wrappedInput)
				itemMap["arguments"] = string(argsJSON)
				input[i] = itemMap
				modified = true
			}

		case "function_call_output":
			callID, _ := itemMap["call_id"].(string)
			mapping := mappings[callID]
			if mapping != nil {
				itemMap["call_id"] = mapping.ProxyToolUseID
				input[i] = itemMap
				modified = true
			}
		}
	}

	if !modified {
		return utils.MarshalNoEscape(request)
	}

	request["input"] = input
	return utils.MarshalNoEscape(request)
}

// =============================================================================
// HELPERS
// =============================================================================

// extractInputSchemaForDisplay extracts the input schema from a tool definition,
// handling both Anthropic (input_schema) and OpenAI (parameters, function.parameters) formats.
func extractInputSchemaForDisplay(def map[string]any) map[string]any {
	// Anthropic: top-level input_schema
	if schema, ok := def["input_schema"].(map[string]any); ok {
		return schema
	}
	// OpenAI Chat Completions: nested function.parameters
	if fn, ok := def["function"].(map[string]any); ok {
		if params, ok := fn["parameters"].(map[string]any); ok {
			return params
		}
	}
	// OpenAI Responses API: top-level parameters
	if params, ok := def["parameters"].(map[string]any); ok {
		return params
	}
	return nil
}
