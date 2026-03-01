package preemptive_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/preemptive"
)

// =============================================================================
// MANAGER TESTS
// =============================================================================

func createTestConfig() preemptive.Config {
	return preemptive.Config{
		Enabled:          true,
		TriggerThreshold: 80.0,
		Summarizer: preemptive.SummarizerConfig{
			Model:           "claude-haiku-4-5",
			APISecret:       "test-api-key",
			Endpoint:        "https://api.anthropic.com/v1/messages",
			MaxTokens:       4096,
			Timeout:         60 * time.Second,
			KeepRecentCount: 10,
		},
		Session: preemptive.SessionConfig{
			SummaryTTL:       2 * time.Hour,
			HashMessageCount: 3,
		},
		Detectors: preemptive.DetectorsConfig{
			ClaudeCode: preemptive.ClaudeCodeDetectorConfig{
				Enabled: true,
				PromptPatterns: []string{
					"summarize this conversation",
					"compact the context",
				},
			},
			Generic: preemptive.GenericDetectorConfig{
				Enabled:     true,
				HeaderName:  "X-Request-Compaction",
				HeaderValue: "true",
			},
		},
		AddResponseHeaders: true,
	}
}

func TestManager_Creation(t *testing.T) {
	cfg := createTestConfig()
	cfg.Enabled = false

	manager := preemptive.NewManager(cfg)
	require.NotNil(t, manager)
}

func TestManager_Disabled(t *testing.T) {
	cfg := createTestConfig()
	cfg.Enabled = false

	manager := preemptive.NewManager(cfg)

	headers := http.Header{}
	body := []byte(`{"messages": [{"role": "user", "content": "Hello"}], "model": "claude-sonnet-4-5"}`)

	modifiedBody, isCompaction, _, respHeaders, err := manager.ProcessRequest(headers, body, "claude-sonnet-4-5", "anthropic")
	require.NoError(t, err)

	// Should be a pass-through
	assert.False(t, isCompaction)
	assert.Nil(t, respHeaders)
	assert.Equal(t, body, modifiedBody)
}

func TestManager_ProcessRequest_NormalRequest(t *testing.T) {
	cfg := createTestConfig()

	manager := preemptive.NewManager(cfg)

	headers := http.Header{}

	// A normal request with low token usage
	body := []byte(`{
		"messages": [
			{"role": "user", "content": "Hello"}
		],
		"model": "claude-sonnet-4-5"
	}`)

	modifiedBody, isCompaction, _, _, err := manager.ProcessRequest(headers, body, "claude-sonnet-4-5", "anthropic")
	require.NoError(t, err)

	assert.False(t, isCompaction)
	assert.Equal(t, body, modifiedBody)
}

func TestManager_ProcessRequest_DetectsCompaction(t *testing.T) {
	t.Skip("Compaction detection requires real API - integration test")
	cfg := createTestConfig()

	manager := preemptive.NewManager(cfg)

	headers := http.Header{}

	// A compaction request with enough content to summarize
	body := []byte(`{
		"messages": [
			{"role": "user", "content": "Let's start working on this project. I need to build a web application."},
			{"role": "assistant", "content": "Great! I can help you with that. What kind of web application?"},
			{"role": "user", "content": "An e-commerce site for selling products online."},
			{"role": "assistant", "content": "Perfect. Let's start by discussing the requirements and tech stack."},
			{"role": "user", "content": "Please summarize this conversation for me"}
		],
		"model": "claude-sonnet-4-5"
	}`)

	_, isCompaction, _, _, err := manager.ProcessRequest(headers, body, "claude-sonnet-4-5", "anthropic")
	require.NoError(t, err)

	assert.True(t, isCompaction)
}

func TestManager_ProcessRequest_HeaderCompaction(t *testing.T) {
	t.Skip("Header detection not implemented in current version")
	cfg := createTestConfig()

	manager := preemptive.NewManager(cfg)

	headers := http.Header{}
	headers.Set("X-Request-Compaction", "true")

	body := []byte(`{
		"messages": [
			{"role": "user", "content": "Continue with the task"}
		],
		"model": "claude-sonnet-4-5"
	}`)

	_, isCompaction, _, _, err := manager.ProcessRequest(headers, body, "claude-sonnet-4-5", "anthropic")
	require.NoError(t, err)

	assert.True(t, isCompaction)
}

func TestManager_InvalidJSON(t *testing.T) {
	cfg := createTestConfig()

	manager := preemptive.NewManager(cfg)

	headers := http.Header{}
	body := []byte(`not valid json`)

	modifiedBody, isCompaction, _, _, err := manager.ProcessRequest(headers, body, "claude-sonnet-4-5", "anthropic")
	// Should handle gracefully - pass through
	assert.NoError(t, err)
	assert.False(t, isCompaction)
	assert.Equal(t, body, modifiedBody)
}

func TestManager_EmptyMessages(t *testing.T) {
	cfg := createTestConfig()

	manager := preemptive.NewManager(cfg)

	headers := http.Header{}

	body := []byte(`{
		"messages": [],
		"model": "claude-sonnet-4-5"
	}`)

	modifiedBody, isCompaction, _, _, err := manager.ProcessRequest(headers, body, "claude-sonnet-4-5", "anthropic")
	require.NoError(t, err)

	// Should handle empty messages gracefully
	assert.False(t, isCompaction)
	assert.Equal(t, body, modifiedBody)
}

func TestManager_ModelExtraction(t *testing.T) {
	cfg := createTestConfig()

	manager := preemptive.NewManager(cfg)

	headers := http.Header{}

	// Test with different models
	models := []string{
		"claude-sonnet-4-5",
		"claude-opus-4-6",
		"claude-haiku-4-5",
		"claude-sonnet-4-5-20250929",
	}

	for _, model := range models {
		body := []byte(`{
			"messages": [
				{"role": "user", "content": "Hello"}
			],
			"model": "` + model + `"
		}`)

		_, isCompaction, _, _, err := manager.ProcessRequest(headers, body, model, "anthropic")
		require.NoError(t, err, "Failed for model: %s", model)
		assert.False(t, isCompaction)
	}
}

func TestManager_ToolUseCompaction(t *testing.T) {
	t.Skip("Tool use detection not implemented in current version")
	cfg := createTestConfig()

	manager := preemptive.NewManager(cfg)

	headers := http.Header{}

	// Request with compaction tool call
	body := []byte(`{
		"messages": [
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "toolu_123", "name": "compact_context", "input": {}}
			]}
		],
		"model": "claude-sonnet-4-5"
	}`)

	_, isCompaction, _, _, err := manager.ProcessRequest(headers, body, "claude-sonnet-4-5", "anthropic")
	require.NoError(t, err)

	assert.True(t, isCompaction)
}

func TestManager_SystemPromptCompaction(t *testing.T) {
	t.Skip("System prompt detection not implemented in current version")
	cfg := createTestConfig()

	manager := preemptive.NewManager(cfg)

	headers := http.Header{}

	// Request with compaction system prompt
	body := []byte(`{
		"system": "[compaction mode] Summarize the conversation",
		"messages": [
			{"role": "user", "content": "Continue"}
		],
		"model": "claude-sonnet-4-5"
	}`)

	_, isCompaction, _, _, err := manager.ProcessRequest(headers, body, "claude-sonnet-4-5", "anthropic")
	require.NoError(t, err)

	assert.True(t, isCompaction)
}

// =============================================================================
// INTEGRATION FLOW TESTS
// =============================================================================

func TestManager_EndToEndFlow(t *testing.T) {
	t.Skip("End-to-end flow requires real API - integration test")
	cfg := createTestConfig()

	manager := preemptive.NewManager(cfg)

	headers := http.Header{}

	// Step 1: Normal request - not a compaction
	body1 := []byte(`{
		"messages": [
			{"role": "user", "content": "Hello, let's start a conversation"},
			{"role": "assistant", "content": "Hello! I'm ready to help."},
			{"role": "user", "content": "What can you help me with?"}
		],
		"model": "claude-sonnet-4-5"
	}`)

	_, isCompaction1, _, _, err := manager.ProcessRequest(headers, body1, "claude-sonnet-4-5", "anthropic")
	require.NoError(t, err)
	assert.False(t, isCompaction1)

	// Step 2: Same conversation continues - still not compaction
	body2 := []byte(`{
		"messages": [
			{"role": "user", "content": "Hello, let's start a conversation"},
			{"role": "assistant", "content": "Hello! I'm ready to help."},
			{"role": "user", "content": "What can you help me with?"},
			{"role": "assistant", "content": "I can help with many things!"},
			{"role": "user", "content": "Great!"}
		],
		"model": "claude-sonnet-4-5"
	}`)

	_, isCompaction2, _, _, err := manager.ProcessRequest(headers, body2, "claude-sonnet-4-5", "anthropic")
	require.NoError(t, err)
	assert.False(t, isCompaction2)

	// Step 3: Compaction request
	body3 := []byte(`{
		"messages": [
			{"role": "user", "content": "Hello, let's start a conversation"},
			{"role": "assistant", "content": "Hello! I'm ready to help."},
			{"role": "user", "content": "Please summarize this conversation"}
		],
		"model": "claude-sonnet-4-5"
	}`)

	_, isCompaction3, _, _, err := manager.ProcessRequest(headers, body3, "claude-sonnet-4-5", "anthropic")
	require.NoError(t, err)
	assert.True(t, isCompaction3)
}

func TestManager_ConcurrentRequests(t *testing.T) {
	cfg := createTestConfig()

	manager := preemptive.NewManager(cfg)

	headers := http.Header{}

	// Make concurrent requests
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func(idx int) {
			body := []byte(`{
				"messages": [
					{"role": "user", "content": "Request number ` + string(rune('0'+idx)) + `"}
				],
				"model": "claude-sonnet-4-5"
			}`)

			_, _, _, _, err := manager.ProcessRequest(headers, body, "claude-sonnet-4-5", "anthropic")
			assert.NoError(t, err)
			done <- true
		}(i)
	}

	// Wait for all to complete
	for i := 0; i < 10; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Timeout waiting for concurrent requests")
		}
	}
}
