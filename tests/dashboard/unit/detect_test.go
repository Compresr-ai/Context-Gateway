package unit

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/compresr/context-gateway/internal/dashboard"
)

func TestDetectAgent(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		expected string
	}{
		{
			name:     "Claude Code user agent",
			headers:  map[string]string{"User-Agent": "claude-code/1.0"},
			expected: "claude_code",
		},
		{
			name:     "Cursor user agent",
			headers:  map[string]string{"User-Agent": "Cursor/0.45"},
			expected: "cursor",
		},
		{
			name:     "Codex via custom header",
			headers:  map[string]string{"X-Codex-Turn-Metadata": "something"},
			expected: "codex",
		},
		{
			name:     "Codex via account header",
			headers:  map[string]string{"Chatgpt-Account-Id": "acc_123"},
			expected: "codex",
		},
		{
			name:     "Unknown agent",
			headers:  map[string]string{"User-Agent": "python-requests/2.31"},
			expected: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tt.headers {
				h.Set(k, v)
			}
			assert.Equal(t, tt.expected, dashboard.DetectAgent(h))
		})
	}
}

func TestExtractLastUserQuery(t *testing.T) {
	// Anthropic format with string content
	body := `{"messages":[{"role":"user","content":"Hello"},{"role":"assistant","content":"Hi"},{"role":"user","content":"Fix the bug"}]}`
	assert.Equal(t, "Fix the bug", dashboard.ExtractLastUserQuery([]byte(body)))

	// Anthropic format with content blocks
	body = `{"messages":[{"role":"user","content":[{"type":"text","text":"What is this?"}]}]}`
	assert.Equal(t, "What is this?", dashboard.ExtractLastUserQuery([]byte(body)))

	// Empty messages
	body = `{"messages":[]}`
	assert.Equal(t, "", dashboard.ExtractLastUserQuery([]byte(body)))
}

func TestDetectWaitingForHuman(t *testing.T) {
	// Anthropic tool_use response
	body := `{"stop_reason":"tool_use","content":[{"type":"tool_use","name":"Read","id":"tu_1","input":{"path":"/foo"}}]}`
	assert.True(t, dashboard.DetectWaitingForHuman([]byte(body)))

	// Normal end_turn response
	body = `{"stop_reason":"end_turn","content":[{"type":"text","text":"Done!"}]}`
	assert.False(t, dashboard.DetectWaitingForHuman([]byte(body)))

	// OpenAI tool_calls finish reason
	body = `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant"}}]}`
	assert.True(t, dashboard.DetectWaitingForHuman([]byte(body)))
}

func TestExtractLastToolUsed(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"do it"},{"role":"assistant","content":[{"type":"text","text":"ok"},{"type":"tool_use","name":"Bash","id":"tu_1","input":{}}]}]}`
	assert.Equal(t, "Bash", dashboard.ExtractLastToolUsed([]byte(body)))

	// No tool use
	body = `{"messages":[{"role":"assistant","content":[{"type":"text","text":"hello"}]}]}`
	assert.Equal(t, "", dashboard.ExtractLastToolUsed([]byte(body)))
}
