package unit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
)

// extractToolContent extracts the tool result text content from an Anthropic-format tool result message.
// Anthropic adapter returns: [{"role":"user","content":[{"type":"tool_result","tool_use_id":"...","content":"TEXT"}]}]
func extractToolContent(t *testing.T, result *gateway.PhantomToolResult) string {
	t.Helper()
	require.Len(t, result.ToolResults, 1)
	blocks, ok := result.ToolResults[0]["content"].([]any)
	require.True(t, ok, "expected content blocks")
	require.Len(t, blocks, 1)
	block, ok := blocks[0].(map[string]any)
	require.True(t, ok)
	text, _ := block["content"].(string)
	return text
}

func TestSearchToolHandler_APINonMeaningfulFallbackKeepsAllTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"selected_names":[]}`))
	}))
	defer server.Close()

	deferred := []adapters.ExtractedContent{
		{ToolName: "read_file", Content: "Read files"},
		{ToolName: "search_code", Content: "Search code"},
	}

	h := gateway.NewSearchToolHandler("gateway_search_tools", 5, nil, gateway.SearchToolHandlerOptions{
		Strategy:    config.StrategyToolSearch,
		APIEndpoint: server.URL,
	})
	h.SetRequestContext(context.Background(), "session-1", deferred, gateway.CapturedAuth{})

	result := h.HandleCalls([]gateway.PhantomToolCall{{
		ToolUseID: "call_1",
		ToolName:  "gateway_search_tools",
		Input:     map[string]any{"query": "file"},
	}}, adapters.NewAnthropicAdapter(), nil)
	require.NotNil(t, result)
	content := extractToolContent(t, result)
	// After API returns non-meaningful selection, falls back to local regex
	// "file" matches "read_file" via regex
	assert.Contains(t, content, "read_file")

	events := h.ConsumeAPIFallbackEvents()
	require.Len(t, events, 1)
	assert.Equal(t, "empty_selection", events[0].Reason)
	assert.Equal(t, 2, events[0].DeferredCount)
}

func TestSearchToolHandler_APIMeaningfulSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)

		var req map[string]any
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "lookup", req["pattern"])

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"selected_names":["search_code"]}`))
	}))
	defer server.Close()

	deferred := []adapters.ExtractedContent{
		{ToolName: "read_file", Content: "Read files"},
		{ToolName: "search_code", Content: "Search code"},
	}

	h := gateway.NewSearchToolHandler("gateway_search_tools", 5, nil, gateway.SearchToolHandlerOptions{
		Strategy:    config.StrategyToolSearch,
		APIEndpoint: server.URL,
	})
	h.SetRequestContext(context.Background(), "session-1", deferred, gateway.CapturedAuth{})

	result := h.HandleCalls([]gateway.PhantomToolCall{{
		ToolUseID: "call_1",
		ToolName:  "gateway_search_tools",
		Input:     map[string]any{"query": "lookup"},
	}}, adapters.NewAnthropicAdapter(), nil)
	require.NotNil(t, result)
	content := extractToolContent(t, result)
	assert.NotContains(t, content, "read_file")
	assert.Contains(t, content, "search_code")

	assert.Nil(t, h.ConsumeAPIFallbackEvents())
}

func TestSearchToolHandler_APIEmptyQueryFallbackNoResults(t *testing.T) {
	deferred := []adapters.ExtractedContent{
		{ToolName: "read_file", Content: "Read files"},
		{ToolName: "search_code", Content: "Search code"},
	}

	h := gateway.NewSearchToolHandler("gateway_search_tools", 5, nil, gateway.SearchToolHandlerOptions{
		Strategy:    config.StrategyToolSearch,
		APIEndpoint: "https://example.com/v1/tool-discovery/search",
	})
	h.SetRequestContext(context.Background(), "session-1", deferred, gateway.CapturedAuth{})

	result := h.HandleCalls([]gateway.PhantomToolCall{{
		ToolUseID: "call_1",
		ToolName:  "gateway_search_tools",
		Input:     map[string]any{"query": "   "},
	}}, adapters.NewAnthropicAdapter(), nil)
	require.NotNil(t, result)
	content := extractToolContent(t, result)
	// Empty query: API returns non-meaningful, local regex also returns nothing
	assert.Contains(t, content, "No tools found")

	events := h.ConsumeAPIFallbackEvents()
	require.Len(t, events, 1)
	assert.Equal(t, "empty_query", events[0].Reason)
	assert.Equal(t, 2, events[0].DeferredCount)
}

// TestSearchToolHandler_AlreadyExpandedToolsFiltered tests that tools already marked
// as expanded in the session are not re-injected, preserving KV-cache.
func TestSearchToolHandler_AlreadyExpandedToolsFiltered(t *testing.T) {
	// Set up session store with one tool already expanded
	sessionStore := gateway.NewToolSessionStore(0)
	sessionStore.MarkExpanded("session-1", []string{"read_file"})

	deferred := []adapters.ExtractedContent{
		{ToolName: "read_file", Content: "Read files", Metadata: map[string]any{"raw_json": `{"name":"read_file"}`}},
		{ToolName: "search_code", Content: "Search code", Metadata: map[string]any{"raw_json": `{"name":"search_code"}`}},
	}

	h := gateway.NewSearchToolHandler("gateway_search_tools", 5, sessionStore, gateway.SearchToolHandlerOptions{
		Strategy: config.StrategyRelevance, // Use local search
	})
	h.SetRequestContext(context.Background(), "session-1", deferred, gateway.CapturedAuth{})

	// Query matches search_code (read_file is already expanded so must not re-appear)
	result := h.HandleCalls([]gateway.PhantomToolCall{{
		ToolUseID: "call_1",
		ToolName:  "gateway_search_tools",
		Input:     map[string]any{"query": "search"},
	}}, adapters.NewAnthropicAdapter(), nil)

	require.NotNil(t, result)
	content := extractToolContent(t, result)

	// read_file was already expanded - should not appear in results
	assert.NotContains(t, content, "read_file")
	// search_code is new - should appear
	assert.Contains(t, content, "search_code")

	// ModifyRequest should only inject search_code (the new tool)
	require.NotNil(t, result.ModifyRequest)
}

// TestSearchToolHandler_AllToolsAlreadyExpanded tests that when all matched tools
// are already expanded, no tools are injected and a helpful message is returned.
func TestSearchToolHandler_AllToolsAlreadyExpanded(t *testing.T) {
	// Set up session store with all tools already expanded
	sessionStore := gateway.NewToolSessionStore(0)
	sessionStore.MarkExpanded("session-1", []string{"read_file", "search_code"})

	deferred := []adapters.ExtractedContent{
		{ToolName: "read_file", Content: "Read files"},
		{ToolName: "search_code", Content: "Search code"},
	}

	h := gateway.NewSearchToolHandler("gateway_search_tools", 5, sessionStore, gateway.SearchToolHandlerOptions{
		Strategy: config.StrategyRelevance,
	})
	h.SetRequestContext(context.Background(), "session-1", deferred, gateway.CapturedAuth{})

	result := h.HandleCalls([]gateway.PhantomToolCall{{
		ToolUseID: "call_1",
		ToolName:  "gateway_search_tools",
		Input:     map[string]any{"query": "search"},
	}}, adapters.NewAnthropicAdapter(), nil)

	require.NotNil(t, result)
	content := extractToolContent(t, result)

	// Should indicate no additional tools found (search_code matched but already expanded)
	assert.Contains(t, content, "No additional tools found")
	assert.Contains(t, content, "already available")

	// ModifyRequest should be nil - no new tools to inject
	assert.Nil(t, result.ModifyRequest)
}
