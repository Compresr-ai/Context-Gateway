// Package monitoring - telemetry.go records gateway events as JSONL files.
package monitoring

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// bufPool is a package-level pool of *bytes.Buffer reused across JSONL write calls
// to reduce allocations on the hot write path.
var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// Tracker handles telemetry event recording to file and stdout.
type Tracker struct {
	config               TelemetryConfig
	requestLogPath       string
	compressionLogPath   string
	toolDiscoveryLogPath string
	taskOutputLogPath    string // unified task output compression log
	sessionToolsPath     string // path for session_tools.json (pretty-printed catalog)
	requestLogFile       *os.File
	compressionLogFile   *os.File
	toolDiscoveryLogFile *os.File
	taskOutputLogFile    *os.File
	requestCount         int
	compressionCount     int
	toolDiscoveryCount   int
	taskOutputCount      int
	seenSessionTools     map[string]map[string]bool // sessionID → tool names already in session_tools.json
	statsTracker         *SessionStatsTracker       // live session_stats.json writer
	expandCallsLogger    *ExpandCallsLogger         // expand_context_calls.jsonl writer
	// Per-file mutexes allow concurrent writes to different log files (P7).
	muRequest       sync.Mutex // guards requestLogFile
	muCompression   sync.Mutex // guards compressionLogFile
	muToolDiscovery sync.Mutex // guards toolDiscoveryLogFile
	muTaskOutput    sync.Mutex // guards taskOutputLogFile
	muSessionTools  sync.Mutex // guards seenSessionTools + sessionToolsPath
}

// NewTracker creates a new telemetry tracker.
func NewTracker(cfg TelemetryConfig) (*Tracker, error) {
	t := &Tracker{
		config:           cfg,
		seenSessionTools: make(map[string]map[string]bool),
	}

	if !cfg.Enabled {
		return t, nil
	}

	// Store paths and open persistent file handles
	if cfg.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.LogPath), 0750); err != nil {
			return nil, err
		}
		t.requestLogPath = cfg.LogPath
		f, err := os.OpenFile(cfg.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return nil, fmt.Errorf("open request log: %w", err)
		}
		t.requestLogFile = f
	}

	if cfg.CompressionLogPath != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.CompressionLogPath), 0750); err != nil {
			return nil, err
		}
		t.compressionLogPath = cfg.CompressionLogPath
		f, err := os.OpenFile(cfg.CompressionLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return nil, fmt.Errorf("open compression log: %w", err)
		}
		t.compressionLogFile = f
	}

	if cfg.ToolDiscoveryLogPath != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.ToolDiscoveryLogPath), 0750); err != nil {
			return nil, err
		}
		t.toolDiscoveryLogPath = cfg.ToolDiscoveryLogPath
		f, err := os.OpenFile(cfg.ToolDiscoveryLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return nil, fmt.Errorf("open tool discovery log: %w", err)
		}
		t.toolDiscoveryLogFile = f
	}

	// Task output unified compression log: {base}_compression.jsonl
	if cfg.TaskOutputLogPath != "" {
		taskOutputCompLog := filepath.Clean(strings.TrimSuffix(cfg.TaskOutputLogPath, ".jsonl") + "_compression.jsonl")
		if err := os.MkdirAll(filepath.Dir(taskOutputCompLog), 0750); err != nil {
			return nil, err
		}
		t.taskOutputLogPath = taskOutputCompLog
		f, err := os.OpenFile(taskOutputCompLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600) // #nosec G304 -- path is from config, cleaned with filepath.Clean
		if err != nil {
			return nil, fmt.Errorf("open task output log: %w", err)
		}
		t.taskOutputLogFile = f
	}

	if cfg.SessionToolsPath != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.SessionToolsPath), 0750); err != nil {
			return nil, err
		}
		t.sessionToolsPath = cfg.SessionToolsPath
	}

	if cfg.SessionStatsPath != "" {
		t.statsTracker = NewSessionStatsTracker(cfg.SessionStatsPath, 3*time.Second)
		t.statsTracker.Start()
	}

	if cfg.ExpandContextCallsPath != "" {
		el, err := NewExpandCallsLogger(cfg.ExpandContextCallsPath)
		if err != nil {
			return nil, fmt.Errorf("open expand_context_calls log: %w", err)
		}
		t.expandCallsLogger = el
	}

	return t, nil
}

// writeJSONL writes a single JSON object as a line to an open file handle.
// Uses bufPool to reuse buffer allocations on the hot write path.
func writeJSONL(f *os.File, event any) error {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	if err := json.NewEncoder(buf).Encode(event); err != nil {
		bufPool.Put(buf)
		return err
	}
	_, err := f.Write(buf.Bytes())
	bufPool.Put(buf)
	return err
}

// RecordRequest records a request event.
func (t *Tracker) RecordRequest(event *RequestEvent) {
	// Stats are independent of telemetry enabled flag — update always.
	t.statsTracker.RecordRequest(event)

	if !t.config.Enabled {
		return
	}

	t.muRequest.Lock()
	defer t.muRequest.Unlock()

	// Log summary to stdout if enabled
	if t.config.LogToStdout {
		reqID := event.RequestID
		if len(reqID) > 8 {
			reqID = reqID[:8]
		}
		log.Info().
			Str("request_id", reqID).
			Str("pipe", string(event.PipeType)).
			// Int("tokens_saved", event.TokensSaved).
			Bool("success", event.Success).
			Msg("telemetry")
	}

	// Append to JSONL file
	if t.requestLogFile != nil {
		if err := writeJSONL(t.requestLogFile, event); err != nil {
			log.Error().Err(err).Str("path", t.requestLogPath).Msg("telemetry: failed to write request event")
		} else {
			t.requestCount++
		}
	}
}

// RecordExpand records an expand_context call.
func (t *Tracker) RecordExpand(event *ExpandEvent) {
	if !t.config.Enabled {
		return
	}

	t.muRequest.Lock()
	defer t.muRequest.Unlock()

	// Append to JSONL file
	if t.requestLogFile != nil {
		if err := writeJSONL(t.requestLogFile, event); err != nil {
			log.Error().Err(err).Str("path", t.requestLogPath).Msg("telemetry: failed to write expand event")
		} else {
			t.requestCount++
		}
	}
}

// CompressionLogEnabled returns true if compression logging is enabled.
func (t *Tracker) CompressionLogEnabled() bool {
	return t.config.Enabled && t.compressionLogPath != ""
}

// ToolDiscoveryLogEnabled returns true if tool discovery logging is enabled.
func (t *Tracker) ToolDiscoveryLogEnabled() bool {
	return t.config.Enabled && t.toolDiscoveryLogPath != ""
}

// now returns the current UTC timestamp in RFC3339 format.
func now() string { return time.Now().UTC().Format(time.RFC3339) }

// LogCompressionComparison logs a tool-output compression event to tool_output_compression.jsonl.
// Converts the internal CompressionComparison to a typed ToolOutputEntry before writing.
func (t *Tracker) LogCompressionComparison(c CompressionComparison) {
	// Stats are independent of JSONL file config — update always.
	t.statsTracker.RecordToolOutput(c.Status, c.OriginalTokens, c.CompressedTokens, c.CacheHit)

	if !t.CompressionLogEnabled() {
		return
	}

	ts := c.Timestamp
	if ts == "" {
		ts = now()
	}
	entry := ToolOutputEntry{
		LogEntryBase:      LogEntryBase{RequestID: c.RequestID, EventType: c.EventType, SessionID: c.SessionID, Timestamp: ts},
		IsMainAgent:       c.IsMainAgent,
		ProviderModel:     c.ProviderModel,
		ToolName:          c.ToolName,
		ShadowID:          c.ShadowID,
		StepID:            c.StepID,
		OriginalTokens:    c.OriginalTokens,
		CompressedTokens:  c.CompressedTokens,
		CompressionRatio:  c.CompressionRatio,
		CacheHit:          c.CacheHit,
		IsLastTool:        c.IsLastTool,
		Status:            c.Status,
		MinThreshold:      c.MinThreshold,
		MaxThreshold:      c.MaxThreshold,
		CompressionModel:  c.CompressionModel,
		Query:             c.Query,
		QueryAgnostic:     c.QueryAgnostic,
		OriginalContent:   c.OriginalContent,
		CompressedContent: c.CompressedContent,
	}

	t.muCompression.Lock()
	defer t.muCompression.Unlock()

	if t.compressionLogFile == nil {
		return
	}
	if err := writeJSONL(t.compressionLogFile, entry); err != nil {
		log.Error().Err(err).Str("path", t.compressionLogPath).Msg("telemetry: failed to write compression event")
	} else {
		t.compressionCount++
	}
}

// writeToolDiscovery is the single centralized write point for all tool_discovery.jsonl entries.
// Caller must not hold t.mu. Sets t.toolDiscoveryCount and handles errors.
func (t *Tracker) writeToolDiscovery(entry any) {
	t.muToolDiscovery.Lock()
	defer t.muToolDiscovery.Unlock()
	if t.toolDiscoveryLogFile == nil {
		return
	}
	if err := writeJSONL(t.toolDiscoveryLogFile, entry); err != nil {
		log.Error().Err(err).Str("path", t.toolDiscoveryLogPath).Msg("telemetry: failed to write tool discovery event")
	} else {
		t.toolDiscoveryCount++
	}
}

// LogLazyLoading logs a lazy-loading tool-filtering event to tool_discovery.jsonl.
// Converts the internal CompressionComparison to a typed LazyLoadingEntry before writing.
func (t *Tracker) LogLazyLoading(c CompressionComparison) {
	// Stats are independent of JSONL file config — update always.
	t.statsTracker.RecordLazyLoading(c.ToolCount, c.StubCount, c.PhantomCount, c.OriginalTokens, c.CompressedTokens, c.CacheHit)

	if !t.ToolDiscoveryLogEnabled() {
		return
	}
	ts := c.Timestamp
	if ts == "" {
		ts = now()
	}
	t.writeToolDiscovery(LazyLoadingEntry{
		LogEntryBase:     LogEntryBase{RequestID: c.RequestID, EventType: c.EventType, SessionID: c.SessionID, Timestamp: ts},
		IsMainAgent:      c.IsMainAgent,
		OriginalTokens:   c.OriginalTokens,
		CompressedTokens: c.CompressedTokens,
		CompressionRatio: c.CompressionRatio,
		CacheHit:         c.CacheHit,
		Status:           c.Status,
		ProviderModel:    c.ProviderModel,
		CompressionModel: c.CompressionModel,
		ToolCount:        c.ToolCount,
		StubCount:        c.StubCount,
		PhantomCount:     c.PhantomCount,
	})
}

// TaskOutputLogEnabled returns true if task output logging is enabled.
func (t *Tracker) TaskOutputLogEnabled() bool {
	return t.config.Enabled && t.taskOutputLogPath != ""
}

// LogTaskOutputComparison logs a task-output event to task_output_compression.jsonl.
// Converts the internal CompressionComparison to a typed TaskOutputEntry before writing.
func (t *Tracker) LogTaskOutputComparison(c CompressionComparison) {
	if !t.TaskOutputLogEnabled() {
		return
	}
	ts := c.Timestamp
	if ts == "" {
		ts = now()
	}
	entry := TaskOutputEntry{
		LogEntryBase:      LogEntryBase{RequestID: c.RequestID, EventType: c.EventType, SessionID: c.SessionID, Timestamp: ts},
		IsMainAgent:       c.IsMainAgent,
		ProviderModel:     c.ProviderModel,
		ToolName:          c.ToolName,
		OriginalTokens:    c.OriginalTokens,
		CompressedTokens:  c.CompressedTokens,
		CompressionRatio:  c.CompressionRatio,
		Status:            c.Status,
		CompressionModel:  c.CompressionModel,
		OriginalContent:   c.OriginalContent,
		CompressedContent: c.CompressedContent,
	}

	t.muTaskOutput.Lock()
	defer t.muTaskOutput.Unlock()
	if t.taskOutputLogFile == nil {
		return
	}
	if err := writeJSONL(t.taskOutputLogFile, entry); err != nil {
		log.Error().Err(err).Str("path", t.taskOutputLogPath).Msg("telemetry: failed to write task output event")
	} else {
		t.taskOutputCount++
	}
}

// LogToolSearch logs a gateway_search_tools call to tool_discovery.jsonl.
// entry.Timestamp is set to the current time if empty.
func (t *Tracker) LogToolSearch(entry ToolSearchResult) {
	// Stats are independent of JSONL file config — update always.
	// Use end-to-end token counts when available, fall back to single-stage.
	origTokens := entry.EndToEndOriginalTokens
	if origTokens == 0 {
		origTokens = entry.OriginalTokens
	}
	compTokens := entry.Stage2CompressedTokens
	if compTokens == 0 {
		compTokens = entry.CompressedTokens
	}
	t.statsTracker.RecordToolSearch(origTokens, compTokens)

	if !t.ToolDiscoveryLogEnabled() {
		return
	}
	if entry.Timestamp == "" {
		entry.Timestamp = now()
	}
	t.writeToolDiscovery(entry)
}

// WriteSessionToolsCatalog writes a human-readable session_tools.json file.
//
// First call for a session: creates the file with all tools.
// Subsequent calls: only tools not yet in the file are appended, then the file
// is rewritten (so it stays valid JSON). This handles subagents that introduce
// new tools mid-session without re-logging tools already present.
//
// The file is pretty-printed with 4-space indentation for human readability.
func (t *Tracker) WriteSessionToolsCatalog(sessionID string, tools []SessionToolEntry) {
	if t.sessionToolsPath == "" || sessionID == "" || len(tools) == 0 {
		return
	}

	t.muSessionTools.Lock()
	defer t.muSessionTools.Unlock()

	seen, ok := t.seenSessionTools[sessionID]
	if !ok {
		seen = make(map[string]bool)
		t.seenSessionTools[sessionID] = seen
	}

	var newTools []SessionToolEntry
	for _, tool := range tools {
		if !seen[tool.ToolName] {
			seen[tool.ToolName] = true
			newTools = append(newTools, tool)
		}
	}
	if len(newTools) == 0 {
		return
	}

	// Read existing catalog (if any) so we can append only the new tools.
	var catalog SessionToolsCatalogFile
	if data, err := os.ReadFile(t.sessionToolsPath); err == nil {
		_ = json.Unmarshal(data, &catalog)
	}

	catalog.SessionID = sessionID
	catalog.UpdatedAt = now()
	catalog.Tools = append(catalog.Tools, newTools...)

	data, err := json.MarshalIndent(catalog, "", "    ")
	if err != nil {
		log.Error().Err(err).Msg("session_tools: marshal failed")
		return
	}
	if err := os.WriteFile(t.sessionToolsPath, data, 0600); err != nil {
		log.Error().Err(err).Str("path", t.sessionToolsPath).Msg("session_tools: write failed")
	}
}

// SetStatsSession sets the session ID on the live stats tracker.
// Should be called once when a new session starts.
func (t *Tracker) SetStatsSession(sessionID string) {
	t.statsTracker.SetSession(sessionID)
}

// RecordPreemptiveStats records original and summarized token counts for one
// preemptive summarization event. Called from handler.go alongside SavingsTracker.
func (t *Tracker) RecordPreemptiveStats(origTokens, summarizedTokens int) {
	t.statsTracker.RecordPreemptive(origTokens, summarizedTokens)
}

// Close syncs and closes all open file handles.
func (t *Tracker) Close() error {
	// Acquire all per-file locks in deterministic order to avoid deadlock.
	t.muRequest.Lock()
	defer t.muRequest.Unlock()
	t.muCompression.Lock()
	defer t.muCompression.Unlock()
	t.muToolDiscovery.Lock()
	defer t.muToolDiscovery.Unlock()
	t.muTaskOutput.Lock()
	defer t.muTaskOutput.Unlock()
	t.muSessionTools.Lock()
	defer t.muSessionTools.Unlock()

	if t.requestLogPath != "" && t.requestCount > 0 {
		log.Info().
			Str("path", t.requestLogPath).
			Int("events", t.requestCount).
			Msg("telemetry: session complete")
	}

	t.statsTracker.Stop()
	t.expandCallsLogger.Close()

	for _, f := range []*os.File{t.requestLogFile, t.compressionLogFile, t.toolDiscoveryLogFile, t.taskOutputLogFile} {
		if f != nil {
			_ = f.Sync()
			_ = f.Close()
		}
	}
	t.requestLogFile = nil
	t.compressionLogFile = nil
	t.toolDiscoveryLogFile = nil
	t.taskOutputLogFile = nil

	return nil
}

// LogExpandContextCall appends an expand_context invocation to expand_context_calls.jsonl.
// Only called when the LLM actually invokes expand_context — never for every compression.
func (t *Tracker) LogExpandContextCall(entry ExpandContextCallEntry) {
	t.expandCallsLogger.Log(entry)
}

// ExpandCallsLogger returns the logger for expand_context_calls.jsonl.
// Returns nil if the feature is disabled. Used to wire ExpandContextHandler.
func (t *Tracker) ExpandCallsLogger() *ExpandCallsLogger {
	if t == nil {
		return nil
	}
	return t.expandCallsLogger
}

// HELPERS FOR VERBOSE PAYLOADS

// SanitizeHeaders removes sensitive headers and returns a safe copy.
func SanitizeHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}

	sanitized := make(map[string]string)
	sensitiveHeaders := map[string]bool{
		"authorization":    true,
		"x-api-key":        true,
		"api-key":          true,
		"x-auth-token":     true,
		"cookie":           true,
		"set-cookie":       true,
		"x-amzn-requestid": false, // Safe
		"cf-ray":           false, // Safe
		"x-request-id":     false, // Safe
		"request-id":       false, // Safe,
	}

	for k, v := range headers {
		lowerK := strings.ToLower(k)
		if sensitiveHeaders[lowerK] {
			// Mask sensitive headers
			if len(v) > 4 {
				sanitized[k] = v[:4] + "..." // Show first 4 chars
			} else {
				sanitized[k] = "***"
			}
		} else {
			sanitized[k] = v
		}
	}

	return sanitized
}

// MaskAuthHeader masks an authorization header value while preserving type info.
func MaskAuthHeader(authHeader string) string {
	if authHeader == "" {
		return ""
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) == 2 {
		authType := parts[0]  // "Bearer", "sk-", etc.
		authValue := parts[1] // actual token
		if len(authValue) > 4 {
			return authType + " " + authValue[:4] + "..."
		}
		return authType + " ***"
	}

	// Mask the whole thing
	if len(authHeader) > 4 {
		return authHeader[:4] + "..."
	}
	return "***"
}

// PreviewBody extracts first N chars of a body string for logging.
func PreviewBody(body string, maxChars int) string {
	if len(body) > maxChars {
		return body[:maxChars] + "...[truncated]"
	}
	return body
}
