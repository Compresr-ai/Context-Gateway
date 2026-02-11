// Prompts for external LLM providers (OpenAI, Anthropic, Gemini).
//
// This file centralizes all prompts used for tool output compression.
// Two modes are supported:
//   - Query-Specific: Uses the user's query for relevance-aware compression
//   - Query-Agnostic: Compresses without knowledge of what user is asking
package external

import (
	"fmt"
	"strings"
)

// =============================================================================
// SYSTEM PROMPTS
// =============================================================================

// SystemPromptQuerySpecific is used when we know the user's question.
// This enables relevance-based compression - keep what's relevant to the query.
const SystemPromptQuerySpecific = `You are a tool output compression assistant. Your task is to compress tool outputs while preserving information relevant to the user's question.

Guidelines:
1. PRESERVE information directly relevant to the user's question
2. REMOVE redundant, repetitive, or boilerplate content
3. MAINTAIN key data: file paths, line numbers, function names, error messages
4. USE bullet points for lists when appropriate
5. KEEP code snippets that answer the question
6. REMOVE verbose logging, timestamps, and metadata unless relevant
7. OUTPUT only the compressed content - no explanations or meta-commentary

Target: Reduce to ~30-50% of original size while keeping relevant information.`

// SystemPromptQueryAgnostic is used when we don't know the user's question.
// This uses general-purpose compression - preserve structure and key information.
const SystemPromptQueryAgnostic = `You are a tool output compression assistant. Your task is to compress tool outputs while preserving the essential information structure.

Guidelines:
1. PRESERVE key structural elements: file paths, function names, class names
2. PRESERVE error messages with line numbers and context
3. PRESERVE numerical data and important strings
4. REMOVE redundant whitespace and boilerplate
5. REMOVE verbose logging and debug output
6. REMOVE repetitive patterns (show first instance + count)
7. USE bullet points for long lists (show first 3 + "... and N more")
8. OUTPUT only the compressed content - no explanations or meta-commentary

Target: Reduce to ~30-50% of original size while keeping essential structure.`

// =============================================================================
// USER PROMPT TEMPLATES
// =============================================================================

// UserPromptQuerySpecific formats the compression prompt when query is known.
func UserPromptQuerySpecific(userQuery, toolName, content string) string {
	return fmt.Sprintf(`User's Question: %s

Tool Name: %s

Tool Output to Compress:
%s

Compress the tool output above, keeping information relevant to the user's question.`, userQuery, toolName, content)
}

// UserPromptQueryAgnostic formats the compression prompt when query is unknown.
func UserPromptQueryAgnostic(toolName, content string) string {
	return fmt.Sprintf(`Tool Name: %s

Tool Output to Compress:
%s

Compress the tool output above, preserving essential structure and key information.`, toolName, content)
}

// =============================================================================
// LLM REQUEST BUILDERS
// =============================================================================

// OpenAIMessage represents a message in OpenAI chat format.
type OpenAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OpenAIChatRequest is the request body for OpenAI chat completions.
type OpenAIChatRequest struct {
	Model               string          `json:"model"`
	Messages            []OpenAIMessage `json:"messages"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Temperature         float64         `json:"temperature,omitempty"`
}

// OpenAIChatResponse is the response from OpenAI chat completions.
type OpenAIChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

// AnthropicMessage represents a message in Anthropic format.
type AnthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// AnthropicRequest is the request body for Anthropic messages API.
type AnthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []AnthropicMessage `json:"messages"`
	Temperature float64            `json:"temperature,omitempty"`
}

// AnthropicResponse is the response from Anthropic messages API.
type AnthropicResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Model        string `json:"model"`
	StopReason   string `json:"stop_reason"`
	StopSequence string `json:"stop_sequence,omitempty"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// BuildOpenAIRequest creates an OpenAI chat request for compression.
func BuildOpenAIRequest(model, toolName, content, userQuery string, queryAgnostic bool, maxTokens int) *OpenAIChatRequest {
	var systemPrompt, userPrompt string

	if queryAgnostic || userQuery == "" {
		systemPrompt = SystemPromptQueryAgnostic
		userPrompt = UserPromptQueryAgnostic(toolName, content)
	} else {
		systemPrompt = SystemPromptQuerySpecific
		userPrompt = UserPromptQuerySpecific(userQuery, toolName, content)
	}

	// Default max tokens based on content size
	if maxTokens == 0 {
		// Rough estimate: 1 token ≈ 4 chars, target 50% compression
		maxTokens = len(content) / 8
		if maxTokens < 256 {
			maxTokens = 256
		}
		if maxTokens > 4096 {
			maxTokens = 4096
		}
	}

	return &OpenAIChatRequest{
		Model: model,
		Messages: []OpenAIMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		MaxCompletionTokens: maxTokens,
		// Temperature omitted - use model default (some models don't support 0)
	}
}

// BuildAnthropicRequest creates an Anthropic messages request for compression.
func BuildAnthropicRequest(model, toolName, content, userQuery string, queryAgnostic bool, maxTokens int) *AnthropicRequest {
	var systemPrompt, userPrompt string

	if queryAgnostic || userQuery == "" {
		systemPrompt = SystemPromptQueryAgnostic
		userPrompt = UserPromptQueryAgnostic(toolName, content)
	} else {
		systemPrompt = SystemPromptQuerySpecific
		userPrompt = UserPromptQuerySpecific(userQuery, toolName, content)
	}

	// Default max tokens based on content size
	if maxTokens == 0 {
		// Rough estimate: 1 token ≈ 4 chars, target 50% compression
		maxTokens = len(content) / 8
		if maxTokens < 256 {
			maxTokens = 256
		}
		if maxTokens > 4096 {
			maxTokens = 4096
		}
	}

	return &AnthropicRequest{
		Model:     model,
		MaxTokens: maxTokens,
		System:    systemPrompt,
		Messages: []AnthropicMessage{
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.0, // Deterministic for consistent compression
	}
}

// ExtractOpenAIResponse extracts the compressed content from OpenAI response.
func ExtractOpenAIResponse(resp *OpenAIChatResponse) (string, error) {
	if resp.Error != nil {
		return "", fmt.Errorf("OpenAI API error: %s", resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("OpenAI response has no choices")
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

// ExtractAnthropicResponse extracts the compressed content from Anthropic response.
func ExtractAnthropicResponse(resp *AnthropicResponse) (string, error) {
	if resp.Error != nil {
		return "", fmt.Errorf("Anthropic API error: %s", resp.Error.Message)
	}
	if len(resp.Content) == 0 {
		return "", fmt.Errorf("Anthropic response has no content")
	}
	// Find text content
	for _, block := range resp.Content {
		if block.Type == "text" {
			return strings.TrimSpace(block.Text), nil
		}
	}
	return "", fmt.Errorf("Anthropic response has no text content")
}

// =============================================================================
// GEMINI TYPES
// =============================================================================

// GeminiPart represents a content part in Gemini format.
type GeminiPart struct {
	Text string `json:"text"`
}

// GeminiContent represents a content block in Gemini format.
type GeminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []GeminiPart `json:"parts"`
}

// GeminiGenerationConfig contains generation parameters.
type GeminiGenerationConfig struct {
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
	Temperature     float64 `json:"temperature"`
}

// GeminiRequest is the request body for Gemini generateContent API.
type GeminiRequest struct {
	SystemInstruction *GeminiContent          `json:"systemInstruction,omitempty"`
	Contents          []GeminiContent         `json:"contents"`
	GenerationConfig  *GeminiGenerationConfig `json:"generationConfig,omitempty"`
}

// GeminiResponse is the response from Gemini generateContent API.
type GeminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []GeminiPart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
}

// BuildGeminiRequest creates a Gemini generateContent request for compression.
func BuildGeminiRequest(model, toolName, content, userQuery string, queryAgnostic bool, maxTokens int) *GeminiRequest {
	var systemPrompt, userPrompt string

	if queryAgnostic || userQuery == "" {
		systemPrompt = SystemPromptQueryAgnostic
		userPrompt = UserPromptQueryAgnostic(toolName, content)
	} else {
		systemPrompt = SystemPromptQuerySpecific
		userPrompt = UserPromptQuerySpecific(userQuery, toolName, content)
	}

	if maxTokens == 0 {
		maxTokens = len(content) / 8
		if maxTokens < 256 {
			maxTokens = 256
		}
		if maxTokens > 4096 {
			maxTokens = 4096
		}
	}

	return &GeminiRequest{
		SystemInstruction: &GeminiContent{
			Parts: []GeminiPart{{Text: systemPrompt}},
		},
		Contents: []GeminiContent{
			{Role: "user", Parts: []GeminiPart{{Text: userPrompt}}},
		},
		GenerationConfig: &GeminiGenerationConfig{
			MaxOutputTokens: maxTokens,
			Temperature:     0.0,
		},
	}
}

// ExtractGeminiResponse extracts the compressed content from Gemini response.
func ExtractGeminiResponse(resp *GeminiResponse) (string, error) {
	if resp.Error != nil {
		return "", fmt.Errorf("Gemini API error (%d): %s", resp.Error.Code, resp.Error.Message)
	}
	if len(resp.Candidates) == 0 {
		return "", fmt.Errorf("Gemini response has no candidates")
	}
	parts := resp.Candidates[0].Content.Parts
	if len(parts) == 0 {
		return "", fmt.Errorf("Gemini response has no content parts")
	}
	return strings.TrimSpace(parts[0].Text), nil
}
