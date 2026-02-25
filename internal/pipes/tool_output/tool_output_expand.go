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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/compresr/context-gateway/internal/store"
)

// V2: ExpandContextToolSchema is the JSON schema for the expand_context tool
// Moved to types.go for shared access

// Expander handles the expand_context loop for retrieving full content from shadow refs.
// V2: Tracks expanded IDs to prevent circular expansion (E10).
type Expander struct {
	store       store.Store
	tracker     *monitoring.Tracker
	expandedIDs map[string]bool // V2: Track expanded IDs (E10)
}

// NewExpander creates a new Expander.
func NewExpander(st store.Store, tracker *monitoring.Tracker) *Expander {
	return &Expander{
		store:       st,
		tracker:     tracker,
		expandedIDs: make(map[string]bool),
	}
}

// ExpandResult contains the result of running the expand loop.
type ExpandResult struct {
	ResponseBody        []byte
	Response            *http.Response
	ForwardLatency      time.Duration
	ExpandLoopCount     int
	ExpandCallsFound    int
	ExpandCallsNotFound int
}

// RunExpandLoop handles the expand context loop - repeatedly forwarding to LLM
// and handling expand_context tool calls until complete.
// V2: Filters phantom tool from response and tracks expanded IDs (E10).
func (e *Expander) RunExpandLoop(
	ctx context.Context,
	forwardFunc func(ctx context.Context, body []byte) (*http.Response, error),
	forwardBody []byte,
	requestID string,
	providerName string,
	isAnthropic bool,
	expandEnabled bool,
) (*ExpandResult, error) {
	result := &ExpandResult{}
	currentBody := forwardBody

	// V2: Reset expanded IDs for this request
	e.expandedIDs = make(map[string]bool)

	for {
		// Forward to LLM
		forwardStart := time.Now()
		resp, err := forwardFunc(ctx, currentBody)
		result.ForwardLatency += time.Since(forwardStart)

		if err != nil {
			return result, err
		}

		// Read response
		responseBody, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return result, err
		}

		result.ResponseBody = responseBody
		result.Response = resp

		// Check for expand_context tool calls (only if enabled)
		var expandCalls []ExpandContextCall
		if expandEnabled {
			expandCalls = e.ParseExpandContextCalls(responseBody)

			// V2: Filter already-expanded IDs (E10: prevent circular expansion)
			expandCalls = e.filterAlreadyExpanded(expandCalls)
		}

		// If no expand calls, disabled, or hit limit - we're done
		if len(expandCalls) == 0 || result.ExpandLoopCount >= MaxExpandLoops {
			if result.ExpandLoopCount >= MaxExpandLoops && len(expandCalls) > 0 {
				log.Warn().Int("max_loops", MaxExpandLoops).Msg("expand_context loop limit reached")
			}

			// V2: Filter phantom tool from final response (E26)
			if expandEnabled {
				result.ResponseBody, _ = e.FilterExpandContextFromResponse(responseBody)
			}
			break
		}

		// Handle expand_context calls
		result.ExpandLoopCount++
		log.Debug().
			Int("expand_calls", len(expandCalls)).
			Int("loop", result.ExpandLoopCount).
			Msg("handling expand_context calls")

		// Create tool result messages with expanded content
		toolResultMsgs, found, notFound := e.CreateExpandResultMessages(expandCalls, isAnthropic)
		result.ExpandCallsFound += found
		result.ExpandCallsNotFound += notFound

		// Record and store expansion records
		for _, call := range expandCalls {
			// V2: Mark as expanded (E10)
			e.expandedIDs[call.ShadowID] = true

			content, ok := e.store.Get(call.ShadowID)
			if e.tracker != nil {
				e.tracker.RecordExpand(&monitoring.ExpandEvent{
					Timestamp:   time.Now(),
					RequestID:   requestID,
					ShadowRefID: call.ShadowID,
					Found:       ok,
					Success:     ok,
				})
			}

			if ok {
				e.StoreExpansionRecord(call, responseBody, content, isAnthropic)
			}
		}

		// Append assistant response and tool results for next loop
		currentBody, err = e.AppendMessagesToRequest(currentBody, responseBody, toolResultMsgs)
		if err != nil {
			log.Error().Err(err).Msg("failed to append expand results to request")
			break
		}
	}

	return result, nil
}

// filterAlreadyExpanded removes calls for IDs that have already been expanded (V2: E10)
func (e *Expander) filterAlreadyExpanded(calls []ExpandContextCall) []ExpandContextCall {
	if len(e.expandedIDs) == 0 {
		return calls
	}

	filtered := make([]ExpandContextCall, 0, len(calls))
	for _, call := range calls {
		if !e.expandedIDs[call.ShadowID] {
			filtered = append(filtered, call)
		} else {
			log.Warn().
				Str("shadow_id", call.ShadowID).
				Msg("expand_context: skipping already-expanded ID (E10)")
		}
	}
	return filtered
}

// ParseExpandContextCalls extracts expand_context tool calls from an LLM response.
func (e *Expander) ParseExpandContextCalls(responseBody []byte) []ExpandContextCall {
	if len(responseBody) == 0 {
		return nil
	}

	// Check for SSE streaming format
	trimmed := bytes.TrimLeft(responseBody, " \t\n\r")
	if len(trimmed) > 0 && trimmed[0] != '{' && trimmed[0] != '[' {
		return nil
	}

	var response map[string]interface{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil
	}

	var calls []ExpandContextCall

	// Anthropic format: content array with tool_use blocks
	if content, ok := response["content"].([]interface{}); ok {
		for _, blockInterface := range content {
			block, ok := blockInterface.(map[string]interface{})
			if !ok {
				continue
			}

			if block["type"] != "tool_use" {
				continue
			}

			name, _ := block["name"].(string)
			if name != ExpandContextToolName {
				continue
			}

			toolUseID, _ := block["id"].(string)
			input, _ := block["input"].(map[string]interface{})
			shadowID, _ := input["id"].(string)

			if toolUseID != "" && shadowID != "" {
				calls = append(calls, ExpandContextCall{
					ToolUseID: toolUseID,
					ShadowID:  shadowID,
				})
			}
		}
	}

	// OpenAI format: choices[].message.tool_calls
	if choices, ok := response["choices"].([]interface{}); ok {
		for _, choiceInterface := range choices {
			choice, ok := choiceInterface.(map[string]interface{})
			if !ok {
				continue
			}

			message, ok := choice["message"].(map[string]interface{})
			if !ok {
				continue
			}

			toolCalls, ok := message["tool_calls"].([]interface{})
			if !ok {
				continue
			}

			for _, tcInterface := range toolCalls {
				tc, ok := tcInterface.(map[string]interface{})
				if !ok {
					continue
				}

				function, ok := tc["function"].(map[string]interface{})
				if !ok {
					continue
				}

				name, _ := function["name"].(string)
				if name != ExpandContextToolName {
					continue
				}

				toolCallID, _ := tc["id"].(string)
				argsStr, _ := function["arguments"].(string)

				var args map[string]interface{}
				if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
					continue
				}

				shadowID, _ := args["id"].(string)
				if toolCallID != "" && shadowID != "" {
					calls = append(calls, ExpandContextCall{
						ToolUseID: toolCallID,
						ShadowID:  shadowID,
					})
				}
			}
		}
	}

	return calls
}

// CreateExpandResultMessages creates messages with the expanded content.
func (e *Expander) CreateExpandResultMessages(calls []ExpandContextCall, isAnthropic bool) ([]map[string]interface{}, int, int) {
	var messages []map[string]interface{}
	found := 0
	notFound := 0

	if isAnthropic {
		// Anthropic: single user message with multiple tool_result content blocks
		contentBlocks := make([]interface{}, 0, len(calls))
		for _, call := range calls {
			content, ok := e.store.Get(call.ShadowID)
			var resultContent string
			if ok {
				resultContent = content
				found++
			} else {
				resultContent = fmt.Sprintf("Error: shadow reference '%s' not found or expired", call.ShadowID)
				notFound++
			}

			contentBlocks = append(contentBlocks, map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": call.ToolUseID,
				"content":     resultContent,
			})
		}

		messages = append(messages, map[string]interface{}{
			"role":    "user",
			"content": contentBlocks,
		})
	} else {
		// OpenAI: separate tool role messages for each result
		for _, call := range calls {
			content, ok := e.store.Get(call.ShadowID)
			var resultContent string
			if ok {
				resultContent = content
				found++
			} else {
				resultContent = fmt.Sprintf("Error: shadow reference '%s' not found or expired", call.ShadowID)
				notFound++
			}

			messages = append(messages, map[string]interface{}{
				"role":         "tool",
				"tool_call_id": call.ToolUseID,
				"content":      resultContent,
			})
		}
	}

	return messages, found, notFound
}

// StoreExpansionRecord stores the expand_context interaction for KV-cache preservation.
func (e *Expander) StoreExpansionRecord(call ExpandContextCall, assistantResponse []byte, expandedContent string, isAnthropic bool) {
	var assistantMsg map[string]interface{}

	if isAnthropic {
		var response map[string]interface{}
		if err := json.Unmarshal(assistantResponse, &response); err != nil {
			log.Error().Err(err).Msg("failed to parse assistant response for expansion record")
			return
		}

		content, ok := response["content"].([]interface{})
		if !ok {
			return
		}

		var toolUseBlock map[string]interface{}
		for _, block := range content {
			b, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			if b["type"] == "tool_use" && b["id"] == call.ToolUseID {
				toolUseBlock = b
				break
			}
		}

		if toolUseBlock == nil {
			return
		}

		assistantMsg = map[string]interface{}{
			"role":    "assistant",
			"content": []interface{}{toolUseBlock},
		}
	} else {
		assistantMsg = map[string]interface{}{
			"role": "assistant",
			"tool_calls": []interface{}{
				map[string]interface{}{
					"id":   call.ToolUseID,
					"type": "function",
					"function": map[string]interface{}{
						"name":      ExpandContextToolName,
						"arguments": fmt.Sprintf(`{"id":"%s"}`, call.ShadowID),
					},
				},
			},
		}
	}

	var toolResultMsg map[string]interface{}
	if isAnthropic {
		toolResultMsg = map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{
					"type":        "tool_result",
					"tool_use_id": call.ToolUseID,
					"content":     expandedContent,
				},
			},
		}
	} else {
		toolResultMsg = map[string]interface{}{
			"role":         "tool",
			"tool_call_id": call.ToolUseID,
			"content":      expandedContent,
		}
	}

	assistantBytes, err := json.Marshal(assistantMsg)
	if err != nil {
		log.Error().Err(err).Msg("failed to serialize assistant message for expansion record")
		return
	}

	toolResultBytes, err := json.Marshal(toolResultMsg)
	if err != nil {
		log.Error().Err(err).Msg("failed to serialize tool result for expansion record")
		return
	}

	expansion := &store.ExpansionRecord{
		AssistantMessage:  assistantBytes,
		ToolResultMessage: toolResultBytes,
	}

	if err := e.store.SetExpansion(call.ShadowID, expansion); err != nil {
		log.Error().Err(err).Str("shadow_id", call.ShadowID).Msg("failed to store expansion record")
		return
	}

	// Delete the compressed mapping to invalidate it
	if err := e.store.DeleteCompressed(call.ShadowID); err != nil {
		log.Warn().Err(err).Str("shadow_id", call.ShadowID).Msg("failed to delete compressed mapping after expansion")
	}

	log.Debug().
		Str("shadow_id", call.ShadowID).
		Str("tool_use_id", call.ToolUseID).
		Msg("stored expansion record for KV-cache preservation")
}

// AppendMessagesToRequest adds the assistant response and tool results to the request
func (e *Expander) AppendMessagesToRequest(body []byte, assistantResponse []byte, toolResultMessages []map[string]interface{}) ([]byte, error) {
	var request map[string]interface{}
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}

	messages, ok := request["messages"].([]interface{})
	if !ok {
		messages = []interface{}{}
	}

	var response map[string]interface{}
	if err := json.Unmarshal(assistantResponse, &response); err != nil {
		return nil, err
	}

	// Anthropic format
	if content, ok := response["content"].([]interface{}); ok {
		assistantMsg := map[string]interface{}{
			"role":    "assistant",
			"content": content,
		}
		messages = append(messages, assistantMsg)
	} else if choices, ok := response["choices"].([]interface{}); ok && len(choices) > 0 {
		// OpenAI format
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if message, ok := choice["message"].(map[string]interface{}); ok {
				messages = append(messages, message)
			}
		}
	}

	for _, msg := range toolResultMessages {
		messages = append(messages, msg)
	}

	request["messages"] = messages
	return json.Marshal(request)
}

// FilterExpandContextFromResponse removes expand_context tool calls from the response
func (e *Expander) FilterExpandContextFromResponse(responseBody []byte) ([]byte, bool) {
	var response map[string]interface{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return responseBody, false
	}

	modified := false

	// Anthropic format: filter content array
	if content, ok := response["content"].([]interface{}); ok {
		// Initialize with empty slice (not nil) to ensure JSON marshals to [] not null
		filteredContent := make([]interface{}, 0, len(content))
		for _, blockInterface := range content {
			block, ok := blockInterface.(map[string]interface{})
			if !ok {
				filteredContent = append(filteredContent, blockInterface)
				continue
			}

			if block["type"] == "tool_use" {
				name, _ := block["name"].(string)
				if name == ExpandContextToolName {
					modified = true
					continue
				}
			}
			filteredContent = append(filteredContent, block)
		}
		response["content"] = filteredContent
	}

	// OpenAI format: filter tool_calls in choices
	if choices, ok := response["choices"].([]interface{}); ok {
		for i, choiceInterface := range choices {
			choice, ok := choiceInterface.(map[string]interface{})
			if !ok {
				continue
			}

			message, ok := choice["message"].(map[string]interface{})
			if !ok {
				continue
			}

			toolCalls, ok := message["tool_calls"].([]interface{})
			if !ok {
				continue
			}

			// Initialize with empty slice (not nil) to ensure JSON marshals to [] not null
			filteredCalls := make([]interface{}, 0, len(toolCalls))
			for _, tcInterface := range toolCalls {
				tc, ok := tcInterface.(map[string]interface{})
				if !ok {
					filteredCalls = append(filteredCalls, tcInterface)
					continue
				}

				function, ok := tc["function"].(map[string]interface{})
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
			choice["message"] = message
			choices[i] = choice
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

// InjectExpandContextTool adds the expand_context tool to a request if shadow refs exist.
// Uses provider name for reliable format detection (OpenAI vs Anthropic).
func InjectExpandContextTool(body []byte, shadowRefs map[string]string, provider string) ([]byte, error) {
	if len(shadowRefs) == 0 {
		return body, nil
	}

	var request map[string]interface{}
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}

	tools, ok := request["tools"].([]interface{})
	if !ok {
		tools = []interface{}{}
	}

	// Check if already exists
	for _, t := range tools {
		if tool, ok := t.(map[string]interface{}); ok {
			// Check both OpenAI and Anthropic format
			if name, _ := tool["name"].(string); name == ExpandContextToolName {
				return body, nil
			}
			if fn, ok := tool["function"].(map[string]interface{}); ok {
				if name, _ := fn["name"].(string); name == ExpandContextToolName {
					return body, nil
				}
			}
		}
	}

	// Detect if this is Responses API format (has "input" field) or Chat Completions (has "messages")
	// Responses API uses flat tool format: {"type":"function","name":"...","parameters":...}
	// Chat Completions uses nested format: {"type":"function","function":{"name":"...","parameters":...}}
	_, hasInput := request["input"]
	isResponsesAPI := hasInput && provider == "openai"
	isOpenAIChatCompletions := provider == "openai" && !isResponsesAPI

	var expandTool map[string]interface{}
	if isResponsesAPI {
		// OpenAI Responses API format (flat)
		expandTool = map[string]interface{}{
			"type":        "function",
			"name":        ExpandContextToolName,
			"description": "Retrieve the full, uncompressed content for a shadow reference. Use this when you need more detail from compressed tool outputs.",
			"parameters":  ExpandContextToolSchema,
		}
	} else if isOpenAIChatCompletions {
		// OpenAI Chat Completions format (nested)
		expandTool = map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        ExpandContextToolName,
				"description": "Retrieve the full, uncompressed content for a shadow reference. Use this when you need more detail from compressed tool outputs.",
				"parameters":  ExpandContextToolSchema,
			},
		}
	} else {
		// Anthropic format
		expandTool = map[string]interface{}{
			"name":         ExpandContextToolName,
			"description":  "Retrieve the full, uncompressed content for a shadow reference. Use this when you need more detail from compressed tool outputs.",
			"input_schema": ExpandContextToolSchema,
		}
	}

	tools = append(tools, expandTool)
	request["tools"] = tools

	return json.Marshal(request)
}

// RewriteHistoryWithExpansion replaces compressed tool outputs in history with full content.
// This is used for streaming expand_context: when LLM calls expand_context, we rewrite
// the history to replace the compressed version with the full version, then re-send.
//
// The design is "selective replace": only tools the LLM explicitly requested are expanded.
// This optimizes KV-cache (prefix preserved up to modified tool) and keeps history clean.
//
// Returns the rewritten body and list of expanded shadow IDs.
func (e *Expander) RewriteHistoryWithExpansion(body []byte, expandCalls []ExpandContextCall) ([]byte, []string, error) {
	if len(expandCalls) == 0 {
		return body, nil, nil
	}

	var request map[string]interface{}
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, nil, fmt.Errorf("failed to parse request: %w", err)
	}

	messages, ok := request["messages"].([]interface{})
	if !ok {
		return body, nil, nil
	}

	// Build shadow ID lookup
	shadowIDsToExpand := make(map[string]bool)
	for _, call := range expandCalls {
		shadowIDsToExpand[call.ShadowID] = true
	}

	var expandedIDs []string
	modified := false

	// Scan messages for tool results containing shadow refs
	for i, msg := range messages {
		m, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}

		role, _ := m["role"].(string)

		// Anthropic format: user message with tool_result content blocks
		if role == "user" {
			if content, ok := m["content"].([]interface{}); ok {
				newContent := make([]interface{}, 0, len(content))
				for _, block := range content {
					b, ok := block.(map[string]interface{})
					if !ok {
						newContent = append(newContent, block)
						continue
					}

					if b["type"] == "tool_result" {
						contentStr, _ := b["content"].(string)
						shadowID := extractShadowIDFromContent(contentStr)

						if shadowID != "" && shadowIDsToExpand[shadowID] {
							// Get full content from store
							if fullContent, found := e.store.Get(shadowID); found {
								b["content"] = fullContent
								expandedIDs = append(expandedIDs, shadowID)
								modified = true

								log.Debug().
									Str("shadow_id", shadowID).
									Int("original_len", len(contentStr)).
									Int("expanded_len", len(fullContent)).
									Msg("rewrite_history: expanded tool result")
							}
						}
					}
					newContent = append(newContent, b)
				}
				m["content"] = newContent
				messages[i] = m
			}
		}

		// OpenAI format: tool role message
		if role == "tool" {
			contentStr, _ := m["content"].(string)
			shadowID := extractShadowIDFromContent(contentStr)

			if shadowID != "" && shadowIDsToExpand[shadowID] {
				if fullContent, found := e.store.Get(shadowID); found {
					m["content"] = fullContent
					expandedIDs = append(expandedIDs, shadowID)
					modified = true
					messages[i] = m

					log.Debug().
						Str("shadow_id", shadowID).
						Int("original_len", len(contentStr)).
						Int("expanded_len", len(fullContent)).
						Msg("rewrite_history: expanded tool message")
				}
			}
		}
	}

	if !modified {
		return body, nil, nil
	}

	// Remove the last assistant message if it only contains expand_context call
	messages = e.stripExpandContextFromLastAssistant(messages)

	request["messages"] = messages

	result, err := json.Marshal(request)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal rewritten request: %w", err)
	}

	return result, expandedIDs, nil
}

// stripExpandContextFromLastAssistant removes expand_context tool calls from the last assistant message.
// If the assistant message ONLY contains expand_context, remove the entire message.
// If it contains other content, just remove the expand_context tool_use blocks.
func (e *Expander) stripExpandContextFromLastAssistant(messages []interface{}) []interface{} {
	if len(messages) == 0 {
		return messages
	}

	// Find last assistant message
	lastAssistantIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if m, ok := messages[i].(map[string]interface{}); ok {
			if role, _ := m["role"].(string); role == "assistant" {
				lastAssistantIdx = i
				break
			}
		}
	}

	if lastAssistantIdx < 0 {
		return messages
	}

	m := messages[lastAssistantIdx].(map[string]interface{})

	// Anthropic format: content array with tool_use blocks
	if content, ok := m["content"].([]interface{}); ok {
		filtered := make([]interface{}, 0, len(content))
		for _, block := range content {
			b, ok := block.(map[string]interface{})
			if !ok {
				filtered = append(filtered, block)
				continue
			}

			if b["type"] == "tool_use" {
				name, _ := b["name"].(string)
				if name == ExpandContextToolName {
					continue // Skip expand_context
				}
			}
			filtered = append(filtered, block)
		}

		if len(filtered) == 0 {
			// Remove entire assistant message
			messages = append(messages[:lastAssistantIdx], messages[lastAssistantIdx+1:]...)
		} else {
			m["content"] = filtered
			messages[lastAssistantIdx] = m
		}
	}

	// OpenAI format: tool_calls array
	if toolCalls, ok := m["tool_calls"].([]interface{}); ok {
		filtered := make([]interface{}, 0, len(toolCalls))
		for _, tc := range toolCalls {
			call, ok := tc.(map[string]interface{})
			if !ok {
				filtered = append(filtered, tc)
				continue
			}

			if fn, ok := call["function"].(map[string]interface{}); ok {
				name, _ := fn["name"].(string)
				if name == ExpandContextToolName {
					continue // Skip expand_context
				}
			}
			filtered = append(filtered, tc)
		}

		if len(filtered) == 0 {
			// Remove tool_calls entirely, keep any text content
			delete(m, "tool_calls")
			// If message has no content either, remove it
			if content, _ := m["content"].(string); content == "" {
				messages = append(messages[:lastAssistantIdx], messages[lastAssistantIdx+1:]...)
			} else {
				messages[lastAssistantIdx] = m
			}
		} else {
			m["tool_calls"] = filtered
			messages[lastAssistantIdx] = m
		}
	}

	return messages
}

// extractShadowIDFromContent extracts a shadow ID from compressed content.
// Format: <<<SHADOW:shadow_xxx>>>\n...
func extractShadowIDFromContent(content string) string {
	const prefix = "<<<SHADOW:"
	const suffix = ">>>"

	idx := strings.Index(content, prefix)
	if idx < 0 {
		return ""
	}

	start := idx + len(prefix)
	end := strings.Index(content[start:], suffix)
	if end < 0 {
		return ""
	}

	return content[start : start+end]
}

// InvalidateExpandedMappings removes compressed cache entries for expanded shadow IDs.
// This ensures the next request will send the original content (client's version).
func (e *Expander) InvalidateExpandedMappings(shadowIDs []string) {
	for _, id := range shadowIDs {
		if err := e.store.DeleteCompressed(id); err != nil {
			log.Warn().Err(err).Str("shadow_id", id).Msg("failed to invalidate compressed mapping")
		} else {
			log.Debug().Str("shadow_id", id).Msg("invalidated compressed mapping after expansion")
		}
	}
}
