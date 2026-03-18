package taskoutput

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/compresr/context-gateway/internal/adapters"
)

// claudeCodeTaskTools is the set of tool names used by Claude Code for subagent delegation.
// These are the only tool names this schema matches.
var claudeCodeTaskTools = map[string]bool{
	"agent": true, // Claude Code spawns subagents via the "Agent" tool (case-insensitive match below)
	"task":  true, // Alternative name used in some Claude Code versions
}

// agentIDPattern matches the agentId line in Claude Code task output metadata.
// Example: "agentId: a1b2c3d4e5f6"
var agentIDPattern = regexp.MustCompile(`(?m)^agentId:\s*(\S+)`)

// usageBlockPattern matches the full <usage> ... </usage> block.
var usageBlockPattern = regexp.MustCompile(`(?s)<usage>(.*?)</usage>`)

// usageFieldPattern matches key: value lines inside the usage block.
var usageFieldPattern = regexp.MustCompile(`(?m)^(\w+):\s*(\d+)`)

// ClaudeCodeSchema handles subagent tool outputs from the Claude Code CLI.
//
// Claude Code uses the "Agent" (and "Task") tool for subagent delegation.
// Tool result content is concatenated from two content blocks:
//
//  1. Primary output: the subagent's free-form response text.
//  2. Metadata block: "agentId: <hex>\n<usage>total_tokens: N\ntool_uses: N\nduration_ms: N</usage>"
//
// The Anthropic adapter concatenates both blocks into one Content string.
// This schema splits them for targeted compression of only the primary content.
type ClaudeCodeSchema struct{}

func (s *ClaudeCodeSchema) Client() ClientAgent { return ClientClaudeCode }

// IsTaskTool returns true for "Agent" and "Task" tool names (case-insensitive).
func (s *ClaudeCodeSchema) IsTaskTool(toolName string, _ string) bool {
	return claudeCodeTaskTools[strings.ToLower(toolName)]
}

// Extract parses Claude Code task output into primary content + metadata.
//
// The Anthropic adapter concatenates content blocks, producing content like:
//
//	"<main output text>agentId: xyz\n<usage>total_tokens: 123\ntool_uses: 5\nduration_ms: 4500</usage>"
//
// This function splits on the agentId marker, which always appears before <usage>.
func (s *ClaudeCodeSchema) Extract(raw adapters.ExtractedContent) TaskOutput {
	content := raw.Content
	meta := TaskOutputMetadata{}

	// Find the metadata block start: look for "agentId:" line.
	agentMatch := agentIDPattern.FindStringIndex(content)
	primaryContent := content
	rawMetaBlock := ""

	if agentMatch != nil {
		// Split at the agentId line (trim any trailing newline from primary).
		primaryContent = strings.TrimRight(content[:agentMatch[0]], "\n")
		rawMetaBlock = content[agentMatch[0]:]

		// Parse agentId.
		idMatches := agentIDPattern.FindStringSubmatch(rawMetaBlock)
		if len(idMatches) > 1 {
			meta.AgentID = idMatches[1]
		}

		// Parse <usage> block fields.
		usageMatch := usageBlockPattern.FindStringSubmatch(rawMetaBlock)
		if len(usageMatch) > 1 {
			for _, fieldMatch := range usageFieldPattern.FindAllStringSubmatch(usageMatch[1], -1) {
				if len(fieldMatch) < 3 {
					continue
				}
				val, err := strconv.ParseInt(fieldMatch[2], 10, 64)
				if err != nil {
					continue
				}
				switch fieldMatch[1] {
				case "total_tokens":
					meta.TotalTokens = int(val)
				case "tool_uses":
					meta.ToolUses = int(val)
				case "duration_ms":
					meta.DurationMS = val
				}
			}
		}

		meta.HasMetadata = true
		meta.RawMetadataBlock = rawMetaBlock
	}

	return TaskOutput{
		PrimaryContent: primaryContent,
		Metadata:       meta,
		Source:         raw,
	}
}

// Reconstruct reassembles the tool output after compression.
// The metadata block (agentId + <usage>) is re-appended unchanged so the
// downstream LLM still receives subagent context/cost information.
func (s *ClaudeCodeSchema) Reconstruct(output TaskOutput, compressedContent string) string {
	if !output.Metadata.HasMetadata || output.Metadata.RawMetadataBlock == "" {
		return compressedContent
	}
	return compressedContent + "\n" + output.Metadata.RawMetadataBlock
}
