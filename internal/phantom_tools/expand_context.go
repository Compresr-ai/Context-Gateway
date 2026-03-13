package phantom_tools

import "encoding/json"

// ExpandContextToolName is the phantom tool name for context expansion.
// When the gateway compresses tool outputs, it injects this tool so the LLM
// can retrieve the original uncompressed content via shadow IDs.
const ExpandContextToolName = "expand_context"

// expandContextDescription is the description shown to the LLM.
const expandContextDescription = "Retrieve the full, uncompressed content for a compressed tool output. " +
	"When you see SHADOW markers (prefixed with shadow_ IDs) in tool results, call this tool with the shadow ID " +
	"to get the complete original content. Always expand if the compressed version lacks details you need."

func init() {
	// Schema for the expand_context tool (single "id" parameter).
	type idParam struct {
		Type        string `json:"type"`
		Description string `json:"description"`
	}
	type properties struct {
		ID idParam `json:"id"`
	}
	type schema struct {
		Type       string     `json:"type"`
		Properties properties `json:"properties"`
		Required   []string   `json:"required"`
	}

	idP := idParam{Type: "string", Description: "The shadow reference ID to expand (e.g. shadow_abc123)"}
	props := properties{ID: idP}
	sch := schema{Type: "object", Properties: props, Required: []string{"id"}}

	precomputed := make(map[ProviderFormat][]byte, 4)

	// Anthropic format: {name, description, input_schema}
	precomputed[FormatAnthropic], _ = json.Marshal(struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema schema `json:"input_schema"`
	}{
		Name:        ExpandContextToolName,
		Description: expandContextDescription,
		InputSchema: sch,
	})

	// Gemini uses the same format as Anthropic for now
	precomputed[FormatGemini] = precomputed[FormatAnthropic]

	// OpenAI Chat Completions: {type, function: {name, description, parameters}}
	precomputed[FormatOpenAIChat], _ = json.Marshal(struct {
		Type     string `json:"type"`
		Function struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Parameters  schema `json:"parameters"`
		} `json:"function"`
	}{
		Type: "function",
		Function: struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Parameters  schema `json:"parameters"`
		}{
			Name:        ExpandContextToolName,
			Description: expandContextDescription,
			Parameters:  sch,
		},
	})

	// OpenAI Responses API: {type, name, description, parameters}
	precomputed[FormatOpenAIResponses], _ = json.Marshal(struct {
		Type        string `json:"type"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  schema `json:"parameters"`
	}{
		Type:        "function",
		Name:        ExpandContextToolName,
		Description: expandContextDescription,
		Parameters:  sch,
	})

	Register(PhantomTool{
		Name:            ExpandContextToolName,
		Description:     expandContextDescription,
		PrecomputedJSON: precomputed,
	})
}
