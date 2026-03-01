// LLM provider request/response types for OpenAI, Anthropic, and Gemini.
//
// These types are used by:
//   - llm.go: CallLLM() for direct provider calls
//   - llm_prompts.go: Build*Request() for compression requests
package external

// =============================================================================
// OpenAI Types
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

// =============================================================================
// Anthropic Types
// =============================================================================

// AnthropicMessage represents a message in Anthropic format.
type AnthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// AnthropicRequest is the request body for Anthropic messages API.
// Also used for Bedrock with Anthropic models (set AnthropicVersion to "bedrock-2023-05-31").
type AnthropicRequest struct {
	Model            string             `json:"model"`
	MaxTokens        int                `json:"max_tokens"`
	System           string             `json:"system,omitempty"`
	Messages         []AnthropicMessage `json:"messages"`
	Temperature      float64            `json:"temperature,omitempty"`
	AnthropicVersion string             `json:"anthropic_version,omitempty"`
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

// =============================================================================
// Gemini Types
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
