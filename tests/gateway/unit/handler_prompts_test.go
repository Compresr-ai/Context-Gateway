package unit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/compresr/context-gateway/internal/prompthistory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// promptsResponse mirrors the JSON shape returned by handlePromptsAPI.
type promptsResponse struct {
	Prompts    []prompthistory.PromptRecord `json:"prompts"`
	Total      int                          `json:"total"`
	Page       int                          `json:"page"`
	Limit      int                          `json:"limit"`
	TotalPages int                          `json:"total_pages"`
	Filters    *prompthistory.FilterOptions `json:"filters"`
}

// newPromptTestStore creates a SQLiteStore backed by a temp directory for test isolation.
func newPromptTestStore(t *testing.T) *prompthistory.SQLiteStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "prompts_test.db")
	store, err := prompthistory.New(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

// seedPrompts inserts a batch of prompt records into the store and returns them.
func seedPrompts(t *testing.T, store *prompthistory.SQLiteStore, records []prompthistory.PromptRecord) {
	t.Helper()
	ctx := context.Background()
	for _, rec := range records {
		require.NoError(t, store.Record(ctx, rec))
	}
}

// queryLikeHandler simulates how handlePromptsAPI builds QueryParams from URL query params
// and queries the store, returning the same response structure the handler produces.
func queryLikeHandler(t *testing.T, store *prompthistory.SQLiteStore, q, session, model, provider string, page, limit int) promptsResponse {
	t.Helper()
	ctx := context.Background()

	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	params := prompthistory.QueryParams{
		Search:   q,
		Session:  session,
		Model:    model,
		Provider: provider,
		Page:     page,
		Limit:    limit,
	}

	result, err := store.Query(ctx, params)
	require.NoError(t, err)

	filters, err := store.FilterOptions(ctx)
	require.NoError(t, err)

	return promptsResponse{
		Prompts:    result.Prompts,
		Total:      result.Total,
		Page:       result.Page,
		Limit:      result.Limit,
		TotalPages: result.TotalPages,
		Filters:    filters,
	}
}

// --- HTTP-level tests using the full gateway ---

// TestHandlePromptsAPI_HTTPResponseStructure verifies the /api/prompts endpoint
// returns a well-formed response with correct headers and JSON structure.
// Note: The gateway uses the default prompt history database, which may contain data.
func TestHandlePromptsAPI_HTTPResponseStructure(t *testing.T) {
	cfg := dashboardConfig()
	gw := gateway.New(cfg)
	defer gw.Shutdown(context.Background())

	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	resp, err := http.Get(gwServer.URL + "/api/prompts")
	require.NoError(t, err)
	defer resp.Body.Close()

	// The gateway always attempts to initialize prompt history.
	// If it succeeds, we get 200; if not, 503. Both are valid in CI.
	if resp.StatusCode == http.StatusServiceUnavailable {
		t.Skip("prompt history not available in this environment")
	}

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))

	var result promptsResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

	// Verify structural correctness regardless of data content
	assert.Equal(t, 1, result.Page)
	assert.Equal(t, 50, result.Limit)
	assert.GreaterOrEqual(t, result.Total, 0)
	assert.GreaterOrEqual(t, result.TotalPages, 0)
	assert.NotNil(t, result.Prompts)
	assert.NotNil(t, result.Filters)
	assert.NotNil(t, result.Filters.Sessions)
	assert.NotNil(t, result.Filters.Models)
	assert.NotNil(t, result.Filters.Providers)
}

// TestHandlePromptsAPI_DefaultPagination verifies the handler uses page=1 and limit=50
// when no pagination params are provided, through the full HTTP endpoint.
func TestHandlePromptsAPI_DefaultPagination(t *testing.T) {
	cfg := dashboardConfig()
	gw := gateway.New(cfg)
	defer gw.Shutdown(context.Background())

	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	resp, err := http.Get(gwServer.URL + "/api/prompts")
	require.NoError(t, err)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		t.Skip("prompt history not available in this environment")
	}

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result promptsResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

	assert.Equal(t, 1, result.Page)
	assert.Equal(t, 50, result.Limit)
}

// TestHandlePromptsAPI_EmptyDatabase verifies the store returns a properly-structured
// empty response with zero total and default pagination when no data exists.
func TestHandlePromptsAPI_EmptyDatabase(t *testing.T) {
	store := newPromptTestStore(t)

	resp := queryLikeHandler(t, store, "", "", "", "", 0, 0)

	assert.Equal(t, 0, resp.Total)
	assert.Equal(t, 0, resp.TotalPages)
	assert.Equal(t, 1, resp.Page)
	assert.Equal(t, 50, resp.Limit)
	assert.NotNil(t, resp.Prompts)
	assert.Len(t, resp.Prompts, 0)
	assert.NotNil(t, resp.Filters)
	assert.Equal(t, []string{}, resp.Filters.Sessions)
	assert.Equal(t, []string{}, resp.Filters.Models)
	assert.Equal(t, []string{}, resp.Filters.Providers)
}

// --- Store-level integration tests simulating the full API flow ---

// TestHandlePromptsAPI_ReturnsPrompts inserts prompts, queries the store as the handler would,
// and verifies the complete JSON response structure including all fields.
func TestHandlePromptsAPI_ReturnsPrompts(t *testing.T) {
	store := newPromptTestStore(t)

	now := time.Now().UTC()
	records := []prompthistory.PromptRecord{
		{
			Text:      "Explain Go interfaces",
			Timestamp: now.Add(-2 * time.Minute).Format(time.RFC3339),
			SessionID: "sess_001",
			Model:     "claude-sonnet-4-20250514",
			Provider:  "anthropic",
			RequestID: "req_aaa",
		},
		{
			Text:      "Write a Python decorator",
			Timestamp: now.Add(-1 * time.Minute).Format(time.RFC3339),
			SessionID: "sess_001",
			Model:     "gpt-4o",
			Provider:  "openai",
			RequestID: "req_bbb",
		},
		{
			Text:      "Debug this segfault in C++",
			Timestamp: now.Format(time.RFC3339),
			SessionID: "sess_002",
			Model:     "claude-sonnet-4-20250514",
			Provider:  "anthropic",
			RequestID: "req_ccc",
		},
	}
	seedPrompts(t, store, records)

	resp := queryLikeHandler(t, store, "", "", "", "", 0, 0)

	// Verify pagination metadata
	assert.Equal(t, 3, resp.Total)
	assert.Equal(t, 1, resp.TotalPages)
	assert.Equal(t, 1, resp.Page)
	assert.Equal(t, 50, resp.Limit)

	// Verify all prompts returned, ordered by timestamp DESC (newest first)
	require.Len(t, resp.Prompts, 3)
	assert.Equal(t, "Debug this segfault in C++", resp.Prompts[0].Text)
	assert.Equal(t, "Write a Python decorator", resp.Prompts[1].Text)
	assert.Equal(t, "Explain Go interfaces", resp.Prompts[2].Text)

	// Verify all fields are populated on returned records
	for _, p := range resp.Prompts {
		assert.NotZero(t, p.ID)
		assert.NotEmpty(t, p.Text)
		assert.NotEmpty(t, p.Timestamp)
		assert.NotEmpty(t, p.SessionID)
		assert.NotEmpty(t, p.Model)
		assert.NotEmpty(t, p.Provider)
		assert.NotEmpty(t, p.RequestID)
	}

	// Verify filters contain correct distinct values
	require.NotNil(t, resp.Filters)
	assert.ElementsMatch(t, []string{"sess_001", "sess_002"}, resp.Filters.Sessions)
	assert.ElementsMatch(t, []string{"claude-sonnet-4-20250514", "gpt-4o"}, resp.Filters.Models)
	assert.ElementsMatch(t, []string{"anthropic", "openai"}, resp.Filters.Providers)

	// Verify JSON round-trip produces valid structure
	jsonBytes, err := json.Marshal(resp)
	require.NoError(t, err)
	var decoded promptsResponse
	require.NoError(t, json.Unmarshal(jsonBytes, &decoded))
	assert.Equal(t, resp.Total, decoded.Total)
	assert.Equal(t, resp.Page, decoded.Page)
	assert.Len(t, decoded.Prompts, 3)
}

// TestHandlePromptsAPI_SearchFilter tests the ?q= search parameter
// which maps to FTS5 full-text search.
func TestHandlePromptsAPI_SearchFilter(t *testing.T) {
	store := newPromptTestStore(t)

	records := []prompthistory.PromptRecord{
		{Text: "Fix the authentication bug in the login handler", Timestamp: "2026-03-09T10:00:01Z", SessionID: "s1", Model: "gpt-4o", Provider: "openai"},
		{Text: "Add rate limiting to the API gateway", Timestamp: "2026-03-09T10:00:02Z", SessionID: "s1", Model: "gpt-4o", Provider: "openai"},
		{Text: "Refactor authentication middleware to use JWT", Timestamp: "2026-03-09T10:00:03Z", SessionID: "s2", Model: "claude-sonnet-4-20250514", Provider: "anthropic"},
		{Text: "Write database migration for user roles", Timestamp: "2026-03-09T10:00:04Z", SessionID: "s2", Model: "claude-sonnet-4-20250514", Provider: "anthropic"},
		{Text: "Update CI pipeline configuration", Timestamp: "2026-03-09T10:00:05Z", SessionID: "s3", Model: "gpt-4o", Provider: "openai"},
	}
	seedPrompts(t, store, records)

	// Search for "authentication" — should match 2 records
	resp := queryLikeHandler(t, store, "authentication", "", "", "", 0, 0)
	assert.Equal(t, 2, resp.Total)
	for _, p := range resp.Prompts {
		assert.Contains(t, p.Text, "authentication")
	}

	// Search for "migration" — should match 1 record
	resp = queryLikeHandler(t, store, "migration", "", "", "", 0, 0)
	assert.Equal(t, 1, resp.Total)
	assert.Contains(t, resp.Prompts[0].Text, "migration")

	// Search for something not present — should return empty results
	resp = queryLikeHandler(t, store, "kubernetes", "", "", "", 0, 0)
	assert.Equal(t, 0, resp.Total)
	assert.Len(t, resp.Prompts, 0)

	// Filters should still reflect all data regardless of search
	assert.Len(t, resp.Filters.Sessions, 3)
	assert.Len(t, resp.Filters.Models, 2)
	assert.Len(t, resp.Filters.Providers, 2)
}

// TestHandlePromptsAPI_SessionFilter tests the ?session= filter parameter.
func TestHandlePromptsAPI_SessionFilter(t *testing.T) {
	store := newPromptTestStore(t)

	records := []prompthistory.PromptRecord{
		{Text: "prompt in session alpha", Timestamp: "2026-03-09T10:00:01Z", SessionID: "alpha", Model: "gpt-4o", Provider: "openai"},
		{Text: "another in alpha", Timestamp: "2026-03-09T10:00:02Z", SessionID: "alpha", Model: "gpt-4o", Provider: "openai"},
		{Text: "prompt in session beta", Timestamp: "2026-03-09T10:00:03Z", SessionID: "beta", Model: "claude-sonnet-4-20250514", Provider: "anthropic"},
		{Text: "one more in beta", Timestamp: "2026-03-09T10:00:04Z", SessionID: "beta", Model: "claude-sonnet-4-20250514", Provider: "anthropic"},
		{Text: "single in gamma", Timestamp: "2026-03-09T10:00:05Z", SessionID: "gamma", Model: "gpt-4o", Provider: "openai"},
	}
	seedPrompts(t, store, records)

	// Filter by "alpha" session
	resp := queryLikeHandler(t, store, "", "alpha", "", "", 0, 0)
	assert.Equal(t, 2, resp.Total)
	for _, p := range resp.Prompts {
		assert.Equal(t, "alpha", p.SessionID)
	}

	// Filter by "beta" session
	resp = queryLikeHandler(t, store, "", "beta", "", "", 0, 0)
	assert.Equal(t, 2, resp.Total)
	for _, p := range resp.Prompts {
		assert.Equal(t, "beta", p.SessionID)
	}

	// Filter by "gamma" session
	resp = queryLikeHandler(t, store, "", "gamma", "", "", 0, 0)
	assert.Equal(t, 1, resp.Total)
	assert.Equal(t, "gamma", resp.Prompts[0].SessionID)

	// Non-existent session
	resp = queryLikeHandler(t, store, "", "nonexistent", "", "", 0, 0)
	assert.Equal(t, 0, resp.Total)
	assert.Len(t, resp.Prompts, 0)
}

// TestHandlePromptsAPI_ModelFilter tests the ?model= filter parameter.
func TestHandlePromptsAPI_ModelFilter(t *testing.T) {
	store := newPromptTestStore(t)

	records := []prompthistory.PromptRecord{
		{Text: "prompt with gpt-4o", Timestamp: "2026-03-09T10:00:01Z", SessionID: "s1", Model: "gpt-4o", Provider: "openai"},
		{Text: "prompt with claude", Timestamp: "2026-03-09T10:00:02Z", SessionID: "s1", Model: "claude-sonnet-4-20250514", Provider: "anthropic"},
		{Text: "another gpt-4o prompt", Timestamp: "2026-03-09T10:00:03Z", SessionID: "s2", Model: "gpt-4o", Provider: "openai"},
		{Text: "prompt with o1", Timestamp: "2026-03-09T10:00:04Z", SessionID: "s2", Model: "o1-preview", Provider: "openai"},
	}
	seedPrompts(t, store, records)

	// Filter by gpt-4o
	resp := queryLikeHandler(t, store, "", "", "gpt-4o", "", 0, 0)
	assert.Equal(t, 2, resp.Total)
	for _, p := range resp.Prompts {
		assert.Equal(t, "gpt-4o", p.Model)
	}

	// Filter by claude model
	resp = queryLikeHandler(t, store, "", "", "claude-sonnet-4-20250514", "", 0, 0)
	assert.Equal(t, 1, resp.Total)
	assert.Equal(t, "claude-sonnet-4-20250514", resp.Prompts[0].Model)

	// Filter by o1-preview
	resp = queryLikeHandler(t, store, "", "", "o1-preview", "", 0, 0)
	assert.Equal(t, 1, resp.Total)
	assert.Equal(t, "o1-preview", resp.Prompts[0].Model)

	// Non-existent model
	resp = queryLikeHandler(t, store, "", "", "llama-3", "", 0, 0)
	assert.Equal(t, 0, resp.Total)
}

// TestHandlePromptsAPI_ProviderFilter tests the ?provider= filter parameter.
func TestHandlePromptsAPI_ProviderFilter(t *testing.T) {
	store := newPromptTestStore(t)

	records := []prompthistory.PromptRecord{
		{Text: "anthropic prompt 1", Timestamp: "2026-03-09T10:00:01Z", SessionID: "s1", Model: "claude-sonnet-4-20250514", Provider: "anthropic"},
		{Text: "openai prompt 1", Timestamp: "2026-03-09T10:00:02Z", SessionID: "s1", Model: "gpt-4o", Provider: "openai"},
		{Text: "anthropic prompt 2", Timestamp: "2026-03-09T10:00:03Z", SessionID: "s2", Model: "claude-sonnet-4-20250514", Provider: "anthropic"},
		{Text: "bedrock prompt 1", Timestamp: "2026-03-09T10:00:04Z", SessionID: "s2", Model: "claude-sonnet-4-20250514", Provider: "bedrock"},
	}
	seedPrompts(t, store, records)

	// Filter by anthropic
	resp := queryLikeHandler(t, store, "", "", "", "anthropic", 0, 0)
	assert.Equal(t, 2, resp.Total)
	for _, p := range resp.Prompts {
		assert.Equal(t, "anthropic", p.Provider)
	}

	// Filter by openai
	resp = queryLikeHandler(t, store, "", "", "", "openai", 0, 0)
	assert.Equal(t, 1, resp.Total)
	assert.Equal(t, "openai", resp.Prompts[0].Provider)

	// Filter by bedrock
	resp = queryLikeHandler(t, store, "", "", "", "bedrock", 0, 0)
	assert.Equal(t, 1, resp.Total)
	assert.Equal(t, "bedrock", resp.Prompts[0].Provider)

	// Non-existent provider
	resp = queryLikeHandler(t, store, "", "", "", "google", 0, 0)
	assert.Equal(t, 0, resp.Total)
}

// TestHandlePromptsAPI_Pagination verifies that page, limit, and total_pages work correctly
// when browsing through a large set of prompts.
func TestHandlePromptsAPI_Pagination(t *testing.T) {
	store := newPromptTestStore(t)

	// Insert 27 prompts with distinct timestamps for deterministic ordering
	var records []prompthistory.PromptRecord
	for i := 1; i <= 27; i++ {
		records = append(records, prompthistory.PromptRecord{
			Text:      fmt.Sprintf("paginated prompt %02d", i),
			Timestamp: time.Date(2026, 3, 9, 10, 0, i, 0, time.UTC).Format(time.RFC3339),
			SessionID: "sess_pag",
			Model:     "gpt-4o",
			Provider:  "openai",
		})
	}
	seedPrompts(t, store, records)

	// Page 1, limit 10
	resp := queryLikeHandler(t, store, "", "", "", "", 1, 10)
	assert.Equal(t, 27, resp.Total)
	assert.Equal(t, 3, resp.TotalPages)
	assert.Equal(t, 1, resp.Page)
	assert.Equal(t, 10, resp.Limit)
	require.Len(t, resp.Prompts, 10)
	// Newest first: prompt 27 at index 0
	assert.Equal(t, "paginated prompt 27", resp.Prompts[0].Text)
	assert.Equal(t, "paginated prompt 18", resp.Prompts[9].Text)

	// Page 2, limit 10
	resp = queryLikeHandler(t, store, "", "", "", "", 2, 10)
	assert.Equal(t, 27, resp.Total)
	assert.Equal(t, 3, resp.TotalPages)
	assert.Equal(t, 2, resp.Page)
	require.Len(t, resp.Prompts, 10)
	assert.Equal(t, "paginated prompt 17", resp.Prompts[0].Text)
	assert.Equal(t, "paginated prompt 08", resp.Prompts[9].Text)

	// Page 3, limit 10 — only 7 results remain
	resp = queryLikeHandler(t, store, "", "", "", "", 3, 10)
	assert.Equal(t, 27, resp.Total)
	assert.Equal(t, 3, resp.TotalPages)
	assert.Equal(t, 3, resp.Page)
	require.Len(t, resp.Prompts, 7)
	assert.Equal(t, "paginated prompt 07", resp.Prompts[0].Text)
	assert.Equal(t, "paginated prompt 01", resp.Prompts[6].Text)

	// Page beyond total — empty results
	resp = queryLikeHandler(t, store, "", "", "", "", 5, 10)
	assert.Equal(t, 27, resp.Total)
	assert.Equal(t, 3, resp.TotalPages)
	assert.Len(t, resp.Prompts, 0)

	// Large limit to get everything in one page
	resp = queryLikeHandler(t, store, "", "", "", "", 1, 100)
	assert.Equal(t, 27, resp.Total)
	assert.Equal(t, 1, resp.TotalPages)
	require.Len(t, resp.Prompts, 27)

	// Limit capped at 200 (handler enforces this)
	resp = queryLikeHandler(t, store, "", "", "", "", 1, 500)
	assert.Equal(t, 200, resp.Limit) // Capped
}

// TestHandlePromptsAPI_FiltersResponse verifies that the "filters" field in the response
// reflects all distinct non-empty values across the entire database, independent of
// any search or filter applied to the prompts themselves.
func TestHandlePromptsAPI_FiltersResponse(t *testing.T) {
	store := newPromptTestStore(t)

	records := []prompthistory.PromptRecord{
		{Text: "prompt 1", Timestamp: "2026-03-09T10:00:01Z", SessionID: "session_a", Model: "gpt-4o", Provider: "openai"},
		{Text: "prompt 2", Timestamp: "2026-03-09T10:00:02Z", SessionID: "session_b", Model: "claude-sonnet-4-20250514", Provider: "anthropic"},
		{Text: "prompt 3", Timestamp: "2026-03-09T10:00:03Z", SessionID: "session_a", Model: "o1-preview", Provider: "openai"},
		{Text: "prompt 4", Timestamp: "2026-03-09T10:00:04Z", SessionID: "session_c", Model: "gpt-4o", Provider: "openai"},
		// Record with empty fields — should not appear in filter options
		{Text: "prompt with no metadata", Timestamp: "2026-03-09T10:00:05Z", SessionID: "", Model: "", Provider: ""},
	}
	seedPrompts(t, store, records)

	resp := queryLikeHandler(t, store, "", "", "", "", 0, 0)
	require.NotNil(t, resp.Filters)

	// Sessions: only non-empty, sorted
	assert.ElementsMatch(t, []string{"session_a", "session_b", "session_c"}, resp.Filters.Sessions)

	// Models: distinct non-empty
	assert.ElementsMatch(t, []string{"claude-sonnet-4-20250514", "gpt-4o", "o1-preview"}, resp.Filters.Models)

	// Providers: distinct non-empty
	assert.ElementsMatch(t, []string{"anthropic", "openai"}, resp.Filters.Providers)

	// Filters remain the same even when filtering narrows the prompt results
	resp = queryLikeHandler(t, store, "", "session_a", "", "", 0, 0)
	assert.Equal(t, 2, resp.Total) // Only 2 prompts in session_a
	// But filters still show ALL distinct values
	assert.ElementsMatch(t, []string{"session_a", "session_b", "session_c"}, resp.Filters.Sessions)
	assert.ElementsMatch(t, []string{"claude-sonnet-4-20250514", "gpt-4o", "o1-preview"}, resp.Filters.Models)
}

// TestHandlePromptsAPI_CombinedFilters tests applying multiple filter parameters together,
// including search + session + model + provider combinations.
func TestHandlePromptsAPI_CombinedFilters(t *testing.T) {
	store := newPromptTestStore(t)

	records := []prompthistory.PromptRecord{
		{Text: "Fix authentication bug in login", Timestamp: "2026-03-09T10:00:01Z", SessionID: "dev", Model: "gpt-4o", Provider: "openai"},
		{Text: "Add authentication tests for API", Timestamp: "2026-03-09T10:00:02Z", SessionID: "dev", Model: "claude-sonnet-4-20250514", Provider: "anthropic"},
		{Text: "Refactor authentication middleware", Timestamp: "2026-03-09T10:00:03Z", SessionID: "staging", Model: "gpt-4o", Provider: "openai"},
		{Text: "Deploy to production server", Timestamp: "2026-03-09T10:00:04Z", SessionID: "dev", Model: "gpt-4o", Provider: "openai"},
		{Text: "Update authentication docs", Timestamp: "2026-03-09T10:00:05Z", SessionID: "dev", Model: "gpt-4o", Provider: "openai"},
		{Text: "Review authentication PR", Timestamp: "2026-03-09T10:00:06Z", SessionID: "staging", Model: "claude-sonnet-4-20250514", Provider: "anthropic"},
	}
	seedPrompts(t, store, records)

	// session=dev + model=gpt-4o (no search) -> 3 results
	resp := queryLikeHandler(t, store, "", "dev", "gpt-4o", "", 0, 0)
	assert.Equal(t, 3, resp.Total)
	for _, p := range resp.Prompts {
		assert.Equal(t, "dev", p.SessionID)
		assert.Equal(t, "gpt-4o", p.Model)
	}

	// session=dev + provider=anthropic -> 1 result
	resp = queryLikeHandler(t, store, "", "dev", "", "anthropic", 0, 0)
	assert.Equal(t, 1, resp.Total)
	assert.Equal(t, "Add authentication tests for API", resp.Prompts[0].Text)

	// search=authentication + session=dev -> 3 results (auth bug, auth tests, auth docs)
	resp = queryLikeHandler(t, store, "authentication", "dev", "", "", 0, 0)
	assert.Equal(t, 3, resp.Total)
	for _, p := range resp.Prompts {
		assert.Equal(t, "dev", p.SessionID)
		assert.Contains(t, p.Text, "authentication")
	}

	// search=authentication + session=dev + model=gpt-4o -> 2 results (auth bug, auth docs)
	resp = queryLikeHandler(t, store, "authentication", "dev", "gpt-4o", "", 0, 0)
	assert.Equal(t, 2, resp.Total)
	for _, p := range resp.Prompts {
		assert.Equal(t, "dev", p.SessionID)
		assert.Equal(t, "gpt-4o", p.Model)
		assert.Contains(t, p.Text, "authentication")
	}

	// search=authentication + session=staging + model=claude + provider=anthropic -> 1 result
	resp = queryLikeHandler(t, store, "authentication", "staging", "claude-sonnet-4-20250514", "anthropic", 0, 0)
	assert.Equal(t, 1, resp.Total)
	assert.Equal(t, "Review authentication PR", resp.Prompts[0].Text)

	// All filters with no match
	resp = queryLikeHandler(t, store, "kubernetes", "dev", "gpt-4o", "openai", 0, 0)
	assert.Equal(t, 0, resp.Total)
	assert.Len(t, resp.Prompts, 0)
}

// TestHandlePromptsAPI_PaginationWithFilters verifies that pagination works correctly
// when combined with filter parameters — ensuring total and total_pages reflect
// filtered counts, not the entire database.
func TestHandlePromptsAPI_PaginationWithFilters(t *testing.T) {
	store := newPromptTestStore(t)

	// Insert 20 records: 12 for session "active", 8 for session "archived"
	var records []prompthistory.PromptRecord
	for i := 1; i <= 12; i++ {
		records = append(records, prompthistory.PromptRecord{
			Text:      fmt.Sprintf("active task %02d", i),
			Timestamp: time.Date(2026, 3, 9, 10, 0, i, 0, time.UTC).Format(time.RFC3339),
			SessionID: "active",
			Model:     "gpt-4o",
			Provider:  "openai",
		})
	}
	for i := 1; i <= 8; i++ {
		records = append(records, prompthistory.PromptRecord{
			Text:      fmt.Sprintf("archived task %02d", i),
			Timestamp: time.Date(2026, 3, 9, 11, 0, i, 0, time.UTC).Format(time.RFC3339),
			SessionID: "archived",
			Model:     "claude-sonnet-4-20250514",
			Provider:  "anthropic",
		})
	}
	seedPrompts(t, store, records)

	// Page through "active" session with limit 5
	resp := queryLikeHandler(t, store, "", "active", "", "", 1, 5)
	assert.Equal(t, 12, resp.Total) // 12 active records
	assert.Equal(t, 3, resp.TotalPages)
	assert.Equal(t, 1, resp.Page)
	require.Len(t, resp.Prompts, 5)

	// Last page of active session
	resp = queryLikeHandler(t, store, "", "active", "", "", 3, 5)
	assert.Equal(t, 12, resp.Total)
	require.Len(t, resp.Prompts, 2)

	// Filters still show all distinct values even when filtering
	assert.ElementsMatch(t, []string{"active", "archived"}, resp.Filters.Sessions)
}

// TestHandlePromptsAPI_ResponseJSONContract verifies the exact JSON contract
// between the API and the frontend by checking field presence and types.
func TestHandlePromptsAPI_ResponseJSONContract(t *testing.T) {
	store := newPromptTestStore(t)

	seedPrompts(t, store, []prompthistory.PromptRecord{
		{
			Text:      "test JSON contract",
			Timestamp: "2026-03-09T14:30:00Z",
			SessionID: "contract_session",
			Model:     "gpt-4o",
			Provider:  "openai",
			RequestID: "req_contract",
		},
	})

	resp := queryLikeHandler(t, store, "", "", "", "", 0, 0)

	// Marshal to JSON and verify all expected top-level fields exist
	jsonBytes, err := json.Marshal(resp)
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(jsonBytes, &raw))

	expectedTopLevel := []string{"prompts", "total", "page", "limit", "total_pages", "filters"}
	for _, key := range expectedTopLevel {
		_, exists := raw[key]
		assert.True(t, exists, "top-level field %q should exist in response JSON", key)
	}

	// Verify prompt record field structure
	require.Len(t, resp.Prompts, 1)
	promptJSON, err := json.Marshal(resp.Prompts[0])
	require.NoError(t, err)

	var promptRaw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(promptJSON, &promptRaw))

	expectedPromptFields := []string{"id", "text", "timestamp", "session_id", "model", "provider", "request_id"}
	for _, key := range expectedPromptFields {
		_, exists := promptRaw[key]
		assert.True(t, exists, "prompt field %q should exist", key)
	}

	// Verify filters structure
	filtersJSON, err := json.Marshal(resp.Filters)
	require.NoError(t, err)

	var filtersRaw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(filtersJSON, &filtersRaw))

	expectedFilterFields := []string{"sessions", "models", "providers"}
	for _, key := range expectedFilterFields {
		_, exists := filtersRaw[key]
		assert.True(t, exists, "filters field %q should exist", key)
	}
}
