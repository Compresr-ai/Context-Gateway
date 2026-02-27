// Expand context handler for tool output expansion.
//
// DESIGN: Implements PhantomToolHandler for expand_context.
// When LLM calls expand_context(id), this handler:
//  1. Retrieves the original content from the store using the shadow ID
//  2. Returns tool_result with the full, uncompressed content
//
// This allows the LLM to request the full content of compressed tool outputs.
package gateway

import (
	"encoding/json"
	"fmt"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/store"
)

// ExpandContextToolName is the name of the expand_context phantom tool.
const ExpandContextToolName = "expand_context"

// ExpandContextHandler implements PhantomToolHandler for expand_context.
type ExpandContextHandler struct {
	store       store.Store
	expandedIDs map[string]bool // Track expanded IDs to prevent circular expansion
}

// NewExpandContextHandler creates a new expand context handler.
func NewExpandContextHandler(st store.Store) *ExpandContextHandler {
	return &ExpandContextHandler{
		store:       st,
		expandedIDs: make(map[string]bool),
	}
}

// ResetExpandedIDs resets the tracking of expanded IDs.
// Call this at the start of each request.
func (h *ExpandContextHandler) ResetExpandedIDs() {
	h.expandedIDs = make(map[string]bool)
}

// Name returns the phantom tool name.
func (h *ExpandContextHandler) Name() string {
	return ExpandContextToolName
}

// HandleCalls processes expand_context calls and returns results.
func (h *ExpandContextHandler) HandleCalls(calls []PhantomToolCall, isAnthropic bool) *PhantomToolResult {
	result := &PhantomToolResult{}

	// Filter already-expanded IDs
	filteredCalls := make([]PhantomToolCall, 0, len(calls))
	for _, call := range calls {
		shadowID, _ := call.Input["id"].(string)
		if h.expandedIDs[shadowID] {
			log.Warn().
				Str("shadow_id", shadowID).
				Msg("expand_context: skipping already-expanded ID")
			continue
		}
		filteredCalls = append(filteredCalls, call)
	}

	if len(filteredCalls) == 0 {
		result.StopLoop = true
		return result
	}

	// Anthropic: group all tool_results in one user message
	if isAnthropic {
		var contentBlocks []any
		for _, call := range filteredCalls {
			shadowID, _ := call.Input["id"].(string)

			// Mark as expanded
			h.expandedIDs[shadowID] = true

			// Retrieve from store
			content, found := h.store.Get(shadowID)
			var resultText string
			if found {
				resultText = content
				log.Debug().
					Str("shadow_id", shadowID).
					Int("content_len", len(content)).
					Msg("expand_context: retrieved content")
			} else {
				resultText = fmt.Sprintf("Error: shadow reference '%s' not found or expired", shadowID)
				log.Warn().
					Str("shadow_id", shadowID).
					Msg("expand_context: shadow ID not found")
			}

			contentBlocks = append(contentBlocks, map[string]any{
				"type":        "tool_result",
				"tool_use_id": call.ToolUseID,
				"content":     resultText,
			})
		}

		result.ToolResults = []map[string]any{{
			"role":    "user",
			"content": contentBlocks,
		}}
	} else {
		// OpenAI: separate tool messages
		for _, call := range filteredCalls {
			shadowID, _ := call.Input["id"].(string)

			// Mark as expanded
			h.expandedIDs[shadowID] = true

			// Retrieve from store
			content, found := h.store.Get(shadowID)
			var resultText string
			if found {
				resultText = content
				log.Debug().
					Str("shadow_id", shadowID).
					Int("content_len", len(content)).
					Msg("expand_context: retrieved content")
			} else {
				resultText = fmt.Sprintf("Error: shadow reference '%s' not found or expired", shadowID)
				log.Warn().
					Str("shadow_id", shadowID).
					Msg("expand_context: shadow ID not found")
			}

			result.ToolResults = append(result.ToolResults, map[string]any{
				"role":         "tool",
				"tool_call_id": call.ToolUseID,
				"content":      resultText,
			})
		}
	}

	return result
}

// FilterFromResponse removes expand_context from the final response.
func (h *ExpandContextHandler) FilterFromResponse(responseBody []byte) ([]byte, bool) {
	var response map[string]any
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return responseBody, false
	}

	modified := false

	// Anthropic format
	if content, ok := response["content"].([]any); ok {
		filteredContent := make([]any, 0, len(content))
		for _, block := range content {
			blockMap, ok := block.(map[string]any)
			if !ok {
				filteredContent = append(filteredContent, block)
				continue
			}

			if blockMap["type"] == "tool_use" {
				name, _ := blockMap["name"].(string)
				if name == ExpandContextToolName {
					modified = true
					continue
				}
			}
			filteredContent = append(filteredContent, block)
		}
		response["content"] = filteredContent
	}

	// OpenAI format
	if choices, ok := response["choices"].([]any); ok {
		for i, choice := range choices {
			choiceMap, ok := choice.(map[string]any)
			if !ok {
				continue
			}

			message, ok := choiceMap["message"].(map[string]any)
			if !ok {
				continue
			}

			toolCalls, ok := message["tool_calls"].([]any)
			if !ok {
				continue
			}

			filteredCalls := make([]any, 0, len(toolCalls))
			for _, tc := range toolCalls {
				tcMap, ok := tc.(map[string]any)
				if !ok {
					filteredCalls = append(filteredCalls, tc)
					continue
				}

				function, ok := tcMap["function"].(map[string]any)
				if ok {
					name, _ := function["name"].(string)
					if name == ExpandContextToolName {
						modified = true
						continue
					}
				}
				filteredCalls = append(filteredCalls, tc)
			}

			message["tool_calls"] = filteredCalls
			choiceMap["message"] = message
			choices[i] = choiceMap
		}
		response["choices"] = choices
	}

	if !modified {
		return responseBody, false
	}

	result, err := json.Marshal(response)
	if err != nil {
		return responseBody, false
	}
	return result, true
}
