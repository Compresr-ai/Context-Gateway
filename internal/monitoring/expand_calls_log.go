// Package monitoring - expand_calls_log.go writes expand_context_calls.jsonl.
package monitoring

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ExpandContextCallEntry records a single expand_context invocation with the
// original and compressed content of the tool output that triggered it.
type ExpandContextCallEntry struct {
	Timestamp         time.Time `json:"timestamp"`
	SessionID         string    `json:"session_id"`
	RequestID         string    `json:"request_id"`
	ShadowID          string    `json:"shadow_id"`
	ToolName          string    `json:"tool_name"`
	Found             bool      `json:"found"` // whether shadow ref was resolved
	OriginalTokens    int       `json:"original_tokens"`
	CompressedTokens  int       `json:"compressed_tokens"`
	OriginalContent   string    `json:"original_content"`   // full uncompressed content
	CompressedContent string    `json:"compressed_content"` // what the model saw before calling expand
}

// ExpandCallsLogger appends ExpandContextCallEntry records to a JSONL file.
// Thread-safe. Safe to call on a nil receiver (disabled).
type ExpandCallsLogger struct {
	mu   sync.Mutex
	file *os.File
}

// NewExpandCallsLogger opens (or creates) the JSONL file for append.
// Returns nil if path is empty (feature disabled).
func NewExpandCallsLogger(path string) (*ExpandCallsLogger, error) {
	if path == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600) // #nosec G304
	if err != nil {
		return nil, err
	}
	return &ExpandCallsLogger{file: f}, nil
}

// Log appends an entry to the JSONL file. Safe to call on nil.
func (l *ExpandCallsLogger) Log(entry ExpandContextCallEntry) {
	if l == nil {
		return
	}
	data, err := json.Marshal(entry)
	if err != nil {
		log.Error().Err(err).Msg("expand_calls: marshal failed")
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.file.Write(append(data, '\n')); err != nil {
		log.Error().Err(err).Msg("expand_calls: write failed")
	}
}

// Close flushes and closes the file. Safe to call on nil.
func (l *ExpandCallsLogger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.file.Close()
}
