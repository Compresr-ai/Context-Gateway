// Session event collector for post-session CLAUDE.md updates.
//
// The collector gathers lightweight events during proxy operation:
// models used, tool calls observed, message counts, etc.
// This provides enough context for the LLM to understand what happened
// without storing full request/response bodies.
package postsession

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// SessionEvent represents a single high-level event observed during the session.
type SessionEvent struct {
	Timestamp time.Time
	Type      string // "request", "tool_call", "compression", "compaction"
	Summary   string // Human-readable one-liner
}

// SessionCollector accumulates session events in a goroutine-safe buffer.
type SessionCollector struct {
	mu     sync.Mutex
	events []SessionEvent

	// Dedup tracking
	modelsSeen map[string]bool
	toolsSeen  map[string]int // tool name -> call count

	// Counters
	requestCount    int
	compactionCount int

	// Captured auth for post-session LLM call
	authToken     string
	authIsXAPIKey bool
	authEndpoint  string
}

// NewSessionCollector creates a new collector.
func NewSessionCollector() *SessionCollector {
	return &SessionCollector{
		modelsSeen: make(map[string]bool),
		toolsSeen:  make(map[string]int),
	}
}

// RecordRequest records that a request was processed with the given model.
func (c *SessionCollector) RecordRequest(model string, messageCount int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.requestCount++
	if model != "" {
		c.modelsSeen[model] = true
	}

	// Only log periodically to keep the event buffer small
	if c.requestCount == 1 || c.requestCount%10 == 0 {
		c.events = append(c.events, SessionEvent{
			Timestamp: time.Now(),
			Type:      "request",
			Summary:   fmt.Sprintf("Request #%d: model=%s, messages=%d", c.requestCount, model, messageCount),
		})
	}
}

// RecordToolCalls records tool calls observed in an LLM response.
func (c *SessionCollector) RecordToolCalls(toolNames []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, name := range toolNames {
		c.toolsSeen[name]++
	}
}

// RecordCompression records a compression event.
func (c *SessionCollector) RecordCompression(toolName string, originalBytes, compressedBytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ratio := float64(compressedBytes) / float64(max(originalBytes, 1))
	c.events = append(c.events, SessionEvent{
		Timestamp: time.Now(),
		Type:      "compression",
		Summary:   fmt.Sprintf("Compressed %s: %d→%d bytes (%.0f%%)", toolName, originalBytes, compressedBytes, ratio*100),
	})
}

// RecordCompaction records a preemptive summarization/compaction event.
func (c *SessionCollector) RecordCompaction(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.compactionCount++
	c.events = append(c.events, SessionEvent{
		Timestamp: time.Now(),
		Type:      "compaction",
		Summary:   fmt.Sprintf("History compaction #%d (model=%s)", c.compactionCount, model),
	})
}

// RecordAssistantContent records a snippet of the assistant's response.
// Content is truncated to keep the buffer lightweight.
func (c *SessionCollector) RecordAssistantContent(content string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Truncate to keep buffer small
	if len(content) > 500 {
		content = content[:500] + "..."
	}

	c.events = append(c.events, SessionEvent{
		Timestamp: time.Now(),
		Type:      "assistant",
		Summary:   content,
	})
}

// BuildSessionLog returns a formatted string summarizing the session.
func (c *SessionCollector) BuildSessionLog() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.requestCount == 0 {
		return ""
	}

	var sb strings.Builder

	// Summary stats
	fmt.Fprintf(&sb, "Session: %d requests, %d compactions\n", c.requestCount, c.compactionCount)

	// Models used
	if len(c.modelsSeen) > 0 {
		models := make([]string, 0, len(c.modelsSeen))
		for m := range c.modelsSeen {
			models = append(models, m)
		}
		fmt.Fprintf(&sb, "Models: %s\n", strings.Join(models, ", "))
	}

	// Tools used (top tools by call count)
	if len(c.toolsSeen) > 0 {
		sb.WriteString("Tools used: ")
		toolStrs := make([]string, 0, len(c.toolsSeen))
		for name, count := range c.toolsSeen {
			toolStrs = append(toolStrs, fmt.Sprintf("%s(%d)", name, count))
		}
		sb.WriteString(strings.Join(toolStrs, ", "))
		sb.WriteString("\n")
	}

	// Events timeline
	sb.WriteString("\nTimeline:\n")
	for _, e := range c.events {
		fmt.Fprintf(&sb, "  [%s] %s: %s\n", e.Timestamp.Format("15:04:05"), e.Type, e.Summary)
	}

	return sb.String()
}

// HasEvents returns true if any events were recorded.
func (c *SessionCollector) HasEvents() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requestCount > 0
}

// CaptureAuth stores auth credentials from incoming requests for use in post-session LLM calls.
func (c *SessionCollector) CaptureAuth(token string, isXAPIKey bool, endpoint string) {
	if token == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.authToken = token
	c.authIsXAPIKey = isXAPIKey
	if endpoint != "" {
		c.authEndpoint = endpoint
	}
}

// GetAuth returns the captured auth credentials.
func (c *SessionCollector) GetAuth() (token string, isXAPIKey bool, endpoint string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.authToken, c.authIsXAPIKey, c.authEndpoint
}
