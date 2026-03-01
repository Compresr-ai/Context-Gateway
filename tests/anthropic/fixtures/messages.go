// Package fixtures provides test data generators for gateway tests.
//
// DESIGN: The gateway now works with raw JSON (no universal types).
// These fixtures generate raw JSON payloads for testing adapters and pipes.
package fixtures

import (
	"encoding/json"
	"fmt"
)

// =============================================================================
// OPENAI RESPONSES API FORMAT FIXTURES
// =============================================================================

// OpenAIResponsesAPIRequest creates a raw OpenAI Responses API format request JSON.
// This format uses input[] array with function_call and function_call_output items.
func OpenAIResponsesAPIRequest(input []map[string]interface{}) []byte {
	req := map[string]interface{}{
		"model": "gpt-4o",
		"input": input,
	}
	body, _ := json.Marshal(req)
	return body
}

// OpenAIFunctionCall creates an OpenAI Responses API function_call item.
func OpenAIFunctionCall(callID, name, args string) map[string]interface{} {
	return map[string]interface{}{
		"type":      "function_call",
		"call_id":   callID,
		"name":      name,
		"arguments": args,
	}
}

// OpenAIFunctionCallOutput creates an OpenAI Responses API function_call_output item.
func OpenAIFunctionCallOutput(callID, output string) map[string]interface{} {
	return map[string]interface{}{
		"type":    "function_call_output",
		"call_id": callID,
		"output":  output,
	}
}

// OpenAIUserInput creates an OpenAI Responses API user input item.
func OpenAIUserInput(content string) map[string]interface{} {
	return map[string]interface{}{
		"role":    "user",
		"content": content,
	}
}

// =============================================================================
// ANTHROPIC FORMAT FIXTURES
// =============================================================================

// AnthropicRequest creates a raw Anthropic format request JSON.
func AnthropicRequest(messages []map[string]interface{}, tools []map[string]interface{}) []byte {
	req := map[string]interface{}{
		"model":      "claude-3-5-sonnet-20241022",
		"max_tokens": 4096,
		"messages":   messages,
	}
	if len(tools) > 0 {
		req["tools"] = tools
	}
	body, _ := json.Marshal(req)
	return body
}

// AnthropicUserMessage creates an Anthropic format user message with text content.
func AnthropicUserMessage(content string) map[string]interface{} {
	return map[string]interface{}{
		"role":    "user",
		"content": content,
	}
}

// AnthropicUserMessageWithToolResult creates an Anthropic format user message with tool_result blocks.
func AnthropicUserMessageWithToolResult(toolResults []map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"role":    "user",
		"content": toolResults,
	}
}

// AnthropicToolResult creates an Anthropic format tool_result content block.
func AnthropicToolResult(toolUseID, content string) map[string]interface{} {
	return map[string]interface{}{
		"type":        "tool_result",
		"tool_use_id": toolUseID,
		"content":     content,
	}
}

// AnthropicAssistantMessage creates an Anthropic format assistant message with tool_use blocks.
func AnthropicAssistantMessage(toolUses []map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"role":    "assistant",
		"content": toolUses,
	}
}

// AnthropicToolUse creates an Anthropic format tool_use content block.
func AnthropicToolUse(id, name string, input map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"type":  "tool_use",
		"id":    id,
		"name":  name,
		"input": input,
	}
}

// =============================================================================
// CONVENIENCE HELPERS
// =============================================================================

// SingleToolOutputRequest creates a request with a single tool output (OpenAI Responses API format).
func SingleToolOutputRequest(toolOutput string) []byte {
	input := []map[string]interface{}{
		OpenAIUserInput("Read the main.go file"),
		OpenAIFunctionCall("call_001", "read_file", `{"path": "main.go"}`),
		OpenAIFunctionCallOutput("call_001", toolOutput),
	}
	return OpenAIResponsesAPIRequest(input)
}

// MultiToolOutputRequest creates a request with multiple tool outputs (OpenAI Responses API format).
func MultiToolOutputRequest(outputs ...string) []byte {
	input := make([]map[string]interface{}, 0, 1+2*len(outputs))
	input = append(input,
		OpenAIUserInput("Read all the go files"),
	)

	// Add function calls
	for i := range outputs {
		input = append(input, OpenAIFunctionCall(
			fmt.Sprintf("call_%03d", i+1),
			"read_file",
			fmt.Sprintf(`{"path": "file%d.go"}`, i+1),
		))
	}

	// Add function call outputs
	for i, output := range outputs {
		input = append(input, OpenAIFunctionCallOutput(
			fmt.Sprintf("call_%03d", i+1),
			output,
		))
	}

	return OpenAIResponsesAPIRequest(input)
}

// AnthropicSingleToolOutputRequest creates an Anthropic request with a single tool output.
func AnthropicSingleToolOutputRequest(toolOutput string) []byte {
	messages := []map[string]interface{}{
		AnthropicUserMessage("Read the main.go file"),
		AnthropicAssistantMessage([]map[string]interface{}{
			AnthropicToolUse("toolu_001", "read_file", map[string]interface{}{"path": "main.go"}),
		}),
		AnthropicUserMessageWithToolResult([]map[string]interface{}{
			AnthropicToolResult("toolu_001", toolOutput),
		}),
	}
	return AnthropicRequest(messages, nil)
}
