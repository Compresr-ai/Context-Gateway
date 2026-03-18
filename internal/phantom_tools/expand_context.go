package phantom_tools

// ExpandContextToolName is the phantom tool name for context expansion.
const ExpandContextToolName = "expand_context"

const expandContextDescription = "Expand a [REF:id] reference to retrieve the full uncompressed content."

// idSchema is the shared JSON schema bytes for the expand_context tool.
const idSchema = `{"type":"object","properties":{"id":{"type":"string","description":"The shadow ID (e.g., shadow_abc123)"}},"required":["id"]}`

func init() {
	precomputed := map[ProviderFormat][]byte{
		FormatAnthropic:       []byte(`{"name":"expand_context","description":"` + expandContextDescription + `","input_schema":` + idSchema + `}`),
		FormatOpenAIChat:      []byte(`{"type":"function","function":{"name":"expand_context","description":"` + expandContextDescription + `","parameters":` + idSchema + `}}`),
		FormatOpenAIResponses: []byte(`{"type":"function","name":"expand_context","description":"` + expandContextDescription + `","parameters":` + idSchema + `}`),
	}

	Register(PhantomTool{
		Name:            ExpandContextToolName,
		Description:     expandContextDescription,
		PrecomputedJSON: precomputed,
	})
}
