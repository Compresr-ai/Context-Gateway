// DeferredCallInterceptor intercepts direct calls to lazy-loaded (deferred) tools.
//
// PROBLEM: When tool discovery stubs all tools, the LLM may use training knowledge
// to call a well-known tool (e.g. Bash, Read) directly with correct parameters,
// bypassing gateway_search_tools entirely. The phantom loop previously let such
// calls pass through unchecked, forwarding a tool_use to the client before the
// full schema was available in the request context.
//
// FIX: This handler implements CatchAllPhantomToolHandler and claims any tool call
// whose name is in the deferred tool set. When fired it:
//  1. Injects the full tool schema into the tools[] array (via ModifyRequest)
//  2. Marks the tool as expanded in the session store (KV-cache dedup)
//  3. Returns a tool_result asking the LLM to retry now that the schema is loaded
//
// StopLoop is always false — the phantom loop continues so the LLM re-calls the
// tool with the full schema now visible in the tools array.
//
// Name() returns "" so the phantom loop does not attempt to filter real tool names
// from the final response (they are client-visible tools, not phantom stubs).
package gateway

import (
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/adapters"
)

// DeferredCallInterceptor implements CatchAllPhantomToolHandler.
type DeferredCallInterceptor struct {
	sessionStore  *ToolSessionStore
	sessionID     string
	deferredIndex map[string]adapters.ExtractedContent

	// injected tracks which tools were schema-injected in this request to prevent
	// double-injection within the same phantom loop run.
	mu       sync.Mutex
	injected map[string]bool
}

// Compile-time assertion that DeferredCallInterceptor satisfies the interface.
var _ CatchAllPhantomToolHandler = (*DeferredCallInterceptor)(nil)

// NewDeferredCallInterceptor creates an interceptor for the given deferred tool set.
func NewDeferredCallInterceptor(
	deferred []adapters.ExtractedContent,
	sessionStore *ToolSessionStore,
	sessionID string,
) *DeferredCallInterceptor {
	idx := make(map[string]adapters.ExtractedContent, len(deferred))
	for _, t := range deferred {
		idx[t.ToolName] = t
	}
	return &DeferredCallInterceptor{
		sessionStore:  sessionStore,
		sessionID:     sessionID,
		deferredIndex: idx,
		injected:      make(map[string]bool),
	}
}

// Name returns "" — this handler intercepts real (non-phantom) tool names.
// The phantom loop skips FilterToolCallFromResponse for empty names, which is
// correct: deferred tools are client-visible and must not be stripped from responses.
func (d *DeferredCallInterceptor) Name() string { return "" }

// ShouldHandle returns true when toolName is deferred AND not yet expanded.
// Once a tool's schema has been injected, we let subsequent calls pass through
// to the actual MCP server instead of intercepting them.
func (d *DeferredCallInterceptor) ShouldHandle(toolName string) bool {
	_, isDeferred := d.deferredIndex[toolName]
	if !isDeferred {
		return false
	}

	// Check if already expanded in this request
	d.mu.Lock()
	alreadyInjectedThisRequest := d.injected[toolName]
	d.mu.Unlock()
	if alreadyInjectedThisRequest {
		return false // let it pass through to actual MCP server
	}

	// Check if already expanded in session (cross-request)
	if d.sessionStore != nil && d.sessionID != "" {
		expanded := d.sessionStore.GetExpanded(d.sessionID)
		if expanded[toolName] {
			return false // let it pass through to actual MCP server
		}
	}

	return true // need to intercept and inject schema
}

// HandleCalls injects full schemas and returns retry tool_results.
// Only called for tools that ShouldHandle returned true for.
func (d *DeferredCallInterceptor) HandleCalls(
	calls []PhantomToolCall,
	adapter adapters.Adapter,
	requestBody []byte,
) *PhantomToolResult {
	// Collect already-expanded tools from session (cross-request KV-cache dedup).
	var alreadyExpanded map[string]bool
	if d.sessionStore != nil && d.sessionID != "" {
		alreadyExpanded = d.sessionStore.GetExpanded(d.sessionID)
	}
	if alreadyExpanded == nil {
		alreadyExpanded = make(map[string]bool)
	}

	d.mu.Lock()
	toInject := make([]adapters.ExtractedContent, 0, len(calls))
	newlyInjected := make([]string, 0, len(calls))
	callsNeedingRetry := make([]PhantomToolCall, 0, len(calls))
	for _, call := range calls {
		name := call.ToolName
		if alreadyExpanded[name] || d.injected[name] {
			// Tool already expanded - this shouldn't happen with ShouldHandle fix,
			// but if it does, don't ask LLM to retry (would cause infinite loop).
			log.Warn().Str("tool", name).
				Msg("deferred_interceptor: HandleCalls called for already-expanded tool, skipping")
			continue
		}
		tool, ok := d.deferredIndex[name]
		if !ok {
			continue
		}
		if tool.Content == "" {
			log.Warn().Str("tool", name).
				Msg("deferred_interceptor: tool has no schema (empty Content), skipping injection")
			continue
		}
		toInject = append(toInject, tool)
		newlyInjected = append(newlyInjected, name)
		callsNeedingRetry = append(callsNeedingRetry, call)
		d.injected[name] = true
	}
	d.mu.Unlock()

	// If nothing to inject, return nil to let calls pass through
	if len(callsNeedingRetry) == 0 {
		return nil
	}

	// Persist newly expanded names to session store for cross-turn dedup.
	if len(newlyInjected) > 0 && d.sessionStore != nil && d.sessionID != "" {
		d.sessionStore.MarkExpanded(d.sessionID, newlyInjected)
	}

	// Build per-call tool_result messages telling the LLM the schema is now loaded.
	// Only for tools that actually needed injection (callsNeedingRetry).
	adapterCalls := make([]adapters.ToolCall, 0, len(callsNeedingRetry))
	content := make([]string, 0, len(callsNeedingRetry))
	for _, call := range callsNeedingRetry {
		adapterCalls = append(adapterCalls, adapters.ToolCall{
			ToolUseID: call.ToolUseID,
			ToolName:  call.ToolName,
			Input:     call.Input,
		})
		content = append(content, fmt.Sprintf(
			"Tool %q schema has been loaded into your tool set. "+
				"Please call it again — the full parameter definitions are now available.",
			call.ToolName,
		))
	}

	result := &PhantomToolResult{
		StopLoop:    false,
		ToolResults: adapter.BuildToolResultMessages(adapterCalls, content, requestBody),
	}

	if len(toInject) > 0 {
		captured := toInject
		result.ModifyRequest = func(body []byte) ([]byte, error) {
			return injectToolsIntoRequest(body, captured)
		}
	}

	calledNames := make([]string, len(callsNeedingRetry))
	for i, c := range callsNeedingRetry {
		calledNames[i] = c.ToolName
	}
	log.Info().
		Strs("called", calledNames).
		Strs("injected", newlyInjected).
		Str("session_id", d.sessionID).
		Msg("deferred_interceptor: intercepted direct call to deferred tool, injecting schema")

	return result
}
