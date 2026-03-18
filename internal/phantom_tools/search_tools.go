package phantom_tools

const SearchToolName = "gateway_search_tools"
const SearchToolDescription = "Search for tools by functionality. Returns full schemas for matching tools."

const searchToolSchema = `{"type":"object","properties":{"query":{"type":"string","description":"Natural language description of what you want to do"}},"required":["query"]}`

func init() {
	precomputed := map[ProviderFormat][]byte{
		FormatAnthropic:       []byte(`{"name":"gateway_search_tools","description":"Search for tools by functionality. Returns full schemas for matching tools.","input_schema":` + searchToolSchema + `}`),
		FormatOpenAIChat:      []byte(`{"type":"function","function":{"name":"gateway_search_tools","description":"Search for tools by functionality. Returns full schemas for matching tools.","parameters":` + searchToolSchema + `}}`),
		FormatOpenAIResponses: []byte(`{"type":"function","name":"gateway_search_tools","description":"Search for tools by functionality. Returns full schemas for matching tools.","parameters":` + searchToolSchema + `}`),
	}
	Register(PhantomTool{
		Name:            SearchToolName,
		Description:     SearchToolDescription,
		PrecomputedJSON: precomputed,
	})
}
