package taskoutput

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/rs/zerolog/log"
)

// Logger is a shared, process-level JSONL event writer.
// One instance is shared across all pool workers to avoid multiple FDs per log file.
type Logger struct {
	mu    sync.Mutex
	files map[string]*os.File
	base  string
}

// NewLogger creates a Logger that writes events to {base}_{provider}.jsonl files.
// If base is empty, Write is a no-op.
func NewLogger(base string) *Logger {
	return &Logger{
		base:  base,
		files: make(map[string]*os.File),
	}
}

// Write serialises evt as JSON and appends it to the provider-specific JSONL log.
// If the Logger has no base path configured, the call is silently ignored.
func (l *Logger) Write(provider string, evt TaskOutputEvent) {
	if l.base == "" {
		return
	}

	data, err := json.Marshal(evt)
	if err != nil {
		return
	}
	data = append(data, '\n')

	f, err := l.openFile(provider)
	if err != nil {
		log.Warn().Err(err).Str("provider", provider).Msg("task_output: cannot open log file")
		return
	}

	l.mu.Lock()
	_, _ = f.Write(data)
	l.mu.Unlock()
}

// Close closes all open log files.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var firstErr error
	for provider, f := range l.files {
		if err := f.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close log for provider %q: %w", provider, err)
		}
		delete(l.files, provider)
	}
	return firstErr
}

// openFile returns (or lazily creates) the log file for the given provider.
func (l *Logger) openFile(provider string) (*os.File, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if f, ok := l.files[provider]; ok {
		return f, nil
	}

	dir := filepath.Dir(l.base)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create log dir %s: %w", dir, err)
	}

	path := fmt.Sprintf("%s_%s.jsonl", l.base, provider)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //#nosec G304 -- path is from operator config, not user input
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	l.files[provider] = f
	return f, nil
}
