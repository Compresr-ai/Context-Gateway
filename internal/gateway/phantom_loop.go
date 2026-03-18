// phantom_loop.go handles gateway-internal phantom tool calls intercepted from LLM responses.
package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/adapters"
	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
)

// MaxPhantomLoops prevents infinite recursion.
const MaxPhantomLoops = 5

// PhantomToolCall represents a detected phantom tool call.
type PhantomToolCall struct {
	ToolUseID string
	ToolName  string
	Input     map[string]any
}

// PhantomToolResult contains the outcome of handling phantom tool calls.
type PhantomToolResult struct {
	ToolResults     []map[string]any             // Tool result messages to append
	ModifyRequest   func([]byte) ([]byte, error) // Optional: modify request before re-forwarding
	StopLoop        bool                         // If true, don't re-forward (return current response)
	RewriteResponse func([]byte) ([]byte, error) // Optional: transform response before returning to client
}

// PhantomToolHandler handles a specific phantom tool.
type PhantomToolHandler interface {
	// Name returns the phantom tool name to detect.
	// Handlers that intercept real (non-phantom) tool names should return ""
	// so the phantom loop does not attempt to filter those names from the response.
	Name() string

	// HandleCalls processes the detected calls and returns results.
	// adapter is the provider adapter for building response messages.
	// requestBody is the current request body (used for format detection).
	HandleCalls(calls []PhantomToolCall, adapter adapters.Adapter, requestBody []byte) *PhantomToolResult
}

// CatchAllPhantomToolHandler is an optional extension of PhantomToolHandler.
// When a handler also implements this interface, the phantom loop calls ShouldHandle
// to match tool calls by name dynamically rather than by a single fixed Name().
// This enables handlers that intercept a variable set of tool names (e.g. any tool
// in the deferred set) without registering each name individually.
//
// Name() on a CatchAllPhantomToolHandler should return "" — the phantom loop skips
// FilterToolCallFromResponse for empty names, which is correct since catch-all handlers
// intercept real client-visible tool names that must not be stripped from the response.
type CatchAllPhantomToolHandler interface {
	PhantomToolHandler
	ShouldHandle(toolName string) bool
}

// PhantomLoopResult contains the result of running the phantom tool loop.
type PhantomLoopResult struct {
	ResponseBody     []byte
	Response         *http.Response
	ForwardLatency   time.Duration
	LoopCount        int
	HandledCalls     map[string]int     // tool_name -> count
	AccumulatedUsage adapters.UsageInfo // Total usage across ALL loop iterations
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
	adapter adapters.Adapter,
) (*PhantomLoopResult, error) {
	result := &PhantomLoopResult{
		HandledCalls: make(map[string]int),
	}
	currentBody := body

	for {
		if ctx.Err() != nil {
			log.Debug().Msg("phantom_loop: context cancelled, stopping loop")
			break
		}
		// Forward to LLM
		forwardStart := time.Now()
		resp, err := forwardFunc(ctx, currentBody)
		result.ForwardLatency += time.Since(forwardStart)

		if err != nil {
			// If we already have a successful response from a previous loop iteration,
			// fall back to it instead of failing the entire request.
			if result.LoopCount > 0 && result.ResponseBody != nil {
				log.Error().Err(err).Int("loop", result.LoopCount).
					Msg("phantom_loop: forward failed mid-loop, falling back to last successful response")
				// Filter phantom tools from last response before returning
				finalResponse := result.ResponseBody
				for _, handler := range p.handlers {
					if handler.Name() == "" {
						continue // catch-all handler: intercepts real tool names, nothing to strip
					}
					if filtered, ok := adapter.FilterToolCallFromResponse(finalResponse, handler.Name()); ok {
						finalResponse = filtered
					}
				}
				result.ResponseBody = finalResponse
				return result, nil
			}
			return result, err
		}

		// Read response — use inner function so defer closes the body even on early return
		responseBody, err := func() ([]byte, error) {
			defer func() { _ = resp.Body.Close() }()
			return io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize))
		}()
		if err != nil {
			return result, err
		}

		result.ResponseBody = responseBody
		result.Response = resp

		// Accumulate usage from every iteration (including the final one)
		iterUsage := adapter.ExtractUsage(responseBody)
		result.AccumulatedUsage.InputTokens += iterUsage.InputTokens
		result.AccumulatedUsage.OutputTokens += iterUsage.OutputTokens
		result.AccumulatedUsage.CacheCreationInputTokens += iterUsage.CacheCreationInputTokens
		result.AccumulatedUsage.CacheReadInputTokens += iterUsage.CacheReadInputTokens
		result.AccumulatedUsage.TotalTokens += iterUsage.TotalTokens

		// Check for phantom tool calls
		allCalls := p.parsePhantomCalls(responseBody, adapter)
		if len(allCalls) == 0 || result.LoopCount >= MaxPhantomLoops {
			if result.LoopCount >= MaxPhantomLoops && len(allCalls) > 0 {
				log.Warn().Int("max_loops", MaxPhantomLoops).Msg("phantom_loop: max loops reached")
			}

			// Filter all phantom tools from final response
			finalResponse := responseBody
			for _, handler := range p.handlers {
				if handler.Name() == "" {
					continue // catch-all handler: intercepts real tool names, nothing to strip
				}
				if filtered, ok := adapter.FilterToolCallFromResponse(finalResponse, handler.Name()); ok {
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
			var calls []PhantomToolCall
			if ca, ok := handler.(CatchAllPhantomToolHandler); ok {
				calls = p.filterCallsByCatchAll(allCalls, ca)
			} else {
				calls = p.filterCallsByName(allCalls, handler.Name())
			}
			if len(calls) == 0 {
				continue
			}

			log.Debug().
				Str("handler", handler.Name()).
				Int("calls", len(calls)).
				Int("loop", result.LoopCount).
				Msg("phantom_loop: handling calls")

			handleResult := handler.HandleCalls(calls, adapter, currentBody)
			result.HandledCalls[handler.Name()] += len(calls)

			if handleResult.StopLoop {
				// Apply response rewrite if provided (call mode: gateway_search_tool -> real tool)
				if handleResult.RewriteResponse != nil {
					rewritten, rwErr := handleResult.RewriteResponse(result.ResponseBody)
					if rwErr != nil {
						log.Warn().Err(rwErr).Msg("phantom_loop: response rewrite failed, returning original")
					} else {
						result.ResponseBody = rewritten
					}
				}
				// Filter remaining phantom tools from response
				for _, h := range p.handlers {
					if h.Name() == "" {
						continue // catch-all handler: intercepts real tool names, nothing to strip
					}
					if filtered, ok := adapter.FilterToolCallFromResponse(result.ResponseBody, h.Name()); ok {
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
		currentBody, err = adapter.AppendMessages(currentBody, responseBody, allToolResults)
		if err != nil {
			log.Error().Err(err).Msg("phantom_loop: failed to append messages")
			break
		}

		// Apply request modifiers (e.g., add tools to tools array)
		// Save backup so we can revert if a modifier corrupts the body
		bodyBeforeModifiers := currentBody
		for _, modifier := range requestModifiers {
			modified, modErr := modifier(currentBody)
			if modErr != nil {
				log.Warn().Err(modErr).Msg("phantom_loop: request modifier failed, reverting to pre-modifier body")
				currentBody = bodyBeforeModifiers
				break
			}
			currentBody = modified
		}
	}

	return result, nil
}

// parsePhantomCalls extracts all phantom tool calls from a response using the adapter.
func (p *PhantomLoop) parsePhantomCalls(responseBody []byte, adapter adapters.Adapter) []PhantomToolCall {
	handlerNames := make(map[string]bool)
	for _, h := range p.handlers {
		if h.Name() != "" {
			handlerNames[h.Name()] = true
		}
	}

	rawCalls, err := adapter.ExtractToolCallsFromResponse(responseBody)
	if err != nil {
		preview := string(responseBody)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		log.Warn().
			Err(err).
			Str("body_preview", preview).
			Int("body_len", len(responseBody)).
			Msg("phantom_loop: failed to extract tool calls from response")
		return nil
	}

	calls := make([]PhantomToolCall, 0, len(rawCalls))
	for _, rc := range rawCalls {
		if !handlerNames[rc.ToolName] && !p.matchesCatchAll(rc.ToolName) {
			continue
		}
		calls = append(calls, PhantomToolCall{
			ToolUseID: rc.ToolUseID,
			ToolName:  rc.ToolName,
			Input:     rc.Input,
		})
	}

	// Also scan text content for <<<EXPAND:shadow_xxx>>> patterns (text-based expand_context)
	if handlerNames[ExpandContextToolName] {
		shadowIDs := tooloutput.ParseExpandPatternsFromText(responseBody)
		for i, shadowID := range shadowIDs {
			calls = append(calls, PhantomToolCall{
				ToolUseID: fmt.Sprintf("text_expand_%d", i),
				ToolName:  ExpandContextToolName,
				Input:     map[string]any{"id": shadowID},
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

// matchesCatchAll returns true if any registered catch-all handler claims toolName.
func (p *PhantomLoop) matchesCatchAll(toolName string) bool {
	for _, h := range p.handlers {
		if ca, ok := h.(CatchAllPhantomToolHandler); ok {
			if ca.ShouldHandle(toolName) {
				return true
			}
		}
	}
	return false
}

// filterCallsByCatchAll collects calls claimed by a catch-all handler.
func (p *PhantomLoop) filterCallsByCatchAll(calls []PhantomToolCall, handler CatchAllPhantomToolHandler) []PhantomToolCall {
	var filtered []PhantomToolCall
	for _, call := range calls {
		if handler.ShouldHandle(call.ToolName) {
			filtered = append(filtered, call)
		}
	}
	return filtered
}
