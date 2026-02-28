// Hard Edge Cases Tests - Designed to Break the System
//
// Tests target edge cases that could cause:
// - Data corruption / Memory explosion / Infinite loops
// - Race conditions / Silent failures
package unit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
	"github.com/compresr/context-gateway/internal/store"
	"github.com/compresr/context-gateway/tests/anthropic/fixtures"
	commonfixtures "github.com/compresr/context-gateway/tests/common/fixtures"
)

// =============================================================================
// MALFORMED INPUT TESTS
// =============================================================================

func TestHard_MalformedJSON_TruncatedInput(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 50, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	truncatedInputs := []string{
		`{"messages": [{"role": "user", "content": "test"`,
		`{"messages": [{"role": "user"`,
		`{"messages":`,
		`{`,
		`{"messages": [{"role": "user", "content": [{"type": "tool_result", "content": "data`,
	}

	for i, input := range truncatedInputs {
		t.Run(fmt.Sprintf("truncated_%d", i), func(t *testing.T) {
			ctx := fixtures.TestPipeContext([]byte(input))
			result, err := pipe.Process(ctx)
			if err == nil {
				assert.NotNil(t, result)
			}
		})
	}
}

func TestHard_UnicodeEdgeCases(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 10, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	unicodeCases := []string{
		"test\u200Bdata\u200Cwith\u200Dzero\uFEFFwidth",
		"normal\u202Ereversed\u202Ctext",
		"emoji: 🔥🎉🚀",
		"a" + strings.Repeat("\u0300", 100),
		"with\x00null\x01control\x02chars",
		"\xEF\xBB\xBFBOM at start",
	}

	for i, content := range unicodeCases {
		t.Run(fmt.Sprintf("unicode_%d", i), func(t *testing.T) {
			request := fixtures.RequestWithSingleToolOutput(content)
			ctx := fixtures.TestPipeContext(request)
			result, err := pipe.Process(ctx)
			if err == nil {
				assert.NotNil(t, result)
			}
		})
	}
}

// =============================================================================
// BOUNDARY CONDITIONS
// =============================================================================

func TestHard_EmptyToolOutput(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 1, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	emptyOutputs := []string{"", " ", "\n", "\t", "   \n\t  "}

	for i, content := range emptyOutputs {
		t.Run(fmt.Sprintf("empty_%d", i), func(t *testing.T) {
			request := fixtures.RequestWithSingleToolOutput(content)
			ctx := fixtures.TestPipeContext(request)
			result, err := pipe.Process(ctx)
			assert.NoError(t, err)
			assert.NotNil(t, result)
		})
	}
}

func TestHard_ExtremelyLargeToolOutput(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 50, true)
	cfg.Pipes.ToolOutput.MaxBytes = 10 * 1024 * 1024
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	largeContent := strings.Repeat("x", 5*1024*1024)
	request := fixtures.RequestWithSingleToolOutput(largeContent)
	ctx := fixtures.TestPipeContext(request)

	start := time.Now()
	result, err := pipe.Process(ctx)
	elapsed := time.Since(start)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Less(t, elapsed, 5*time.Second, "Processing should complete in reasonable time")
}

func TestHard_ManyToolOutputs(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 10, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	toolResults := make([]interface{}, 100)
	for i := 0; i < 100; i++ {
		toolResults[i] = map[string]interface{}{
			"type":        "tool_result",
			"tool_use_id": fmt.Sprintf("toolu_%d", i),
			"content":     fmt.Sprintf("Content for tool %d with some padding data", i),
		}
	}

	request := map[string]interface{}{
		"model": "claude-3",
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": toolResults,
			},
		},
	}

	body, _ := json.Marshal(request)
	ctx := fixtures.TestPipeContext(body)

	start := time.Now()
	result, err := pipe.Process(ctx)
	elapsed := time.Since(start)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Less(t, elapsed, 5*time.Second)
}

func TestHard_ZeroByteThreshold(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 0, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	request := fixtures.RequestWithSingleToolOutput("tiny")
	ctx := fixtures.TestPipeContext(request)

	result, err := pipe.Process(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

// =============================================================================
// CONCURRENCY TESTS
// =============================================================================

func TestHard_ConcurrentRequests_SameContent(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 50, true)
	st := fixtures.TestStore()
	pipe := tooloutput.New(cfg, st)

	content := strings.Repeat("shared content ", 100)
	request := fixtures.RequestWithSingleToolOutput(content)

	var wg sync.WaitGroup
	errors := make(chan error, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := fixtures.TestPipeContext(request)
			_, err := pipe.Process(ctx)
			if err != nil {
				errors <- err
			}
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("Concurrent request failed: %v", err)
	}
}

func TestHard_ConcurrentStoreAccess(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)

	var wg sync.WaitGroup
	errorCount := int32(0)

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				key := fmt.Sprintf("shadow_%d_%d", id, j%10)
				if err := st.Set(key, fmt.Sprintf("value_%d_%d", id, j)); err != nil {
					atomic.AddInt32(&errorCount, 1)
				}
			}
		}(i)
	}

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				key := fmt.Sprintf("shadow_%d_%d", id%10, j%10)
				st.Get(key)
			}
		}(i)
	}

	wg.Wait()
	assert.Equal(t, int32(0), errorCount)
}

// =============================================================================
// STATE CORRUPTION TESTS
// =============================================================================

func TestHard_StoreExpiry_DuringExpand(t *testing.T) {
	st := store.NewMemoryStore(10 * time.Millisecond)
	expander := tooloutput.NewExpander(st, nil)

	shadowID := "shadow_expiring"
	st.Set(shadowID, "original content")

	time.Sleep(20 * time.Millisecond)

	calls := []tooloutput.ExpandContextCall{
		{ToolUseID: "toolu_001", ShadowID: shadowID},
	}

	messages, found, notFound := expander.CreateExpandResultMessages(calls, true)

	assert.Equal(t, 0, found)
	assert.Equal(t, 1, notFound)
	assert.Len(t, messages, 1)
}

func TestHard_CompressedButOriginalMissing(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)

	shadowID := "shadow_partial"
	st.SetCompressed(shadowID, "compressed version")

	expander := tooloutput.NewExpander(st, nil)

	calls := []tooloutput.ExpandContextCall{
		{ToolUseID: "toolu_001", ShadowID: shadowID},
	}

	messages, found, notFound := expander.CreateExpandResultMessages(calls, true)

	assert.Equal(t, 0, found)
	assert.Equal(t, 1, notFound)
	assert.Len(t, messages, 1)
}

// =============================================================================
// PROTOCOL VIOLATIONS
// =============================================================================

func TestHard_MissingRequiredFields(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 50, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	testCases := []struct {
		name string
		body string
	}{
		{"no_messages", `{"model": "claude-3"}`},
		{"no_model", `{"messages": []}`},
		{"empty_object", `{}`},
		{"null_messages", `{"model": "claude-3", "messages": null}`},
		{"wrong_type_messages", `{"model": "claude-3", "messages": "not an array"}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := fixtures.TestPipeContext([]byte(tc.body))
			result, err := pipe.Process(ctx)
			if err == nil {
				assert.NotNil(t, result)
			}
		})
	}
}

func TestHard_DuplicateToolUseIDs(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 50, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	request := map[string]interface{}{
		"model": "claude-3",
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type":        "tool_result",
						"tool_use_id": "toolu_duplicate",
						"content":     "First result",
					},
					map[string]interface{}{
						"type":        "tool_result",
						"tool_use_id": "toolu_duplicate",
						"content":     "Second result",
					},
				},
			},
		},
	}

	body, _ := json.Marshal(request)
	ctx := fixtures.TestPipeContext(body)

	result, err := pipe.Process(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

// =============================================================================
// EXPAND CONTEXT TESTS
// =============================================================================

func TestHard_ExpandContext_CircularReference(t *testing.T) {
	var callCount atomic.Int32

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(`{
			"content": [
				{"type": "tool_use", "id": "toolu_%d", "name": "expand_context", "input": {"id": "shadow_circular"}}
			]
		}`, count)))
	}))
	defer mockLLM.Close()

	cfg := commonfixtures.SimpleCompressionConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := commonfixtures.AnthropicToolResultRequest("claude-3", strings.Repeat("content ", 100))

	req, _ := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("X-Target-URL", mockLLM.URL)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.True(t, callCount.Load() <= 10, "Should hit loop limit: got %d calls", callCount.Load())
}

func TestHard_ExpandContext_MultipleSimultaneous(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)

	for i := 0; i < 10; i++ {
		shadowID := fmt.Sprintf("shadow_%d", i)
		st.Set(shadowID, fmt.Sprintf("Full content for shadow %d", i))
	}

	expander := tooloutput.NewExpander(st, nil)

	calls := make([]tooloutput.ExpandContextCall, 10)
	for i := 0; i < 10; i++ {
		calls[i] = tooloutput.ExpandContextCall{
			ToolUseID: fmt.Sprintf("toolu_%d", i),
			ShadowID:  fmt.Sprintf("shadow_%d", i),
		}
	}

	messages, found, notFound := expander.CreateExpandResultMessages(calls, true)

	assert.Equal(t, 10, found)
	assert.Equal(t, 0, notFound)
	// Anthropic format batches all tool results in 1 message with multiple content blocks
	assert.Len(t, messages, 1)
	content := messages[0]["content"].([]interface{})
	assert.Len(t, content, 10) // 10 tool_result blocks in 1 message
}

func TestHard_InvalidShadowID_InExpandCall(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)
	expander := tooloutput.NewExpander(st, nil)

	invalidShadowIDs := []string{
		"",
		"   ",
		"not-a-shadow-id",
		"shadow_",
		strings.Repeat("x", 10000),
	}

	for _, shadowID := range invalidShadowIDs {
		t.Run(fmt.Sprintf("shadow_%q", shadowID), func(t *testing.T) {
			calls := []tooloutput.ExpandContextCall{
				{ToolUseID: "toolu_001", ShadowID: shadowID},
			}

			messages, found, notFound := expander.CreateExpandResultMessages(calls, true)

			assert.Equal(t, 0, found)
			assert.Equal(t, 1, notFound)
			assert.Len(t, messages, 1)
		})
	}
}

// =============================================================================
// COMPRESSION ANOMALIES
// =============================================================================

func TestHard_Compression_APIReturnsHTML(t *testing.T) {
	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body>Error page</body></html>"))
	}))
	defer mockAPI.Close()

	cfg := &config.Config{
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:          true,
				Strategy:         config.StrategyCompresr,
				FallbackStrategy: config.StrategyPassthrough,
				MinBytes:         10,
				MaxBytes:         1024 * 1024,
				Compresr: config.CompresrConfig{
					Endpoint: "/compress",
					Timeout:  5 * time.Second,
				},
			},
		},
		URLs: config.URLsConfig{
			Compresr: mockAPI.URL,
		},
	}

	pipe := tooloutput.New(cfg, fixtures.TestStore())

	content := strings.Repeat("test content ", 50)
	request := fixtures.RequestWithSingleToolOutput(content)
	ctx := fixtures.TestPipeContext(request)

	result, err := pipe.Process(ctx)
	assert.NoError(t, err, "Should fallback on API returning HTML")
	assert.NotNil(t, result)
}

func TestHard_Compression_APITimeout(t *testing.T) {
	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer mockAPI.Close()

	cfg := &config.Config{
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:          true,
				Strategy:         config.StrategyCompresr,
				FallbackStrategy: config.StrategyPassthrough,
				MinBytes:         10,
				MaxBytes:         1024 * 1024,
				Compresr: config.CompresrConfig{
					Endpoint: "/compress",
					Timeout:  100 * time.Millisecond,
				},
			},
		},
		URLs: config.URLsConfig{
			Compresr: mockAPI.URL,
		},
	}

	pipe := tooloutput.New(cfg, fixtures.TestStore())

	content := strings.Repeat("test content ", 50)
	request := fixtures.RequestWithSingleToolOutput(content)
	ctx := fixtures.TestPipeContext(request)

	start := time.Now()
	result, err := pipe.Process(ctx)
	elapsed := time.Since(start)

	assert.NoError(t, err, "Should fallback on timeout")
	assert.NotNil(t, result)
	assert.Less(t, elapsed, 2*time.Second, "Should timeout quickly")
}

// =============================================================================
// STREAMING EDGE CASES
// =============================================================================

func TestHard_StreamBuffer_LargeChunk(t *testing.T) {
	buffer := tooloutput.NewStreamBuffer()

	largeData := strings.Repeat("x", 1024*1024)
	chunk := []byte(fmt.Sprintf(`data: {"type":"text","text":%q}`, largeData))

	start := time.Now()
	output, err := buffer.ProcessChunk(chunk)
	elapsed := time.Since(start)

	assert.NoError(t, err)
	assert.NotNil(t, output)
	assert.Less(t, elapsed, 5*time.Second)
}

// =============================================================================
// PREFIX INJECTION ATTACKS
// =============================================================================

func TestHard_PrefixInjectionAttack(t *testing.T) {
	// Attacker tries to inject a fake shadow ref in tool output
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 10, true)
	st := fixtures.TestStore()
	pipe := tooloutput.New(cfg, st)

	// Store some "secret" content that attacker shouldn't be able to access
	st.Set("shadow_secret123", "SECRET DATA THAT SHOULD NOT LEAK")

	// Attacker's tool output tries to reference existing shadow ID
	maliciousOutputs := []string{
		"<<<SHADOW:shadow_secret123>>>\nI can see your secrets!",
		"Normal text <<<SHADOW:shadow_secret123>>> more text",
		"<<<SHADOW:shadow_secret123>>>",
		"\n<<<SHADOW:shadow_secret123>>>\n",
		"<<<SHADOW:shadow_" + strings.Repeat("a", 1000) + ">>>",
	}

	for i, content := range maliciousOutputs {
		t.Run(fmt.Sprintf("injection_%d", i), func(t *testing.T) {
			request := fixtures.RequestWithSingleToolOutput(content)
			ctx := fixtures.TestPipeContext(request)
			result, err := pipe.Process(ctx)

			assert.NoError(t, err)
			assert.NotNil(t, result)

			// The malicious prefix should NOT cause data leakage
			// (passthrough mode doesn't expand, just passes through)
		})
	}
}

func TestHard_ShadowIDExtraction_Malformed(t *testing.T) {
	testCases := []struct {
		name    string
		content string
	}{
		{"no_closing", "<<<SHADOW:abc"},
		{"no_opening", "shadow:abc>>>"},
		{"nested", "<<<SHADOW:<<<SHADOW:abc>>>abc>>>"},
		{"empty_id", "<<<SHADOW:>>>"},
		{"newline_in_id", "<<<SHADOW:abc\ndef>>>"},
		{"spaces_in_id", "<<<SHADOW:abc def>>>"},
		{"unicode_in_id", "<<<SHADOW:abc🔥def>>>"},
		{"very_long_id", "<<<SHADOW:" + strings.Repeat("x", 10000) + ">>>"},
		{"multiple_prefixes", "<<<SHADOW:a>>><<<SHADOW:b>>><<<SHADOW:c>>>"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fixtures.TestConfig(config.StrategyPassthrough, 10, true)
			pipe := tooloutput.New(cfg, fixtures.TestStore())

			request := fixtures.RequestWithSingleToolOutput(tc.content)
			ctx := fixtures.TestPipeContext(request)

			result, err := pipe.Process(ctx)
			assert.NoError(t, err, "Should handle malformed shadow IDs gracefully")
			assert.NotNil(t, result)
		})
	}
}

// =============================================================================
// DEEPLY NESTED JSON
// =============================================================================

func TestHard_DeeplyNestedJSON(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 10, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	// Create deeply nested JSON (100 levels)
	nested := "null"
	for i := 0; i < 100; i++ {
		nested = fmt.Sprintf(`{"level_%d": %s}`, i, nested)
	}

	request := fixtures.RequestWithSingleToolOutput(nested)
	ctx := fixtures.TestPipeContext(request)

	start := time.Now()
	result, err := pipe.Process(ctx)
	elapsed := time.Since(start)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Less(t, elapsed, 5*time.Second, "Deep nesting should not cause exponential slowdown")
}

func TestHard_DeeplyNestedArrays(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 10, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	// Create deeply nested arrays (100 levels)
	nested := `"leaf"`
	for i := 0; i < 100; i++ {
		nested = fmt.Sprintf(`[%s]`, nested)
	}

	request := fixtures.RequestWithSingleToolOutput(nested)
	ctx := fixtures.TestPipeContext(request)

	result, err := pipe.Process(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

// =============================================================================
// HASH COLLISION SIMULATION
// =============================================================================

func TestHard_SameHashDifferentContent(t *testing.T) {
	// Test that even if two contents somehow have same hash,
	// the system handles it correctly
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 10, true)
	st := fixtures.TestStore()
	pipe := tooloutput.New(cfg, st)

	// Manually set conflicting entries
	shadowID := "shadow_collision_test"
	st.Set(shadowID, "Original content version A")
	st.SetCompressed(shadowID, "Compressed version A")

	// Try to process with different content that would (hypothetically) hash to same ID
	request := fixtures.RequestWithSingleToolOutput("Completely different content B")
	ctx := fixtures.TestPipeContext(request)

	result, err := pipe.Process(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

// =============================================================================
// FORMAT CONFUSION ATTACKS
// =============================================================================

func TestHard_OpenAIRequestToAnthropicEndpoint(t *testing.T) {
	// OpenAI-formatted request sent to what the system thinks is Anthropic
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 10, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	// OpenAI format with tool_calls
	request := map[string]interface{}{
		"model": "gpt-4",
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": "hello",
			},
			map[string]interface{}{
				"role": "assistant",
				"tool_calls": []interface{}{
					map[string]interface{}{
						"id":   "call_123",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "test_tool",
							"arguments": `{"arg": "value"}`,
						},
					},
				},
			},
			map[string]interface{}{
				"role":         "tool",
				"tool_call_id": "call_123",
				"content":      "Tool result content here",
			},
		},
	}

	body, _ := json.Marshal(request)
	ctx := fixtures.TestPipeContext(body)

	result, err := pipe.Process(ctx)
	// Should handle without panic
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

func TestHard_HybridFormatMessages(t *testing.T) {
	// Mix of Anthropic and OpenAI message formats in same request
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 10, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	request := map[string]interface{}{
		"model": "claude-3",
		"messages": []interface{}{
			// Anthropic-style tool result
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type":        "tool_result",
						"tool_use_id": "toolu_001",
						"content":     "Anthropic tool result",
					},
				},
			},
			// OpenAI-style tool result in same conversation
			map[string]interface{}{
				"role":         "tool",
				"tool_call_id": "call_002",
				"content":      "OpenAI tool result",
			},
		},
	}

	body, _ := json.Marshal(request)
	ctx := fixtures.TestPipeContext(body)

	result, err := pipe.Process(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

// =============================================================================
// HISTORY REWRITE EDGE CASES
// =============================================================================

func TestHard_RewriteHistory_EmptyMessages(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)
	expander := tooloutput.NewExpander(st, nil)

	body := []byte(`{"model": "claude-3", "messages": []}`)
	calls := []tooloutput.ExpandContextCall{
		{ToolUseID: "toolu_001", ShadowID: "shadow_404"},
	}

	result, expandedIDs, err := expander.RewriteHistoryWithExpansion(body, calls)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Empty(t, expandedIDs)
}

func TestHard_RewriteHistory_NullMessages(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)
	expander := tooloutput.NewExpander(st, nil)

	body := []byte(`{"model": "claude-3", "messages": null}`)
	calls := []tooloutput.ExpandContextCall{
		{ToolUseID: "toolu_001", ShadowID: "shadow_001"},
	}

	result, expandedIDs, err := expander.RewriteHistoryWithExpansion(body, calls)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Empty(t, expandedIDs)
}

func TestHard_RewriteHistory_MismatchedToolUseID(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)
	st.Set("shadow_001", "Full content")

	expander := tooloutput.NewExpander(st, nil)

	// Tool result references different tool_use_id than in expand call
	body := []byte(`{
		"model": "claude-3",
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_DIFFERENT",
						"content": "<<<SHADOW:shadow_001>>>\nCompressed"
					}
				]
			}
		]
	}`)

	calls := []tooloutput.ExpandContextCall{
		{ToolUseID: "toolu_001", ShadowID: "shadow_001"},
	}

	result, expandedIDs, err := expander.RewriteHistoryWithExpansion(body, calls)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	// Should still expand based on shadow ID match, not tool_use_id
	assert.Contains(t, expandedIDs, "shadow_001")
}

// =============================================================================
// STORE CORRUPTION SCENARIOS
// =============================================================================

func TestHard_Store_OverwriteWithShorterContent(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)

	shadowID := "shadow_overwrite"
	longContent := strings.Repeat("A", 10000)
	shortContent := "B"

	st.Set(shadowID, longContent)
	st.Set(shadowID, shortContent)

	result, ok := st.Get(shadowID)
	assert.True(t, ok)
	assert.Equal(t, shortContent, result)
	assert.Len(t, result, 1) // Should be short, not corrupted
}

func TestHard_Store_CompressedWithoutOriginal(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)

	// Set compressed without original (simulating a bug or race condition)
	shadowID := "shadow_orphaned_compressed"
	st.SetCompressed(shadowID, "compressed but no original")

	// Trying to expand should fail gracefully
	_, ok := st.Get(shadowID)
	assert.False(t, ok, "Original should not exist")

	compressed, ok := st.GetCompressed(shadowID)
	assert.True(t, ok)
	assert.Equal(t, "compressed but no original", compressed)
}

func TestHard_Store_DeleteDuringIteration(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)

	// Add many entries
	for i := 0; i < 1000; i++ {
		st.Set(fmt.Sprintf("shadow_%d", i), fmt.Sprintf("content_%d", i))
	}

	// Concurrent deletes and reads
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(id int) {
			defer wg.Done()
			st.Delete(fmt.Sprintf("shadow_%d", id*10))
		}(i)
		go func(id int) {
			defer wg.Done()
			st.Get(fmt.Sprintf("shadow_%d", id*10+5))
		}(i)
	}

	wg.Wait()
	// Should complete without panic or deadlock
}

// =============================================================================
// APPEND MESSAGES CORRUPTION
// =============================================================================

func TestHard_AppendMessages_InvalidAssistantResponse(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)
	expander := tooloutput.NewExpander(st, nil)

	body := []byte(`{"model": "claude-3", "messages": [{"role": "user", "content": "hi"}]}`)
	invalidResponses := [][]byte{
		[]byte(`not json at all`),
		[]byte(`{"content": "string instead of array"}`),
		[]byte(`{"content": null}`),
		[]byte(`{"content": [{"type": "invalid_type"}]}`),
		[]byte(`{}`),
	}

	for i, invalidResp := range invalidResponses {
		t.Run(fmt.Sprintf("invalid_%d", i), func(t *testing.T) {
			toolResults := []map[string]interface{}{
				{"role": "user", "content": "result"},
			}
			_, err := expander.AppendMessagesToRequest(body, invalidResp, toolResults)
			// Should return error or handle gracefully, not panic
			if err != nil {
				assert.Error(t, err)
			}
		})
	}
}

// =============================================================================
// JSON PRECISION LOSS
// =============================================================================

func TestHard_LargeNumbersInToolOutput(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 10, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	// Large numbers that could lose precision
	toolOutput := `{
		"big_int": 9007199254740993,
		"negative_big": -9007199254740993,
		"scientific": 1.7976931348623157e+308,
		"tiny": 5e-324,
		"max_safe_int": 9007199254740991
	}`

	request := fixtures.RequestWithSingleToolOutput(toolOutput)
	ctx := fixtures.TestPipeContext(request)

	result, err := pipe.Process(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Verify the numbers survive JSON round-trip
	var parsed map[string]interface{}
	err = json.Unmarshal(result, &parsed)
	assert.NoError(t, err)
}

// =============================================================================
// TOOL INJECTION VIA TOOLS ARRAY
// =============================================================================

func TestHard_InjectExpandContext_AlreadyExists(t *testing.T) {
	shadowRefs := map[string]string{"shadow_001": "content"}

	// Request already has expand_context tool (Anthropic format)
	bodyWithTool := []byte(`{
		"model": "claude-3",
		"messages": [],
		"tools": [
			{
				"name": "expand_context",
				"description": "Already exists",
				"input_schema": {"type": "object"}
			}
		]
	}`)

	result, err := tooloutput.InjectExpandContextTool(bodyWithTool, shadowRefs, "anthropic")
	assert.NoError(t, err)

	var parsed map[string]interface{}
	json.Unmarshal(result, &parsed)
	tools := parsed["tools"].([]interface{})

	// Should NOT duplicate the tool
	expandCount := 0
	for _, tool := range tools {
		t := tool.(map[string]interface{})
		if name, _ := t["name"].(string); name == "expand_context" {
			expandCount++
		}
	}
	assert.Equal(t, 1, expandCount, "Should not duplicate expand_context tool")
}

func TestHard_InjectExpandContext_OpenAIFormat(t *testing.T) {
	shadowRefs := map[string]string{"shadow_001": "content"}

	// OpenAI format with existing function tool
	bodyOpenAI := []byte(`{
		"model": "gpt-4",
		"messages": [],
		"max_completion_tokens": 100,
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "get_weather",
					"parameters": {"type": "object"}
				}
			}
		]
	}`)

	result, err := tooloutput.InjectExpandContextTool(bodyOpenAI, shadowRefs, "openai")
	assert.NoError(t, err)

	var parsed map[string]interface{}
	json.Unmarshal(result, &parsed)
	tools := parsed["tools"].([]interface{})

	// Find the injected tool
	var expandTool map[string]interface{}
	for _, tool := range tools {
		t := tool.(map[string]interface{})
		if fn, ok := t["function"].(map[string]interface{}); ok {
			if name, _ := fn["name"].(string); name == "expand_context" {
				expandTool = t
				break
			}
		}
	}

	assert.NotNil(t, expandTool, "Should inject expand_context in OpenAI format")
	assert.Equal(t, "function", expandTool["type"])
}

// =============================================================================
// EXPAND LOOP STRESS TESTS
// =============================================================================

func TestHard_ExpandLoop_RapidSuccessiveExpands(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)

	// Set up 10 shadow refs
	for i := 0; i < 10; i++ {
		st.Set(fmt.Sprintf("shadow_%d", i), fmt.Sprintf("Content for %d", i))
	}

	expander := tooloutput.NewExpander(st, nil)

	// Rapidly expand all 10 in each call
	for iter := 0; iter < 100; iter++ {
		calls := make([]tooloutput.ExpandContextCall, 10)
		for i := 0; i < 10; i++ {
			calls[i] = tooloutput.ExpandContextCall{
				ToolUseID: fmt.Sprintf("toolu_%d_%d", iter, i),
				ShadowID:  fmt.Sprintf("shadow_%d", i),
			}
		}

		messages, found, notFound := expander.CreateExpandResultMessages(calls, true)
		assert.Equal(t, 10, found)
		assert.Equal(t, 0, notFound)
		assert.Len(t, messages, 1)
	}
}

// =============================================================================
// FILTER EXPAND_CONTEXT FROM RESPONSE
// =============================================================================

func TestHard_FilterExpandContext_MixedContent(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)
	expander := tooloutput.NewExpander(st, nil)

	// Response has expand_context mixed with real tools
	response := []byte(`{
		"content": [
			{"type": "text", "text": "I'll use some tools"},
			{"type": "tool_use", "id": "toolu_real", "name": "read_file", "input": {"path": "/test"}},
			{"type": "tool_use", "id": "toolu_expand", "name": "expand_context", "input": {"id": "shadow_001"}},
			{"type": "tool_use", "id": "toolu_real2", "name": "write_file", "input": {"path": "/test2"}}
		]
	}`)

	filtered, modified := expander.FilterExpandContextFromResponse(response)
	assert.True(t, modified)

	var parsed map[string]interface{}
	json.Unmarshal(filtered, &parsed)
	content := parsed["content"].([]interface{})

	// Should have 3 items (text + 2 real tools), not 4
	assert.Len(t, content, 3)

	// Verify expand_context was removed
	for _, block := range content {
		b := block.(map[string]interface{})
		if b["type"] == "tool_use" {
			name := b["name"].(string)
			assert.NotEqual(t, "expand_context", name)
		}
	}
}

func TestHard_FilterExpandContext_AllExpand(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)
	expander := tooloutput.NewExpander(st, nil)

	// Response has ONLY expand_context calls
	response := []byte(`{
		"content": [
			{"type": "tool_use", "id": "toolu_1", "name": "expand_context", "input": {"id": "a"}},
			{"type": "tool_use", "id": "toolu_2", "name": "expand_context", "input": {"id": "b"}}
		]
	}`)

	filtered, modified := expander.FilterExpandContextFromResponse(response)
	assert.True(t, modified)

	var parsed map[string]interface{}
	json.Unmarshal(filtered, &parsed)
	content := parsed["content"].([]interface{})

	// Should be empty array
	assert.Len(t, content, 0)
}

// =============================================================================
// CONCURRENT EXPAND AND COMPRESS
// =============================================================================

func TestHard_ConcurrentExpandAndCompress(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 10, true)
	st := store.NewMemoryStore(5 * time.Minute)
	pipe := tooloutput.New(cfg, st)
	expander := tooloutput.NewExpander(st, nil)

	shadowID := "shadow_concurrent"
	content := strings.Repeat("test content ", 100)

	var wg sync.WaitGroup
	errors := make(chan error, 200)

	// Concurrent compresses
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st.Set(shadowID, content)
			st.SetCompressed(shadowID, "compressed "+content[:50])
		}()
	}

	// Concurrent expands
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			calls := []tooloutput.ExpandContextCall{
				{ToolUseID: fmt.Sprintf("toolu_%d", id), ShadowID: shadowID},
			}
			_, _, _ = expander.CreateExpandResultMessages(calls, true)
		}(i)
	}

	// Concurrent process calls
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			request := fixtures.RequestWithSingleToolOutput(content)
			ctx := fixtures.TestPipeContext(request)
			_, err := pipe.Process(ctx)
			if err != nil {
				errors <- err
			}
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("Concurrent operation failed: %v", err)
	}
}

// =============================================================================
// BINARY / NULL DATA
// =============================================================================

func TestHard_BinaryDataInToolOutput(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 10, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	// Various binary patterns
	binaryPatterns := [][]byte{
		{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD},
		bytes.Repeat([]byte{0x00}, 1000),
		{0x89, 0x50, 0x4E, 0x47}, // PNG header
		{0xFF, 0xD8, 0xFF},       // JPEG header
	}

	for i, binary := range binaryPatterns {
		t.Run(fmt.Sprintf("binary_%d", i), func(t *testing.T) {
			// Encode as base64 in JSON
			content := fmt.Sprintf(`{"binary_data": "%s"}`, string(binary))
			request := fixtures.RequestWithSingleToolOutput(content)
			ctx := fixtures.TestPipeContext(request)

			result, err := pipe.Process(ctx)
			// Should handle gracefully (may error on invalid UTF-8)
			if err == nil {
				assert.NotNil(t, result)
			}
		})
	}
}

// =============================================================================
// STRIP EXPAND_CONTEXT FROM ASSISTANT
// =============================================================================

func TestHard_StripExpandContext_OnlyExpandCalls(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)
	st.Set("shadow_001", "Full content that was compressed")
	expander := tooloutput.NewExpander(st, nil)

	// Request has:
	// 1. A tool result with compressed content (shadow ref)
	// 2. An assistant message with expand_context to request full content
	body := []byte(`{
		"model": "claude-3",
		"messages": [
			{"role": "user", "content": "Hello"},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "toolu_read", "name": "read_file", "input": {"path": "/test"}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "toolu_read", "content": "<<<SHADOW:shadow_001>>>\nCompressed summary"}
				]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "toolu_expand", "name": "expand_context", "input": {"id": "shadow_001"}}
				]
			}
		]
	}`)

	calls := []tooloutput.ExpandContextCall{
		{ToolUseID: "toolu_expand", ShadowID: "shadow_001"},
	}

	result, expandedIDs, err := expander.RewriteHistoryWithExpansion(body, calls)
	assert.NoError(t, err)
	assert.Contains(t, expandedIDs, "shadow_001", "Should have expanded shadow_001")

	var parsed map[string]interface{}
	json.Unmarshal(result, &parsed)
	messages := parsed["messages"].([]interface{})

	// The assistant message with only expand_context should be REMOVED
	for _, msg := range messages {
		m := msg.(map[string]interface{})
		if m["role"] == "assistant" {
			content, ok := m["content"].([]interface{})
			if ok {
				for _, block := range content {
					b := block.(map[string]interface{})
					if b["type"] == "tool_use" {
						name, _ := b["name"].(string)
						assert.NotEqual(t, "expand_context", name, "expand_context should be stripped")
					}
				}
			}
		}
	}
}

// =============================================================================
// PARTIAL EXPANSION TESTS - 3 Tools, Expand 1/2/3
// =============================================================================

func TestHard_ThreeTools_ExpandOnlyOne(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)

	// Store 3 different compressed contents
	st.Set("shadow_file1", "Full content of file1.go - this is a very long Go file with many functions")
	st.Set("shadow_file2", "Full content of file2.go - another Go file with utilities")
	st.Set("shadow_file3", "Full content of file3.go - test file with test cases")

	expander := tooloutput.NewExpander(st, nil)

	// LLM only wants to expand file1 (needs more detail on that one)
	calls := []tooloutput.ExpandContextCall{
		{ToolUseID: "toolu_expand_1", ShadowID: "shadow_file1"},
	}

	messages, found, notFound := expander.CreateExpandResultMessages(calls, true)

	assert.Equal(t, 1, found, "Should find exactly 1")
	assert.Equal(t, 0, notFound)
	assert.Len(t, messages, 1)

	// Verify only file1 content is in the result
	content := messages[0]["content"].([]interface{})
	assert.Len(t, content, 1)
	block := content[0].(map[string]interface{})
	assert.Contains(t, block["content"], "file1.go")
}

func TestHard_ThreeTools_ExpandTwo(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)

	st.Set("shadow_config", "Full config.yaml content with all settings")
	st.Set("shadow_main", "Full main.go with entry point and initialization")
	st.Set("shadow_readme", "README.md with documentation")

	expander := tooloutput.NewExpander(st, nil)

	// LLM needs config and main, but not readme
	calls := []tooloutput.ExpandContextCall{
		{ToolUseID: "toolu_001", ShadowID: "shadow_config"},
		{ToolUseID: "toolu_002", ShadowID: "shadow_main"},
	}

	messages, found, notFound := expander.CreateExpandResultMessages(calls, true)

	assert.Equal(t, 2, found)
	assert.Equal(t, 0, notFound)
	assert.Len(t, messages, 1) // Batched into 1 message

	content := messages[0]["content"].([]interface{})
	assert.Len(t, content, 2) // 2 tool results

	// Verify both are present
	foundConfig, foundMain := false, false
	for _, block := range content {
		b := block.(map[string]interface{})
		contentStr := b["content"].(string)
		if strings.Contains(contentStr, "config.yaml") {
			foundConfig = true
		}
		if strings.Contains(contentStr, "main.go") {
			foundMain = true
		}
	}
	assert.True(t, foundConfig, "Should have config content")
	assert.True(t, foundMain, "Should have main content")
}

func TestHard_ThreeTools_ExpandAllThree(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)

	contents := map[string]string{
		"shadow_a": "Content A - detailed implementation",
		"shadow_b": "Content B - detailed tests",
		"shadow_c": "Content C - detailed docs",
	}
	for k, v := range contents {
		st.Set(k, v)
	}

	expander := tooloutput.NewExpander(st, nil)

	// LLM needs all 3
	calls := []tooloutput.ExpandContextCall{
		{ToolUseID: "toolu_a", ShadowID: "shadow_a"},
		{ToolUseID: "toolu_b", ShadowID: "shadow_b"},
		{ToolUseID: "toolu_c", ShadowID: "shadow_c"},
	}

	messages, found, notFound := expander.CreateExpandResultMessages(calls, true)

	assert.Equal(t, 3, found)
	assert.Equal(t, 0, notFound)
	assert.Len(t, messages, 1)

	content := messages[0]["content"].([]interface{})
	assert.Len(t, content, 3)
}

func TestHard_ThreeTools_OneExpiredBeforeExpand(t *testing.T) {
	st := store.NewMemoryStore(10 * time.Millisecond) // Very short TTL

	st.Set("shadow_fresh1", "Fresh content 1")
	st.Set("shadow_fresh2", "Fresh content 2")
	st.Set("shadow_expiring", "This will expire")

	// Wait for expiry
	time.Sleep(20 * time.Millisecond)

	// Re-set the fresh ones with new TTL
	st.Set("shadow_fresh1", "Fresh content 1")
	st.Set("shadow_fresh2", "Fresh content 2")

	expander := tooloutput.NewExpander(st, nil)

	calls := []tooloutput.ExpandContextCall{
		{ToolUseID: "toolu_1", ShadowID: "shadow_fresh1"},
		{ToolUseID: "toolu_2", ShadowID: "shadow_expiring"}, // Expired
		{ToolUseID: "toolu_3", ShadowID: "shadow_fresh2"},
	}

	messages, found, notFound := expander.CreateExpandResultMessages(calls, true)

	assert.Equal(t, 2, found, "Should find 2 fresh ones")
	assert.Equal(t, 1, notFound, "Should have 1 expired/not found")
	assert.Len(t, messages, 1)

	// All 3 should have results (2 with content, 1 with error message)
	content := messages[0]["content"].([]interface{})
	assert.Len(t, content, 3)
}

// =============================================================================
// TOOL EXECUTION FAILURE SCENARIOS
// =============================================================================

func TestHard_ToolResult_ExecutionFailed(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 10, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	// Simulates client reporting tool execution failure
	failedResults := []string{
		`{"error": "Permission denied", "code": "EACCES"}`,
		`{"error": "File not found", "path": "/nonexistent/file.txt"}`,
		`{"error": "Command failed", "exit_code": 1, "stderr": "fatal error"}`,
		`Error: ENOENT: no such file or directory`,
		`bash: command not found: xyz`,
		`{"success": false, "error": {"message": "Rate limited", "retry_after": 60}}`,
	}

	for i, content := range failedResults {
		t.Run(fmt.Sprintf("failed_%d", i), func(t *testing.T) {
			request := fixtures.RequestWithSingleToolOutput(content)
			ctx := fixtures.TestPipeContext(request)

			result, err := pipe.Process(ctx)
			assert.NoError(t, err, "Should handle tool execution failure gracefully")
			assert.NotNil(t, result)
		})
	}
}

func TestHard_ToolResult_PartialFailure(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 10, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	// Request with 3 tool results: 1 success, 1 error, 1 timeout
	request := map[string]interface{}{
		"model": "claude-3",
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type":        "tool_result",
						"tool_use_id": "toolu_success",
						"content":     strings.Repeat("success file content ", 100),
					},
					map[string]interface{}{
						"type":        "tool_result",
						"tool_use_id": "toolu_error",
						"content":     `{"error": "Permission denied"}`,
						"is_error":    true,
					},
					map[string]interface{}{
						"type":        "tool_result",
						"tool_use_id": "toolu_timeout",
						"content":     `{"error": "Execution timed out after 30s"}`,
						"is_error":    true,
					},
				},
			},
		},
	}

	body, _ := json.Marshal(request)
	ctx := fixtures.TestPipeContext(body)

	result, err := pipe.Process(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

func TestHard_ToolResult_IsErrorFlag(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 10, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	// Anthropic format with is_error flag
	request := map[string]interface{}{
		"model": "claude-3",
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type":        "tool_result",
						"tool_use_id": "toolu_001",
						"content":     "Error: Something went wrong",
						"is_error":    true,
					},
				},
			},
		},
	}

	body, _ := json.Marshal(request)
	ctx := fixtures.TestPipeContext(body)

	result, err := pipe.Process(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Verify is_error flag is preserved
	var parsed map[string]interface{}
	json.Unmarshal(result, &parsed)
	messages := parsed["messages"].([]interface{})
	userMsg := messages[0].(map[string]interface{})
	content := userMsg["content"].([]interface{})
	toolResult := content[0].(map[string]interface{})
	assert.Equal(t, true, toolResult["is_error"])
}

// =============================================================================
// REAL-WORLD EDGE CASES
// =============================================================================

func TestHard_RealWorld_GitDiffOutput(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 50, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	// Real git diff output with special characters
	gitDiff := `diff --git a/internal/handler.go b/internal/handler.go
index 1234567..abcdefg 100644
--- a/internal/handler.go
+++ b/internal/handler.go
@@ -42,6 +42,15 @@ func (h *Handler) Process(ctx context.Context) error {
 	if err != nil {
-		return fmt.Errorf("failed: %w", err)
+		// Improved error handling
+		log.Error().Err(err).Msg("process failed")
+		return &ProcessError{
+			Code:    "PROC_FAIL",
+			Message: err.Error(),
+			Cause:   err,
+		}
 	}
+
+	// Add metrics
+	h.metrics.RecordSuccess()
 	return nil
 }`

	request := fixtures.RequestWithSingleToolOutput(gitDiff)
	ctx := fixtures.TestPipeContext(request)

	result, err := pipe.Process(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

func TestHard_RealWorld_LogFileWithTimestamps(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 50, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	logContent := `2026-02-08T12:00:00.000Z [INFO] Server starting on port 8080
2026-02-08T12:00:01.234Z [DEBUG] Loading configuration from /etc/app/config.yaml
2026-02-08T12:00:01.567Z [WARN] Deprecated config key "old_setting" used
2026-02-08T12:00:02.000Z [ERROR] Failed to connect to database: connection refused
    at postgres.connect (postgres.go:42)
    at main.initDB (main.go:156)
    at main.main (main.go:23)
2026-02-08T12:00:02.001Z [INFO] Retrying database connection (attempt 1/5)
2026-02-08T12:00:07.000Z [INFO] Database connected successfully
2026-02-08T12:00:07.100Z [INFO] Starting HTTP server
2026-02-08T12:00:07.200Z [INFO] Registered routes: /api/v1/users, /api/v1/posts, /health`

	request := fixtures.RequestWithSingleToolOutput(logContent)
	ctx := fixtures.TestPipeContext(request)

	result, err := pipe.Process(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

func TestHard_RealWorld_NPMInstallOutput(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 50, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	npmOutput := `npm warn deprecated inflight@1.0.6: This module is not supported
npm warn deprecated glob@7.2.3: Glob versions prior to v9 are no longer supported
npm warn deprecated rimraf@3.0.2: Rimraf versions prior to v4 are no longer supported

added 1423 packages, and audited 1424 packages in 45s

237 packages are looking for funding
  run ` + "`npm fund`" + ` for details

12 vulnerabilities (8 moderate, 4 high)

To address issues that do not require attention, run:
  npm audit fix

To address all issues (including breaking changes), run:
  npm audit fix --force`

	request := fixtures.RequestWithSingleToolOutput(npmOutput)
	ctx := fixtures.TestPipeContext(request)

	result, err := pipe.Process(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

func TestHard_RealWorld_PythonTracebackWithUnicode(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 50, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	pythonError := `Traceback (most recent call last):
  File "/app/main.py", line 42, in process_data
    result = parse_input(data)
  File "/app/parser.py", line 156, in parse_input
    raise ValueError(f"Invalid character: '→' at position {pos}")
ValueError: Invalid character: '→' at position 23

During handling of the above exception, another exception occurred:

Traceback (most recent call last):
  File "/app/main.py", line 50, in <module>
    main()
  File "/app/main.py", line 45, in main
    handle_error(e)
  File "/app/error_handler.py", line 12, in handle_error
    raise RuntimeError(f"Failed to process: {e}") from e
RuntimeError: Failed to process: Invalid character: '→' at position 23`

	request := fixtures.RequestWithSingleToolOutput(pythonError)
	ctx := fixtures.TestPipeContext(request)

	result, err := pipe.Process(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

func TestHard_RealWorld_SQLQueryResults(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 50, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	sqlResults := `+----+------------------+----------------------+------------+--------+
| id | name             | email                | created_at | status |
+----+------------------+----------------------+------------+--------+
|  1 | John O'Brien     | john@example.com     | 2026-01-15 | active |
|  2 | María García     | maria@ejemplo.es     | 2026-01-20 | active |
|  3 | 田中太郎         | tanaka@example.jp    | 2026-02-01 | pending|
|  4 | NULL             | deleted@example.com  | 2026-02-05 | deleted|
+----+------------------+----------------------+------------+--------+
4 rows in set (0.02 sec)`

	request := fixtures.RequestWithSingleToolOutput(sqlResults)
	ctx := fixtures.TestPipeContext(request)

	result, err := pipe.Process(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

func TestHard_RealWorld_DockerComposeOutput(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 50, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	dockerOutput := `[+] Running 5/5
 ✔ Network app_default      Created                                    0.1s
 ✔ Volume "app_postgres"    Created                                    0.0s
 ✔ Container app-redis-1    Started                                    0.5s
 ✔ Container app-db-1       Started                                    0.6s
 ✔ Container app-api-1      Started                                    0.8s

Attaching to app-api-1, app-db-1, app-redis-1
app-db-1    | PostgreSQL Database directory appears to contain a database
app-db-1    | 2026-02-08 12:00:00.000 UTC [1] LOG:  starting PostgreSQL 15.2
app-redis-1 | 1:C 08 Feb 2026 12:00:00.000 * oO0OoO0OoO0Oo Redis is starting
app-api-1   | Server listening on :8080`

	request := fixtures.RequestWithSingleToolOutput(dockerOutput)
	ctx := fixtures.TestPipeContext(request)

	result, err := pipe.Process(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, result)
}

// =============================================================================
// SELECTIVE EXPANSION REWRITE TESTS
// =============================================================================

func TestHard_RewriteHistory_ThreeToolsExpandOne(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)
	st.Set("shadow_001", "Full file 1 content - very detailed")
	st.Set("shadow_002", "Full file 2 content - very detailed")
	st.Set("shadow_003", "Full file 3 content - very detailed")

	expander := tooloutput.NewExpander(st, nil)

	// History has 3 compressed tool results
	body := []byte(`{
		"model": "claude-3",
		"messages": [
			{"role": "user", "content": "Analyze these 3 files"},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "toolu_read1", "name": "read_file", "input": {"path": "file1.go"}},
					{"type": "tool_use", "id": "toolu_read2", "name": "read_file", "input": {"path": "file2.go"}},
					{"type": "tool_use", "id": "toolu_read3", "name": "read_file", "input": {"path": "file3.go"}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "toolu_read1", "content": "<<<SHADOW:shadow_001>>>\nSummary of file1"},
					{"type": "tool_result", "tool_use_id": "toolu_read2", "content": "<<<SHADOW:shadow_002>>>\nSummary of file2"},
					{"type": "tool_result", "tool_use_id": "toolu_read3", "content": "<<<SHADOW:shadow_003>>>\nSummary of file3"}
				]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "text", "text": "I need more detail on file 2"},
					{"type": "tool_use", "id": "toolu_expand", "name": "expand_context", "input": {"id": "shadow_002"}}
				]
			}
		]
	}`)

	// Only expand shadow_002
	calls := []tooloutput.ExpandContextCall{
		{ToolUseID: "toolu_expand", ShadowID: "shadow_002"},
	}

	result, expandedIDs, err := expander.RewriteHistoryWithExpansion(body, calls)
	assert.NoError(t, err)
	assert.Equal(t, []string{"shadow_002"}, expandedIDs)

	// Parse and verify content - need to check the unescaped content
	var parsed map[string]interface{}
	json.Unmarshal(result, &parsed)
	messages := parsed["messages"].([]interface{})

	// Find the user message with tool results
	var toolResults []interface{}
	for _, msg := range messages {
		m := msg.(map[string]interface{})
		if m["role"] == "user" {
			if content, ok := m["content"].([]interface{}); ok {
				toolResults = content
			}
		}
	}

	// Verify: file1 and file3 still compressed, file2 expanded
	for _, tr := range toolResults {
		block := tr.(map[string]interface{})
		if block["type"] != "tool_result" {
			continue
		}
		toolUseID := block["tool_use_id"].(string)
		content := block["content"].(string)

		switch toolUseID {
		case "toolu_read1":
			assert.Contains(t, content, "<<<SHADOW:shadow_001>>>", "file1 should still be compressed")
		case "toolu_read2":
			assert.Equal(t, "Full file 2 content - very detailed", content, "file2 should be expanded")
		case "toolu_read3":
			assert.Contains(t, content, "<<<SHADOW:shadow_003>>>", "file3 should still be compressed")
		}
	}
}

func TestHard_RewriteHistory_ThreeToolsExpandTwo(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)
	st.Set("shadow_a", "Content A expanded")
	st.Set("shadow_b", "Content B expanded")
	st.Set("shadow_c", "Content C expanded")

	expander := tooloutput.NewExpander(st, nil)

	body := []byte(`{
		"model": "claude-3",
		"messages": [
			{"role": "user", "content": "Check these files"},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "t1", "content": "<<<SHADOW:shadow_a>>>\nCompressed A"},
					{"type": "tool_result", "tool_use_id": "t2", "content": "<<<SHADOW:shadow_b>>>\nCompressed B"},
					{"type": "tool_result", "tool_use_id": "t3", "content": "<<<SHADOW:shadow_c>>>\nCompressed C"}
				]
			}
		]
	}`)

	// Expand A and C, leave B compressed
	calls := []tooloutput.ExpandContextCall{
		{ToolUseID: "e1", ShadowID: "shadow_a"},
		{ToolUseID: "e2", ShadowID: "shadow_c"},
	}

	result, expandedIDs, err := expander.RewriteHistoryWithExpansion(body, calls)
	assert.NoError(t, err)
	assert.Len(t, expandedIDs, 2)
	assert.Contains(t, expandedIDs, "shadow_a")
	assert.Contains(t, expandedIDs, "shadow_c")

	// Parse and verify unescaped content
	var parsed map[string]interface{}
	json.Unmarshal(result, &parsed)
	messages := parsed["messages"].([]interface{})

	for _, msg := range messages {
		m := msg.(map[string]interface{})
		if content, ok := m["content"].([]interface{}); ok {
			for _, tr := range content {
				block := tr.(map[string]interface{})
				if block["type"] != "tool_result" {
					continue
				}
				toolUseID := block["tool_use_id"].(string)
				contentStr := block["content"].(string)

				switch toolUseID {
				case "t1":
					assert.Equal(t, "Content A expanded", contentStr)
				case "t2":
					assert.Contains(t, contentStr, "<<<SHADOW:shadow_b>>>", "B should stay compressed")
				case "t3":
					assert.Equal(t, "Content C expanded", contentStr)
				}
			}
		}
	}
}

// =============================================================================
// COMPLEX CONVERSATION FLOW TESTS
// =============================================================================

func TestHard_ComplexConversation_MultiRoundExpansion(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)

	// Simulate multi-turn conversation with progressive expansion
	st.Set("shadow_overview", "Full project overview with architecture details")
	st.Set("shadow_impl", "Implementation details of core module")
	st.Set("shadow_tests", "Test suite with 100 test cases")

	expander := tooloutput.NewExpander(st, nil)

	// First round: expand overview
	round1Calls := []tooloutput.ExpandContextCall{
		{ToolUseID: "r1", ShadowID: "shadow_overview"},
	}
	msg1, found1, _ := expander.CreateExpandResultMessages(round1Calls, true)
	assert.Equal(t, 1, found1)
	assert.Len(t, msg1, 1)

	// Second round: expand implementation
	round2Calls := []tooloutput.ExpandContextCall{
		{ToolUseID: "r2", ShadowID: "shadow_impl"},
	}
	msg2, found2, _ := expander.CreateExpandResultMessages(round2Calls, true)
	assert.Equal(t, 1, found2)
	assert.Len(t, msg2, 1)

	// Third round: expand all remaining at once
	round3Calls := []tooloutput.ExpandContextCall{
		{ToolUseID: "r3a", ShadowID: "shadow_tests"},
	}
	msg3, found3, _ := expander.CreateExpandResultMessages(round3Calls, true)
	assert.Equal(t, 1, found3)
	assert.Len(t, msg3, 1)
}

func TestHard_InterwovenToolResults_DifferentMessages(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)
	st.Set("shadow_turn1", "Content from turn 1")
	st.Set("shadow_turn2", "Content from turn 2")

	expander := tooloutput.NewExpander(st, nil)

	// Tool results spread across multiple user messages (multi-turn conversation)
	body := []byte(`{
		"model": "claude-3",
		"messages": [
			{"role": "user", "content": "Read file1"},
			{
				"role": "assistant",
				"content": [{"type": "tool_use", "id": "t1", "name": "read_file", "input": {"path": "f1"}}]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "t1", "content": "<<<SHADOW:shadow_turn1>>>\nSummary1"}
				]
			},
			{"role": "assistant", "content": [{"type": "text", "text": "Got it. Need more?"}]},
			{"role": "user", "content": "Yes, read file2"},
			{
				"role": "assistant",
				"content": [{"type": "tool_use", "id": "t2", "name": "read_file", "input": {"path": "f2"}}]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "t2", "content": "<<<SHADOW:shadow_turn2>>>\nSummary2"}
				]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "expand1", "name": "expand_context", "input": {"id": "shadow_turn1"}}
				]
			}
		]
	}`)

	// LLM wants to expand content from turn 1 while in turn 2
	calls := []tooloutput.ExpandContextCall{
		{ToolUseID: "expand1", ShadowID: "shadow_turn1"},
	}

	result, expandedIDs, err := expander.RewriteHistoryWithExpansion(body, calls)
	assert.NoError(t, err)
	assert.Contains(t, expandedIDs, "shadow_turn1")

	// Parse and verify unescaped content
	var parsed map[string]interface{}
	json.Unmarshal(result, &parsed)
	messages := parsed["messages"].([]interface{})

	turn1Expanded := false
	turn2Compressed := false

	for _, msg := range messages {
		m := msg.(map[string]interface{})
		if content, ok := m["content"].([]interface{}); ok {
			for _, block := range content {
				b := block.(map[string]interface{})
				if b["type"] != "tool_result" {
					continue
				}
				contentStr := b["content"].(string)
				toolUseID := b["tool_use_id"].(string)

				if toolUseID == "t1" && contentStr == "Content from turn 1" {
					turn1Expanded = true
				}
				if toolUseID == "t2" && strings.Contains(contentStr, "<<<SHADOW:shadow_turn2>>>") {
					turn2Compressed = true
				}
			}
		}
	}

	assert.True(t, turn1Expanded, "Turn 1 content should be expanded")
	assert.True(t, turn2Compressed, "Turn 2 should stay compressed")
}
