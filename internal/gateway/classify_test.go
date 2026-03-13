package gateway

import (
	"encoding/json"
	"testing"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// mustJSON marshals v to compact JSON bytes; panics on failure.
func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// ---------------------------------------------------------------------------
// 1. New user turn detection
// ---------------------------------------------------------------------------

func TestClassify_NewUserTurn_Anthropic(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"system": "You are Claude Code, Anthropic's official CLI.",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Explain the code in main.go"},
		},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.True(t, mc.IsNewUserTurn, "single user text should be a new user turn")
	assert.Equal(t, "Explain the code in main.go", mc.CleanUserPrompt)
	assert.Equal(t, "Explain the code in main.go", mc.RawLastUserContent)
	assert.False(t, mc.HasToolResults)
	assert.True(t, mc.IsMainAgent)
}

func TestClassify_NewUserTurn_OpenAI(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "system", "content": "You are Claude Code, the AI."},
			{"role": "user", "content": "Write a unit test"},
		},
	})

	mc := classifyUserMessage(body, adapters.NewOpenAIAdapter())

	assert.True(t, mc.IsNewUserTurn)
	assert.Equal(t, "Write a unit test", mc.CleanUserPrompt)
	assert.False(t, mc.HasToolResults)
	assert.True(t, mc.IsMainAgent)
}

// ---------------------------------------------------------------------------
// 2. Tool loop detection (preceding assistant has tool_use)
// ---------------------------------------------------------------------------

func TestClassify_ToolLoop_Anthropic(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"system": "You are Claude Code, Anthropic's official CLI.",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Read file.txt"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "text", "text": "I'll read that file for you."},
					{"type": "tool_use", "id": "tu_1", "name": "Read", "input": map[string]string{"path": "file.txt"}},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "tool_result", "tool_use_id": "tu_1", "content": "file contents here"},
				},
			},
		},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.False(t, mc.IsNewUserTurn, "tool loop should NOT be a new user turn")
	assert.True(t, mc.HasToolResults)
	// CleanUserPrompt should be empty because the only content is a tool_result (no text blocks)
	assert.Empty(t, mc.CleanUserPrompt)
}

func TestClassify_ToolLoop_OpenAI(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "system", "content": "You are Claude Code, the AI."},
			{"role": "user", "content": "Read the config"},
			{
				"role": "assistant",
				"content":    "Let me read the config.",
				"tool_calls": []map[string]interface{}{
					{"id": "call_1", "type": "function", "function": map[string]string{"name": "read_file", "arguments": `{"path":"config.json"}`}},
				},
			},
			{"role": "tool", "tool_call_id": "call_1", "content": `{"key":"value"}`},
			{"role": "user", "content": "Thanks, now explain it"},
		},
	})

	mc := classifyUserMessage(body, adapters.NewOpenAIAdapter())

	// The preceding assistant has tool_calls, so this is still a tool loop
	// even though the last message is a user message with text.
	// Wait -- actually the last user message is preceded by a tool message,
	// and checkPrecedingAssistantToolUse looks for the *assistant* message
	// before the *last user message*. In this case the last user message is
	// at index 4, and the preceding non-user message scanning backward is
	// the tool message at index 3. The function skips non-assistant roles and
	// continues searching, finding the assistant at index 2 which has tool_calls.
	// So precedingHasToolUse == true.
	assert.False(t, mc.IsNewUserTurn, "preceding assistant used tools -> tool loop")
	assert.False(t, mc.HasToolResults, "OpenAI uses separate tool role, not tool_result blocks")
	assert.Equal(t, "Thanks, now explain it", mc.CleanUserPrompt)
}

// ---------------------------------------------------------------------------
// 3. Tool result message (user message with tool_result blocks, no text)
// ---------------------------------------------------------------------------

func TestClassify_ToolResultOnly_Anthropic(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Run the tests"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "tool_use", "id": "tu_99", "name": "Bash", "input": map[string]string{"cmd": "go test ./..."}},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "tool_result", "tool_use_id": "tu_99", "content": "PASS"},
				},
			},
		},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.False(t, mc.IsNewUserTurn)
	assert.True(t, mc.HasToolResults)
	assert.Empty(t, mc.CleanUserPrompt, "pure tool_result has no human text")
	assert.Equal(t, "", mc.RawLastUserContent, "no text blocks in pure tool_result message")
}

// ---------------------------------------------------------------------------
// 4. Mixed content: text + tool_result in same message (Bug D fix)
// ---------------------------------------------------------------------------

func TestClassify_MixedContent_BugD_Anthropic(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Initial question"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "tool_use", "id": "tu_7", "name": "Bash", "input": map[string]string{"cmd": "ls"}},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "tool_result", "tool_use_id": "tu_7", "content": "file1.go\nfile2.go"},
					{"type": "text", "text": "Now refactor file1.go please"},
				},
			},
		},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.True(t, mc.HasToolResults, "message contains tool_result blocks")
	// Even though there is human text, the preceding assistant used tools
	// AND the message has tool_results, so IsNewUserTurn should be false.
	assert.False(t, mc.IsNewUserTurn, "has tool_results -> not a new user turn")
	// The human text should still be captured in CleanUserPrompt (Bug D fix).
	assert.Equal(t, "Now refactor file1.go please", mc.CleanUserPrompt)
	// RawLastUserContent should contain the human text block
	assert.Equal(t, "Now refactor file1.go please", mc.RawLastUserContent)
}

// ---------------------------------------------------------------------------
// 5. Injected tag filtering
// ---------------------------------------------------------------------------

func TestClassify_InjectedTagFiltering_Anthropic(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"system": "You are Claude Code, Anthropic's official CLI.",
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "<system-reminder>\nToday is 2026-03-10.\n</system-reminder>"},
					{"type": "text", "text": "Fix the bug in handler.go"},
				},
			},
		},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.True(t, mc.IsNewUserTurn)
	// CleanUserPrompt should NOT contain the system-reminder
	assert.Equal(t, "Fix the bug in handler.go", mc.CleanUserPrompt)
	// RawLastUserContent SHOULD contain ALL text blocks including injected ones
	assert.Contains(t, mc.RawLastUserContent, "<system-reminder>")
	assert.Contains(t, mc.RawLastUserContent, "Fix the bug in handler.go")
}

func TestClassify_AllInjectedTags_NoNewUserTurn(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "<system-reminder>\nSome reminder\n</system-reminder>"},
					{"type": "text", "text": "<available-deferred-tools>\ntool1\n</available-deferred-tools>"},
				},
			},
		},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	// When ALL text blocks are injected tags, CleanUserPrompt is empty
	// and IsNewUserTurn is false (no real user text).
	assert.False(t, mc.IsNewUserTurn)
	assert.Empty(t, mc.CleanUserPrompt)
	// RawLastUserContent still has everything
	assert.Contains(t, mc.RawLastUserContent, "<system-reminder>")
	assert.Contains(t, mc.RawLastUserContent, "<available-deferred-tools>")
}

func TestClassify_MultipleInjectedPrefixes(t *testing.T) {
	prefixes := []string{
		"<system-reminder>",
		"<available-deferred-tools>",
		"<user-prompt-submit-hook>",
		"<fast_mode_info>",
		"<command-name>",
		"<antml_thinking>",
		"<antml_thinking_mode>",
		"<antml_reasoning_effort>",
	}

	for _, prefix := range prefixes {
		t.Run(prefix, func(t *testing.T) {
			body := mustJSON(t, map[string]interface{}{
				"messages": []map[string]interface{}{
					{
						"role": "user",
						"content": []map[string]interface{}{
							{"type": "text", "text": prefix + "\nsome content\n"},
							{"type": "text", "text": "real user text"},
						},
					},
				},
			})

			mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())
			assert.Equal(t, "real user text", mc.CleanUserPrompt,
				"injected prefix %q should be filtered from CleanUserPrompt", prefix)
		})
	}
}

// ---------------------------------------------------------------------------
// 6. FirstUserCleanContent extraction
// ---------------------------------------------------------------------------

func TestClassify_FirstUserCleanContent_Anthropic(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "<system-reminder>some injected stuff</system-reminder>"},
					{"type": "text", "text": "Build the project"},
				},
			},
			{"role": "assistant", "content": "Sure, building now..."},
			{"role": "user", "content": "Add tests too"},
		},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	// FirstUserCleanContent should be from the FIRST user message, with injected tags stripped
	assert.Equal(t, "Build the project", mc.FirstUserCleanContent)
	// CleanUserPrompt should be from the LAST user message
	assert.Equal(t, "Add tests too", mc.CleanUserPrompt)
}

func TestClassify_FirstUserCleanContent_StringContent(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": "My first question"},
			{"role": "assistant", "content": "Answer"},
			{"role": "user", "content": "My follow-up"},
		},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.Equal(t, "My first question", mc.FirstUserCleanContent)
	assert.Equal(t, "My follow-up", mc.CleanUserPrompt)
}

func TestClassify_FirstUserCleanContent_InjectedOnly(t *testing.T) {
	// First user message is entirely injected content
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "<system-reminder>You are in test mode</system-reminder>"},
				},
			},
		},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.Empty(t, mc.FirstUserCleanContent)
}

// ---------------------------------------------------------------------------
// 7. Main agent detection (IsMainAgent)
// ---------------------------------------------------------------------------

func TestClassify_IsMainAgent_AnthropicSystemString(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"system":   "You are Claude Code, Anthropic's official CLI for Claude.",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Hello"},
		},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.True(t, mc.IsMainAgent)
}

func TestClassify_IsMainAgent_AnthropicSystemArray(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"system": []map[string]interface{}{
			{"type": "text", "text": "You are Claude Code, the best coding assistant."},
		},
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Hello"},
		},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.True(t, mc.IsMainAgent)
}

func TestClassify_IsMainAgent_OpenAISystemRole(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "system", "content": "You are Claude Code, Anthropic's CLI."},
			{"role": "user", "content": "Hello"},
		},
	})

	mc := classifyUserMessage(body, adapters.NewOpenAIAdapter())

	assert.True(t, mc.IsMainAgent)
}

func TestClassify_IsMainAgent_OpenAIDeveloperRole(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "developer", "content": "You are Claude Code. Follow instructions carefully."},
			{"role": "user", "content": "Hello"},
		},
	})

	mc := classifyUserMessage(body, adapters.NewOpenAIAdapter())

	assert.True(t, mc.IsMainAgent)
}

func TestClassify_NotMainAgent_Subagent(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"system":   "You are a helpful file search assistant.",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Find all Go files"},
		},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.False(t, mc.IsMainAgent)
}

func TestClassify_NotMainAgent_NoSystemPrompt(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Hello"},
		},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.False(t, mc.IsMainAgent, "no system prompt -> not main agent")
}

// ---------------------------------------------------------------------------
// 8. Edge cases: empty/missing messages
// ---------------------------------------------------------------------------

func TestClassify_EmptyMessages(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.False(t, mc.IsNewUserTurn)
	assert.Empty(t, mc.CleanUserPrompt)
	assert.Empty(t, mc.RawLastUserContent)
	assert.Empty(t, mc.FirstUserCleanContent)
	assert.False(t, mc.HasToolResults)
	assert.False(t, mc.IsMainAgent)
}

func TestClassify_NoMessagesField(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"model": "claude-3-sonnet",
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.False(t, mc.IsNewUserTurn)
	assert.Empty(t, mc.CleanUserPrompt)
	assert.Empty(t, mc.RawLastUserContent)
	assert.Empty(t, mc.FirstUserCleanContent)
	assert.False(t, mc.HasToolResults)
}

func TestClassify_InvalidJSON(t *testing.T) {
	body := []byte(`not valid json`)

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.False(t, mc.IsNewUserTurn)
	assert.Empty(t, mc.CleanUserPrompt)
	assert.Empty(t, mc.RawLastUserContent)
}

func TestClassify_EmptyBody(t *testing.T) {
	body := []byte(``)

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.False(t, mc.IsNewUserTurn)
	assert.Empty(t, mc.CleanUserPrompt)
}

func TestClassify_UserMessageEmptyContent(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": ""},
		},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.False(t, mc.IsNewUserTurn, "empty content is not a new user turn")
	assert.Empty(t, mc.CleanUserPrompt)
}

func TestClassify_UserMessageEmptyArrayContent(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": []map[string]interface{}{}},
		},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.False(t, mc.IsNewUserTurn)
	assert.Empty(t, mc.CleanUserPrompt)
	assert.False(t, mc.HasToolResults)
}

func TestClassify_OnlyAssistantMessages(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "assistant", "content": "I can help you."},
		},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	assert.False(t, mc.IsNewUserTurn)
	assert.Empty(t, mc.CleanUserPrompt)
	assert.Empty(t, mc.FirstUserCleanContent)
}

// ---------------------------------------------------------------------------
// ExtractLastUserContent — Anthropic adapter
// ---------------------------------------------------------------------------

func TestExtractLastUserContent_Anthropic_StringContent(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Hello world"},
		},
	})

	adapter := adapters.NewAnthropicAdapter()
	textBlocks, hasToolResults := adapter.ExtractLastUserContent(body)

	assert.Equal(t, []string{"Hello world"}, textBlocks)
	assert.False(t, hasToolResults)
}

func TestExtractLastUserContent_Anthropic_ArrayContent(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "First block"},
					{"type": "text", "text": "Second block"},
				},
			},
		},
	})

	adapter := adapters.NewAnthropicAdapter()
	textBlocks, hasToolResults := adapter.ExtractLastUserContent(body)

	assert.Equal(t, []string{"First block", "Second block"}, textBlocks)
	assert.False(t, hasToolResults)
}

func TestExtractLastUserContent_Anthropic_MixedToolResultAndText(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "tool_result", "tool_use_id": "tu_1", "content": "tool output"},
					{"type": "text", "text": "Please continue"},
				},
			},
		},
	})

	adapter := adapters.NewAnthropicAdapter()
	textBlocks, hasToolResults := adapter.ExtractLastUserContent(body)

	assert.Equal(t, []string{"Please continue"}, textBlocks)
	assert.True(t, hasToolResults)
}

func TestExtractLastUserContent_Anthropic_ToolResultOnly(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "tool_result", "tool_use_id": "tu_1", "content": "PASS"},
				},
			},
		},
	})

	adapter := adapters.NewAnthropicAdapter()
	textBlocks, hasToolResults := adapter.ExtractLastUserContent(body)

	assert.Empty(t, textBlocks)
	assert.True(t, hasToolResults)
}

func TestExtractLastUserContent_Anthropic_Empty(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{},
	})

	adapter := adapters.NewAnthropicAdapter()
	textBlocks, hasToolResults := adapter.ExtractLastUserContent(body)

	assert.Nil(t, textBlocks)
	assert.False(t, hasToolResults)
}

func TestExtractLastUserContent_Anthropic_FindsLastUser(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": "First message"},
			{"role": "assistant", "content": "Response"},
			{"role": "user", "content": "Second message"},
		},
	})

	adapter := adapters.NewAnthropicAdapter()
	textBlocks, _ := adapter.ExtractLastUserContent(body)

	assert.Equal(t, []string{"Second message"}, textBlocks)
}

// ---------------------------------------------------------------------------
// ExtractLastUserContent — OpenAI adapter
// ---------------------------------------------------------------------------

func TestExtractLastUserContent_OpenAI_ChatCompletions(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "What is Go?"},
		},
	})

	adapter := adapters.NewOpenAIAdapter()
	textBlocks, hasToolResults := adapter.ExtractLastUserContent(body)

	assert.Equal(t, []string{"What is Go?"}, textBlocks)
	assert.False(t, hasToolResults, "OpenAI never returns hasToolResults=true")
}

func TestExtractLastUserContent_OpenAI_SkipsToolMessages(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Read config.json"},
			{
				"role":    "assistant",
				"content": "Reading...",
				"tool_calls": []map[string]interface{}{
					{"id": "call_1", "type": "function", "function": map[string]string{"name": "read_file", "arguments": "{}"}},
				},
			},
			{"role": "tool", "tool_call_id": "call_1", "content": `{"key":"value"}`},
			{"role": "user", "content": "Now explain it"},
		},
	})

	adapter := adapters.NewOpenAIAdapter()
	textBlocks, hasToolResults := adapter.ExtractLastUserContent(body)

	assert.Equal(t, []string{"Now explain it"}, textBlocks)
	assert.False(t, hasToolResults)
}

func TestExtractLastUserContent_OpenAI_ResponsesAPI(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"input": []map[string]interface{}{
			{"type": "message", "role": "user", "content": "Summarize this"},
		},
	})

	adapter := adapters.NewOpenAIAdapter()
	textBlocks, hasToolResults := adapter.ExtractLastUserContent(body)

	assert.Equal(t, []string{"Summarize this"}, textBlocks)
	assert.False(t, hasToolResults)
}

func TestExtractLastUserContent_OpenAI_Empty(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{},
	})

	adapter := adapters.NewOpenAIAdapter()
	textBlocks, hasToolResults := adapter.ExtractLastUserContent(body)

	assert.Nil(t, textBlocks)
	assert.False(t, hasToolResults)
}

// ---------------------------------------------------------------------------
// checkPrecedingAssistantToolUse (internal helper)
// ---------------------------------------------------------------------------

func TestCheckPrecedingAssistantToolUse_Anthropic(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Do something"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "tool_use", "id": "tu_1", "name": "Bash", "input": map[string]string{}},
				},
			},
			{"role": "user", "content": "Follow-up"},
		},
	})

	assert.True(t, checkPrecedingAssistantToolUse(body))
}

func TestCheckPrecedingAssistantToolUse_OpenAI(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Do something"},
			{
				"role":    "assistant",
				"content": "Sure.",
				"tool_calls": []map[string]interface{}{
					{"id": "call_1", "type": "function", "function": map[string]string{"name": "bash", "arguments": "{}"}},
				},
			},
			{"role": "user", "content": "Follow-up"},
		},
	})

	assert.True(t, checkPrecedingAssistantToolUse(body))
}

func TestCheckPrecedingAssistantToolUse_NoToolUse(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi there!"},
			{"role": "user", "content": "How are you?"},
		},
	})

	assert.False(t, checkPrecedingAssistantToolUse(body))
}

func TestCheckPrecedingAssistantToolUse_SingleMessage(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Hello"},
		},
	})

	assert.False(t, checkPrecedingAssistantToolUse(body))
}

func TestCheckPrecedingAssistantToolUse_LastNotUser(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Goodbye"},
		},
	})

	assert.False(t, checkPrecedingAssistantToolUse(body))
}

// ---------------------------------------------------------------------------
// extractFirstUserCleanContent (internal helper)
// ---------------------------------------------------------------------------

func TestExtractFirstUserCleanContent_StringContent(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": "My first message"},
			{"role": "assistant", "content": "Response"},
			{"role": "user", "content": "Second message"},
		},
	})

	result := extractFirstUserCleanContent(body)
	assert.Equal(t, "My first message", result)
}

func TestExtractFirstUserCleanContent_ArrayContent_WithInjected(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "<system-reminder>Today is Monday</system-reminder>"},
					{"type": "text", "text": "Help me debug"},
				},
			},
		},
	})

	result := extractFirstUserCleanContent(body)
	assert.Equal(t, "Help me debug", result)
}

func TestExtractFirstUserCleanContent_OnlyInjected(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": "<system-reminder>Just a reminder</system-reminder>"},
		},
	})

	result := extractFirstUserCleanContent(body)
	assert.Empty(t, result)
}

func TestExtractFirstUserCleanContent_NoMessages(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{},
	})

	result := extractFirstUserCleanContent(body)
	assert.Empty(t, result)
}

// ---------------------------------------------------------------------------
// Full pipeline: realistic multi-turn Anthropic conversation
// ---------------------------------------------------------------------------

func TestClassify_FullPipeline_AnthropicMultiTurn(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"system": "You are Claude Code, Anthropic's official CLI for Claude.",
		"messages": []map[string]interface{}{
			// Turn 1: user asks a question
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "<system-reminder>\nToday is 2026-03-10.\n</system-reminder>"},
					{"type": "text", "text": "Refactor the handler"},
				},
			},
			// Turn 1: assistant responds with tool use
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "text", "text": "I'll read the handler first."},
					{"type": "tool_use", "id": "tu_1", "name": "Read", "input": map[string]string{"path": "handler.go"}},
				},
			},
			// Turn 2: tool result + new human text (Bug D scenario)
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "tool_result", "tool_use_id": "tu_1", "content": "package main\n\nfunc main() {}"},
					{"type": "text", "text": "<available-deferred-tools>\nsome tools\n</available-deferred-tools>"},
					{"type": "text", "text": "Also add error handling"},
				},
			},
		},
	})

	mc := classifyUserMessage(body, adapters.NewAnthropicAdapter())

	// Preceding assistant used tools, so this is not a new user turn
	assert.False(t, mc.IsNewUserTurn)
	assert.True(t, mc.HasToolResults)

	// CleanUserPrompt: only human-typed text, no injected tags
	assert.Equal(t, "Also add error handling", mc.CleanUserPrompt)

	// RawLastUserContent: all text blocks (but NOT tool_result content)
	assert.Contains(t, mc.RawLastUserContent, "<available-deferred-tools>")
	assert.Contains(t, mc.RawLastUserContent, "Also add error handling")

	// FirstUserCleanContent: from the FIRST user message, injected tags stripped
	assert.Equal(t, "Refactor the handler", mc.FirstUserCleanContent)

	// IsMainAgent: system prompt contains "You are Claude Code"
	assert.True(t, mc.IsMainAgent)
}

// ---------------------------------------------------------------------------
// Full pipeline: realistic OpenAI Chat Completions conversation
// ---------------------------------------------------------------------------

func TestClassify_FullPipeline_OpenAIMultiTurn(t *testing.T) {
	body := mustJSON(t, map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "developer", "content": "You are Claude Code, a CLI tool."},
			{"role": "user", "content": "List all Go files"},
			{
				"role":    "assistant",
				"content": "I'll search for Go files.",
				"tool_calls": []map[string]interface{}{
					{"id": "call_1", "type": "function", "function": map[string]string{"name": "glob", "arguments": `{"pattern":"**/*.go"}`}},
				},
			},
			{"role": "tool", "tool_call_id": "call_1", "content": "main.go\nhandler.go\nclassify.go"},
			{"role": "user", "content": "Great, now explain classify.go"},
		},
	})

	mc := classifyUserMessage(body, adapters.NewOpenAIAdapter())

	// Preceding assistant has tool_calls -> tool loop
	assert.False(t, mc.IsNewUserTurn)
	assert.False(t, mc.HasToolResults, "OpenAI uses role=tool, not inline tool_result")
	assert.Equal(t, "Great, now explain classify.go", mc.CleanUserPrompt)
	assert.Equal(t, "List all Go files", mc.FirstUserCleanContent)
	assert.True(t, mc.IsMainAgent)
}

// ---------------------------------------------------------------------------
// Responses API (Codex) tests
// ---------------------------------------------------------------------------

func TestClassify_ResponsesAPI_StringInput(t *testing.T) {
	body := mustJSON(t, map[string]any{
		"model": "gpt-4o-mini",
		"input": "Say hello briefly.",
	})

	mc := classifyUserMessage(body, adapters.NewOpenAIAdapter())

	assert.True(t, mc.IsNewUserTurn)
	assert.Equal(t, "Say hello briefly.", mc.CleanUserPrompt)
	assert.Equal(t, "Say hello briefly.", mc.RawLastUserContent)
	assert.Equal(t, "Say hello briefly.", mc.FirstUserCleanContent)
	assert.False(t, mc.HasToolResults)
}

func TestClassify_ResponsesAPI_ArrayInput_NewUserTurn(t *testing.T) {
	body := mustJSON(t, map[string]any{
		"model": "gpt-4o-mini",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "Read config.yaml"},
		},
	})

	mc := classifyUserMessage(body, adapters.NewOpenAIAdapter())

	assert.True(t, mc.IsNewUserTurn)
	assert.Equal(t, "Read config.yaml", mc.CleanUserPrompt)
	assert.Equal(t, "Read config.yaml", mc.FirstUserCleanContent)
	assert.False(t, mc.HasToolResults)
}

func TestClassify_ResponsesAPI_ToolLoop(t *testing.T) {
	body := mustJSON(t, map[string]any{
		"model": "gpt-4o-mini",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "Read config.yaml"},
			map[string]any{
				"type":    "function_call",
				"call_id": "call_001",
				"name":    "read_file",
			},
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call_001",
				"output":  "file contents here",
			},
			map[string]any{"type": "message", "role": "user", "content": "Now explain it"},
		},
	})

	mc := classifyUserMessage(body, adapters.NewOpenAIAdapter())

	// Preceding function_call before last user message → tool loop
	assert.False(t, mc.IsNewUserTurn)
	assert.Equal(t, "Now explain it", mc.CleanUserPrompt)
	assert.Equal(t, "Read config.yaml", mc.FirstUserCleanContent)
}

func TestClassify_ResponsesAPI_EmptyStringInput(t *testing.T) {
	body := mustJSON(t, map[string]any{
		"model": "gpt-4o-mini",
		"input": "",
	})

	mc := classifyUserMessage(body, adapters.NewOpenAIAdapter())

	assert.False(t, mc.IsNewUserTurn)
	assert.Empty(t, mc.CleanUserPrompt)
	assert.Empty(t, mc.FirstUserCleanContent)
}
