package prompthistory

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helper to create a store backed by a temp directory.
func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := New(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

func TestNew_CreatesDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "subdir", "nested", "test.db")
	store, err := New(dbPath)
	require.NoError(t, err)
	defer store.Close()

	// Verify the database is usable by running a simple query.
	var count int
	err = store.db.QueryRow("SELECT COUNT(*) FROM prompts").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Verify schema_version was populated.
	var version int
	err = store.db.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version)
	require.NoError(t, err)
	assert.Equal(t, 1, version)
}

func TestRecord_InsertsPrompt(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	rec := PromptRecord{
		Text:      "Explain concurrency in Go",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		SessionID: "sess_001",
		Model:     "claude-sonnet-4-20250514",
		Provider:  "anthropic",
		RequestID: "req_abc",
	}

	err := store.Record(ctx, rec)
	require.NoError(t, err)

	// Verify insertion.
	var text, sessionID, model, provider, requestID string
	err = store.db.QueryRow("SELECT text, session_id, model, provider, request_id FROM prompts WHERE id = 1").
		Scan(&text, &sessionID, &model, &provider, &requestID)
	require.NoError(t, err)
	assert.Equal(t, rec.Text, text)
	assert.Equal(t, rec.SessionID, sessionID)
	assert.Equal(t, rec.Model, model)
	assert.Equal(t, rec.Provider, provider)
	assert.Equal(t, rec.RequestID, requestID)
}

func TestRecord_SkipsEmptyText(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Empty string
	err := store.Record(ctx, PromptRecord{Text: "", Timestamp: time.Now().UTC().Format(time.RFC3339)})
	require.NoError(t, err)

	// Whitespace only
	err = store.Record(ctx, PromptRecord{Text: "   ", Timestamp: time.Now().UTC().Format(time.RFC3339)})
	require.NoError(t, err)

	var count int
	err = store.db.QueryRow("SELECT COUNT(*) FROM prompts").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestQuery_Pagination(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Insert 15 records.
	for i := 1; i <= 15; i++ {
		ts := time.Date(2026, 3, 9, 10, 0, i, 0, time.UTC).Format(time.RFC3339)
		err := store.Record(ctx, PromptRecord{
			Text:      fmt.Sprintf("prompt %d", i),
			Timestamp: ts,
			SessionID: "sess",
			Model:     "model",
			Provider:  "provider",
		})
		require.NoError(t, err)
	}

	// Page 1, limit 5
	result, err := store.Query(ctx, QueryParams{Page: 1, Limit: 5})
	require.NoError(t, err)
	assert.Equal(t, 15, result.Total)
	assert.Equal(t, 3, result.TotalPages)
	assert.Equal(t, 1, result.Page)
	assert.Equal(t, 5, result.Limit)
	assert.Len(t, result.Prompts, 5)
	// Results ordered by timestamp DESC, so newest first.
	assert.Equal(t, "prompt 15", result.Prompts[0].Text)
	assert.Equal(t, "prompt 11", result.Prompts[4].Text)

	// Page 3, limit 5
	result, err = store.Query(ctx, QueryParams{Page: 3, Limit: 5})
	require.NoError(t, err)
	assert.Len(t, result.Prompts, 5)
	assert.Equal(t, "prompt 5", result.Prompts[0].Text)
	assert.Equal(t, "prompt 1", result.Prompts[4].Text)

	// Page beyond total
	result, err = store.Query(ctx, QueryParams{Page: 10, Limit: 5})
	require.NoError(t, err)
	assert.Len(t, result.Prompts, 0)
	assert.Equal(t, 15, result.Total)
}

func TestQuery_DefaultsAndLimits(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Zero page and limit should use defaults.
	result, err := store.Query(ctx, QueryParams{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Page)
	assert.Equal(t, 50, result.Limit)

	// Limit above 200 should be capped.
	result, err = store.Query(ctx, QueryParams{Limit: 500})
	require.NoError(t, err)
	assert.Equal(t, 200, result.Limit)
}

func TestQuery_FTS5Search(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	records := []PromptRecord{
		{Text: "Fix the authentication bug in handler.go", Timestamp: "2026-03-09T10:00:01Z", SessionID: "s1"},
		{Text: "Add unit tests for the payment module", Timestamp: "2026-03-09T10:00:02Z", SessionID: "s1"},
		{Text: "Refactor authentication middleware", Timestamp: "2026-03-09T10:00:03Z", SessionID: "s2"},
		{Text: "Update the README with setup instructions", Timestamp: "2026-03-09T10:00:04Z", SessionID: "s2"},
	}
	for _, r := range records {
		require.NoError(t, store.Record(ctx, r))
	}

	// Search for "authentication"
	result, err := store.Query(ctx, QueryParams{Search: "authentication"})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Total)
	for _, p := range result.Prompts {
		assert.Contains(t, p.Text, "authentication")
	}

	// Search for "README"
	result, err = store.Query(ctx, QueryParams{Search: "README"})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Total)
	assert.Equal(t, "Update the README with setup instructions", result.Prompts[0].Text)

	// Search with no matches
	result, err = store.Query(ctx, QueryParams{Search: "kubernetes"})
	require.NoError(t, err)
	assert.Equal(t, 0, result.Total)
	assert.Len(t, result.Prompts, 0)
}

func TestQuery_FilterBySession(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.Record(ctx, PromptRecord{Text: "prompt A", Timestamp: "2026-03-09T10:00:01Z", SessionID: "sess_1"}))
	require.NoError(t, store.Record(ctx, PromptRecord{Text: "prompt B", Timestamp: "2026-03-09T10:00:02Z", SessionID: "sess_2"}))
	require.NoError(t, store.Record(ctx, PromptRecord{Text: "prompt C", Timestamp: "2026-03-09T10:00:03Z", SessionID: "sess_1"}))

	result, err := store.Query(ctx, QueryParams{Session: "sess_1"})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Total)
	for _, p := range result.Prompts {
		assert.Equal(t, "sess_1", p.SessionID)
	}
}

func TestQuery_FilterByModel(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.Record(ctx, PromptRecord{Text: "prompt A", Timestamp: "2026-03-09T10:00:01Z", Model: "gpt-4o"}))
	require.NoError(t, store.Record(ctx, PromptRecord{Text: "prompt B", Timestamp: "2026-03-09T10:00:02Z", Model: "claude-sonnet-4-20250514"}))
	require.NoError(t, store.Record(ctx, PromptRecord{Text: "prompt C", Timestamp: "2026-03-09T10:00:03Z", Model: "gpt-4o"}))

	result, err := store.Query(ctx, QueryParams{Model: "gpt-4o"})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Total)
	for _, p := range result.Prompts {
		assert.Equal(t, "gpt-4o", p.Model)
	}
}

func TestQuery_FilterByProvider(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.Record(ctx, PromptRecord{Text: "prompt A", Timestamp: "2026-03-09T10:00:01Z", Provider: "anthropic"}))
	require.NoError(t, store.Record(ctx, PromptRecord{Text: "prompt B", Timestamp: "2026-03-09T10:00:02Z", Provider: "openai"}))
	require.NoError(t, store.Record(ctx, PromptRecord{Text: "prompt C", Timestamp: "2026-03-09T10:00:03Z", Provider: "anthropic"}))

	result, err := store.Query(ctx, QueryParams{Provider: "anthropic"})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Total)
	for _, p := range result.Prompts {
		assert.Equal(t, "anthropic", p.Provider)
	}
}

func TestQuery_CombinedFilters(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	records := []PromptRecord{
		{Text: "Fix authentication bug", Timestamp: "2026-03-09T10:00:01Z", SessionID: "s1", Model: "gpt-4o", Provider: "openai"},
		{Text: "Add authentication tests", Timestamp: "2026-03-09T10:00:02Z", SessionID: "s1", Model: "claude-sonnet-4-20250514", Provider: "anthropic"},
		{Text: "Refactor authentication layer", Timestamp: "2026-03-09T10:00:03Z", SessionID: "s2", Model: "gpt-4o", Provider: "openai"},
		{Text: "Deploy to production", Timestamp: "2026-03-09T10:00:04Z", SessionID: "s1", Model: "gpt-4o", Provider: "openai"},
	}
	for _, r := range records {
		require.NoError(t, store.Record(ctx, r))
	}

	// FTS + session filter
	result, err := store.Query(ctx, QueryParams{Search: "authentication", Session: "s1"})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Total)

	// FTS + session + model
	result, err = store.Query(ctx, QueryParams{Search: "authentication", Session: "s1", Model: "gpt-4o"})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Total)
	assert.Equal(t, "Fix authentication bug", result.Prompts[0].Text)

	// Session + model + provider (no FTS)
	result, err = store.Query(ctx, QueryParams{Session: "s1", Model: "gpt-4o", Provider: "openai"})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Total)
}

func TestFilterOptions(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	records := []PromptRecord{
		{Text: "prompt 1", Timestamp: "2026-03-09T10:00:01Z", SessionID: "s1", Model: "gpt-4o", Provider: "openai"},
		{Text: "prompt 2", Timestamp: "2026-03-09T10:00:02Z", SessionID: "s2", Model: "claude-sonnet-4-20250514", Provider: "anthropic"},
		{Text: "prompt 3", Timestamp: "2026-03-09T10:00:03Z", SessionID: "s1", Model: "gpt-4o", Provider: "openai"},
		{Text: "prompt 4", Timestamp: "2026-03-09T10:00:04Z", SessionID: "", Model: "", Provider: ""},
	}
	for _, r := range records {
		require.NoError(t, store.Record(ctx, r))
	}

	opts, err := store.FilterOptions(ctx)
	require.NoError(t, err)

	// Empty strings should be excluded.
	assert.ElementsMatch(t, []string{"s1", "s2"}, opts.Sessions)
	assert.ElementsMatch(t, []string{"claude-sonnet-4-20250514", "gpt-4o"}, opts.Models)
	assert.ElementsMatch(t, []string{"anthropic", "openai"}, opts.Providers)
}

func TestFilterOptions_Empty(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	opts, err := store.FilterOptions(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{}, opts.Sessions)
	assert.Equal(t, []string{}, opts.Models)
	assert.Equal(t, []string{}, opts.Providers)
}

func TestMigrate_Idempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// Open and close the store twice — migrations should not fail the second time.
	store1, err := New(dbPath)
	require.NoError(t, err)

	// Insert a record so we can verify data survives.
	err = store1.Record(context.Background(), PromptRecord{
		Text:      "persisted prompt",
		Timestamp: "2026-03-09T10:00:01Z",
		SessionID: "s1",
	})
	require.NoError(t, err)
	require.NoError(t, store1.Close())

	// Re-open — should run migrate() again without error.
	store2, err := New(dbPath)
	require.NoError(t, err)
	defer store2.Close()

	// Verify the version is still 1 (not duplicated).
	var versionCount int
	err = store2.db.QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&versionCount)
	require.NoError(t, err)
	assert.Equal(t, 1, versionCount)

	// Verify previous data is still accessible.
	result, err := store2.Query(context.Background(), QueryParams{})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Total)
	assert.Equal(t, "persisted prompt", result.Prompts[0].Text)

	// FTS should still work on previously inserted data.
	result, err = store2.Query(context.Background(), QueryParams{Search: "persisted"})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Total)
}

func TestQuery_EmptyDatabase(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	result, err := store.Query(ctx, QueryParams{})
	require.NoError(t, err)
	assert.Equal(t, 0, result.Total)
	assert.Equal(t, 0, result.TotalPages)
	assert.Equal(t, 1, result.Page)
	assert.Equal(t, 50, result.Limit)
	assert.Equal(t, []PromptRecord{}, result.Prompts)
}

func TestRecord_ConcurrentWrites(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Launch concurrent writes to exercise the mutex.
	const n = 50
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			errs <- store.Record(ctx, PromptRecord{
				Text:      fmt.Sprintf("concurrent prompt %d", idx),
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				SessionID: "concurrent",
			})
		}(i)
	}

	for i := 0; i < n; i++ {
		require.NoError(t, <-errs)
	}

	var count int
	err := store.db.QueryRow("SELECT COUNT(*) FROM prompts").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, n, count)
}

func TestDefaultDBPath(t *testing.T) {
	path, err := DefaultDBPath()
	require.NoError(t, err)
	assert.Contains(t, path, ".config")
	assert.Contains(t, path, "context-gateway")
	assert.Contains(t, path, "prompt_history.db")
}

func TestNew_WALMode(t *testing.T) {
	store := newTestStore(t)

	var mode string
	err := store.db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	require.NoError(t, err)
	assert.Equal(t, "wal", mode)
}

func TestQuery_FTSWithSpecialCharacters(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.Record(ctx, PromptRecord{
		Text:      "What is the error in handler.go on line 42?",
		Timestamp: "2026-03-09T10:00:01Z",
	}))
	require.NoError(t, store.Record(ctx, PromptRecord{
		Text:      "handler function needs refactoring",
		Timestamp: "2026-03-09T10:00:02Z",
	}))

	// FTS5 should match partial tokens.
	result, err := store.Query(ctx, QueryParams{Search: "handler"})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Total)
}

func TestClose(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := New(dbPath)
	require.NoError(t, err)

	require.NoError(t, store.Close())

	// After close, operations should fail.
	err = store.db.Ping()
	assert.Error(t, err)
}

func TestQuery_AllFieldsReturned(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	rec := PromptRecord{
		Text:      "full record test",
		Timestamp: "2026-03-09T14:30:00Z",
		SessionID: "session_xyz",
		Model:     "claude-sonnet-4-20250514",
		Provider:  "anthropic",
		RequestID: "req_123",
	}
	require.NoError(t, store.Record(ctx, rec))

	result, err := store.Query(ctx, QueryParams{})
	require.NoError(t, err)
	require.Len(t, result.Prompts, 1)

	p := result.Prompts[0]
	assert.Equal(t, int64(1), p.ID)
	assert.Equal(t, rec.Text, p.Text)
	assert.Equal(t, rec.Timestamp, p.Timestamp)
	assert.Equal(t, rec.SessionID, p.SessionID)
	assert.Equal(t, rec.Model, p.Model)
	assert.Equal(t, rec.Provider, p.Provider)
	assert.Equal(t, rec.RequestID, p.RequestID)
}

func TestStoreImplementsInterface(t *testing.T) {
	// Compile-time check that SQLiteStore satisfies Store.
	var _ Store = (*SQLiteStore)(nil)

	// Also verify with an actual instance.
	store := newTestStore(t)
	var s Store = store
	assert.NotNil(t, s)
}

func TestQuery_FTSAndPaginationCombined(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Insert 12 records containing "deploy", 3 without.
	for i := 1; i <= 12; i++ {
		ts := time.Date(2026, 3, 9, 10, 0, i, 0, time.UTC).Format(time.RFC3339)
		require.NoError(t, store.Record(ctx, PromptRecord{
			Text:      fmt.Sprintf("deploy service %d to production", i),
			Timestamp: ts,
		}))
	}
	for i := 1; i <= 3; i++ {
		ts := time.Date(2026, 3, 9, 11, 0, i, 0, time.UTC).Format(time.RFC3339)
		require.NoError(t, store.Record(ctx, PromptRecord{
			Text:      fmt.Sprintf("refactor module %d", i),
			Timestamp: ts,
		}))
	}

	// Search "deploy" with limit 5.
	result, err := store.Query(ctx, QueryParams{Search: "deploy", Limit: 5, Page: 1})
	require.NoError(t, err)
	assert.Equal(t, 12, result.Total)
	assert.Equal(t, 3, result.TotalPages)
	assert.Len(t, result.Prompts, 5)

	// Page 3 should have 2 results.
	result, err = store.Query(ctx, QueryParams{Search: "deploy", Limit: 5, Page: 3})
	require.NoError(t, err)
	assert.Len(t, result.Prompts, 2)
}

func TestNew_InvalidPath(t *testing.T) {
	// Attempt to create a database at a path that cannot be a directory.
	// On most systems, /dev/null/subdir is invalid.
	_, err := New("/dev/null/subdir/test.db")
	assert.Error(t, err)
}

func TestSQLiteStore_SatisfiesStoreInterface(t *testing.T) {
	// Ensure *SQLiteStore can be assigned to Store at compile time.
	fn := func() Store {
		return &SQLiteStore{db: &sql.DB{}}
	}
	assert.NotNil(t, fn)
}
