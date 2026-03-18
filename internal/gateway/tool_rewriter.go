// tool_rewriter.go rewrites gateway_search_tool calls bidirectionally between LLM and client.
package gateway

import (
	"encoding/json"
	"fmt"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/utils"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// rewriteResponseForClient transforms gateway_search_tool call-mode blocks
// into real tool_use blocks before sending to the client.
func rewriteResponseForClient(responseBody []byte, mappings []*ToolCallMapping, provider adapters.Provider) ([]byte, error) {
	var response map[string]any
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return responseBody, err
	}

	lookup := make(map[string]*ToolCallMapping, len(mappings))
	for _, m := range mappings {
		lookup[m.ProxyToolUseID] = m
	}

	if provider == adapters.ProviderAnthropic || provider == adapters.ProviderBedrock {
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

// rewriteInboundMessages transforms all tool_use/tool_result references in the
// message history from client-facing names back to gateway_search_tool.
// Uses gjson/sjson for surgical field replacements to preserve original JSON key
// ordering — re-marshaling a map would reorder keys and break KV-cache prefix hits.
func rewriteInboundMessages(body []byte, mappings map[string]*ToolCallMapping, provider adapters.Provider, searchToolName string) ([]byte, error) {
	if len(mappings) == 0 {
		return body, nil
	}
	// Responses API: uses input[] instead of messages[]
	if gjson.GetBytes(body, "input").Exists() && !gjson.GetBytes(body, "messages").Exists() {
		return rewriteInboundInputItemsSjson(body, mappings, searchToolName)
	}
	if provider == adapters.ProviderAnthropic || provider == adapters.ProviderBedrock {
		return rewriteAnthropicMessagesSjson(body, mappings, searchToolName)
	}
	return rewriteOpenAIMessagesSjson(body, mappings, searchToolName)
}

// rewriteAnthropicMessagesSjson rewrites tool_use/tool_result content blocks using
// sjson path-based replacements so unmodified fields keep their original byte order.
func rewriteAnthropicMessagesSjson(body []byte, mappings map[string]*ToolCallMapping, searchToolName string) ([]byte, error) {
	result := body
	modified := false

	var msgIdx int
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		var blockIdx int
		msg.Get("content").ForEach(func(_, block gjson.Result) bool {
			basePath := fmt.Sprintf("messages.%d.content.%d", msgIdx, blockIdx)
			switch block.Get("type").String() {
			case "tool_use":
				id := block.Get("id").String()
				if mapping := mappings[id]; mapping != nil {
					newInput := map[string]any{
						"tool_name":  mapping.ClientToolName,
						"tool_input": mapping.OriginalInput,
					}
					inputJSON, _ := json.Marshal(newInput)
					result, _ = sjson.SetBytes(result, basePath+".name", searchToolName)
					result, _ = sjson.SetBytes(result, basePath+".id", mapping.ProxyToolUseID)
					result, _ = sjson.SetRawBytes(result, basePath+".input", inputJSON)
					modified = true
				}
			case "tool_result":
				toolUseID := block.Get("tool_use_id").String()
				if mapping := mappings[toolUseID]; mapping != nil {
					result, _ = sjson.SetBytes(result, basePath+".tool_use_id", mapping.ProxyToolUseID)
					modified = true
				}
			}
			blockIdx++
			return true
		})
		msgIdx++
		return true
	})

	if !modified {
		return body, nil
	}
	return result, nil
}

// rewriteOpenAIMessagesSjson rewrites tool_calls/tool_call_id fields using
// sjson path-based replacements so unmodified fields keep their original byte order.
func rewriteOpenAIMessagesSjson(body []byte, mappings map[string]*ToolCallMapping, searchToolName string) ([]byte, error) {
	result := body
	modified := false

	var msgIdx int
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		baseMsgPath := fmt.Sprintf("messages.%d", msgIdx)
		switch msg.Get("role").String() {
		case "assistant":
			var tcIdx int
			msg.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
				tcID := tc.Get("id").String()
				if mapping := mappings[tcID]; mapping != nil {
					wrappedInput := map[string]any{
						"tool_name":  mapping.ClientToolName,
						"tool_input": mapping.OriginalInput,
					}
					argsJSON, _ := json.Marshal(wrappedInput)
					tcPath := fmt.Sprintf("%s.tool_calls.%d", baseMsgPath, tcIdx)
					result, _ = sjson.SetBytes(result, tcPath+".id", mapping.ProxyToolUseID)
					result, _ = sjson.SetBytes(result, tcPath+".function.name", searchToolName)
					result, _ = sjson.SetBytes(result, tcPath+".function.arguments", string(argsJSON))
					modified = true
				}
				tcIdx++
				return true
			})
		case "tool":
			toolCallID := msg.Get("tool_call_id").String()
			if mapping := mappings[toolCallID]; mapping != nil {
				result, _ = sjson.SetBytes(result, baseMsgPath+".tool_call_id", mapping.ProxyToolUseID)
				modified = true
			}
		}
		msgIdx++
		return true
	})

	if !modified {
		return body, nil
	}
	return result, nil
}

// rewriteInboundInputItemsSjson rewrites function_call/function_call_output references
// in Responses API input[] items using sjson so unmodified items keep original byte order.
func rewriteInboundInputItemsSjson(body []byte, mappings map[string]*ToolCallMapping, searchToolName string) ([]byte, error) {
	result := body
	modified := false

	var idx int
	gjson.GetBytes(body, "input").ForEach(func(_, item gjson.Result) bool {
		basePath := fmt.Sprintf("input.%d", idx)
		switch item.Get("type").String() {
		case "function_call":
			callID := item.Get("call_id").String()
			if mapping := mappings[callID]; mapping != nil {
				wrappedInput := map[string]any{
					"tool_name":  mapping.ClientToolName,
					"tool_input": mapping.OriginalInput,
				}
				argsJSON, _ := json.Marshal(wrappedInput)
				result, _ = sjson.SetBytes(result, basePath+".name", searchToolName)
				result, _ = sjson.SetBytes(result, basePath+".call_id", mapping.ProxyToolUseID)
				result, _ = sjson.SetBytes(result, basePath+".arguments", string(argsJSON))
				modified = true
			}
		case "function_call_output":
			callID := item.Get("call_id").String()
			if mapping := mappings[callID]; mapping != nil {
				result, _ = sjson.SetBytes(result, basePath+".call_id", mapping.ProxyToolUseID)
				modified = true
			}
		}
		idx++
		return true
	})

	if !modified {
		return body, nil
	}
	return result, nil
}

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
