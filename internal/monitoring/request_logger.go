// Package monitoring - request_logger.go logs HTTP request lifecycle.
package monitoring

import (
	"net/http"
	"time"
)

// RequestLogger logs HTTP request lifecycle events.
type RequestLogger struct {
	logger *Logger
}

// NewRequestLogger creates a new request logger.
func NewRequestLogger(logger *Logger) *RequestLogger {
	return &RequestLogger{logger: logger}
}

// RequestInfo contains incoming request information.
type RequestInfo struct {
	RequestID  string
	Method     string
	Path       string
	RemoteAddr string
	BodySize   int
	StartTime  time.Time
}

// NewRequestInfo creates RequestInfo from an HTTP request.
func NewRequestInfo(r *http.Request, requestID string, bodySize int) *RequestInfo {
	return &RequestInfo{
		RequestID:  requestID,
		Method:     r.Method,
		Path:       r.URL.Path,
		RemoteAddr: r.RemoteAddr,
		BodySize:   bodySize,
		StartTime:  time.Now(),
	}
}

// LogIncoming logs an incoming request.
func (rl *RequestLogger) LogIncoming(info *RequestInfo) {
	rl.logger.Debug().
		Str("request_id", info.RequestID).
		Str("method", info.Method).
		Str("path", info.Path).
		Int("body_size", info.BodySize).
		Msg("incoming")
}

// OutgoingRequestInfo contains outgoing request information.
type OutgoingRequestInfo struct {
	RequestID  string
	Provider   string
	TargetURL  string
	Method     string
	BodySize   int
	Compressed bool
}

// LogOutgoing logs an outgoing request.
func (rl *RequestLogger) LogOutgoing(info *OutgoingRequestInfo) {
	event := rl.logger.Debug().
		Str("request_id", info.RequestID).
		Str("provider", info.Provider).
		Str("method", info.Method).
		Str("target_url", info.TargetURL).
		Int("body_size", info.BodySize)
	if info.Compressed {
		event = event.Bool("compressed", true)
	}
	event.Msg("outgoing")
}

// ResponseInfo contains response information.
type ResponseInfo struct {
	RequestID  string
	StatusCode int
	Latency    time.Duration
}

// LogResponse logs a response.
func (rl *RequestLogger) LogResponse(info *ResponseInfo) {
	rl.logger.Debug().
		Str("request_id", info.RequestID).
		Int("status", info.StatusCode).
		Dur("latency", info.Latency).
		Msg("response")
}

// PipelineStageInfo contains pipeline stage information.
type PipelineStageInfo struct {
	RequestID string
	Stage     string
	Pipe      string
}

// LogPipelineStage logs a pipeline stage.
func (rl *RequestLogger) LogPipelineStage(info *PipelineStageInfo) {
	rl.logger.Debug().
		Str("request_id", info.RequestID).
		Str("stage", info.Stage).
		Str("pipe", info.Pipe).
		Msg("pipeline")
}

// CompressionInfo contains compression operation information.
type CompressionInfo struct {
	RequestID        string
	ToolName         string
	ToolCallID       string
	ShadowID         string
	OriginalTokens   int     // Token count before compression
	CompressedTokens int     // Token count after compression
	CompressionRatio float64 // Removed fraction: 1 - compressed/original (0.9 = 90% removed; higher = more aggressive)
	CacheHit         bool
	IsLastTool       bool
	MappingStatus    string
	Duration         time.Duration
}

// LogCompression logs a compression operation.
func (rl *RequestLogger) LogCompression(info *CompressionInfo) {
	rl.logger.Debug().
		Str("request_id", info.RequestID).
		Str("tool", info.ToolName).
		Int("original_tokens", info.OriginalTokens).
		Int("compressed_tokens", info.CompressedTokens).
		Float64("ratio", info.CompressionRatio).
		Bool("cache_hit", info.CacheHit).
		Msg("compression")
}

// ExpandContextInfo contains expand_context usage information.
type ExpandContextInfo struct {
	RequestID     string
	ShadowIDs     []string
	CallsFound    int
	CallsNotFound int
	TotalLoops    int
}

// LogExpandContext logs when LLM uses expand_context to request full content.
func (rl *RequestLogger) LogExpandContext(info *ExpandContextInfo) {
	rl.logger.Info().
		Str("request_id", info.RequestID).
		Int("calls_found", info.CallsFound).
		Int("calls_not_found", info.CallsNotFound).
		Int("total_loops", info.TotalLoops).
		Strs("shadow_ids", info.ShadowIDs).
		Msg("expand_context")
}
