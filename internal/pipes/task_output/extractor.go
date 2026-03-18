package taskoutput

import "github.com/compresr/context-gateway/internal/adapters"

// TaskOutputExtractor identifies and parses task/subagent tool outputs using
// the schema for the detected ClientAgent.
//
// It replaces the previous pattern-based detector (IsTaskOutput + glob patterns)
// with schema-driven detection: each ClientAgent has a registered ClientSchema
// that knows its own tool names and content format.
type TaskOutputExtractor struct {
	schema ClientSchema
}

// NewExtractor creates a TaskOutputExtractor for the given client agent.
// Uses GenericSchema (matches nothing) for unknown clients.
func NewExtractor(client ClientAgent) *TaskOutputExtractor {
	return &TaskOutputExtractor{schema: SchemaForClient(client)}
}

// IsTaskTool reports whether a tool output is a subagent task result for this client.
func (e *TaskOutputExtractor) IsTaskTool(toolName, content string) bool {
	return e.schema.IsTaskTool(toolName, content)
}

// ExtractAll filters rawOutputs to those matching the schema's task tools
// and returns normalized TaskOutput structs.
func (e *TaskOutputExtractor) ExtractAll(rawOutputs []adapters.ExtractedContent) []TaskOutput {
	var result []TaskOutput
	for _, raw := range rawOutputs {
		if e.schema.IsTaskTool(raw.ToolName, raw.Content) {
			result = append(result, e.schema.Extract(raw))
		}
	}
	return result
}

// Reconstruct delegates to the schema to reassemble content after compression.
func (e *TaskOutputExtractor) Reconstruct(output TaskOutput, compressedContent string) string {
	return e.schema.Reconstruct(output, compressedContent)
}

// Schema returns the underlying ClientSchema.
func (e *TaskOutputExtractor) Schema() ClientSchema { return e.schema }
