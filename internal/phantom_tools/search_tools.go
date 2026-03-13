package phantom_tools

import "encoding/json"

// SearchToolName is the default name for the gateway search/call tool.
const SearchToolName = "gateway_search_tools"

// SearchToolDescription is the description for the gateway_search_tools tool.
// Two modes: search (provide query) and call (provide tool_name + tool_input).
const SearchToolDescription = `Search for or execute available tools.

MODE 1 - SEARCH: Provide "query" to find tools by describing what you need.
Returns tool names, descriptions, and full input schemas.
Example: {"query": "read a file from disk"}

MODE 2 - CALL: Provide "tool_name" and "tool_input" to execute a discovered tool.
Use the exact parameter names and types from the schema returned by search.
Example: {"tool_name": "Read", "tool_input": {"file_path": "/foo/bar.txt"}}

Always search first if you haven't seen the tool's schema yet.`

// SearchToolSchema is the JSON schema for the gateway_search_tools tool.
// Supports two modes: search (query) and call (tool_name + tool_input).
// Validation of which fields are required happens in the handler, not the schema.
var SearchToolSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"query": map[string]any{
			"type":        "string",
			"description": "Search query to find available tools. Describe what you need to do. Example: 'read a file', 'search code', 'run a shell command'.",
		},
		"tool_name": map[string]any{
			"type":        "string",
			"description": "Name of a previously discovered tool to execute. Use this after finding a tool via query.",
		},
		"tool_input": map[string]any{
			"type":                 "object",
			"description":          "Input parameters for the tool being called. Must match the schema returned by the search results.",
			"additionalProperties": true,
		},
	},
	"required": []string{},
}

func init() {
	// Marshal the schema once (map ordering is alphabetical in Go's json.Marshal)
	schemaBytes, _ := json.Marshal(SearchToolSchema)
	descBytes, _ := json.Marshal(SearchToolDescription)

	precomputed := make(map[ProviderFormat][]byte, 4)

	// Anthropic format: {name, description, input_schema}
	precomputed[FormatAnthropic] = []byte(`{"name":"` + SearchToolName + `"` +
		`,"description":` + string(descBytes) +
		`,"input_schema":` + string(schemaBytes) + `}`)

	// Gemini uses the same format as Anthropic for now
	precomputed[FormatGemini] = precomputed[FormatAnthropic]

	// OpenAI Chat Completions: {type, function: {name, description, parameters}}
	precomputed[FormatOpenAIChat] = []byte(`{"type":"function","function":{"name":"` + SearchToolName + `"` +
		`,"description":` + string(descBytes) +
		`,"parameters":` + string(schemaBytes) + `}}`)

	// OpenAI Responses API: {type, name, description, parameters}
	precomputed[FormatOpenAIResponses] = []byte(`{"type":"function","name":"` + SearchToolName + `"` +
		`,"description":` + string(descBytes) +
		`,"parameters":` + string(schemaBytes) + `}`)

	Register(PhantomTool{
		Name:            SearchToolName,
		Description:     SearchToolDescription,
		PrecomputedJSON: precomputed,
	})
}
