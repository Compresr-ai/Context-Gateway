package taskoutput

import "github.com/compresr/context-gateway/internal/adapters"

// GenericSchema is the fallback schema for unknown or unrecognized AI clients.
// It matches no tool outputs (IsTaskTool always returns false), so the task_output
// pipe becomes a pure pass-through for unknown clients. Tool outputs are processed
// by the tool_output pipe as usual.
type GenericSchema struct{}

func (s *GenericSchema) Client() ClientAgent { return ClientGeneric }

// IsTaskTool always returns false for the generic schema.
// Unknown clients have no known subagent tool names.
func (s *GenericSchema) IsTaskTool(_ string, _ string) bool { return false }

// Extract returns the content unchanged as a TaskOutput with no metadata.
func (s *GenericSchema) Extract(raw adapters.ExtractedContent) TaskOutput {
	return TaskOutput{
		PrimaryContent: raw.Content,
		Source:         raw,
	}
}

// Reconstruct returns the compressed content unchanged (no metadata to preserve).
func (s *GenericSchema) Reconstruct(_ TaskOutput, compressedContent string) string {
	return compressedContent
}
