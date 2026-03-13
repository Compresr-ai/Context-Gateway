// Compression prompts and request builders for external LLM providers.
//
// USAGE:
//   - BuildOpenAIRequest() / BuildAnthropicRequest() / BuildGeminiRequest()
//   - ExtractOpenAIResponse() / ExtractAnthropicResponse() / ExtractGeminiResponse()
//
// Two compression modes:
//   - Query-Specific: Uses the user's query for relevance-aware compression
//   - Query-Agnostic: Compresses without knowledge of what user is asking
package external

import (
	"fmt"
	"strings"
)

// =============================================================================
// System Prompts
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
// User Prompt Templates
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
// Structured Tail Prompts (verbatim prefix preserved, only tail compressed)
// =============================================================================

// SystemPromptStructuredTail is used when the beginning of structured output
// has been preserved verbatim and only the tail needs compression.
const SystemPromptStructuredTail = `You are compressing the TAIL portion of a structured data output.
The BEGINNING of this output has already been preserved verbatim and will be shown to the user.
Your job is to summarize only the remaining data.

Guidelines:
1. PRESERVE all unique keys/field names not already visible in the preserved prefix
2. PRESERVE counts and aggregates (e.g., "15 items total, 12 completed")
3. PRESERVE error messages, status values, and important identifiers
4. SUMMARIZE repetitive structures (e.g., "N more entries with same schema as above")
5. Do NOT repeat information already shown in the verbatim prefix
6. OUTPUT only the summary - no explanations or meta-commentary

Target: Concise summary of remaining data that complements the verbatim prefix.`

// UserPromptStructuredTail formats the compression prompt for a structured tail.
func UserPromptStructuredTail(format, toolName, tailContent string) string {
	return fmt.Sprintf(`Format: %s
Tool Name: %s

Remaining data to summarize (beginning was preserved verbatim):
%s

Summarize the remaining data above.`, format, toolName, tailContent)
}

// =============================================================================
// Request Builders
// =============================================================================

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

// =============================================================================
// Response Extractors
// =============================================================================

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
		return "", fmt.Errorf("anthropic API error: %s", resp.Error.Message)
	}
	if len(resp.Content) == 0 {
		return "", fmt.Errorf("anthropic response has no content")
	}
	// Find text content
	for _, block := range resp.Content {
		if block.Type == "text" {
			return strings.TrimSpace(block.Text), nil
		}
	}
	return "", fmt.Errorf("anthropic response has no text content")
}

// ExtractGeminiResponse extracts the compressed content from Gemini response.
func ExtractGeminiResponse(resp *GeminiResponse) (string, error) {
	if resp.Error != nil {
		return "", fmt.Errorf("gemini API error (%d): %s", resp.Error.Code, resp.Error.Message)
	}
	if len(resp.Candidates) == 0 {
		return "", fmt.Errorf("gemini response has no candidates")
	}
	parts := resp.Candidates[0].Content.Parts
	if len(parts) == 0 {
		return "", fmt.Errorf("gemini response has no content parts")
	}
	return strings.TrimSpace(parts[0].Text), nil
}
