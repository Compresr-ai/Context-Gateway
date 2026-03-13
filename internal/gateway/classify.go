// classify.go provides unified user message classification for the gateway.
//
// DESIGN: Instead of 6+ separate functions each trying to extract "user intent"
// from request bodies, this file provides a single classification computed once
// per request at the top of handleProxy. The result is carried through
// PipelineContext to all consumers.
//
// This fixes bugs where different extraction functions disagreed:
//   - Bug A: System-injected content leaking into compression context
//   - Bug B: Unstable session IDs from injected XML tags
//   - Bug C: Array content blocks missed in trajectory extraction
//   - Bug D: Human text lost when tool_result blocks coexist
package gateway

import (
	"strings"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/tidwall/gjson"
)

// MessageClassification is the single source of truth for user message analysis.
// Computed once per request at the top of handleProxy, then carried through
// PipelineContext to all consumers.
type MessageClassification struct {
	// IsNewUserTurn is true when a human initiated this request (not a tool loop).
	// False when the preceding assistant used tools and this message is the automated response.
	IsNewUserTurn bool

	// CleanUserPrompt is the human-typed text with injected tags stripped.
	// Used for trajectory recording, prompt history, and compression context.
	CleanUserPrompt string

	// RawLastUserContent is ALL text from the last user message, unfiltered.
	// Used for /savings command detection where exact content matching is needed.
	RawLastUserContent string

	// FirstUserCleanContent is the first user message with injected tags stripped.
	// Used for stable session ID hashing (immune to mid-conversation tag changes).
	FirstUserCleanContent string

	// IsMainAgent is true when the request is from the main Claude Code agent
	// (not a subagent). Determined by checking for "You are Claude Code" in system prompt.
	IsMainAgent bool

	// HasToolResults is true when the last user message contains tool_result blocks.
	HasToolResults bool
}

// classifyUserMessage computes a unified classification of the user message.
// This is the SINGLE entry point for understanding what the user sent.
// All downstream consumers should use the classification instead of
// re-parsing the request body independently.
func classifyUserMessage(body []byte, adapter adapters.Adapter) MessageClassification {
	mc := MessageClassification{}

	// Step 1: Structural extraction via adapter (provider-agnostic)
	textBlocks, hasToolResults := adapter.ExtractLastUserContent(body)
	mc.HasToolResults = hasToolResults

	// Step 2: Build raw content (all text, unfiltered) for /savings detection
	mc.RawLastUserContent = strings.Join(textBlocks, "\n")

	// Step 3: Check if preceding assistant used tools (determines tool loop vs new turn)
	precedingHasToolUse := checkPrecedingAssistantToolUse(body)

	// Step 4: Filter injected tags for clean prompt
	var cleanTexts []string
	for _, text := range textBlocks {
		if !isInjectedText(text) {
			cleaned := strings.TrimSpace(text)
			if cleaned != "" {
				cleanTexts = append(cleanTexts, cleaned)
			}
		}
	}
	mc.CleanUserPrompt = strings.TrimSpace(strings.Join(cleanTexts, "\n"))

	// Step 5: Determine IsNewUserTurn
	// A request is a new user turn when:
	// - There is user content (text blocks exist)
	// - The preceding assistant did NOT use tools (not a tool loop)
	// - The message does NOT contain tool_result blocks (not automated tool response)
	// - There is clean (non-injected) user text
	mc.IsNewUserTurn = len(textBlocks) > 0 &&
		!precedingHasToolUse &&
		!hasToolResults &&
		mc.CleanUserPrompt != ""

	// Step 6: Extract first user message clean content for stable session ID
	mc.FirstUserCleanContent = extractFirstUserCleanContent(body)

	// Step 7: Check if this is the main Claude Code agent
	mc.IsMainAgent = isMainAgentRequest(body)

	return mc
}

// checkPrecedingAssistantToolUse returns true if the assistant message immediately
// before the last user message contained tool_use (Anthropic) or tool_calls (OpenAI).
// This indicates the request is part of a tool loop, not a new user turn.
func checkPrecedingAssistantToolUse(body []byte) bool {
	// Try Chat Completions format (messages[])
	messages := gjson.GetBytes(body, "messages")
	if messages.IsArray() {
		return checkToolUseInArray(messages.Array())
	}

	// Try Responses API format (input[])
	input := gjson.GetBytes(body, "input")
	if input.IsArray() {
		return checkToolUseInResponsesInput(input.Array())
	}

	return false
}

// checkToolUseInArray checks for tool_use/tool_calls in Chat Completions messages.
func checkToolUseInArray(arr []gjson.Result) bool {
	if len(arr) < 2 {
		return false
	}

	// Last message must be "user" for this check to be relevant
	if arr[len(arr)-1].Get("role").String() != "user" {
		return false
	}

	// Find the preceding assistant message
	for i := len(arr) - 2; i >= 0; i-- {
		if arr[i].Get("role").String() != "assistant" {
			continue
		}

		prevAssistant := arr[i]

		// Check Anthropic format: content array with tool_use blocks
		assistantContent := prevAssistant.Get("content")
		if assistantContent.IsArray() {
			for _, block := range assistantContent.Array() {
				if block.Get("type").String() == "tool_use" {
					return true
				}
			}
		}

		// Check OpenAI format: tool_calls field on assistant message
		toolCalls := prevAssistant.Get("tool_calls")
		if toolCalls.IsArray() && len(toolCalls.Array()) > 0 {
			return true
		}

		return false
	}

	return false
}

// checkToolUseInResponsesInput checks for function_call items preceding the last
// user message in Responses API input[].
func checkToolUseInResponsesInput(items []gjson.Result) bool {
	if len(items) < 2 {
		return false
	}

	// Find the last user message index
	lastUserIdx := -1
	for i := len(items) - 1; i >= 0; i-- {
		itemType := items[i].Get("type").String()
		if itemType == "message" && items[i].Get("role").String() == "user" {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 1 {
		return false
	}

	// Check items immediately before the last user message for function_call
	for i := lastUserIdx - 1; i >= 0; i-- {
		itemType := items[i].Get("type").String()
		if itemType == "function_call" {
			return true
		}
		// Stop at previous message boundary
		if itemType == "message" {
			break
		}
	}

	return false
}

// extractFirstUserCleanContent finds the first user message in the conversation
// and returns its text content with injected tags stripped.
// Used for stable session ID hashing (Bug B fix).
func extractFirstUserCleanContent(body []byte) string {
	// Try Chat Completions format (messages[])
	messages := gjson.GetBytes(body, "messages")
	if messages.IsArray() {
		return extractFirstUserFromMessages(messages.Array())
	}

	// Try Responses API format (input can be string or array)
	input := gjson.GetBytes(body, "input")
	if input.Type == gjson.String {
		text := input.String()
		if text != "" && !isInjectedText(text) {
			return strings.TrimSpace(text)
		}
		return ""
	}
	if input.IsArray() {
		for _, item := range input.Array() {
			if item.Get("type").String() == "message" && item.Get("role").String() == "user" {
				content := item.Get("content")
				if content.Type == gjson.String {
					text := content.String()
					if !isInjectedText(text) {
						return strings.TrimSpace(text)
					}
				}
				return ""
			}
		}
	}

	return ""
}

// extractFirstUserFromMessages extracts first user content from Chat Completions messages.
func extractFirstUserFromMessages(msgs []gjson.Result) string {
	for _, msg := range msgs {
		if msg.Get("role").String() != "user" {
			continue
		}

		content := msg.Get("content")
		if !content.Exists() {
			continue
		}

		// String content
		if content.Type == gjson.String {
			text := content.String()
			if !isInjectedText(text) {
				return strings.TrimSpace(text)
			}
			return ""
		}

		// Array content — extract non-injected text blocks
		if content.IsArray() {
			var cleanTexts []string
			for _, block := range content.Array() {
				if block.Get("type").String() != "text" {
					continue
				}
				text := block.Get("text").String()
				if text != "" && !isInjectedText(text) {
					cleanTexts = append(cleanTexts, strings.TrimSpace(text))
				}
			}
			return strings.TrimSpace(strings.Join(cleanTexts, "\n"))
		}

		return ""
	}

	return ""
}
