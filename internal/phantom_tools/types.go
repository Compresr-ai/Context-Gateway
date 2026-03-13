// Package phantom_tools provides a centralized registry for phantom tool definitions.
//
// DESIGN: Phantom tools are tools injected by the gateway into LLM requests.
// They are intercepted on response and handled internally - the client never
// sees them. This package centralizes all phantom tool definitions so that:
//   - Adding a new phantom tool = create a file + register in init()
//   - The gateway can iterate all tools and inject them in one pass
//   - Pre-computed JSON bytes ensure KV-cache stability
//   - Future MCP server tools can plug in via the same registry
//
// Each phantom tool provides pre-computed JSON bytes for each provider format.
// This avoids runtime marshaling and ensures byte-identical output across calls.
package phantom_tools

import "encoding/json"

// ProviderFormat represents the JSON format required by a specific provider API.
type ProviderFormat int

const (
	// FormatAnthropic is the Anthropic Messages API format: {name, description, input_schema}
	FormatAnthropic ProviderFormat = iota

	// FormatOpenAIChat is the OpenAI Chat Completions format: {type, function: {name, description, parameters}}
	FormatOpenAIChat

	// FormatOpenAIResponses is the OpenAI Responses API format: {type, name, description, parameters}
	FormatOpenAIResponses

	// FormatGemini is the Google Gemini format. Currently maps to Anthropic format
	// but kept separate for future Gemini-specific tool schemas.
	FormatGemini
)

// PhantomTool represents a single phantom tool with pre-computed JSON for each provider format.
type PhantomTool struct {
	// Name is the tool name as seen by the LLM (e.g., "expand_context", "gateway_search_tools").
	Name string

	// Description is a human-readable description of the tool.
	Description string

	// PrecomputedJSON holds pre-built JSON bytes for each provider format.
	// Computed once at init() time for deterministic, cache-safe output.
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
// Used when phantom tool calls appear in conversation history - the stub ensures
// the LLM doesn't error on unknown tool names while keeping token overhead minimal.
type StubBuilder struct{}

// BuildStub creates a minimal tool definition stub for the given tool name and format.
// The stub has an empty schema ({type: "object", properties: {}}) to satisfy validation.
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
