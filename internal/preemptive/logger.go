// Compaction event logging for preemptive summarization.
//
// Logs compaction events to a dedicated JSONL file for debugging.
package preemptive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CompactionLogger writes compaction events to a dedicated log file.
type CompactionLogger struct {
	mu      sync.Mutex
	file    *os.File
	path    string
	enabled bool
}

// CompactionEvent represents a log entry.
type CompactionEvent struct {
	Timestamp          string                 `json:"timestamp"`
	Event              string                 `json:"event"`
	SessionID          string                 `json:"session_id,omitempty"`
	Model              string                 `json:"model,omitempty"`
	DetectedBy         string                 `json:"detected_by,omitempty"`
	Confidence         float64                `json:"confidence,omitempty"`
	UsagePercent       float64                `json:"usage_percent,omitempty"`
	Threshold          float64                `json:"threshold,omitempty"`
	WasPrecomputed     bool                   `json:"was_precomputed,omitempty"`
	MessagesSummarized int                    `json:"messages_summarized,omitempty"`
	SummaryTokens      int                    `json:"summary_tokens,omitempty"`
	DurationMs         int64                  `json:"duration_ms,omitempty"`
	Error              string                 `json:"error,omitempty"`
	Details            map[string]interface{} `json:"details,omitempty"`
}

var (
	compactionLogger *CompactionLogger
	compactionOnce   sync.Once
)

// InitCompactionLogger initializes the logger with a directory path.
func InitCompactionLogger(logDir string) error {
	return InitCompactionLoggerWithPath(filepath.Join(logDir, "history_compaction.jsonl"))
}

// InitCompactionLoggerWithPath initializes the logger with a file path.
func InitCompactionLoggerWithPath(logPath string) error {
	var initErr error
	compactionOnce.Do(func() {
		// Handle both directory and file paths
		path := logPath
		if filepath.Ext(logPath) != ".jsonl" {
			path = filepath.Join(logPath, "history_compaction.jsonl")
		}

		// Create directory
		if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
			initErr = fmt.Errorf("create log dir: %w", err)
			return
		}

		// Open file
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600) // #nosec G304 -- user-configured log path
		if err != nil {
			initErr = fmt.Errorf("open log file: %w", err)
			return
		}

		compactionLogger = &CompactionLogger{file: file, path: path, enabled: true}
		compactionLogger.Log(CompactionEvent{Event: "logger_initialized", Details: map[string]interface{}{"path": path}})
	})
	return initErr
}

// LogSessionConfig logs the configuration used for this gateway session.
func LogSessionConfig(configName, configSource string, summarizerProvider, summarizerModel string) {
	if l := GetCompactionLogger(); l != nil {
		l.Log(CompactionEvent{
			Event: "session_config",
			Details: map[string]interface{}{
				"config_name":         configName,
				"config_source":       configSource,
				"summarizer_provider": summarizerProvider,
				"summarizer_model":    summarizerModel,
			},
		})
	}
}

// GetCompactionLogger returns the global logger (nil if not initialized).
func GetCompactionLogger() *CompactionLogger {
	return compactionLogger
}

// Log writes an event to the log file.
func (cl *CompactionLogger) Log(event CompactionEvent) {
	if cl == nil || !cl.enabled {
		return
	}
	cl.mu.Lock()
	defer cl.mu.Unlock()

	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if data, err := json.Marshal(event); err == nil {
		_, _ = cl.file.Write(append(data, '\n'))
	}
}

// LogPreemptiveTrigger logs when background summarization starts.
func (cl *CompactionLogger) LogPreemptiveTrigger(sessionID, model string, msgCount int, usage, threshold float64, summarizerProvider, summarizerModel string) {
	cl.Log(CompactionEvent{
		Event:        "preemptive_trigger",
		SessionID:    sessionID,
		Model:        model,
		UsagePercent: usage,
		Threshold:    threshold,
		Details: map[string]interface{}{
			"message_count":       msgCount,
			"summarizer_provider": summarizerProvider,
			"summarizer_model":    summarizerModel,
		},
	})
}

// LogPreemptiveComplete logs when background summarization finishes.
func (cl *CompactionLogger) LogPreemptiveComplete(sessionID, model string, msgsSummarized, tokens int, duration time.Duration, summarizerProvider, summarizerModel string) {
	cl.Log(CompactionEvent{
		Event:              "preemptive_complete",
		SessionID:          sessionID,
		Model:              model,
		MessagesSummarized: msgsSummarized,
		SummaryTokens:      tokens,
		DurationMs:         duration.Milliseconds(),
		Details: map[string]interface{}{
			"summarizer_provider": summarizerProvider,
			"summarizer_model":    summarizerModel,
		},
	})
}

// LogCompactionDetected logs when a compaction request is detected.
func (cl *CompactionLogger) LogCompactionDetected(sessionID, model, detectedBy string, confidence float64) {
	cl.Log(CompactionEvent{
		Event:      "compaction_detected",
		SessionID:  sessionID,
		Model:      model,
		DetectedBy: detectedBy,
		Confidence: confidence,
	})
}

// LogCompactionApplied logs when compaction is applied.
func (cl *CompactionLogger) LogCompactionApplied(sessionID, model string, precomputed bool, msgsSummarized, tokens, size int, details map[string]interface{}) {
	if details == nil {
		details = make(map[string]interface{})
	}
	details["compacted_body_size"] = size
	cl.Log(CompactionEvent{
		Event:              "compaction_applied",
		SessionID:          sessionID,
		Model:              model,
		WasPrecomputed:     precomputed,
		MessagesSummarized: msgsSummarized,
		SummaryTokens:      tokens,
		Details:            details,
	})
}

// LogCompactionFallback logs when synchronous summarization is used.
func (cl *CompactionLogger) LogCompactionFallback(sessionID, model, reason string) {
	cl.Log(CompactionEvent{
		Event:     "compaction_fallback",
		SessionID: sessionID,
		Model:     model,
		Details:   map[string]interface{}{"reason": reason},
	})
}

// LogError logs an error.
func (cl *CompactionLogger) LogError(sessionID, event string, err error, details map[string]interface{}) {
	cl.Log(CompactionEvent{
		Event:     event + "_error",
		SessionID: sessionID,
		Error:     err.Error(),
		Details:   details,
	})
}

// LogSkip logs when summarization is skipped (not an error, just not enough content).
func (cl *CompactionLogger) LogSkip(sessionID, event, reason string, details map[string]interface{}) {
	if details == nil {
		details = make(map[string]interface{})
	}
	details["reason"] = reason
	cl.Log(CompactionEvent{
		Event:     event + "_skip",
		SessionID: sessionID,
		Details:   details,
	})
}

// LogEvent logs a generic event.
func (cl *CompactionLogger) LogEvent(event, sessionID, model string, err error, details map[string]interface{}) {
	evt := CompactionEvent{Event: event, SessionID: sessionID, Model: model, Details: details}
	if err != nil {
		evt.Error = err.Error()
	}
	cl.Log(evt)
}

// Close closes the log file.
func (cl *CompactionLogger) Close() error {
	if cl == nil || cl.file == nil {
		return nil
	}
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.enabled = false
	return cl.file.Close()
}
