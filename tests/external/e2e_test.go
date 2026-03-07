package external_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/external"
	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
	"github.com/compresr/context-gateway/internal/store"
)

// TestExternalProvider_E2E_OpenAI tests end-to-end compression flow with OpenAI.
func TestExternalProvider_E2E_OpenAI(t *testing.T) {
	t.Run("full_compression_flow_openai", func(t *testing.T) {
		var receivedReq external.OpenAIChatRequest

		// Mock OpenAI compression server
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &receivedReq)

			// Verify it's OpenAI format
			assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			// Return OpenAI response
			resp := external.OpenAIChatResponse{
				ID:      "chatcmpl-123",
				Object:  "chat.completion",
				Created: time.Now().Unix(),
				Model:   receivedReq.Model,
				Choices: []struct {
					Index   int `json:"index"`
					Message struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"message"`
					FinishReason string `json:"finish_reason"`
				}{
					{
						Index: 0,
						Message: struct {
							Role    string `json:"role"`
							Content string `json:"content"`
						}{
							Role:    "assistant",
							Content: "Compressed: files found",
						},
						FinishReason: "stop",
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		// Create pipe with external_provider strategy
		st := store.NewMemoryStore(time.Hour)
		pipe := tooloutput.New(cfg(server.URL), st)

		// Create OpenAI request with tool output
		openaiReq := map[string]interface{}{
			"model": "gpt-5",
			"messages": []map[string]interface{}{
				{"role": "user", "content": "list the files"},
				{"role": "assistant", "content": nil, "tool_calls": []map[string]interface{}{
					{
						"id":   "call_123",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "bash",
							"arguments": `{"command": "ls -la"}`,
						},
					},
				}},
				{"role": "tool", "tool_call_id": "call_123", "content": "drwxr-xr-x 15 user staff 480 main.go\n-rw-r--r-- 1 user staff 1234 utils.go\n-rw-r--r-- 1 user staff 5678 config.go"},
			},
		}
		reqBody, _ := json.Marshal(openaiReq)

		// Create context with OpenAI adapter
		adapter := adapters.NewOpenAIAdapter()
		ctx := pipes.NewPipeContext(adapter, reqBody)

		result, err := pipe.Process(ctx)
		require.NoError(t, err)
		assert.NotNil(t, result)

		// Verify OpenAI request was built correctly
		assert.Equal(t, "gpt-5-nano", receivedReq.Model)
		assert.Len(t, receivedReq.Messages, 2)
		assert.Equal(t, "system", receivedReq.Messages[0].Role)
		assert.Equal(t, "user", receivedReq.Messages[1].Role)
		assert.Contains(t, receivedReq.Messages[1].Content, "bash")
	})

	t.Run("query_agnostic_mode_openai", func(t *testing.T) {
		var receivedReq external.OpenAIChatRequest

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &receivedReq)

			resp := external.OpenAIChatResponse{
				Choices: []struct {
					Index   int `json:"index"`
					Message struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"message"`
					FinishReason string `json:"finish_reason"`
				}{
					{
						Message: struct {
							Role    string `json:"role"`
							Content string `json:"content"`
						}{
							Role:    "assistant",
							Content: "Compressed output",
						},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		st := store.NewMemoryStore(time.Hour)
		pipe := tooloutput.New(cfgQueryAgnostic(server.URL), st)

		openaiReq := map[string]interface{}{
			"model": "gpt-5",
			"messages": []map[string]interface{}{
				{"role": "user", "content": "what's the status?"},
				{"role": "assistant", "content": nil, "tool_calls": []map[string]interface{}{
					{"id": "call_1", "type": "function", "function": map[string]interface{}{"name": "bash", "arguments": "{}"}},
				}},
				{"role": "tool", "tool_call_id": "call_1", "content": "Process running (PID 1234), memory usage 512MB, uptime 3 days"},
			},
		}
		reqBody, _ := json.Marshal(openaiReq)

		adapter := adapters.NewOpenAIAdapter()
		ctx := pipes.NewPipeContext(adapter, reqBody)

		_, err := pipe.Process(ctx)
		require.NoError(t, err)

		// Verify query agnostic system prompt was used
		assert.Contains(t, receivedReq.Messages[0].Content, "essential information structure")
		assert.NotContains(t, receivedReq.Messages[1].Content, "User's Question:")
	})
}

// TestExternalProvider_E2E_Anthropic tests end-to-end compression flow with Anthropic.
func TestExternalProvider_E2E_Anthropic(t *testing.T) {
	t.Run("full_compression_flow_anthropic", func(t *testing.T) {
		var receivedReq external.AnthropicRequest

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &receivedReq)

			// Verify Anthropic headers
			assert.Equal(t, "test-key", r.Header.Get("x-api-key"))
			assert.Equal(t, "2023-06-01", r.Header.Get("anthropic-version"))

			// Return Anthropic response
			resp := external.AnthropicResponse{
				ID:   "msg_123",
				Type: "message",
				Role: "assistant",
				Content: []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}{
					{Type: "text", Text: "Compressed: code found"},
				},
				Model:      receivedReq.Model,
				StopReason: "end_turn",
			}
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		// Note: endpoint contains "anthropic" to trigger Anthropic format detection
		st := store.NewMemoryStore(time.Hour)
		pipe := tooloutput.New(cfgAnthropic(server.URL+"/anthropic/v1/messages"), st)

		// Create Anthropic request with tool result
		anthropicReq := map[string]interface{}{
			"model":      "claude-sonnet-4-5",
			"max_tokens": 4096,
			"messages": []map[string]interface{}{
				{"role": "user", "content": "read the config file"},
				{"role": "assistant", "content": []map[string]interface{}{
					{
						"type": "tool_use",
						"id":   "toolu_123",
						"name": "str_replace_editor",
						"input": map[string]interface{}{
							"command": "view",
							"path":    "/app/config.yaml",
						},
					},
				}},
				{"role": "user", "content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_123",
						"content":     "Viewing /app/config.yaml (6 lines) - contains server config with port 8080, localhost binding, postgres DB with pool_size 10",
					},
				}},
			},
		}
		reqBody, _ := json.Marshal(anthropicReq)

		adapter := adapters.NewAnthropicAdapter()
		ctx := pipes.NewPipeContext(adapter, reqBody)

		result, err := pipe.Process(ctx)
		require.NoError(t, err)
		assert.NotNil(t, result)

		// Verify Anthropic request was built correctly
		assert.Equal(t, "claude-haiku-4-5", receivedReq.Model)
		assert.Len(t, receivedReq.Messages, 1)
		assert.Equal(t, "user", receivedReq.Messages[0].Role)
		assert.Contains(t, receivedReq.System, "relevant to the user's question")
	})

	t.Run("handles_multiple_tool_results_anthropic", func(t *testing.T) {
		var callCount int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&callCount, 1)
			resp := external.AnthropicResponse{
				Content: []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}{
					{Type: "text", Text: "Compressed output"},
				},
			}
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		st := store.NewMemoryStore(time.Hour)
		pipe := tooloutput.New(cfgAnthropic(server.URL+"/anthropic/messages"), st)

		anthropicReq := map[string]interface{}{
			"model": "claude-sonnet-4-5",
			"messages": []map[string]interface{}{
				{"role": "user", "content": "run both commands"},
				{"role": "assistant", "content": []map[string]interface{}{
					{"type": "tool_use", "id": "toolu_1", "name": "bash", "input": map[string]interface{}{}},
					{"type": "tool_use", "id": "toolu_2", "name": "bash", "input": map[string]interface{}{}},
				}},
				{"role": "user", "content": []map[string]interface{}{
					{"type": "tool_result", "tool_use_id": "toolu_1", "content": "output from first command with lots of detail"},
					{"type": "tool_result", "tool_use_id": "toolu_2", "content": "output from second command with more content"},
				}},
			},
		}
		reqBody, _ := json.Marshal(anthropicReq)

		adapter := adapters.NewAnthropicAdapter()
		ctx := pipes.NewPipeContext(adapter, reqBody)

		_, err := pipe.Process(ctx)
		require.NoError(t, err)

		// Both tool outputs should be compressed
		assert.Equal(t, int32(2), atomic.LoadInt32(&callCount))
	})
}

// TestExternalProvider_ErrorHandling tests error scenarios.
func TestExternalProvider_ErrorHandling(t *testing.T) {
	t.Run("handles_api_error_response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]interface{}{
					"message": "Rate limit exceeded",
					"type":    "rate_limit_error",
				},
			})
		}))
		defer server.Close()

		st := store.NewMemoryStore(time.Hour)
		pipe := tooloutput.New(cfgWithFallback(server.URL), st)

		openaiReq := map[string]interface{}{
			"model": "gpt-5",
			"messages": []map[string]interface{}{
				{"role": "user", "content": "test"},
				{"role": "assistant", "tool_calls": []map[string]interface{}{
					{"id": "call_1", "type": "function", "function": map[string]interface{}{"name": "bash", "arguments": "{}"}},
				}},
				{"role": "tool", "tool_call_id": "call_1", "content": "large output that needs compression here"},
			},
		}
		reqBody, _ := json.Marshal(openaiReq)

		adapter := adapters.NewOpenAIAdapter()
		ctx := pipes.NewPipeContext(adapter, reqBody)

		result, err := pipe.Process(ctx)
		require.NoError(t, err)
		// Should fall back to passthrough
		assert.NotNil(t, result)
	})

	t.Run("handles_timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(2 * time.Second) // Delay longer than timeout
		}))
		defer server.Close()

		st := store.NewMemoryStore(time.Hour)
		pipe := tooloutput.New(cfgWithTimeout(server.URL), st)

		openaiReq := map[string]interface{}{
			"model": "gpt-5",
			"messages": []map[string]interface{}{
				{"role": "user", "content": "test"},
				{"role": "assistant", "tool_calls": []map[string]interface{}{
					{"id": "call_1", "type": "function", "function": map[string]interface{}{"name": "bash", "arguments": "{}"}},
				}},
				{"role": "tool", "tool_call_id": "call_1", "content": "content that will timeout during compression"},
			},
		}
		reqBody, _ := json.Marshal(openaiReq)

		adapter := adapters.NewOpenAIAdapter()
		ctx := pipes.NewPipeContext(adapter, reqBody)

		result, err := pipe.Process(ctx)
		// Should fall back to passthrough due to timeout
		require.NoError(t, err)
		assert.NotNil(t, result)
	})

	t.Run("skips_small_content", func(t *testing.T) {
		callCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
		}))
		defer server.Close()

		st := store.NewMemoryStore(time.Hour)
		pipe := tooloutput.New(cfgHighMinBytes(server.URL), st)

		openaiReq := map[string]interface{}{
			"model": "gpt-5",
			"messages": []map[string]interface{}{
				{"role": "user", "content": "test"},
				{"role": "assistant", "tool_calls": []map[string]interface{}{
					{"id": "call_1", "type": "function", "function": map[string]interface{}{"name": "bash", "arguments": "{}"}},
				}},
				{"role": "tool", "tool_call_id": "call_1", "content": "small"}, // Too small
			},
		}
		reqBody, _ := json.Marshal(openaiReq)

		adapter := adapters.NewOpenAIAdapter()
		ctx := pipes.NewPipeContext(adapter, reqBody)

		_, err := pipe.Process(ctx)
		require.NoError(t, err)

		// No API call should be made
		assert.Equal(t, 0, callCount)
	})
}

// TestExternalProvider_LargeContent tests with realistic large content.
func TestExternalProvider_LargeContent(t *testing.T) {
	t.Run("compresses_large_file_output", func(t *testing.T) {
		var receivedBody []byte

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedBody, _ = io.ReadAll(r.Body)

			resp := external.OpenAIChatResponse{
				Choices: []struct {
					Index   int `json:"index"`
					Message struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"message"`
					FinishReason string `json:"finish_reason"`
				}{
					{
						Message: struct {
							Role    string `json:"role"`
							Content string `json:"content"`
						}{
							Content: "Summary: Go package with main function and imports",
						},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		st := store.NewMemoryStore(time.Hour)
		pipe := tooloutput.New(cfg(server.URL), st)

		// Large file content
		largeContent := `package main

import (
	"fmt"
	"net/http"
	"encoding/json"
	"database/sql"
	"context"
	"time"
)

// User represents a user in the system
type User struct {
	ID        int64
	Name      string
	Email     string
	CreatedAt time.Time
}

// UserService handles user operations
type UserService struct {
	db *sql.DB
}

// GetUser retrieves a user by ID
func (s *UserService) GetUser(ctx context.Context, id int64) (*User, error) {
	var user User
	err := s.db.QueryRowContext(ctx, "SELECT id, name, email, created_at FROM users WHERE id = $1", id).
		Scan(&user.ID, &user.Name, &user.Email, &user.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	return &user, nil
}

// CreateUser creates a new user
func (s *UserService) CreateUser(ctx context.Context, name, email string) (*User, error) {
	var user User
	err := s.db.QueryRowContext(ctx,
		"INSERT INTO users (name, email, created_at) VALUES ($1, $2, $3) RETURNING id, name, email, created_at",
		name, email, time.Now(),
	).Scan(&user.ID, &user.Name, &user.Email, &user.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}
	return &user, nil
}

func main() {
	http.HandleFunc("/users", handleUsers)
	fmt.Println("Starting server...")
	http.ListenAndServe(":18080", nil)
}

func handleUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}`

		openaiReq := map[string]interface{}{
			"model": "gpt-5",
			"messages": []map[string]interface{}{
				{"role": "user", "content": "show me the user service code"},
				{"role": "assistant", "tool_calls": []map[string]interface{}{
					{"id": "call_1", "type": "function", "function": map[string]interface{}{"name": "read_file", "arguments": `{"path": "user.go"}`}},
				}},
				{"role": "tool", "tool_call_id": "call_1", "content": largeContent},
			},
		}
		reqBody, _ := json.Marshal(openaiReq)

		adapter := adapters.NewOpenAIAdapter()
		ctx := pipes.NewPipeContext(adapter, reqBody)

		result, err := pipe.Process(ctx)
		require.NoError(t, err)
		assert.NotNil(t, result)

		// Verify the request was sent with the content
		assert.True(t, len(receivedBody) > 0)
		assert.True(t, bytes.Contains(receivedBody, []byte("user service")))
	})
}

// TestStructuredPrefix_E2E tests the structured verbatim prefix pipeline end-to-end.
func TestStructuredPrefix_E2E(t *testing.T) {
	t.Run("large_json_gets_verbatim_prefix_and_compressed_tail", func(t *testing.T) {
		var receivedReq external.OpenAIChatRequest

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &receivedReq)

			// Return a short compressed summary (must be shorter than the tail)
			resp := external.OpenAIChatResponse{
				Choices: []struct {
					Index   int `json:"index"`
					Message struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"message"`
					FinishReason string `json:"finish_reason"`
				}{
					{Message: struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					}{Role: "assistant", Content: "10 more items with same schema"}},
				},
			}
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		// MinBytes=55: first JSON object ends with }, at ~position 53, within search zone
		c := cfg(server.URL)
		c.Pipes.ToolOutput.MinBytes = 55
		st := store.NewMemoryStore(time.Hour)
		pipe := tooloutput.New(c, st)

		// Build a JSON array large enough that prefix+separator+compressed < original.
		// Need ~400+ bytes so overhead (prefix ~40 + separator ~50 + summary ~30) is well under total.
		jsonContent := `[` +
			`{"id":1,"name":"alpha","status":"active","value":100},` +
			`{"id":2,"name":"bravo","status":"active","value":200},` +
			`{"id":3,"name":"charlie","status":"paused","value":300},` +
			`{"id":4,"name":"delta","status":"active","value":400},` +
			`{"id":5,"name":"echo","status":"active","value":500},` +
			`{"id":6,"name":"foxtrot","status":"paused","value":600},` +
			`{"id":7,"name":"golf","status":"active","value":700},` +
			`{"id":8,"name":"hotel","status":"active","value":800}]`

		openaiReq := map[string]interface{}{
			"model": "gpt-5",
			"messages": []map[string]interface{}{
				{"role": "user", "content": "list items"},
				{"role": "assistant", "content": nil, "tool_calls": []map[string]interface{}{
					{"id": "call_1", "type": "function", "function": map[string]interface{}{"name": "bash", "arguments": "{}"}},
				}},
				{"role": "tool", "tool_call_id": "call_1", "content": jsonContent},
			},
		}
		reqBody, _ := json.Marshal(openaiReq)

		adapter := adapters.NewOpenAIAdapter()
		ctx := pipes.NewPipeContext(adapter, reqBody)

		result, err := pipe.Process(ctx)
		require.NoError(t, err)
		assert.NotNil(t, result)

		// Verify structured tail prompt was used (not the standard one)
		assert.Contains(t, receivedReq.Messages[0].Content, "TAIL portion")
		assert.Contains(t, receivedReq.Messages[1].Content, "Format: json")

		// Verify the compressed output contains verbatim prefix + separator
		// Extract the tool result from the processed request
		var processed map[string]interface{}
		json.Unmarshal(result, &processed)
		msgs := processed["messages"].([]interface{})
		toolMsg := msgs[len(msgs)-1].(map[string]interface{})
		compressedContent := toolMsg["content"].(string)

		assert.Contains(t, compressedContent, tooloutput.StructuredSeparator)

		// Verbatim prefix should start with valid JSON
		parts := strings.SplitN(compressedContent, tooloutput.StructuredSeparator, 2)
		prefix := strings.TrimSpace(parts[0])
		assert.True(t, strings.HasPrefix(prefix, "["), "prefix should start with JSON array")
		// Prefix should end at a JSON boundary
		lastChar := prefix[len(prefix)-1]
		assert.Contains(t, ",}]", string(lastChar), "prefix should end at JSON boundary")
	})

	t.Run("small_structured_content_passthrough", func(t *testing.T) {
		callCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			// Should not be called — content is too small for compression
			resp := external.OpenAIChatResponse{
				Choices: []struct {
					Index   int `json:"index"`
					Message struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"message"`
					FinishReason string `json:"finish_reason"`
				}{
					{Message: struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					}{Role: "assistant", Content: "summary"}},
				},
			}
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		// MinBytes=50: content 30 bytes < 50 → passthrough before even reaching compression
		c := cfg(server.URL)
		c.Pipes.ToolOutput.MinBytes = 50
		st := store.NewMemoryStore(time.Hour)
		pipe := tooloutput.New(c, st)

		// Small JSON — below min_bytes threshold
		smallJSON := `{"status":"ok","count":3}`

		openaiReq := map[string]interface{}{
			"model": "gpt-5",
			"messages": []map[string]interface{}{
				{"role": "user", "content": "check status"},
				{"role": "assistant", "content": nil, "tool_calls": []map[string]interface{}{
					{"id": "call_1", "type": "function", "function": map[string]interface{}{"name": "bash", "arguments": "{}"}},
				}},
				{"role": "tool", "tool_call_id": "call_1", "content": smallJSON},
			},
		}
		reqBody, _ := json.Marshal(openaiReq)

		adapter := adapters.NewOpenAIAdapter()
		ctx := pipes.NewPipeContext(adapter, reqBody)

		result, err := pipe.Process(ctx)
		require.NoError(t, err)
		assert.NotNil(t, result)

		// Verify content passed through unchanged (no compression, no separator)
		var processed map[string]interface{}
		json.Unmarshal(result, &processed)
		msgs := processed["messages"].([]interface{})
		toolMsg := msgs[len(msgs)-1].(map[string]interface{})
		content := toolMsg["content"].(string)

		assert.Equal(t, smallJSON, content, "small structured content should pass through unchanged")
		assert.NotContains(t, content, tooloutput.StructuredSeparator)
		assert.Equal(t, 0, callCount, "no LLM call should be made for small content")
	})
}

// Helper functions for creating test configs

func cfg(endpoint string) *config.Config {
	return &config.Config{
		Pipes: pipes.Config{
			ToolOutput: pipes.ToolOutputConfig{
				Enabled:          true,
				Strategy:         pipes.StrategyExternalProvider,
				FallbackStrategy: pipes.StrategyPassthrough,
				MinBytes:         10,
				Compresr: pipes.CompresrConfig{
					Endpoint:      endpoint,
					AuthParam:     "test-key",
					Model:         "gpt-5-nano",
					Timeout:       30 * time.Second,
					QueryAgnostic: false,
				},
			},
		},
	}
}

func cfgQueryAgnostic(endpoint string) *config.Config {
	c := cfg(endpoint)
	c.Pipes.ToolOutput.Compresr.QueryAgnostic = true
	return c
}

func cfgAnthropic(endpoint string) *config.Config {
	return &config.Config{
		Pipes: pipes.Config{
			ToolOutput: pipes.ToolOutputConfig{
				Enabled:          true,
				Strategy:         pipes.StrategyExternalProvider,
				FallbackStrategy: pipes.StrategyPassthrough,
				MinBytes:         10,
				Compresr: pipes.CompresrConfig{
					Endpoint:      endpoint,
					AuthParam:     "test-key",
					Model:         "claude-haiku-4-5",
					Timeout:       30 * time.Second,
					QueryAgnostic: false,
				},
			},
		},
	}
}

func cfgWithFallback(endpoint string) *config.Config {
	return &config.Config{
		Pipes: pipes.Config{
			ToolOutput: pipes.ToolOutputConfig{
				Enabled:          true,
				Strategy:         pipes.StrategyExternalProvider,
				FallbackStrategy: pipes.StrategyPassthrough,
				MinBytes:         10,
				Compresr: pipes.CompresrConfig{
					Endpoint:  endpoint,
					AuthParam: "key",
					Model:     "gpt-5-nano",
					Timeout:   5 * time.Second,
				},
			},
		},
	}
}

func cfgWithTimeout(endpoint string) *config.Config {
	return &config.Config{
		Pipes: pipes.Config{
			ToolOutput: pipes.ToolOutputConfig{
				Enabled:          true,
				Strategy:         pipes.StrategyExternalProvider,
				FallbackStrategy: pipes.StrategyPassthrough,
				MinBytes:         10,
				Compresr: pipes.CompresrConfig{
					Endpoint:  endpoint,
					AuthParam: "key",
					Model:     "gpt-5-nano",
					Timeout:   100 * time.Millisecond, // Short timeout
				},
			},
		},
	}
}

func cfgHighMinBytes(endpoint string) *config.Config {
	return &config.Config{
		Pipes: pipes.Config{
			ToolOutput: pipes.ToolOutputConfig{
				Enabled:  true,
				Strategy: pipes.StrategyExternalProvider,
				MinBytes: 1000, // High threshold
				Compresr: pipes.CompresrConfig{
					Endpoint:  endpoint,
					AuthParam: "key",
					Model:     "gpt-5-nano",
					Timeout:   5 * time.Second,
				},
			},
		},
	}
}
