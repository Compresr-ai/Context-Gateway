package taskoutput

import (
	"encoding/json"
	"strings"

	"github.com/compresr/context-gateway/internal/adapters"
)

// codexTaskTools is the set of tool names used by Codex for SUBAGENT SPAWNING.
// Only wait_agent is a true subagent primitive — it waits for a separately spawned
// agent process and receives that agent's final output as its result.
//
// exec_command and shell_command are regular shell execution tools: they run a
// command in the current process and return stdout/stderr. Their outputs are
// compressed by the tool_output pipe like any other tool result.
var codexTaskTools = map[string]bool{
	"wait_agent": true, // Codex: wait for a spawned sub-agent and receive its output
}

// codexStructuredOutput represents the JSON output format from Codex exec_command.
// Example: {"output": "hello world", "metadata": {"exit_code": 0, "duration_seconds": 1.2}}
type codexStructuredOutput struct {
	Output   string         `json:"output"`
	Metadata map[string]any `json:"metadata"`
}

// CodexSchema handles subagent tool outputs from the OpenAI Codex CLI.
//
// Codex uses the Responses API (input[] not messages[]) and produces tool results
// for exec_command, shell_command, and wait_agent.
//
// Output format is either:
//   - Structured JSON: {"output": "...", "metadata": {"exit_code": 0, "duration_seconds": 1.2}}
//   - Plain text: "Exit code: 0\nWall time: 1.2 seconds\n<output>"
type CodexSchema struct{}

func (s *CodexSchema) Client() ClientAgent { return ClientCodex }

// IsTaskTool returns true for Codex execution tool names (case-insensitive).
func (s *CodexSchema) IsTaskTool(toolName string, _ string) bool {
	return codexTaskTools[strings.ToLower(toolName)]
}

// Extract parses Codex tool output into primary content + metadata.
// Handles both JSON and plain-text output formats.
func (s *CodexSchema) Extract(raw adapters.ExtractedContent) TaskOutput {
	content := raw.Content
	meta := TaskOutputMetadata{}

	// Try JSON format first.
	var structured codexStructuredOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &structured); err == nil && structured.Output != "" {
		meta.HasMetadata = true
		if structured.Metadata != nil {
			if exitCode, ok := structured.Metadata["exit_code"]; ok {
				switch v := exitCode.(type) {
				case float64:
					meta.ExitCode = int(v)
				case int:
					meta.ExitCode = v
				}
			}
			if dur, ok := structured.Metadata["duration_seconds"]; ok {
				if d, ok := dur.(float64); ok {
					meta.DurationMS = int64(d * 1000)
				}
			}
		}

		// Re-encode metadata for reconstruction.
		if metaBytes, err := json.Marshal(structured.Metadata); err == nil {
			meta.RawMetadataBlock = string(metaBytes)
		}

		return TaskOutput{
			PrimaryContent: structured.Output,
			Metadata:       meta,
			Source:         raw,
		}
	}

	// Plain text format: just use the full content as primary.
	return TaskOutput{
		PrimaryContent: content,
		Metadata:       meta,
		Source:         raw,
	}
}

// Reconstruct reassembles Codex tool output after compression.
// For structured JSON format, re-wraps compressed output back into the JSON envelope.
// For plain text, returns compressed content directly.
func (s *CodexSchema) Reconstruct(output TaskOutput, compressedContent string) string {
	if !output.Metadata.HasMetadata || output.Metadata.RawMetadataBlock == "" {
		return compressedContent
	}

	// Re-wrap in JSON envelope.
	var metaObj any
	if err := json.Unmarshal([]byte(output.Metadata.RawMetadataBlock), &metaObj); err != nil {
		return compressedContent
	}

	reconstructed := map[string]any{
		"output":   compressedContent,
		"metadata": metaObj,
	}
	bytes, err := json.Marshal(reconstructed)
	if err != nil {
		return compressedContent
	}
	return string(bytes)
}
