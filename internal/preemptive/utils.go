// Package preemptive - utils.go contains helper functions.
//
// DESIGN: Utility functions for token calculations, message parsing,
// and response building. Separate from config (data) and types (definitions).
package preemptive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// =============================================================================
// SESSION ID HELPERS
// =============================================================================

// ComputeSessionID computes a stable session ID from request body.
// Uses the same logic as preemptive summarization: SHA256 hash of first user message.
// Returns empty string if no user message found.
// This is the canonical function - used by both preemptive layer and trajectory tracking.
func ComputeSessionID(body []byte) string {
	if len(body) == 0 {
		return ""
	}

	// Parse request to extract messages
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}

	messages, ok := req["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		return ""
	}

	// Find the FIRST user message - this is the task identifier that never changes
	for _, msg := range messages {
		parsed, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}

		role, ok := parsed["role"].(string)
		if !ok {
			continue
		}

		if role == "user" {
			// Hash the entire first user message (content + any metadata)
			h := sha256.New()
			canonical, _ := json.Marshal(parsed)
			h.Write(canonical)
			return hex.EncodeToString(h.Sum(nil))[:16]
		}
	}

	// No user message found (likely a subagent)
	return ""
}

// =============================================================================
// MODEL CONTEXT WINDOW HELPERS
// =============================================================================

// GetModelContextWindow returns context window for a model.
// Falls back to DefaultUnknownModelContextWindow if model is not found.
func GetModelContextWindow(model string) ModelContextWindow {
	if mw, ok := DefaultModelContextWindows[model]; ok {
		return mw
	}
	// Return fallback with the actual model name
	fallback := DefaultUnknownModelContextWindow
	fallback.Model = model
	return fallback
}

// =============================================================================
// TOKEN USAGE HELPERS
// =============================================================================

// CalculateUsage calculates token usage percentage.
func CalculateUsage(inputTokens, maxTokens int) TokenUsage {
	if maxTokens <= 0 {
		maxTokens = 128000
	}
	percent := float64(inputTokens) / float64(maxTokens) * 100
	if percent > 100 {
		percent = 100
	}
	return TokenUsage{InputTokens: inputTokens, MaxTokens: maxTokens, UsagePercent: percent}
}

// =============================================================================
// MESSAGE PARSING HELPERS
// =============================================================================

// ParseMessages extracts messages array from request body.
func ParseMessages(body []byte) ([]json.RawMessage, error) {
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	return req.Messages, nil
}

// ExtractText extracts text from message content (works for both Anthropic and OpenAI).
func ExtractText(content interface{}) string {
	if content == nil {
		return ""
	}

	// String content (common for both)
	if s, ok := content.(string); ok {
		return s
	}

	// Array content (Anthropic blocks or OpenAI multimodal)
	if arr, ok := content.([]interface{}); ok {
		var parts []string
		for _, item := range arr {
			if block, ok := item.(map[string]interface{}); ok {
				if block["type"] == "text" {
					if text, ok := block["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	}

	return ""
}

// ExtractContentString extracts content string from message content with tool support.
func ExtractContentString(content interface{}) string {
	if content == nil {
		return ""
	}
	if s, ok := content.(string); ok {
		return s
	}
	if arr, ok := content.([]interface{}); ok {
		var parts []string
		for _, item := range arr {
			if block, ok := item.(map[string]interface{}); ok {
				switch block["type"] {
				case "text":
					if text, ok := block["text"].(string); ok {
						parts = append(parts, text)
					}
				case "tool_use":
					name, _ := block["name"].(string)
					parts = append(parts, fmt.Sprintf("[Tool: %s]", name))
				case "tool_result":
					tc := ExtractContentString(block["content"])
					if len(tc) > 500 {
						tc = tc[:500] + "..."
					}
					parts = append(parts, fmt.Sprintf("[Tool Result: %s]", tc))
				}
			}
		}
		return JoinNonEmpty(parts, "\n")
	}
	return ""
}

// FormatMessages formats messages for summarization input.
func FormatMessages(messages []json.RawMessage) string {
	var buf bytes.Buffer
	for i, raw := range messages {
		var msg map[string]interface{}
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}

		role, _ := msg["role"].(string)
		content := ExtractContentString(msg["content"])
		if content == "" {
			continue
		}

		if len(content) > 10000 {
			content = content[:10000] + "\n... [truncated]"
		}
		fmt.Fprintf(&buf, "[Message %d - %s]\n%s\n\n", i+1, role, content)
	}
	return buf.String()
}

// JoinNonEmpty joins non-empty strings with separator.
func JoinNonEmpty(parts []string, sep string) string {
	var nonEmpty []string
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	if len(nonEmpty) == 0 {
		return ""
	}
	var buf bytes.Buffer
	for i, p := range nonEmpty {
		if i > 0 {
			buf.WriteString(sep)
		}
		buf.WriteString(p)
	}
	return buf.String()
}

// =============================================================================
// RESPONSE BUILDING HELPERS
// =============================================================================

// BuildAnthropicResponse creates a synthetic Anthropic API response.
// This is returned directly to the client without hitting the API.
// It includes the summary + all messages that came after the summarized portion.
// If excludeLastMessage is true, the last message (compaction instruction) is excluded.
func BuildAnthropicResponse(summary string, messages []json.RawMessage, lastIndex int, model string, excludeLastMessage bool) []byte {
	// Build the response text: summary + recent messages
	var text strings.Builder
	text.WriteString("<summary>\n")
	text.WriteString(summary)
	text.WriteString("\n</summary>")

	// Determine end index for recent messages
	endIndex := len(messages)
	if excludeLastMessage && endIndex > 0 {
		endIndex = endIndex - 1 // Exclude the compaction instruction message
	}

	recentCount := 0
	// Append recent messages that weren't summarized (excluding compaction prompt)
	if lastIndex+1 < endIndex {
		text.WriteString("\n\n<recent_messages>\n")
		for i := lastIndex + 1; i < endIndex; i++ {
			var msg map[string]interface{}
			if err := json.Unmarshal(messages[i], &msg); err == nil {
				role, _ := msg["role"].(string)
				content := extractContent(msg["content"])
				_, _ = fmt.Fprintf(&text, "[%s]: %s\n\n", role, content)
				recentCount++
			}
		}
		text.WriteString("</recent_messages>")
	}

	// Log summary stats for debugging
	log.Debug().
		Int("summary_len", len(summary)).
		Int("messages_summarized", lastIndex+1).
		Int("recent_appended", recentCount).
		Int("total_messages", len(messages)).
		Bool("excluded_compact_msg", excludeLastMessage).
		Str("summary_preview", truncate(summary, 200)).
		Msg("Built compaction response")

	content := text.String()
	resp := map[string]interface{}{
		"id":            fmt.Sprintf("msg_precomputed_%d", time.Now().UnixNano()),
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"content":       []map[string]interface{}{{"type": "text", "text": content}},
		"usage":         map[string]interface{}{"input_tokens": 0, "output_tokens": len(content) / 4},
	}
	data, _ := json.Marshal(resp)
	return data
}

// truncate shortens a string for logging
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// extractContent extracts text content from Anthropic message content field.
func extractContent(content interface{}) string {
	switch c := content.(type) {
	case string:
		return c
	case []interface{}:
		var parts []string
		for _, item := range c {
			if m, ok := item.(map[string]interface{}); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, " ")
	default:
		return fmt.Sprintf("%v", content)
	}
}

// BuildOpenAICompactedRequest creates a compacted request for OpenAI API.
// Old messages are replaced with a summary, then forwarded to the API.
// If excludeLastMessage is true, the last message (compaction instruction) is excluded.
func BuildOpenAICompactedRequest(messages []json.RawMessage, summary string, lastIndex int, excludeLastMessage bool) []byte {
	newMsgs := []interface{}{
		map[string]interface{}{
			"role":    "user",
			"content": "## Conversation Summary\n\n" + summary + "\n\n---\n\nPlease continue helping me.",
		},
		map[string]interface{}{
			"role":    "assistant",
			"content": "I've reviewed the summary. How can I help?",
		},
	}

	// Determine end index for recent messages
	endIndex := len(messages)
	if excludeLastMessage && endIndex > 0 {
		endIndex = endIndex - 1 // Exclude the compaction instruction message
	}

	for i := lastIndex + 1; i < endIndex; i++ {
		var msg interface{}
		if json.Unmarshal(messages[i], &msg) == nil {
			newMsgs = append(newMsgs, msg)
		}
	}

	data, _ := json.Marshal(map[string]interface{}{"messages": newMsgs})
	return data
}

// =============================================================================
// CONFIG HELPERS
// =============================================================================

// WithDefaults applies default values to config fields that are zero.
func WithDefaults(cfg Config) Config {
	if cfg.PendingJobTimeout == 0 {
		cfg.PendingJobTimeout = 90 * time.Second
	}
	if cfg.SyncTimeout == 0 {
		cfg.SyncTimeout = 2 * time.Minute
	}
	if cfg.TokenEstimateRatio == 0 {
		cfg.TokenEstimateRatio = 4
	}
	if cfg.LogDir == "" {
		cfg.LogDir = "logs"
	}
	// Apply default prompt patterns if not specified
	if len(cfg.Detectors.ClaudeCode.PromptPatterns) == 0 {
		cfg.Detectors.ClaudeCode.PromptPatterns = DefaultClaudeCodePromptPatterns
	}
	// (Codex prompt pattern logic removed)
	return cfg
}
