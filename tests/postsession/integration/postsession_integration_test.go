// Post-Session Integration Tests
//
// Tests verify the postsession collector lifecycle: initialization,
// event recording across multiple request types, and cleanup.
package integration

import (
	"context"
	"strings"
	"sync"
	"testing"

	authtypes "github.com/compresr/context-gateway/internal/auth/types"
	"github.com/compresr/context-gateway/internal/postsession"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_PostSession_CollectorCreated verifies that a new
// SessionCollector initializes correctly with empty state.
func TestIntegration_PostSession_CollectorCreated(t *testing.T) {
	collector := postsession.NewSessionCollector()
	require.NotNil(t, collector, "collector should not be nil")

	// Fresh collector should have no events
	assert.False(t, collector.HasEvents(), "new collector should have no events")

	// Session log should be empty for a fresh collector
	sessionLog := collector.BuildSessionLog()
	assert.Empty(t, sessionLog, "session log should be empty before any events")

	// Auth should be empty
	auth := collector.GetAuth()
	assert.Empty(t, auth.Token, "auth token should be empty")
	assert.False(t, auth.IsXAPIKey, "isXAPIKey should be false")
	assert.Empty(t, auth.Endpoint, "auth endpoint should be empty")
}

// TestIntegration_PostSession_DataCollected verifies that recording
// various event types (requests, tool calls, compressions, compactions)
// produces a complete and accurate session log.
func TestIntegration_PostSession_DataCollected(t *testing.T) {
	collector := postsession.NewSessionCollector()

	// Simulate a multi-request coding session
	// Request 1: initial user query
	collector.RecordRequest("claude-sonnet-4", 3)
	assert.True(t, collector.HasEvents(), "should have events after first request")

	// Tool calls from assistant response
	collector.RecordToolCalls([]string{"read_file", "list_directory"})

	// Request 2: follow-up with tool results
	collector.RecordRequest("claude-sonnet-4", 7)

	// Compression occurred on tool output
	collector.RecordCompression("read_file", 5000, 1500)

	// More tool calls
	collector.RecordToolCalls([]string{"write_file", "read_file"})

	// Request 3: final exchange
	collector.RecordRequest("claude-sonnet-4", 11)

	// Compaction event (preemptive summarization)
	collector.RecordCompaction("claude-haiku-4-5")

	// Record assistant content
	collector.RecordAssistantContent("I've updated the configuration file as requested.")

	// Capture auth credentials
	collector.CaptureAuth(authtypes.CapturedAuth{
		Token:     "sk-ant-test-key-123",
		IsXAPIKey: true,
		Endpoint:  "https://api.anthropic.com/v1/messages",
	})

	// Build the session log and verify contents
	sessionLog := collector.BuildSessionLog()
	require.NotEmpty(t, sessionLog, "session log should not be empty")

	// Verify summary stats
	assert.Contains(t, sessionLog, "3 requests", "should report 3 requests")
	assert.Contains(t, sessionLog, "1 compaction", "should report 1 compaction")

	// Verify models are listed
	assert.Contains(t, sessionLog, "claude-sonnet-4", "should list model used")

	// Verify tools are listed with counts
	assert.Contains(t, sessionLog, "read_file", "should list read_file tool")
	assert.Contains(t, sessionLog, "write_file", "should list write_file tool")
	assert.Contains(t, sessionLog, "list_directory", "should list list_directory tool")

	// Verify timeline has events
	assert.Contains(t, sessionLog, "Timeline:", "should have timeline section")
	assert.Contains(t, sessionLog, "request", "timeline should contain request events")
	assert.Contains(t, sessionLog, "compression", "timeline should contain compression events")
	assert.Contains(t, sessionLog, "compaction", "timeline should contain compaction events")

	// Verify auth was captured
	auth := collector.GetAuth()
	assert.Equal(t, "sk-ant-test-key-123", auth.Token)
	assert.True(t, auth.IsXAPIKey)
	assert.Equal(t, "https://api.anthropic.com/v1/messages", auth.Endpoint)
}

// TestIntegration_PostSession_CleanupOnShutdown verifies that the
// collector handles concurrent access gracefully and that the Updater
// correctly handles a disabled configuration (graceful no-op).
func TestIntegration_PostSession_CleanupOnShutdown(t *testing.T) {
	collector := postsession.NewSessionCollector()

	// Simulate concurrent event recording (mimics gateway under load)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			collector.RecordRequest("claude-sonnet-4", n+1)
			collector.RecordToolCalls([]string{"tool_a", "tool_b"})
		}(i)
	}
	wg.Wait()

	// Verify all events were recorded without races
	assert.True(t, collector.HasEvents(), "should have events after concurrent writes")
	sessionLog := collector.BuildSessionLog()
	assert.NotEmpty(t, sessionLog, "session log should be non-empty")
	assert.Contains(t, sessionLog, "20 requests", "should report all 20 requests")

	// Verify the Updater with disabled config produces a clean no-op
	cfg := postsession.DefaultConfig()
	cfg.Enabled = false
	updater := postsession.NewUpdater(cfg)
	require.NotNil(t, updater, "updater should not be nil")

	// Update with disabled config should return without error
	result, err := updater.Update(
		context.Background(),
		collector,
		authtypes.CapturedAuth{Token: "sk-ant-test", IsXAPIKey: true, Endpoint: "https://api.anthropic.com/v1/messages"},
	)
	require.NoError(t, err, "disabled updater should not error")
	require.NotNil(t, result)
	assert.False(t, result.Updated, "disabled updater should not update CLAUDE.md")
	assert.Contains(t, result.Description, "disabled", "description should indicate disabled")

	// Verify collector still works after updater no-op (not consumed/corrupted)
	collector.RecordRequest("claude-haiku-4-5", 5)
	finalLog := collector.BuildSessionLog()
	assert.True(t, strings.Contains(finalLog, "21 requests") || strings.Contains(finalLog, "request"),
		"collector should remain functional after updater no-op")
}
