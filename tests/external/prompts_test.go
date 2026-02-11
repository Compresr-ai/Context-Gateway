package external_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/external"
)

// TestSystemPrompts tests the system prompt constants.
func TestSystemPrompts(t *testing.T) {
	t.Run("SystemPromptQuerySpecific_contains_guidelines", func(t *testing.T) {
		prompt := external.SystemPromptQuerySpecific

		assert.Contains(t, prompt, "30-50%")
		assert.Contains(t, prompt, "PRESERVE")
		assert.Contains(t, prompt, "relevant to the user's question")
	})

	t.Run("SystemPromptQueryAgnostic_contains_guidelines", func(t *testing.T) {
		prompt := external.SystemPromptQueryAgnostic

		assert.Contains(t, prompt, "essential information structure")
		assert.Contains(t, prompt, "PRESERVE")
		assert.Contains(t, prompt, "30-50%")
	})
}

// TestUserPrompts tests user prompt formatting functions.
func TestUserPrompts(t *testing.T) {
	t.Run("UserPromptQuerySpecific_formats_correctly", func(t *testing.T) {
		prompt := external.UserPromptQuerySpecific(
			"How do I fix this error?",
			"bash",
			"Error: file not found",
		)

		assert.Contains(t, prompt, "How do I fix this error?")
		assert.Contains(t, prompt, "bash")
		assert.Contains(t, prompt, "Error: file not found")
		assert.Contains(t, prompt, "User's Question:")
		assert.Contains(t, prompt, "Tool Name:")
		assert.Contains(t, prompt, "Tool Output to Compress:")
	})

	t.Run("UserPromptQueryAgnostic_formats_correctly", func(t *testing.T) {
		prompt := external.UserPromptQueryAgnostic(
			"read_file",
			"package main\nfunc main() {}",
		)

		assert.Contains(t, prompt, "read_file")
		assert.Contains(t, prompt, "package main")
		assert.Contains(t, prompt, "Tool Name:")
		assert.Contains(t, prompt, "Tool Output to Compress:")
		assert.NotContains(t, prompt, "User's Question:")
	})
}

// TestBuildOpenAIRequest tests OpenAI request building.
func TestBuildOpenAIRequest(t *testing.T) {
	t.Run("builds_query_specific_request", func(t *testing.T) {
		req := external.BuildOpenAIRequest(
			"gpt-5-nano",
			"bash",
			"output content here",
			"what files are there?",
			false, // query_agnostic = false (use query)
			0,
		)

		assert.Equal(t, "gpt-5-nano", req.Model)
		assert.Len(t, req.Messages, 2)
		assert.Equal(t, "system", req.Messages[0].Role)
		assert.Equal(t, "user", req.Messages[1].Role)
		assert.Contains(t, req.Messages[0].Content, "relevant to the user's question")
		assert.Contains(t, req.Messages[1].Content, "what files are there?")
		// Temperature omitted - uses model default
	})

	t.Run("builds_query_agnostic_request", func(t *testing.T) {
		req := external.BuildOpenAIRequest(
			"gpt-5-nano",
			"read_file",
			"file content",
			"user query",
			true, // query_agnostic = true (ignore query)
			0,
		)

		assert.Contains(t, req.Messages[0].Content, "essential information structure")
		assert.NotContains(t, req.Messages[1].Content, "User's Question:")
	})

	t.Run("auto_calculates_max_tokens", func(t *testing.T) {
		// Small content
		req := external.BuildOpenAIRequest("gpt-5-nano", "bash", "short", "", true, 0)
		assert.Equal(t, 256, req.MaxCompletionTokens) // minimum

		// Large content (10KB)
		largeContent := strings.Repeat("x", 10000)
		req = external.BuildOpenAIRequest("gpt-5-nano", "bash", largeContent, "", true, 0)
		assert.Equal(t, 1250, req.MaxCompletionTokens) // 10000 / 8

		// Very large content
		veryLarge := strings.Repeat("x", 100000)
		req = external.BuildOpenAIRequest("gpt-5-nano", "bash", veryLarge, "", true, 0)
		assert.Equal(t, 4096, req.MaxCompletionTokens) // maximum cap
	})

	t.Run("respects_explicit_max_tokens", func(t *testing.T) {
		req := external.BuildOpenAIRequest("gpt-5-nano", "bash", "content", "", true, 1000)
		assert.Equal(t, 1000, req.MaxCompletionTokens)
	})
}

// TestBuildAnthropicRequest tests Anthropic request building.
func TestBuildAnthropicRequest(t *testing.T) {
	t.Run("builds_query_specific_request", func(t *testing.T) {
		req := external.BuildAnthropicRequest(
			"claude-haiku-4-5",
			"str_replace_editor",
			"code content",
			"fix the bug",
			false, // query_agnostic = false
			0,
		)

		assert.Equal(t, "claude-haiku-4-5", req.Model)
		assert.Len(t, req.Messages, 1)
		assert.Equal(t, "user", req.Messages[0].Role)
		assert.Contains(t, req.System, "relevant to the user's question")
		assert.Contains(t, req.Messages[0].Content, "fix the bug")
		assert.Equal(t, 0.0, req.Temperature)
	})

	t.Run("builds_query_agnostic_request", func(t *testing.T) {
		req := external.BuildAnthropicRequest(
			"claude-haiku-4-5",
			"bash",
			"output",
			"query",
			true, // query_agnostic
			0,
		)

		assert.Contains(t, req.System, "essential information structure")
		assert.NotContains(t, req.Messages[0].Content, "User's Question:")
	})

	t.Run("auto_calculates_max_tokens", func(t *testing.T) {
		req := external.BuildAnthropicRequest("claude-haiku-4-5", "bash", "short", "", true, 0)
		assert.Equal(t, 256, req.MaxTokens)
	})
}

// TestExtractResponses tests response extraction.
func TestExtractResponses(t *testing.T) {
	t.Run("extracts_openai_response", func(t *testing.T) {
		resp := &external.OpenAIChatResponse{
			Choices: []struct {
				Index   int `json:"index"`
				Message struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			}{
				{
					Index: 0,
					Message: struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					}{
						Role:    "assistant",
						Content: "  compressed content  ",
					},
					FinishReason: "stop",
				},
			},
		}

		content, err := external.ExtractOpenAIResponse(resp)
		require.NoError(t, err)
		assert.Equal(t, "compressed content", content) // trimmed
	})

	t.Run("handles_openai_error", func(t *testing.T) {
		resp := &external.OpenAIChatResponse{
			Error: &struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
			}{
				Message: "rate limit exceeded",
				Type:    "rate_limit_error",
			},
		}

		_, err := external.ExtractOpenAIResponse(resp)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rate limit exceeded")
	})

	t.Run("handles_empty_openai_choices", func(t *testing.T) {
		resp := &external.OpenAIChatResponse{
			Choices: []struct {
				Index   int `json:"index"`
				Message struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			}{},
		}

		_, err := external.ExtractOpenAIResponse(resp)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no choices")
	})

	t.Run("extracts_anthropic_response", func(t *testing.T) {
		resp := &external.AnthropicResponse{
			Content: []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{
				{Type: "text", Text: "  compressed  "},
			},
		}

		content, err := external.ExtractAnthropicResponse(resp)
		require.NoError(t, err)
		assert.Equal(t, "compressed", content) // trimmed
	})

	t.Run("handles_anthropic_error", func(t *testing.T) {
		resp := &external.AnthropicResponse{
			Error: &struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			}{
				Type:    "invalid_request_error",
				Message: "invalid model",
			},
		}

		_, err := external.ExtractAnthropicResponse(resp)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid model")
	})

	t.Run("handles_empty_anthropic_content", func(t *testing.T) {
		resp := &external.AnthropicResponse{
			Content: []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{},
		}

		_, err := external.ExtractAnthropicResponse(resp)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no content")
	})

	t.Run("finds_text_in_mixed_anthropic_content", func(t *testing.T) {
		resp := &external.AnthropicResponse{
			Content: []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{
				{Type: "thinking", Text: "thinking..."},
				{Type: "text", Text: "actual result"},
			},
		}

		content, err := external.ExtractAnthropicResponse(resp)
		require.NoError(t, err)
		assert.Equal(t, "actual result", content)
	})
}

// TestBuildGeminiRequest tests Gemini request building.
func TestBuildGeminiRequest(t *testing.T) {
	t.Run("builds_query_specific_request", func(t *testing.T) {
		req := external.BuildGeminiRequest(
			"gemini-2.0-flash",
			"bash",
			"output content here",
			"what files are there?",
			false,
			0,
		)

		require.NotNil(t, req.SystemInstruction)
		assert.Len(t, req.SystemInstruction.Parts, 1)
		assert.Contains(t, req.SystemInstruction.Parts[0].Text, "relevant to the user's question")
		assert.Len(t, req.Contents, 1)
		assert.Equal(t, "user", req.Contents[0].Role)
		assert.Contains(t, req.Contents[0].Parts[0].Text, "what files are there?")
		require.NotNil(t, req.GenerationConfig)
		assert.Equal(t, 0.0, req.GenerationConfig.Temperature)
	})

	t.Run("builds_query_agnostic_request", func(t *testing.T) {
		req := external.BuildGeminiRequest(
			"gemini-2.0-flash",
			"read_file",
			"file content",
			"user query",
			true,
			0,
		)

		assert.Contains(t, req.SystemInstruction.Parts[0].Text, "essential information structure")
		assert.NotContains(t, req.Contents[0].Parts[0].Text, "User's Question:")
	})

	t.Run("auto_calculates_max_tokens", func(t *testing.T) {
		req := external.BuildGeminiRequest("gemini-2.0-flash", "bash", "short", "", true, 0)
		assert.Equal(t, 256, req.GenerationConfig.MaxOutputTokens)

		largeContent := strings.Repeat("x", 10000)
		req = external.BuildGeminiRequest("gemini-2.0-flash", "bash", largeContent, "", true, 0)
		assert.Equal(t, 1250, req.GenerationConfig.MaxOutputTokens)

		veryLarge := strings.Repeat("x", 100000)
		req = external.BuildGeminiRequest("gemini-2.0-flash", "bash", veryLarge, "", true, 0)
		assert.Equal(t, 4096, req.GenerationConfig.MaxOutputTokens)
	})

	t.Run("respects_explicit_max_tokens", func(t *testing.T) {
		req := external.BuildGeminiRequest("gemini-2.0-flash", "bash", "content", "", true, 1000)
		assert.Equal(t, 1000, req.GenerationConfig.MaxOutputTokens)
	})
}

// TestExtractGeminiResponse tests Gemini response extraction.
func TestExtractGeminiResponse(t *testing.T) {
	t.Run("extracts_successful_response", func(t *testing.T) {
		resp := &external.GeminiResponse{}
		resp.Candidates = append(resp.Candidates, struct {
			Content struct {
				Parts []external.GeminiPart `json:"parts"`
			} `json:"content"`
		}{
			Content: struct {
				Parts []external.GeminiPart `json:"parts"`
			}{
				Parts: []external.GeminiPart{{Text: "  compressed content  "}},
			},
		})

		content, err := external.ExtractGeminiResponse(resp)
		require.NoError(t, err)
		assert.Equal(t, "compressed content", content)
	})

	t.Run("handles_api_error", func(t *testing.T) {
		resp := &external.GeminiResponse{
			Error: &struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Status  string `json:"status"`
			}{
				Code:    429,
				Message: "quota exceeded",
				Status:  "RESOURCE_EXHAUSTED",
			},
		}

		_, err := external.ExtractGeminiResponse(resp)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "quota exceeded")
		assert.Contains(t, err.Error(), "429")
	})

	t.Run("handles_no_candidates", func(t *testing.T) {
		resp := &external.GeminiResponse{}
		_, err := external.ExtractGeminiResponse(resp)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no candidates")
	})

	t.Run("handles_no_parts", func(t *testing.T) {
		resp := &external.GeminiResponse{}
		resp.Candidates = append(resp.Candidates, struct {
			Content struct {
				Parts []external.GeminiPart `json:"parts"`
			} `json:"content"`
		}{
			Content: struct {
				Parts []external.GeminiPart `json:"parts"`
			}{
				Parts: []external.GeminiPart{},
			},
		})

		_, err := external.ExtractGeminiResponse(resp)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no content parts")
	})
}
