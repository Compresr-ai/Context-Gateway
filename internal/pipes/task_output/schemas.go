package taskoutput

import "github.com/compresr/context-gateway/internal/adapters"

// ClientSchema is a provider-aware schema that knows how to identify and process
// task/subagent tool outputs for a specific AI client.
//
// Each AI client (Claude Code, Codex, etc.) has its own schema implementation.
// The schema is selected based on the detected ClientAgent from the request context.
type ClientSchema interface {
	// Client returns the agent type this schema handles.
	Client() ClientAgent

	// IsTaskTool returns true if this tool result is a subagent task output
	// for this client. Implementations check tool name (and optionally content).
	IsTaskTool(toolName string, content string) bool

	// Extract parses a raw tool output into a normalized TaskOutput.
	// Returns the original content as PrimaryContent if parsing fails gracefully.
	Extract(raw adapters.ExtractedContent) TaskOutput

	// Reconstruct builds the final content string to store after compression.
	// The compressed text replaces PrimaryContent; any metadata block is re-appended.
	// For passthrough, call Reconstruct(output, output.PrimaryContent) to get original.
	Reconstruct(output TaskOutput, compressedContent string) string
}

// schemaRegistry maps ClientAgent to its schema.
var schemaRegistry = map[ClientAgent]ClientSchema{
	ClientClaudeCode: &ClaudeCodeSchema{},
	ClientCodex:      &CodexSchema{},
	ClientGeneric:    &GenericSchema{},
}

// SchemaForClient returns the registered schema for a given client agent.
// Falls back to GenericSchema for unknown clients.
func SchemaForClient(client ClientAgent) ClientSchema {
	if schema, ok := schemaRegistry[client]; ok {
		return schema
	}
	return schemaRegistry[ClientGeneric]
}
