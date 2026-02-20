package unit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/gateway"
)

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
		Strategy:    "api",
		APIEndpoint: server.URL,
	})
	h.SetRequestContext("session-1", deferred)

	result := h.HandleCalls([]gateway.PhantomToolCall{{
		ToolUseID: "call_1",
		ToolName:  "gateway_search_tools",
		Input:     map[string]any{"query": "find files"},
	}}, false)
	require.NotNil(t, result)
	require.Len(t, result.ToolResults, 1)
	content, _ := result.ToolResults[0]["content"].(string)
	assert.Contains(t, content, "read_file")
	assert.Contains(t, content, "search_code")

	events := h.ConsumeAPIFallbackEvents()
	require.Len(t, events, 1)
	assert.Equal(t, "empty_selection", events[0].Reason)
	assert.Equal(t, 2, events[0].DeferredCount)
	assert.Equal(t, 2, events[0].ReturnedCount)
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
		Strategy:    "api",
		APIEndpoint: server.URL,
	})
	h.SetRequestContext("session-1", deferred)

	result := h.HandleCalls([]gateway.PhantomToolCall{{
		ToolUseID: "call_1",
		ToolName:  "gateway_search_tools",
		Input:     map[string]any{"query": "lookup"},
	}}, false)
	require.NotNil(t, result)
	require.Len(t, result.ToolResults, 1)
	content, _ := result.ToolResults[0]["content"].(string)
	assert.NotContains(t, content, "read_file")
	assert.Contains(t, content, "search_code")

	assert.Nil(t, h.ConsumeAPIFallbackEvents())
}

func TestSearchToolHandler_APIEmptyQueryFallbackKeepsAllTools(t *testing.T) {
	deferred := []adapters.ExtractedContent{
		{ToolName: "read_file", Content: "Read files"},
		{ToolName: "search_code", Content: "Search code"},
	}

	h := gateway.NewSearchToolHandler("gateway_search_tools", 5, nil, gateway.SearchToolHandlerOptions{
		Strategy:    "api",
		APIEndpoint: "https://example.com/v1/tool-discovery/search",
	})
	h.SetRequestContext("session-1", deferred)

	result := h.HandleCalls([]gateway.PhantomToolCall{{
		ToolUseID: "call_1",
		ToolName:  "gateway_search_tools",
		Input:     map[string]any{"query": "   "},
	}}, false)
	require.NotNil(t, result)
	require.Len(t, result.ToolResults, 1)
	content, _ := result.ToolResults[0]["content"].(string)
	assert.Contains(t, content, "read_file")
	assert.Contains(t, content, "search_code")

	events := h.ConsumeAPIFallbackEvents()
	require.Len(t, events, 1)
	assert.Equal(t, "empty_query", events[0].Reason)
	assert.Equal(t, 2, events[0].DeferredCount)
	assert.Equal(t, 2, events[0].ReturnedCount)
}
