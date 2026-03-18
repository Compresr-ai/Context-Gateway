package unit

import (
	"testing"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/stretchr/testify/assert"
)

// =============================================================================
// USAGE EXTRACTION UNIT TESTS
// Tests that adapters correctly extract token usage from API responses.
// =============================================================================

// -----------------------------------------------------------------------------
// OpenAI Usage Extraction
// -----------------------------------------------------------------------------

func TestOpenAI_ExtractUsage_ChatCompletions(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	// Standard chat completions response
	response := []byte(`{
		"id": "chatcmpl-abc123",
		"object": "chat.completion",
		"model": "gpt-4o",
		"usage": {
			"prompt_tokens": 1500,
			"completion_tokens": 500,
			"total_tokens": 2000
		}
	}`)

	usage := adapter.ExtractUsage(response)

	assert.Equal(t, 1500, usage.InputTokens, "should extract prompt_tokens as InputTokens")
	assert.Equal(t, 500, usage.OutputTokens, "should extract completion_tokens as OutputTokens")
	assert.Equal(t, 2000, usage.TotalTokens, "should extract total_tokens")
}

func TestOpenAI_ExtractUsage_ResponsesAPI(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	// Responses API format (newer OpenAI API)
	response := []byte(`{
		"id": "resp_abc",
		"output": [{"type": "message", "content": [{"type": "text", "text": "Hello"}]}],
		"usage": {
			"prompt_tokens": 2500,
			"completion_tokens": 150,
			"total_tokens": 2650
		}
	}`)

	usage := adapter.ExtractUsage(response)

	assert.Equal(t, 2500, usage.InputTokens)
	assert.Equal(t, 150, usage.OutputTokens)
	assert.Equal(t, 2650, usage.TotalTokens)
}

func TestOpenAI_ExtractUsage_NoUsageField(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	// Response without usage field (e.g., error or incomplete response)
	response := []byte(`{"id": "resp_123", "error": "something went wrong"}`)

	usage := adapter.ExtractUsage(response)

	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
	assert.Equal(t, 0, usage.TotalTokens)
}

func TestOpenAI_ExtractUsage_EmptyResponse(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	usage := adapter.ExtractUsage([]byte(``))

	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
	assert.Equal(t, 0, usage.TotalTokens)
}

func TestOpenAI_ExtractUsage_InvalidJSON(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	usage := adapter.ExtractUsage([]byte(`{invalid json`))

	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
	assert.Equal(t, 0, usage.TotalTokens)
}

// -----------------------------------------------------------------------------
// Anthropic Usage Extraction
// -----------------------------------------------------------------------------

func TestAnthropic_ExtractUsage_Standard(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	// Standard Anthropic response (no caching)
	response := []byte(`{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "text", "text": "Hello!"}],
		"usage": {
			"input_tokens": 2000,
			"output_tokens": 800
		}
	}`)

	usage := adapter.ExtractUsage(response)

	// No cache tokens, so InputTokens = input_tokens as-is
	assert.Equal(t, 2000, usage.InputTokens, "should extract input_tokens (no cache)")
	assert.Equal(t, 800, usage.OutputTokens, "should extract output_tokens")
	assert.Equal(t, 2800, usage.TotalTokens, "should calculate total (input + output)")
}

func TestAnthropic_ExtractUsage_WithCacheTokens(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	// Response with prompt caching.
	// Anthropic's input_tokens is ALREADY the non-cached suffix (50 new tokens).
	// cache_creation_input_tokens + cache_read_input_tokens are billed separately.
	// Total = 50 + 1000 + 500 + 1500 = 3050
	response := []byte(`{
		"id": "msg_456",
		"usage": {
			"input_tokens": 50,
			"output_tokens": 1500,
			"cache_creation_input_tokens": 1000,
			"cache_read_input_tokens": 500
		}
	}`)

	usage := adapter.ExtractUsage(response)

	assert.Equal(t, 50, usage.InputTokens, "input_tokens is already the non-cached portion")
	assert.Equal(t, 1500, usage.OutputTokens)
	assert.Equal(t, 1000, usage.CacheCreationInputTokens)
	assert.Equal(t, 500, usage.CacheReadInputTokens)
	// TotalTokens = input(50) + cache_creation(1000) + cache_read(500) + output(1500) = 3050
	assert.Equal(t, 3050, usage.TotalTokens, "total must include all input categories")
}

func TestAnthropic_ExtractUsage_NoUsageField(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	response := []byte(`{"id": "msg_123", "content": []}`)

	usage := adapter.ExtractUsage(response)

	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
	assert.Equal(t, 0, usage.TotalTokens)
}

func TestAnthropic_ExtractUsage_EmptyResponse(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	usage := adapter.ExtractUsage([]byte(``))

	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
	assert.Equal(t, 0, usage.TotalTokens)
}

func TestAnthropic_ExtractUsage_InvalidJSON(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	usage := adapter.ExtractUsage([]byte(`{invalid`))

	assert.Equal(t, 0, usage.InputTokens)
	assert.Equal(t, 0, usage.OutputTokens)
	assert.Equal(t, 0, usage.TotalTokens)
}

// -----------------------------------------------------------------------------
// Model Extraction
// -----------------------------------------------------------------------------

func TestOpenAI_ExtractModel(t *testing.T) {
	adapter := adapters.NewOpenAIAdapter()

	tests := []struct {
		name        string
		requestBody string
		wantModel   string
	}{
		{
			name:        "simple model",
			requestBody: `{"model": "gpt-4o", "input": []}`,
			wantModel:   "gpt-4o",
		},
		{
			name:        "model with provider prefix",
			requestBody: `{"model": "openai/gpt-4o-mini", "input": []}`,
			wantModel:   "gpt-4o-mini",
		},
		{
			name:        "o1 model",
			requestBody: `{"model": "o1", "input": [{"type": "message"}]}`,
			wantModel:   "o1",
		},
		{
			name:        "empty body",
			requestBody: ``,
			wantModel:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := adapter.ExtractModel([]byte(tt.requestBody))
			assert.Equal(t, tt.wantModel, model)
		})
	}
}

func TestAnthropic_ExtractModel(t *testing.T) {
	adapter := adapters.NewAnthropicAdapter()

	tests := []struct {
		name        string
		requestBody string
		wantModel   string
	}{
		{
			name:        "claude-3-5-sonnet",
			requestBody: `{"model": "claude-3-5-sonnet-20241022", "messages": []}`,
			wantModel:   "claude-3-5-sonnet-20241022",
		},
		{
			name:        "model with provider prefix",
			requestBody: `{"model": "anthropic/claude-3-5-haiku-20241022", "messages": []}`,
			wantModel:   "claude-3-5-haiku-20241022",
		},
		{
			name:        "claude-3-opus",
			requestBody: `{"model": "claude-3-opus-20240229", "max_tokens": 1024}`,
			wantModel:   "claude-3-opus-20240229",
		},
		{
			name:        "empty body",
			requestBody: ``,
			wantModel:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := adapter.ExtractModel([]byte(tt.requestBody))
			assert.Equal(t, tt.wantModel, model)
		})
	}
}
