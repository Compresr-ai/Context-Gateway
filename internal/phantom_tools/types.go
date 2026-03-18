// Package phantom_tools provides a registry for gateway-injected phantom tools.
package phantom_tools

import "encoding/json"

// ProviderFormat represents the JSON format required by a specific provider API.
type ProviderFormat int

const (
	// FormatAnthropic is the Anthropic Messages API format.
	FormatAnthropic ProviderFormat = iota

	// FormatOpenAIChat is the OpenAI Chat Completions format.
	FormatOpenAIChat

	// FormatOpenAIResponses is the OpenAI Responses API format.
	FormatOpenAIResponses

	// FormatGemini is the Google Gemini format (kept separate for future Gemini-specific schemas).
	FormatGemini
)

// PhantomTool represents a single phantom tool with pre-computed JSON for each provider format.
type PhantomTool struct {
	Name            string
	Description     string
	PrecomputedJSON map[ProviderFormat][]byte
}

// GetJSON returns the pre-computed JSON bytes for the given provider format.
// Returns nil if no JSON is registered for that format.
func (t *PhantomTool) GetJSON(format ProviderFormat) []byte {
	if t.PrecomputedJSON == nil {
		return nil
	}
	return t.PrecomputedJSON[format]
}

// StubBuilder generates minimal tool stubs for phantom tools.
type StubBuilder struct{}

// BuildStub creates a minimal tool definition stub for the given tool name and format.
func (s *StubBuilder) BuildStub(toolName string, format ProviderFormat) []byte {
	emptySchema := struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
	}{
		Type:       "object",
		Properties: map[string]any{},
	}

	switch format {
	case FormatOpenAIChat:
		b, _ := json.Marshal(struct {
			Type     string `json:"type"`
			Function struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Parameters  any    `json:"parameters"`
			} `json:"function"`
		}{
			Type: "function",
			Function: struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Parameters  any    `json:"parameters"`
			}{
				Name:        toolName,
				Description: "Gateway-managed tool.",
				Parameters:  emptySchema,
			},
		})
		return b

	case FormatOpenAIResponses:
		b, _ := json.Marshal(struct {
			Type        string `json:"type"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Parameters  any    `json:"parameters"`
		}{
			Type:        "function",
			Name:        toolName,
			Description: "Gateway-managed tool.",
			Parameters:  emptySchema,
		})
		return b

	default: // Anthropic / Gemini
		b, _ := json.Marshal(struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			InputSchema any    `json:"input_schema"`
		}{
			Name:        toolName,
			Description: "Gateway-managed tool.",
			InputSchema: emptySchema,
		})
		return b
	}
}
