// Tool output expansion - handles expand_context loop.
//
// V2 DESIGN: When tool outputs are compressed, we inject an expand_context tool.
// If the LLM needs full content, it calls expand_context(shadow_id).
// This file handles that loop:
//  1. Forward request to LLM
//  2. Check response for expand_context tool calls
//  3. If found: retrieve original from store, inject as tool result, repeat
//  4. If not found: filter phantom tool from response, return to client
//
// V2 Improvements:
//   - E10: Circular expansion prevention (track expanded IDs)
//   - E14/E15: Stream buffering for phantom tool suppression
//   - E26: Filter expand_context from final response
//
// MaxExpandLoops (5) prevents infinite recursion.
package tooloutput

import (
	"encoding/json"
	"strings"
)

// extractExpandPatterns extracts all shadow IDs from <<<EXPAND:shadow_xxx>>> patterns in text.
func extractExpandPatterns(text string) []string {
	var ids []string
	remaining := text
	for {
		idx := strings.Index(remaining, ExpandContextTextPrefix)
		if idx < 0 {
			break
		}
		start := idx + len(ExpandContextTextPrefix)
		end := strings.Index(remaining[start:], ExpandContextTextSuffix)
		if end < 0 {
			break
		}
		id := remaining[start : start+end]
		if id != "" {
			ids = append(ids, id)
		}
		remaining = remaining[start+end+len(ExpandContextTextSuffix):]
	}
	return ids
}

// ParseExpandPatternsFromText scans assistant text content for <<<EXPAND:shadow_xxx>>> patterns.
// Returns a list of shadow IDs found. Works for both Anthropic and OpenAI response formats.
func ParseExpandPatternsFromText(responseBody []byte) []string {
	var response map[string]any
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil
	}

	var allText []string

	// Anthropic format: content array with text blocks
	if content, ok := response["content"].([]any); ok {
		for _, blockInterface := range content {
			block, ok := blockInterface.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] == "text" {
				if text, ok := block["text"].(string); ok {
					allText = append(allText, text)
				}
			}
		}
	}

	// OpenAI format: choices[].message.content
	if choices, ok := response["choices"].([]any); ok {
		for _, choiceInterface := range choices {
			choice, ok := choiceInterface.(map[string]any)
			if !ok {
				continue
			}
			message, ok := choice["message"].(map[string]any)
			if !ok {
				continue
			}
			if content, ok := message["content"].(string); ok {
				allText = append(allText, content)
			}
		}
	}

	// Extract shadow IDs from all text
	var shadowIDs []string
	seen := make(map[string]bool)
	for _, text := range allText {
		ids := extractExpandPatterns(text)
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				shadowIDs = append(shadowIDs, id)
			}
		}
	}

	return shadowIDs
}
