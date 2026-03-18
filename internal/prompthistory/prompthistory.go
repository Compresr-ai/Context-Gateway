// Package prompthistory provides a SQLite-backed store for recording and querying
// user prompts sent through the Context Gateway proxy.
package prompthistory

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // Pure-Go SQLite driver, registered as "sqlite".
)

// PromptRecord represents a single recorded prompt.
type PromptRecord struct {
	ID        int64  `json:"id"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	RequestID string `json:"request_id"`
	AgentName string `json:"agent_name,omitempty"`
}

// QueryParams controls filtering, searching, and pagination when querying prompts.
type QueryParams struct {
	Search   string // FTS5 search term
	Session  string // Filter by session ID
	Model    string // Filter by model
	Provider string // Filter by provider
	Page     int    // 1-based page number
	Limit    int    // Results per page (default 50, max 200)
}

// QueryResult is the paginated result returned by Query.
type QueryResult struct {
	Prompts    []PromptRecord `json:"prompts"`
	Total      int            `json:"total"`
	Page       int            `json:"page"`
	Limit      int            `json:"limit"`
	TotalPages int            `json:"total_pages"`
}

// FilterOptions contains the distinct values available for filtering.
type FilterOptions struct {
	Sessions  []string `json:"sessions"`
	Models    []string `json:"models"`
	Providers []string `json:"providers"`
}

// Store defines the interface for prompt history storage.
type Store interface {
	Record(ctx context.Context, rec PromptRecord) error
	Query(ctx context.Context, params QueryParams) (*QueryResult, error)
	FilterOptions(ctx context.Context) (*FilterOptions, error)
	DeleteByID(ctx context.Context, id int64) error
	EraseAll(ctx context.Context) error
	EraseBySession(ctx context.Context, sessionID string) error
	Close() error
}

// SQLiteStore implements Store using a local SQLite database.
type SQLiteStore struct {
	db *sql.DB
	mu sync.Mutex // serializes writes
}

// DefaultDBPath returns the default database file path:
// ~/.config/context-gateway/prompt_history.db
func DefaultDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("prompthistory: unable to determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "context-gateway", "prompt_history.db"), nil
}

// NewDefault opens (or creates) the prompt history database at the default path.
func NewDefault() (*SQLiteStore, error) {
	dbPath, err := DefaultDBPath()
	if err != nil {
		return nil, err
	}
	return New(dbPath)
}

// New opens (or creates) the prompt history database at the given path,
// enables WAL mode, and runs any pending migrations.
func New(dbPath string) (*SQLiteStore, error) {
	// Ensure parent directory exists.
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o750); err != nil { // #nosec G301
		return nil, fmt.Errorf("prompthistory: create directory %s: %w", dir, err)
	}

	dsn := fmt.Sprintf("file:%s?_busy_timeout=5000", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("prompthistory: open database: %w", err)
	}

	// Enable WAL mode for concurrent reads.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			return nil, fmt.Errorf("prompthistory: enable WAL: %w (also failed to close: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("prompthistory: enable WAL: %w", err)
	}

	s := &SQLiteStore{db: db}

	if err := s.migrate(); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			return nil, fmt.Errorf("prompthistory: migrate: %w (also failed to close: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("prompthistory: migrate: %w", err)
	}

	return s, nil
}

// migrate applies versioned schema migrations inside a transaction.
func (s *SQLiteStore) migrate() error {
	// Ensure the schema_version table exists first.
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT    NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_version table: %w", err)
	}

	// Determine current version.
	var current int
	row := s.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version")
	if err := row.Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	// Migration v1: core tables, indexes, FTS, and triggers.
	if current < 1 {
		if err := s.applyMigrationV1(); err != nil {
			return fmt.Errorf("migration v1: %w", err)
		}
	}

	// Migration v2: add agent_name column.
	if current < 2 {
		if err := s.applyMigrationV2(); err != nil {
			return fmt.Errorf("migration v2: %w", err)
		}
	}

	return nil
}

func (s *SQLiteStore) applyMigrationV1() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	statements := []string{
		`CREATE TABLE IF NOT EXISTS prompts (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			text       TEXT    NOT NULL,
			timestamp  TEXT    NOT NULL,
			session_id TEXT    NOT NULL DEFAULT '',
			model      TEXT    NOT NULL DEFAULT '',
			provider   TEXT    NOT NULL DEFAULT '',
			request_id TEXT    NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_prompts_timestamp  ON prompts(timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_prompts_session_id ON prompts(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_prompts_model      ON prompts(model)`,
		`CREATE INDEX IF NOT EXISTS idx_prompts_provider   ON prompts(provider)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS prompts_fts USING fts5(text, content='prompts', content_rowid='id')`,
		`CREATE TRIGGER IF NOT EXISTS prompts_ai AFTER INSERT ON prompts BEGIN
			INSERT INTO prompts_fts(rowid, text) VALUES (new.id, new.text);
		END`,
		`CREATE TRIGGER IF NOT EXISTS prompts_ad AFTER DELETE ON prompts BEGIN
			INSERT INTO prompts_fts(prompts_fts, rowid, text) VALUES('delete', old.id, old.text);
		END`,
		`CREATE TRIGGER IF NOT EXISTS prompts_au AFTER UPDATE ON prompts BEGIN
			INSERT INTO prompts_fts(prompts_fts, rowid, text) VALUES('delete', old.id, old.text);
			INSERT INTO prompts_fts(rowid, text) VALUES (new.id, new.text);
		END`,
	}

	for _, stmt := range statements {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:min(len(stmt), 60)], err)
		}
	}

	// Record migration version.
	if _, err := tx.Exec(
		"INSERT INTO schema_version (version, applied_at) VALUES (?, ?)",
		1, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("insert schema_version: %w", err)
	}

	return tx.Commit()
}

func (s *SQLiteStore) applyMigrationV2() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	statements := []string{
		`ALTER TABLE prompts ADD COLUMN agent_name TEXT NOT NULL DEFAULT ''`,
	}

	for _, stmt := range statements {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:min(len(stmt), 60)], err)
		}
	}

	if _, err := tx.Exec(
		"INSERT INTO schema_version (version, applied_at) VALUES (?, ?)",
		2, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("insert schema_version: %w", err)
	}

	return tx.Commit()
}

// Record upserts a prompt record into the database. If the text is empty the
// call is a no-op. Writes are serialized with a mutex.
// Deduplicates globally by text: if the same prompt text already exists, its
// timestamp and metadata are updated to the latest values instead of inserting
// a duplicate row.
func (s *SQLiteStore) Record(ctx context.Context, rec PromptRecord) error {
	if strings.TrimSpace(rec.Text) == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if this exact text already exists anywhere in the database.
	var existingID int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM prompts WHERE text = ? LIMIT 1`,
		rec.Text,
	).Scan(&existingID)

	if err == nil {
		// Duplicate found: update timestamp and metadata so it surfaces as the most recent.
		_, err = s.db.ExecContext(ctx,
			`UPDATE prompts SET timestamp = ?, session_id = ?, model = ?, provider = ?, request_id = ?, agent_name = ?
			 WHERE id = ?`,
			rec.Timestamp, rec.SessionID, rec.Model, rec.Provider, rec.RequestID, rec.AgentName, existingID,
		)
		if err != nil {
			return fmt.Errorf("prompthistory: update existing: %w", err)
		}
		return nil
	}

	// No duplicate: insert as a new record.
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO prompts (text, timestamp, session_id, model, provider, request_id, agent_name)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rec.Text, rec.Timestamp, rec.SessionID, rec.Model, rec.Provider, rec.RequestID, rec.AgentName,
	)
	if err != nil {
		return fmt.Errorf("prompthistory: record: %w", err)
	}
	return nil
}

// sanitizeFTS5Query escapes user input so it is safe to use in an FTS5 MATCH clause.
// FTS5 has its own query syntax where characters like ", *, OR, AND, NOT, (, ) have
// special meaning. We wrap each whitespace-delimited token in double quotes to treat
// it as a literal phrase, which neutralizes operators and special characters.
func sanitizeFTS5Query(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	// Split on whitespace and quote each token as a literal.
	tokens := strings.Fields(raw)
	for i, t := range tokens {
		// Escape any embedded double quotes by doubling them (FTS5 convention).
		t = strings.ReplaceAll(t, `"`, `""`)
		tokens[i] = `"` + t + `"`
	}
	return strings.Join(tokens, " ")
}

// Query retrieves prompts matching the given parameters with pagination.
// When Search is non-empty an FTS5 MATCH join is used. Results are ordered
// by timestamp descending.
func (s *SQLiteStore) Query(ctx context.Context, params QueryParams) (*QueryResult, error) {
	// Apply defaults and limits.
	if params.Page < 1 {
		params.Page = 1
	}
	if params.Limit <= 0 {
		params.Limit = 50
	}
	if params.Limit > 200 {
		params.Limit = 200
	}

	// Build the WHERE clause and args dynamically.
	var conditions []string
	var args []any

	useFTS := strings.TrimSpace(params.Search) != ""

	if params.Session != "" {
		conditions = append(conditions, "p.session_id = ?")
		args = append(args, params.Session)
	}
	if params.Model != "" {
		conditions = append(conditions, "p.model = ?")
		args = append(args, params.Model)
	}
	if params.Provider != "" {
		conditions = append(conditions, "p.provider = ?")
		args = append(args, params.Provider)
	}

	// Build FROM clause.
	fromClause := "FROM prompts p"
	if useFTS {
		fromClause = "FROM prompts p JOIN prompts_fts f ON p.id = f.rowid"
		conditions = append(conditions, "f.text MATCH ?")
		args = append(args, sanitizeFTS5Query(params.Search))
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Count total matching rows.
	countQuery := fmt.Sprintf("SELECT COUNT(*) %s %s", fromClause, whereClause) //nolint:gosec // #nosec G201 -- fromClause/whereClause are hardcoded strings, all user values use ? placeholders
	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("prompthistory: count query: %w", err)
	}

	totalPages := 0
	if total > 0 {
		totalPages = (total + params.Limit - 1) / params.Limit
	}

	// Fetch paginated results.
	offset := (params.Page - 1) * params.Limit
	selectQuery := fmt.Sprintf( //nolint:gosec // #nosec G201 -- fromClause/whereClause are hardcoded strings, all user values use ? placeholders
		`SELECT p.id, p.text, p.timestamp, p.session_id, p.model, p.provider, p.request_id, p.agent_name
		 %s %s ORDER BY p.timestamp DESC LIMIT ? OFFSET ?`,
		fromClause, whereClause,
	)
	selectArgs := append(args, params.Limit, offset) //nolint:gocritic

	rows, err := s.db.QueryContext(ctx, selectQuery, selectArgs...)
	if err != nil {
		return nil, fmt.Errorf("prompthistory: select query: %w", err)
	}
	defer rows.Close()

	var prompts []PromptRecord
	for rows.Next() {
		var r PromptRecord
		if err := rows.Scan(&r.ID, &r.Text, &r.Timestamp, &r.SessionID, &r.Model, &r.Provider, &r.RequestID, &r.AgentName); err != nil {
			return nil, fmt.Errorf("prompthistory: scan row: %w", err)
		}
		prompts = append(prompts, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("prompthistory: rows iteration: %w", err)
	}

	if prompts == nil {
		prompts = []PromptRecord{}
	}

	return &QueryResult{
		Prompts:    prompts,
		Total:      total,
		Page:       params.Page,
		Limit:      params.Limit,
		TotalPages: totalPages,
	}, nil
}

// FilterOptions returns the distinct non-empty values for session_id, model,
// and provider across all stored prompts.
func (s *SQLiteStore) FilterOptions(ctx context.Context) (*FilterOptions, error) {
	opts := &FilterOptions{}

	sessions, err := s.distinctValues(ctx, "session_id")
	if err != nil {
		return nil, fmt.Errorf("prompthistory: filter sessions: %w", err)
	}
	opts.Sessions = sessions

	models, err := s.distinctValues(ctx, "model")
	if err != nil {
		return nil, fmt.Errorf("prompthistory: filter models: %w", err)
	}
	opts.Models = models

	providers, err := s.distinctValues(ctx, "provider")
	if err != nil {
		return nil, fmt.Errorf("prompthistory: filter providers: %w", err)
	}
	opts.Providers = providers

	return opts, nil
}

// distinctColumnQueries maps allowed column names to pre-built SQL queries.
// Using static strings avoids dynamic SQL formatting entirely.
var distinctColumnQueries = map[string]string{
	"session_id": "SELECT DISTINCT session_id FROM prompts WHERE session_id != '' ORDER BY session_id",
	"model":      "SELECT DISTINCT model FROM prompts WHERE model != '' ORDER BY model",
	"provider":   "SELECT DISTINCT provider FROM prompts WHERE provider != '' ORDER BY provider",
}

func (s *SQLiteStore) distinctValues(ctx context.Context, column string) ([]string, error) {
	query, ok := distinctColumnQueries[column]
	if !ok {
		return nil, fmt.Errorf("prompthistory: invalid filter column: %q", column)
	}
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vals []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		vals = append(vals, v)
	}
	if vals == nil {
		vals = []string{}
	}
	return vals, rows.Err()
}

// EraseAll deletes all prompt records and rebuilds the FTS index.
func (s *SQLiteStore) EraseAll(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("prompthistory: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, "DELETE FROM prompts"); err != nil {
		return fmt.Errorf("prompthistory: delete prompts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO prompts_fts(prompts_fts) VALUES('rebuild')"); err != nil {
		return fmt.Errorf("prompthistory: rebuild fts: %w", err)
	}

	return tx.Commit()
}

// DeleteByID deletes a single prompt by its ID.
func (s *SQLiteStore) DeleteByID(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.ExecContext(ctx, "DELETE FROM prompts WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("prompthistory: delete prompt %d: %w", id, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("prompthistory: delete prompt %d rows affected: %w", id, err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("prompthistory: prompt %d not found", id)
	}
	return nil
}

// EraseBySession deletes all prompts for a specific session.
func (s *SQLiteStore) EraseBySession(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("prompthistory: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, "DELETE FROM prompts WHERE session = ?", sessionID); err != nil {
		return fmt.Errorf("prompthistory: delete prompts for session %s: %w", sessionID, err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO prompts_fts(prompts_fts) VALUES('rebuild')"); err != nil {
		return fmt.Errorf("prompthistory: rebuild fts: %w", err)
	}

	return tx.Commit()
}

// Close closes the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
