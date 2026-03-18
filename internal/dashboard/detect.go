// Agent and status detection from request/response characteristics.
package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"
)

// DetectAgent identifies the agent type from HTTP headers.
func DetectAgent(h http.Header) string {
	ua := strings.ToLower(h.Get("User-Agent"))

	switch {
	case strings.Contains(ua, "claude-code") || strings.Contains(ua, "claude_code"):
		return "claude_code"
	case strings.Contains(ua, "cursor"):
		return "cursor"
	case strings.Contains(ua, "codex"):
		return "codex"
	case strings.Contains(ua, "opencode"):
		return "opencode"
	case strings.Contains(ua, "openclaw"):
		return "openclaw"
	case strings.Contains(ua, "windsurf"):
		return "windsurf"
	case strings.Contains(ua, "aider"):
		return "aider"
	}

	// Check custom headers some agents send
	if h.Get("X-Codex-Turn-Metadata") != "" {
		return "codex"
	}
	if h.Get("Chatgpt-Account-Id") != "" {
		return "codex"
	}

	return "unknown"
}

// injectedPrefixes are XML tag prefixes used by Claude Code / IDE integrations
// to inject system content into user messages. Text blocks containing these are
// not user-typed and should be skipped when extracting the user query.
var injectedPrefixes = []string{
	"<system-reminder>",
	"<available-deferred-tools>",
	"<user-prompt-submit-hook>",
	"<fast_mode_info>",
	"<command-name>",
	"<local-command-caveat>",
	"<antml_thinking>",
	"<antml_thinking_mode>",
	"<antml_reasoning_effort>",
}

// isInjected returns true if a text block contains system-injected content.
func isInjected(text string) bool {
	for _, prefix := range injectedPrefixes {
		if strings.Contains(text, prefix) {
			return true
		}
	}
	return false
}

// ExtractLastUserQuery pulls the last user message from the request body.
// Filters out system-injected content blocks (e.g. <system-reminder>).
func ExtractLastUserQuery(body []byte) string {
	// Anthropic format
	var anthropicReq struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &anthropicReq); err == nil && len(anthropicReq.Messages) > 0 {
		// Walk backwards to find last user message with real user text.
		// Skip messages that are injected content or tool_result-only.
		for i := len(anthropicReq.Messages) - 1; i >= 0; i-- {
			msg := anthropicReq.Messages[i]
			if msg.Role != "user" {
				continue
			}
			// Content can be string or array
			var text string
			if err := json.Unmarshal(msg.Content, &text); err == nil {
				if !isInjected(text) {
					return text
				}
				continue // keep searching earlier messages
			}
			// Array of content blocks — find first non-injected text block
			var blocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(msg.Content, &blocks); err == nil {
				for _, b := range blocks {
					if b.Type == "text" && b.Text != "" && !isInjected(b.Text) {
						return b.Text
					}
				}
			}
			continue // keep searching earlier messages
		}
	}

	// OpenAI format
	var openaiReq struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &openaiReq); err == nil {
		for i := len(openaiReq.Messages) - 1; i >= 0; i-- {
			if openaiReq.Messages[i].Role == "user" && !isInjected(openaiReq.Messages[i].Content) {
				return openaiReq.Messages[i].Content
			}
		}
	}

	return ""
}

// ExtractLastToolUsed pulls the last tool_use name from the request body (from assistant messages).
func ExtractLastToolUsed(body []byte) string {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}

	// Walk backwards for last assistant message with tool_use
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "assistant" {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(req.Messages[i].Content, &blocks); err != nil {
			continue
		}
		// Last tool_use in the message
		for j := len(blocks) - 1; j >= 0; j-- {
			if blocks[j].Type == "tool_use" && blocks[j].Name != "" {
				return blocks[j].Name
			}
		}
		break
	}
	return ""
}
