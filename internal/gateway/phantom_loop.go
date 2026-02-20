// Phantom tool loop - generic handler for gateway-internal tools.
//
// DESIGN: Both expand_context and gateway_search_tools are "phantom tools" that:
//   - Are injected by the gateway
//   - Are intercepted by the gateway when the LLM calls them
//   - Are never seen by the client
//
// This file provides a generic loop that:
//  1. Forwards request to LLM
//  2. Checks response for phantom tool calls
//  3. If found: handles them, appends tool_result, re-forwards
//  4. If not found: filters phantom tools from response, returns to client
//
// Phantom tool handlers implement PhantomToolHandler interface.
package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/adapters"
)

// MaxPhantomLoops prevents infinite recursion.
const MaxPhantomLoops = 5

// PhantomToolCall represents a detected phantom tool call.
type PhantomToolCall struct {
	ToolUseID string         // The tool_use block ID
	ToolName  string         // The phantom tool name
	Input     map[string]any // The input arguments
}

// PhantomToolResult contains the outcome of handling phantom tool calls.
type PhantomToolResult struct {
	ToolResults   []map[string]any             // Tool result messages to append
	ModifyRequest func([]byte) ([]byte, error) // Optional: modify request before re-forwarding
	StopLoop      bool                         // If true, don't re-forward (return current response)
}

// PhantomToolHandler handles a specific phantom tool.
type PhantomToolHandler interface {
	// Name returns the phantom tool name to detect.
	Name() string

	// HandleCalls processes the detected calls and returns results.
	// Called when the LLM invokes this phantom tool.
	HandleCalls(calls []PhantomToolCall, isAnthropic bool) *PhantomToolResult

	// FilterFromResponse removes this phantom tool from the final response.
	FilterFromResponse(response []byte) ([]byte, bool)
}

// PhantomLoopResult contains the result of running the phantom tool loop.
type PhantomLoopResult struct {
	ResponseBody   []byte
	Response       *http.Response
	ForwardLatency time.Duration
	LoopCount      int
	HandledCalls   map[string]int // tool_name -> count
}

// PhantomLoop runs the phantom tool handling loop.
type PhantomLoop struct {
	handlers []PhantomToolHandler
}

// NewPhantomLoop creates a new phantom loop with the given handlers.
func NewPhantomLoop(handlers ...PhantomToolHandler) *PhantomLoop {
	return &PhantomLoop{handlers: handlers}
}

// Run executes the phantom tool loop.
func (p *PhantomLoop) Run(
	ctx context.Context,
	forwardFunc func(ctx context.Context, body []byte) (*http.Response, error),
	body []byte,
	provider adapters.Provider,
) (*PhantomLoopResult, error) {
	result := &PhantomLoopResult{
		HandledCalls: make(map[string]int),
	}
	currentBody := body
	isAnthropic := provider == adapters.ProviderAnthropic

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

		// Check for phantom tool calls
		allCalls := p.parsePhantomCalls(responseBody, provider)
		if len(allCalls) == 0 || result.LoopCount >= MaxPhantomLoops {
			if result.LoopCount >= MaxPhantomLoops && len(allCalls) > 0 {
				log.Warn().Int("max_loops", MaxPhantomLoops).Msg("phantom_loop: max loops reached")
			}

			// Filter all phantom tools from final response
			finalResponse := responseBody
			for _, handler := range p.handlers {
				if filtered, ok := handler.FilterFromResponse(finalResponse); ok {
					finalResponse = filtered
				}
			}
			result.ResponseBody = finalResponse
			break
		}

		// Handle calls by type
		result.LoopCount++
		var allToolResults []map[string]any
		var requestModifiers []func([]byte) ([]byte, error)

		for _, handler := range p.handlers {
			calls := p.filterCallsByName(allCalls, handler.Name())
			if len(calls) == 0 {
				continue
			}

			log.Debug().
				Str("handler", handler.Name()).
				Int("calls", len(calls)).
				Int("loop", result.LoopCount).
				Msg("phantom_loop: handling calls")

			handleResult := handler.HandleCalls(calls, isAnthropic)
			result.HandledCalls[handler.Name()] += len(calls)

			if handleResult.StopLoop {
				// Filter and return
				for _, h := range p.handlers {
					if filtered, ok := h.FilterFromResponse(result.ResponseBody); ok {
						result.ResponseBody = filtered
					}
				}
				return result, nil
			}

			allToolResults = append(allToolResults, handleResult.ToolResults...)
			if handleResult.ModifyRequest != nil {
				requestModifiers = append(requestModifiers, handleResult.ModifyRequest)
			}
		}

		if len(allToolResults) == 0 {
			// No results to append, we're done
			break
		}

		// Append assistant response and tool results
		currentBody, err = appendMessagesToRequest(currentBody, responseBody, allToolResults, isAnthropic)
		if err != nil {
			log.Error().Err(err).Msg("phantom_loop: failed to append messages")
			break
		}

		// Apply request modifiers (e.g., add tools to tools array)
		for _, modifier := range requestModifiers {
			currentBody, err = modifier(currentBody)
			if err != nil {
				log.Warn().Err(err).Msg("phantom_loop: request modifier failed")
			}
		}
	}

	return result, nil
}

// parsePhantomCalls extracts all phantom tool calls from a response.
func (p *PhantomLoop) parsePhantomCalls(responseBody []byte, provider adapters.Provider) []PhantomToolCall {
	var response map[string]any
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil
	}

	// Get handler names for lookup
	handlerNames := make(map[string]bool)
	for _, h := range p.handlers {
		handlerNames[h.Name()] = true
	}

	var calls []PhantomToolCall

	switch provider {
	case adapters.ProviderAnthropic:
		content, ok := response["content"].([]any)
		if !ok {
			return nil
		}

		for _, block := range content {
			blockMap, ok := block.(map[string]any)
			if !ok {
				continue
			}

			if blockMap["type"] != "tool_use" {
				continue
			}

			name, _ := blockMap["name"].(string)
			if !handlerNames[name] {
				continue
			}

			toolUseID, _ := blockMap["id"].(string)
			input, _ := blockMap["input"].(map[string]any)

			calls = append(calls, PhantomToolCall{
				ToolUseID: toolUseID,
				ToolName:  name,
				Input:     input,
			})
		}

	case adapters.ProviderOpenAI:
		choices, ok := response["choices"].([]any)
		if !ok || len(choices) == 0 {
			return nil
		}

		choice, ok := choices[0].(map[string]any)
		if !ok {
			return nil
		}

		message, ok := choice["message"].(map[string]any)
		if !ok {
			return nil
		}

		toolCalls, ok := message["tool_calls"].([]any)
		if !ok {
			return nil
		}

		for _, tc := range toolCalls {
			tcMap, ok := tc.(map[string]any)
			if !ok {
				continue
			}

			function, ok := tcMap["function"].(map[string]any)
			if !ok {
				continue
			}

			name, _ := function["name"].(string)
			if !handlerNames[name] {
				continue
			}

			toolCallID, _ := tcMap["id"].(string)
			argsStr, _ := function["arguments"].(string)

			var input map[string]any
			if err := json.Unmarshal([]byte(argsStr), &input); err != nil {
				input = make(map[string]any)
			}

			calls = append(calls, PhantomToolCall{
				ToolUseID: toolCallID,
				ToolName:  name,
				Input:     input,
			})
		}
	}

	return calls
}

// filterCallsByName filters calls for a specific tool name.
func (p *PhantomLoop) filterCallsByName(calls []PhantomToolCall, name string) []PhantomToolCall {
	var filtered []PhantomToolCall
	for _, call := range calls {
		if call.ToolName == name {
			filtered = append(filtered, call)
		}
	}
	return filtered
}

// appendMessagesToRequest adds assistant response and tool results to the request.
func appendMessagesToRequest(body []byte, assistantResponse []byte, toolResults []map[string]any, isAnthropic bool) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}

	messages, _ := request["messages"].([]any)
	if messages == nil {
		messages = []any{}
	}

	var response map[string]any
	if err := json.Unmarshal(assistantResponse, &response); err != nil {
		return nil, err
	}

	// Add assistant message
	if isAnthropic {
		if content, ok := response["content"].([]any); ok {
			messages = append(messages, map[string]any{
				"role":    "assistant",
				"content": content,
			})
		}
	} else {
		if choices, ok := response["choices"].([]any); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]any); ok {
				if message, ok := choice["message"].(map[string]any); ok {
					messages = append(messages, message)
				}
			}
		}
	}

	// Add tool results
	for _, result := range toolResults {
		messages = append(messages, result)
	}

	request["messages"] = messages
	return json.Marshal(request)
}
