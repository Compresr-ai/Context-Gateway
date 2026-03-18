// Expand context handler for tool output expansion.
package gateway

import (
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/compresr/context-gateway/internal/pipes"
	"github.com/compresr/context-gateway/internal/store"
	"github.com/compresr/context-gateway/internal/tokenizer"
)

// ExpandContextToolName is the name of the expand_context phantom tool.
const ExpandContextToolName = "expand_context"

// ExpandContextHandler implements PhantomToolHandler for expand_context.
type ExpandContextHandler struct {
	store            store.Store
	expandLog        *monitoring.ExpandLog
	expandCallsLog   *monitoring.ExpandCallsLogger          // writes expand_context_calls.jsonl
	compressionIndex map[string]pipes.ToolOutputCompression // shadow_id → compression metadata
	requestID        string
	sessionID        string
	mu               sync.Mutex      // Protects expandedIDs from concurrent access
	expandedIDs      map[string]bool // Track expanded IDs to prevent circular expansion
}

// NewExpandContextHandler creates a new expand context handler.
func NewExpandContextHandler(st store.Store) *ExpandContextHandler {
	return &ExpandContextHandler{
		store:       st,
		expandedIDs: make(map[string]bool),
	}
}

// WithExpandLog sets the expand log for recording expand_context calls.
// Holds mu to be consistent with the read paths that access these fields.
func (h *ExpandContextHandler) WithExpandLog(el *monitoring.ExpandLog, requestID, sessionID string) *ExpandContextHandler {
	h.mu.Lock()
	h.expandLog = el
	h.requestID = requestID
	h.sessionID = sessionID
	h.mu.Unlock()
	return h
}

// WithExpandCallsLog sets the JSONL logger and compression index for expand_context_calls.jsonl.
// compressions maps shadow_id → ToolOutputCompression for the current request.
func (h *ExpandContextHandler) WithExpandCallsLog(logger *monitoring.ExpandCallsLogger, compressions []pipes.ToolOutputCompression) *ExpandContextHandler {
	index := make(map[string]pipes.ToolOutputCompression, len(compressions))
	for _, c := range compressions {
		if c.ShadowID != "" {
			index[c.ShadowID] = c
		}
	}
	h.mu.Lock()
	h.expandCallsLog = logger
	h.compressionIndex = index
	h.mu.Unlock()
	return h
}

// ResetExpandedIDs resets the tracking of expanded IDs.
// Call this at the start of each request.
func (h *ExpandContextHandler) ResetExpandedIDs() {
	h.mu.Lock()
	h.expandedIDs = make(map[string]bool)
	h.mu.Unlock()
}

// Name returns the phantom tool name.
func (h *ExpandContextHandler) Name() string {
	return ExpandContextToolName
}

// HandleCalls processes expand_context calls and returns results.
// Supports both shadow IDs (whole content) and field refs (field-level expansion).
func (h *ExpandContextHandler) HandleCalls(calls []PhantomToolCall, adapter adapters.Adapter, requestBody []byte) *PhantomToolResult {
	result := &PhantomToolResult{}

	h.mu.Lock()

	// Filter already-expanded IDs
	filteredCalls := make([]PhantomToolCall, 0, len(calls))
	for _, call := range calls {
		refID, _ := call.Input["id"].(string)
		if h.expandedIDs[refID] {
			log.Warn().
				Str("ref_id", refID).
				Msg("expand_context: skipping already-expanded ID")
			continue
		}
		filteredCalls = append(filteredCalls, call)
	}

	if len(filteredCalls) == 0 {
		h.mu.Unlock()
		result.StopLoop = true
		return result
	}

	// Mark all filtered calls as expanded before releasing lock
	for _, call := range filteredCalls {
		refID, _ := call.Input["id"].(string)
		h.expandedIDs[refID] = true
	}
	h.mu.Unlock()

	// Build adapter-native ToolCall slice and content per call
	adapterCalls := make([]adapters.ToolCall, 0, len(filteredCalls))
	contentPerCall := make([]string, 0, len(filteredCalls))

	for _, call := range filteredCalls {
		refID, _ := call.Input["id"].(string)

		var resultText string
		var found bool
		var content string

		// Check if this is a field ref (field-level expansion) or shadow ID (whole content)
		if isFieldRef(refID) {
			// Field-level expansion: retrieve only the specific field value
			fieldRef, ok := h.store.GetFieldRef(refID)
			if ok {
				found = true
				content = fieldRef.Original
				resultText = content
				log.Debug().
					Str("field_ref", refID).
					Str("field", fieldRef.Field).
					Str("parent", fieldRef.ParentID).
					Int("content_len", len(content)).
					Msg("expand_context: retrieved field ref")
			} else {
				found = false
				resultText = fmt.Sprintf("[The full content for field reference '%s' is no longer available. The compressed summary is already present in your context — please continue working with that.]", refID)
				log.Warn().
					Str("field_ref", refID).
					Str("request_id", h.requestID).
					Msg("expand_context: field ref not found in store")
			}
		} else {
			// Shadow ID: retrieve whole content
			content, found = h.store.Get(refID)
			if found {
				resultText = content
				log.Debug().
					Str("shadow_id", refID).
					Int("content_len", len(content)).
					Msg("expand_context: retrieved content")
			} else {
				resultText = fmt.Sprintf("[The full content for shadow reference '%s' is no longer available (gateway was restarted between sessions). The compressed summary is already present in your context — please continue working with that.]", refID)
				log.Error().
					Str("shadow_id", refID).
					Str("request_id", h.requestID).
					Str("reason", "ttl_expired_or_missing").
					Msg("expand_context: shadow ID not found in store")
			}
		}
		h.recordExpandEntry(refID, found, content)

		adapterCalls = append(adapterCalls, adapters.ToolCall{
			ToolUseID: call.ToolUseID,
			ToolName:  call.ToolName,
			Input:     call.Input,
		})
		contentPerCall = append(contentPerCall, resultText)
	}

	// Delegate format-specific message construction to adapter
	result.ToolResults = adapter.BuildToolResultMessages(adapterCalls, contentPerCall, requestBody)
	return result
}

// isFieldRef checks if the ref ID is a field-level reference.
func isFieldRef(refID string) bool {
	return len(refID) > 6 && refID[:6] == "field_"
}

// recordExpandEntry logs an expand_context call to the in-memory expand log
// and, if configured, to expand_context_calls.jsonl with full content.
func (h *ExpandContextHandler) recordExpandEntry(shadowID string, found bool, content string) {
	now := time.Now()

	if h.expandLog != nil {
		preview := content
		if len(preview) > 100 {
			preview = preview[:100]
		}
		h.expandLog.Record(monitoring.ExpandLogEntry{
			Timestamp:      now,
			SessionID:      h.sessionID,
			RequestID:      h.requestID,
			ShadowID:       shadowID,
			Found:          found,
			ContentPreview: preview,
			ContentLength:  len(content),
			ContentTokens:  tokenizer.CountTokens(content),
		})
	}

	if h.expandCallsLog != nil {
		entry := monitoring.ExpandContextCallEntry{
			Timestamp:       now,
			SessionID:       h.sessionID,
			RequestID:       h.requestID,
			ShadowID:        shadowID,
			Found:           found,
			OriginalTokens:  tokenizer.CountTokens(content),
			OriginalContent: content,
		}
		if comp, ok := h.compressionIndex[shadowID]; ok {
			entry.ToolName = comp.ToolName
			entry.CompressedTokens = comp.CompressedTokens
			entry.CompressedContent = comp.CompressedContent
		}
		h.expandCallsLog.Log(entry)
	}
}
