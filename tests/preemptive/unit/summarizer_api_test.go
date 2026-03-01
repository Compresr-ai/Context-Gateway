package preemptive_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/preemptive"
)

// =============================================================================
// HELPERS
// =============================================================================

// makeMessage creates a JSON-encoded message for testing.
func makeMessage(role, content string) json.RawMessage {
	msg, _ := json.Marshal(map[string]interface{}{
		"role":    role,
		"content": content,
	})
	return msg
}

// makeContentBlockMessage creates a message with Anthropic content blocks.
func makeContentBlockMessage(role string, blocks []map[string]interface{}) json.RawMessage {
	msg, _ := json.Marshal(map[string]interface{}{
		"role":    role,
		"content": blocks,
	})
	return msg
}

// mockCompresrServer creates an httptest server that mimics the Compresr API.
// It validates the request and returns the given response.
func mockCompresrServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body map[string]interface{})) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Validate basic request properties
		assert.Equal(t, http.MethodPost, r.Method, "expected POST method")
		assert.Equal(t, "/api/compress/history/", r.URL.Path, "expected /api/compress/history/ path")
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"), "expected JSON content type")
		assert.NotEmpty(t, r.Header.Get("X-API-Key"), "expected X-API-Key header")

		var body map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&body)
		require.NoError(t, err, "failed to decode request body")

		handler(w, r, body)
	}))
}

// successResponse creates a successful Compresr API response.
func successResponse(summary string, originalTokens, compressedTokens, msgsCompressed, msgsKept int, ratio float64) map[string]interface{} {
	return map[string]interface{}{
		"success": true,
		"data": map[string]interface{}{
			"summary":             summary,
			"original_tokens":     originalTokens,
			"compressed_tokens":   compressedTokens,
			"messages_compressed": msgsCompressed,
			"messages_kept":       msgsKept,
			"compression_ratio":   ratio,
			"duration_ms":         150,
		},
	}
}

// newAPISummarizer creates a Summarizer with API strategy pointing to a test server.
func newAPISummarizer(serverURL string) *preemptive.Summarizer {
	cfg := preemptive.SummarizerConfig{
		Strategy:        preemptive.StrategyCompresr,
		CompresrBaseURL: serverURL, // httptest server URL as base (like cfg.URLs.Compresr)
		Compresr: &preemptive.CompresrConfig{
			Endpoint: "/api/compress/history/",
			APIKey:   "cmp_test-key-12345",
			Model:    "hcc_espresso_v1",
			Timeout:  30 * time.Second,
		},
		KeepRecentCount: 3,
	}
	return preemptive.NewSummarizer(cfg)
}

// =============================================================================
// API STRATEGY: SUCCESSFUL SUMMARIZATION TESTS
// =============================================================================

func TestSummarizeViaAPI_BasicConversation(t *testing.T) {
	server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		// Validate messages were sent
		messages, ok := body["messages"].([]interface{})
		require.True(t, ok, "messages should be an array")
		assert.Equal(t, 6, len(messages), "expected 6 messages")

		// Validate first message
		msg0, _ := messages[0].(map[string]interface{})
		assert.Equal(t, "user", msg0["role"])
		assert.Equal(t, "Hello, help me with my code", msg0["content"])

		// Validate model name
		assert.Equal(t, "hcc_espresso_v1", body["compression_model_name"])

		// Validate keep_recent
		keepRecent, _ := body["keep_recent"].(float64) // JSON numbers are float64
		assert.Equal(t, float64(3), keepRecent)

		// Validate source
		assert.Equal(t, "gateway", body["source"])

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(successResponse(
			"User asked for code help. Assistant provided a Python function. User requested modifications.",
			1500, 300, 3, 3, 0.8,
		))
	})
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "Hello, help me with my code"),
			makeMessage("assistant", "Sure! What do you need help with?"),
			makeMessage("user", "Write a function to sort a list"),
			makeMessage("assistant", "Here is a Python sort function:\n```python\ndef sort_list(items):\n    return sorted(items)\n```"),
			makeMessage("user", "Can you add reverse sorting?"),
			makeMessage("assistant", "Here is the updated function:\n```python\ndef sort_list(items, reverse=False):\n    return sorted(items, reverse=reverse)\n```"),
		},
		KeepRecentCount: 3,
	}

	result, err := summarizer.Summarize(context.Background(), input)
	require.NoError(t, err)
	assert.NotEmpty(t, result.Summary)
	assert.Contains(t, result.Summary, "code help")
	assert.Equal(t, 300, result.SummaryTokens)
	assert.Equal(t, 1500, result.InputTokens)
	assert.Equal(t, 300, result.OutputTokens)
	assert.Equal(t, 2, result.LastSummarizedIndex) // 6 total - 3 kept - 1 = 2
	assert.Greater(t, result.Duration, time.Duration(0))
}

func TestSummarizeViaAPI_AnthropicContentBlocks(t *testing.T) {
	// Test that Anthropic content block arrays are properly extracted to plain text
	server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		messages, _ := body["messages"].([]interface{})
		require.Equal(t, 4, len(messages))

		// Content blocks should be extracted to plain text
		msg1, _ := messages[1].(map[string]interface{})
		content := msg1["content"].(string)
		assert.Contains(t, content, "Here is the answer")

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(successResponse(
			"User asked a question. Assistant answered with text blocks.",
			800, 200, 2, 2, 0.75,
		))
	})
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "What is Go?"),
			makeContentBlockMessage("assistant", []map[string]interface{}{
				{"type": "text", "text": "Here is the answer:"},
				{"type": "text", "text": "Go is a programming language by Google."},
			}),
			makeMessage("user", "Thanks"),
			makeMessage("assistant", "You're welcome!"),
		},
		KeepRecentCount: 2,
	}

	result, err := summarizer.Summarize(context.Background(), input)
	require.NoError(t, err)
	assert.NotEmpty(t, result.Summary)
}

func TestSummarizeViaAPI_ToolUseMessages(t *testing.T) {
	// Test that tool_use and tool_result content blocks are handled
	server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		messages, _ := body["messages"].([]interface{})
		require.Equal(t, 5, len(messages))

		// Tool use message should have extracted content like "[Tool: read_file]"
		msg1, _ := messages[1].(map[string]interface{})
		assert.Contains(t, msg1["content"].(string), "[Tool: read_file]")

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(successResponse(
			"User requested file read. Assistant used read_file tool.",
			1200, 350, 3, 2, 0.7,
		))
	})
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "Read the main.go file"),
			makeContentBlockMessage("assistant", []map[string]interface{}{
				{"type": "tool_use", "id": "toolu_abc123", "name": "read_file", "input": map[string]interface{}{"path": "main.go"}},
			}),
			makeContentBlockMessage("user", []map[string]interface{}{
				{"type": "tool_result", "tool_use_id": "toolu_abc123", "content": "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}"},
			}),
			makeMessage("user", "Explain this code"),
			makeMessage("assistant", "This is a simple Go program that prints hello."),
		},
		KeepRecentCount: 2,
	}

	result, err := summarizer.Summarize(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Summary, "file read")
}

// =============================================================================
// API STRATEGY: KEEP_RECENT LOGIC TESTS
// =============================================================================

func TestSummarizeViaAPI_KeepRecentFromInput(t *testing.T) {
	// Input KeepRecentCount should take priority over config
	server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		keepRecent, _ := body["keep_recent"].(float64)
		assert.Equal(t, float64(5), keepRecent, "should use input keep_recent=5")

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(successResponse("summary", 1000, 200, 5, 5, 0.8))
	})
	defer server.Close()

	summarizer := newAPISummarizer(server.URL) // config has KeepRecentCount=3
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "msg1"), makeMessage("assistant", "resp1"),
			makeMessage("user", "msg2"), makeMessage("assistant", "resp2"),
			makeMessage("user", "msg3"), makeMessage("assistant", "resp3"),
			makeMessage("user", "msg4"), makeMessage("assistant", "resp4"),
			makeMessage("user", "msg5"), makeMessage("assistant", "resp5"),
		},
		KeepRecentCount: 5, // Override config's 3
	}

	_, err := summarizer.Summarize(context.Background(), input)
	require.NoError(t, err)
}

func TestSummarizeViaAPI_KeepRecentDefaultsTo3(t *testing.T) {
	// When neither input nor config sets KeepRecentCount, should default to 3
	server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		keepRecent, _ := body["keep_recent"].(float64)
		assert.Equal(t, float64(3), keepRecent, "should default to 3")

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(successResponse("summary", 500, 100, 3, 3, 0.8))
	})
	defer server.Close()

	cfg := preemptive.SummarizerConfig{
		Strategy:        preemptive.StrategyCompresr,
		CompresrBaseURL: server.URL,
		Compresr: &preemptive.CompresrConfig{
			Endpoint: "/api/compress/history/",
			APIKey:   "cmp_test-key",
			Model:    "hcc_espresso_v1",
			Timeout:  30 * time.Second,
		},
		KeepRecentCount: 0, // Not set
	}
	summarizer := preemptive.NewSummarizer(cfg)

	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "msg1"), makeMessage("assistant", "resp1"),
			makeMessage("user", "msg2"), makeMessage("assistant", "resp2"),
			makeMessage("user", "msg3"), makeMessage("assistant", "resp3"),
		},
		KeepRecentCount: 0, // Not set
	}

	_, err := summarizer.Summarize(context.Background(), input)
	require.NoError(t, err)
}

// =============================================================================
// API STRATEGY: LAST SUMMARIZED INDEX CALCULATION
// =============================================================================

func TestSummarizeViaAPI_LastSummarizedIndex(t *testing.T) {
	tests := []struct {
		name             string
		totalMessages    int
		messagesKept     int
		expectedLastIdx  int
	}{
		{"10 msgs, 3 kept", 10, 3, 6},    // 10 - 3 - 1 = 6
		{"5 msgs, 2 kept", 5, 2, 2},      // 5 - 2 - 1 = 2
		{"3 msgs, 1 kept", 3, 1, 1},      // 3 - 1 - 1 = 1
		{"4 msgs, 4 kept (all)", 4, 4, 0}, // max(4-4-1, 0) = 0
		{"2 msgs, 2 kept (all)", 2, 2, 0}, // max(2-2-1, 0) = 0
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(successResponse(
					"Summary text", 1000, 200,
					tt.totalMessages-tt.messagesKept, tt.messagesKept, 0.8,
				))
			})
			defer server.Close()

			summarizer := newAPISummarizer(server.URL)
			msgs := make([]json.RawMessage, tt.totalMessages)
			for i := 0; i < tt.totalMessages; i++ {
				if i%2 == 0 {
					msgs[i] = makeMessage("user", "message")
				} else {
					msgs[i] = makeMessage("assistant", "response")
				}
			}

			result, err := summarizer.Summarize(context.Background(), preemptive.SummarizeInput{
				Messages:        msgs,
				KeepRecentCount: tt.messagesKept,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.expectedLastIdx, result.LastSummarizedIndex)
		})
	}
}

// =============================================================================
// API STRATEGY: ERROR HANDLING TESTS
// =============================================================================

func TestSummarizeViaAPI_EmptyMessages(t *testing.T) {
	summarizer := newAPISummarizer("http://unused")
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{},
	}

	_, err := summarizer.Summarize(context.Background(), input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no messages")
}

func TestSummarizeViaAPI_NilAPIConfig(t *testing.T) {
	cfg := preemptive.SummarizerConfig{
		Strategy: preemptive.StrategyCompresr,
		Compresr: nil,
	}
	summarizer := preemptive.NewSummarizer(cfg)

	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "hello"),
			makeMessage("assistant", "hi"),
		},
	}

	_, err := summarizer.Summarize(context.Background(), input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compresr config is nil")
}

func TestSummarizeViaAPI_ServerReturns500(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "hello"),
			makeMessage("assistant", "hi"),
		},
	}

	_, err := summarizer.Summarize(context.Background(), input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compresr API call failed")
}

func TestSummarizeViaAPI_ServerReturns401(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "hello"),
			makeMessage("assistant", "hi"),
		},
	}

	_, err := summarizer.Summarize(context.Background(), input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compresr API call failed")
}

func TestSummarizeViaAPI_ServerReturnsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "rate limit exceeded: free tier allows 100 requests/day",
		})
	}))
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "hello"),
			makeMessage("assistant", "hi"),
		},
	}

	_, err := summarizer.Summarize(context.Background(), input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compresr API call failed")
}

func TestSummarizeViaAPI_ServerReturnsMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not valid json {{{"))
	}))
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "hello"),
			makeMessage("assistant", "hi"),
		},
	}

	_, err := summarizer.Summarize(context.Background(), input)
	require.Error(t, err)
}

func TestSummarizeViaAPI_ServerReturns429RateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("rate limit exceeded"))
	}))
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "hello"),
			makeMessage("assistant", "hi"),
		},
	}

	_, err := summarizer.Summarize(context.Background(), input)
	require.Error(t, err)
}

// =============================================================================
// API STRATEGY: REQUEST PAYLOAD VALIDATION TESTS
// =============================================================================

func TestSummarizeViaAPI_RequestPayloadFields(t *testing.T) {
	// Verify every field in the request payload sent to api.compresr.ai
	server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		// 1. Verify "messages" field
		messages, ok := body["messages"].([]interface{})
		require.True(t, ok, "messages must be an array")
		require.Equal(t, 4, len(messages))

		for _, msg := range messages {
			m, ok := msg.(map[string]interface{})
			require.True(t, ok, "each message must be an object")
			assert.Contains(t, m, "role", "each message must have role")
			assert.Contains(t, m, "content", "each message must have content")
			// Content should be plain string (not nested blocks)
			_, isString := m["content"].(string)
			assert.True(t, isString, "content should be extracted to plain string")
		}

		// 2. Verify "compression_model_name" field
		modelName, ok := body["compression_model_name"].(string)
		require.True(t, ok)
		assert.Equal(t, "hcc_espresso_v1", modelName)

		// 3. Verify "keep_recent" field
		keepRecent, ok := body["keep_recent"].(float64)
		require.True(t, ok)
		assert.Equal(t, float64(2), keepRecent)

		// 4. Verify "source" field
		source, ok := body["source"].(string)
		require.True(t, ok)
		assert.Equal(t, "gateway", source)

		// 5. Verify X-API-Key header
		assert.Equal(t, "cmp_test-key-12345", r.Header.Get("X-API-Key"))

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(successResponse("compressed summary", 800, 200, 2, 2, 0.75))
	})
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "First question"),
			makeMessage("assistant", "First answer"),
			makeMessage("user", "Second question"),
			makeMessage("assistant", "Second answer"),
		},
		KeepRecentCount: 2,
	}

	_, err := summarizer.Summarize(context.Background(), input)
	require.NoError(t, err)
}

func TestSummarizeViaAPI_ContentBlocksExtractedToPlainText(t *testing.T) {
	// Verify that Anthropic content blocks are flattened to plain strings before sending
	server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		messages, _ := body["messages"].([]interface{})

		// Message with text blocks should be extracted
		msg0, _ := messages[0].(map[string]interface{})
		content := msg0["content"].(string)
		assert.Equal(t, "user", msg0["role"])
		assert.Contains(t, content, "part one")
		assert.Contains(t, content, "part two")

		// Message with tool_use should include [Tool: xxx]
		msg1, _ := messages[1].(map[string]interface{})
		content1 := msg1["content"].(string)
		assert.Contains(t, content1, "[Tool: bash]")

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(successResponse("summary", 500, 100, 1, 1, 0.8))
	})
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeContentBlockMessage("user", []map[string]interface{}{
				{"type": "text", "text": "part one"},
				{"type": "text", "text": "part two"},
			}),
			makeContentBlockMessage("assistant", []map[string]interface{}{
				{"type": "text", "text": "Let me run that"},
				{"type": "tool_use", "id": "toolu_1", "name": "bash", "input": map[string]interface{}{"command": "ls"}},
			}),
		},
		KeepRecentCount: 1,
	}

	_, err := summarizer.Summarize(context.Background(), input)
	require.NoError(t, err)
}

func TestSummarizeViaAPI_ModelNamePassedCorrectly(t *testing.T) {
	// Verify the model name from config is sent as compression_model_name
	models := []string{"hcc_espresso_v1", "hcc_latte_v1", "custom_model_v2"}

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]interface{}
				json.NewDecoder(r.Body).Decode(&body)
				assert.Equal(t, model, body["compression_model_name"])

				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(successResponse("s", 100, 50, 1, 1, 0.5))
			}))
			defer server.Close()

			cfg := preemptive.SummarizerConfig{
				Strategy:        preemptive.StrategyCompresr,
				CompresrBaseURL: server.URL,
				Compresr: &preemptive.CompresrConfig{
					Endpoint: "/api/compress/history/",
					APIKey:   "cmp_key",
					Model:    model,
					Timeout:  30 * time.Second,
				},
			}
			summarizer := preemptive.NewSummarizer(cfg)
			input := preemptive.SummarizeInput{
				Messages: []json.RawMessage{
					makeMessage("user", "hi"),
					makeMessage("assistant", "hello"),
				},
			}

			_, err := summarizer.Summarize(context.Background(), input)
			require.NoError(t, err)
		})
	}
}

// =============================================================================
// API STRATEGY: RESPONSE PARSING TESTS
// =============================================================================

func TestSummarizeViaAPI_ResponseFieldMapping(t *testing.T) {
	// Verify all response fields are correctly mapped to SummarizeOutput
	server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"summary":             "This is the compressed summary of the conversation.",
				"original_tokens":     2500,
				"compressed_tokens":   500,
				"messages_compressed": 7,
				"messages_kept":       3,
				"compression_ratio":   0.80,
				"duration_ms":         245,
			},
		})
	})
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	msgs := make([]json.RawMessage, 10)
	for i := range msgs {
		if i%2 == 0 {
			msgs[i] = makeMessage("user", "question "+string(rune('A'+i)))
		} else {
			msgs[i] = makeMessage("assistant", "answer "+string(rune('A'+i)))
		}
	}

	result, err := summarizer.Summarize(context.Background(), preemptive.SummarizeInput{
		Messages:        msgs,
		KeepRecentCount: 3,
	})
	require.NoError(t, err)

	assert.Equal(t, "This is the compressed summary of the conversation.", result.Summary)
	assert.Equal(t, 500, result.SummaryTokens)      // mapped from compressed_tokens
	assert.Equal(t, 2500, result.InputTokens)        // mapped from original_tokens
	assert.Equal(t, 500, result.OutputTokens)         // mapped from compressed_tokens
	assert.Equal(t, 6, result.LastSummarizedIndex)   // 10 - 3 - 1 = 6
}

// =============================================================================
// API STRATEGY: EDGE CASES
// =============================================================================

func TestSummarizeViaAPI_LargeConversation(t *testing.T) {
	// Simulate a large conversation (100 messages)
	messageCount := 100
	server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		messages, _ := body["messages"].([]interface{})
		assert.Equal(t, messageCount, len(messages))

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(successResponse(
			"Long conversation about building a web app with many iterations.",
			50000, 5000, 95, 5, 0.9,
		))
	})
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	msgs := make([]json.RawMessage, messageCount)
	for i := range msgs {
		if i%2 == 0 {
			msgs[i] = makeMessage("user", "This is a long user message with code and explanations that spans multiple paragraphs to simulate realistic conversation content.")
		} else {
			msgs[i] = makeMessage("assistant", "This is a detailed assistant response with code examples, explanations, and suggestions for improvements.")
		}
	}

	result, err := summarizer.Summarize(context.Background(), preemptive.SummarizeInput{
		Messages:        msgs,
		KeepRecentCount: 5,
	})
	require.NoError(t, err)
	assert.Equal(t, 5000, result.SummaryTokens)
	assert.Equal(t, 94, result.LastSummarizedIndex) // 100 - 5 - 1 = 94
}

func TestSummarizeViaAPI_UnicodeMessages(t *testing.T) {
	server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		messages, _ := body["messages"].([]interface{})
		msg0, _ := messages[0].(map[string]interface{})
		// Verify unicode is preserved
		assert.Contains(t, msg0["content"].(string), "日本語")
		assert.Contains(t, msg0["content"].(string), "العربية")

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(successResponse("多言語の会話の要約", 300, 100, 1, 1, 0.67))
	})
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "Help me translate: 日本語 and العربية and 中文"),
			makeMessage("assistant", "Here are the translations"),
		},
		KeepRecentCount: 1,
	}

	result, err := summarizer.Summarize(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "多言語の会話の要約", result.Summary)
}

func TestSummarizeViaAPI_EmptyContentMessages(t *testing.T) {
	// Messages with empty content should still be sent
	server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		messages, _ := body["messages"].([]interface{})
		assert.Equal(t, 3, len(messages))

		// Empty content message should have empty string
		msg1, _ := messages[1].(map[string]interface{})
		assert.Equal(t, "", msg1["content"].(string))

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(successResponse("summary", 200, 50, 1, 2, 0.75))
	})
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "hello"),
			makeMessage("assistant", ""),
			makeMessage("user", "follow up"),
		},
		KeepRecentCount: 2,
	}

	_, err := summarizer.Summarize(context.Background(), input)
	require.NoError(t, err)
}

func TestSummarizeViaAPI_MixedContentTypes(t *testing.T) {
	// Conversation with mix of string content, content blocks, tool use, and tool results
	server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		messages, _ := body["messages"].([]interface{})
		assert.Equal(t, 6, len(messages))

		// All content should be plain strings
		for i, msg := range messages {
			m, _ := msg.(map[string]interface{})
			_, isString := m["content"].(string)
			assert.True(t, isString, "message %d content should be string, got %T", i, m["content"])
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(successResponse("summary of mixed content", 2000, 400, 4, 2, 0.8))
	})
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "Read and edit the file"),
			makeContentBlockMessage("assistant", []map[string]interface{}{
				{"type": "text", "text": "I'll read the file first."},
				{"type": "tool_use", "id": "t1", "name": "read_file", "input": map[string]interface{}{}},
			}),
			makeContentBlockMessage("user", []map[string]interface{}{
				{"type": "tool_result", "tool_use_id": "t1", "content": "file content here"},
			}),
			makeContentBlockMessage("assistant", []map[string]interface{}{
				{"type": "text", "text": "Now I'll edit it."},
				{"type": "tool_use", "id": "t2", "name": "edit_file", "input": map[string]interface{}{}},
			}),
			makeContentBlockMessage("user", []map[string]interface{}{
				{"type": "tool_result", "tool_use_id": "t2", "content": "edit successful"},
			}),
			makeMessage("assistant", "Done! I've edited the file."),
		},
		KeepRecentCount: 2,
	}

	result, err := summarizer.Summarize(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "summary of mixed content", result.Summary)
	assert.Equal(t, 3, result.LastSummarizedIndex) // 6 - 2 - 1 = 3
}

func TestSummarizeViaAPI_SingleMessagePair(t *testing.T) {
	// Minimal conversation: 2 messages
	server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		messages, _ := body["messages"].([]interface{})
		assert.Equal(t, 2, len(messages))

		keepRecent, _ := body["keep_recent"].(float64)
		assert.Equal(t, float64(1), keepRecent)

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(successResponse("short summary", 100, 30, 1, 1, 0.7))
	})
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "Hello"),
			makeMessage("assistant", "Hi!"),
		},
		KeepRecentCount: 1,
	}

	result, err := summarizer.Summarize(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, "short summary", result.Summary)
	assert.Equal(t, 0, result.LastSummarizedIndex) // 2 - 1 - 1 = 0
}

// =============================================================================
// API STRATEGY: STRATEGY DISPATCH TEST
// =============================================================================

func TestSummarize_DispatchesToAPIStrategy(t *testing.T) {
	// Verify that strategy: "compresr" routes to summarizeViaAPI (not summarizeViaLLM)
	apiCalled := false
	server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		apiCalled = true
		// If this handler is called, we know the API strategy was used
		// (LLM strategy would call a different endpoint like /v1/messages)
		assert.Equal(t, "/api/compress/history/", r.URL.Path)
		assert.Equal(t, "hcc_espresso_v1", body["compression_model_name"])

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(successResponse("api summary", 500, 100, 1, 1, 0.8))
	})
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "test"),
			makeMessage("assistant", "response"),
		},
		KeepRecentCount: 1,
	}

	result, err := summarizer.Summarize(context.Background(), input)
	require.NoError(t, err)
	assert.True(t, apiCalled, "API strategy handler should have been called")
	assert.Equal(t, "api summary", result.Summary)
}

// =============================================================================
// API STRATEGY: MESSAGE ROLE PRESERVATION
// =============================================================================

func TestSummarizeViaAPI_RolesPreserved(t *testing.T) {
	// Verify user/assistant/system roles are correctly forwarded
	server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		messages, _ := body["messages"].([]interface{})

		expectedRoles := []string{"system", "user", "assistant", "user", "assistant"}
		for i, msg := range messages {
			m, _ := msg.(map[string]interface{})
			assert.Equal(t, expectedRoles[i], m["role"], "message %d role mismatch", i)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(successResponse("summary", 500, 100, 3, 2, 0.8))
	})
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("system", "You are a helpful assistant"),
			makeMessage("user", "Hello"),
			makeMessage("assistant", "Hi!"),
			makeMessage("user", "Help me"),
			makeMessage("assistant", "Sure!"),
		},
		KeepRecentCount: 2,
	}

	_, err := summarizer.Summarize(context.Background(), input)
	require.NoError(t, err)
}

// =============================================================================
// API STRATEGY: COMPRESSION RATIO EDGE CASES
// =============================================================================

func TestSummarizeViaAPI_HighCompressionRatio(t *testing.T) {
	// 95% compression — only 5% of tokens remain
	server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(successResponse(
			"Brief summary",
			100000, 5000, 97, 3, 0.95,
		))
	})
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	msgs := make([]json.RawMessage, 100)
	for i := range msgs {
		if i%2 == 0 {
			msgs[i] = makeMessage("user", "question")
		} else {
			msgs[i] = makeMessage("assistant", "answer with lots of detail and code examples")
		}
	}

	result, err := summarizer.Summarize(context.Background(), preemptive.SummarizeInput{
		Messages:        msgs,
		KeepRecentCount: 3,
	})
	require.NoError(t, err)
	assert.Equal(t, 5000, result.SummaryTokens)
	assert.Equal(t, 100000, result.InputTokens)
}

func TestSummarizeViaAPI_ZeroCompressionRatio(t *testing.T) {
	// Edge case: no compression achieved (ratio = 0)
	server := mockCompresrServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(successResponse(
			"The full conversation content repeated as summary",
			1000, 1000, 0, 4, 0.0,
		))
	})
	defer server.Close()

	summarizer := newAPISummarizer(server.URL)
	input := preemptive.SummarizeInput{
		Messages: []json.RawMessage{
			makeMessage("user", "a"), makeMessage("assistant", "b"),
			makeMessage("user", "c"), makeMessage("assistant", "d"),
		},
		KeepRecentCount: 2,
	}

	result, err := summarizer.Summarize(context.Background(), preemptive.SummarizeInput{
		Messages:        input.Messages,
		KeepRecentCount: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, 1000, result.SummaryTokens)
}
