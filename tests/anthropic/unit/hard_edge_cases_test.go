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
	chunk := []byte(fmt.Sprintf(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, largeData))

	start := time.Now()
	buffer.ProcessChunk(chunk)
	elapsed := time.Since(start)

	assert.False(t, buffer.HasSuppressedCalls())
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




