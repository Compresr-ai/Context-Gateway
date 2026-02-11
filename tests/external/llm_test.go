package external_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/external"
)

// =============================================================================
// PROVIDER DETECTION
// =============================================================================

func TestDetectProvider(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		{"anthropic api", "https://api.anthropic.com/v1/messages", "anthropic"},
		{"anthropic in path", "https://proxy.example.com/anthropic/v1", "anthropic"},
		{"gemini api", "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent", "gemini"},
		{"openai api", "https://api.openai.com/v1/chat/completions", "openai"},
		{"localhost default", "http://localhost:8080/v1/chat/completions", "openai"},
		{"custom proxy", "https://my-proxy.com/v1/chat", "openai"},
		{"empty string", "", "openai"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, external.DetectProvider(tt.endpoint))
		})
	}
}

// =============================================================================
// VALIDATION
// =============================================================================

func TestCallLLM_Validation(t *testing.T) {
	t.Run("missing_endpoint", func(t *testing.T) {
		_, err := external.CallLLM(context.Background(), external.CallLLMParams{
			APIKey: "key", Model: "model",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "endpoint required")
	})

	t.Run("missing_api_key", func(t *testing.T) {
		_, err := external.CallLLM(context.Background(), external.CallLLMParams{
			Endpoint: "http://localhost", Model: "model",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "api key required")
	})

	t.Run("missing_model", func(t *testing.T) {
		_, err := external.CallLLM(context.Background(), external.CallLLMParams{
			Endpoint: "http://localhost", APIKey: "key",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "model required")
	})
}

// =============================================================================
// PER-PROVIDER MOCK SERVER TESTS
// =============================================================================

func TestCallLLM_Anthropic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "test-key", r.Header.Get("x-api-key"))
		assert.Equal(t, "2023-06-01", r.Header.Get("anthropic-version"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req external.AnthropicRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "claude-haiku-4-5", req.Model)
		assert.Equal(t, 1000, req.MaxTokens)
		assert.NotEmpty(t, req.System)
		assert.Len(t, req.Messages, 1)

		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": "compressed output"},
			},
			"usage": map[string]interface{}{
				"input_tokens": 100, "output_tokens": 20,
			},
		})
	}))
	defer server.Close()

	result, err := external.CallLLM(context.Background(), external.CallLLMParams{
		Endpoint:     server.URL,
		Provider:     "anthropic",
		APIKey:       "test-key",
		Model:        "claude-haiku-4-5",
		SystemPrompt: "compress this",
		UserPrompt:   "content to compress",
		MaxTokens:    1000,
	})
	require.NoError(t, err)
	assert.Equal(t, "compressed output", result.Content)
	assert.Equal(t, "anthropic", result.Provider)
	assert.Equal(t, 100, result.InputTokens)
	assert.Equal(t, 20, result.OutputTokens)
}

func TestCallLLM_OpenAI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		var req external.OpenAIChatRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "gpt-4o-mini", req.Model)
		assert.Len(t, req.Messages, 2)
		assert.Equal(t, "system", req.Messages[0].Role)
		assert.Equal(t, "user", req.Messages[1].Role)

		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"content": "compressed output"}},
			},
			"usage": map[string]interface{}{
				"prompt_tokens": 80, "completion_tokens": 15,
			},
		})
	}))
	defer server.Close()

	result, err := external.CallLLM(context.Background(), external.CallLLMParams{
		Endpoint:     server.URL,
		Provider:     "openai",
		APIKey:       "test-key",
		Model:        "gpt-4o-mini",
		SystemPrompt: "compress this",
		UserPrompt:   "content to compress",
		MaxTokens:    1000,
	})
	require.NoError(t, err)
	assert.Equal(t, "compressed output", result.Content)
	assert.Equal(t, "openai", result.Provider)
	assert.Equal(t, 80, result.InputTokens)
	assert.Equal(t, 15, result.OutputTokens)
}

func TestCallLLM_Gemini(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-key", r.Header.Get("x-goog-api-key"))

		var req external.GeminiRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		require.NotNil(t, req.SystemInstruction)
		assert.Len(t, req.Contents, 1)
		assert.Equal(t, "user", req.Contents[0].Role)

		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"candidates": []map[string]interface{}{
				{"content": map[string]interface{}{
					"parts": []map[string]interface{}{
						{"text": "compressed output"},
					},
				}},
			},
			"usageMetadata": map[string]interface{}{
				"promptTokenCount": 90, "candidatesTokenCount": 18,
			},
		})
	}))
	defer server.Close()

	result, err := external.CallLLM(context.Background(), external.CallLLMParams{
		Endpoint:     server.URL,
		Provider:     "gemini",
		APIKey:       "test-key",
		Model:        "gemini-2.0-flash",
		SystemPrompt: "compress this",
		UserPrompt:   "content to compress",
		MaxTokens:    1000,
	})
	require.NoError(t, err)
	assert.Equal(t, "compressed output", result.Content)
	assert.Equal(t, "gemini", result.Provider)
	assert.Equal(t, 90, result.InputTokens)
	assert.Equal(t, 18, result.OutputTokens)
}

// =============================================================================
// EXPLICIT PROVIDER OVERRIDE
// =============================================================================

func TestCallLLM_ExplicitProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// URL has "anthropic" but Provider override says "openai"
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		assert.Empty(t, r.Header.Get("x-api-key"))

		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"content": "ok"}},
			},
			"usage": map[string]interface{}{},
		})
	}))
	defer server.Close()

	// URL looks like anthropic but Provider override says openai
	result, err := external.CallLLM(context.Background(), external.CallLLMParams{
		Endpoint:     server.URL + "/anthropic/v1",
		Provider:     "openai",
		APIKey:       "test-key",
		Model:        "gpt-4o",
		SystemPrompt: "test",
		UserPrompt:   "test",
		MaxTokens:    100,
	})
	require.NoError(t, err)
	assert.Equal(t, "openai", result.Provider)
}

// =============================================================================
// ERROR HANDLING
// =============================================================================

func TestCallLLM_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"error": "rate limited"}`))
	}))
	defer server.Close()

	_, err := external.CallLLM(context.Background(), external.CallLLMParams{
		Endpoint: server.URL, Provider: "openai",
		APIKey: "key", Model: "m", MaxTokens: 100,
		SystemPrompt: "s", UserPrompt: "u",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "429")
	assert.Contains(t, err.Error(), "rate limited")
}

func TestCallLLM_ErrorBodyTruncation(t *testing.T) {
	longError := strings.Repeat("x", 1000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(longError))
	}))
	defer server.Close()

	_, err := external.CallLLM(context.Background(), external.CallLLMParams{
		Endpoint: server.URL, Provider: "openai",
		APIKey: "key", Model: "m", MaxTokens: 100,
		SystemPrompt: "s", UserPrompt: "u",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "truncated")
	assert.Less(t, len(err.Error()), 700) // well under 1000
}

func TestCallLLM_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	_, err := external.CallLLM(context.Background(), external.CallLLMParams{
		Endpoint: server.URL, Provider: "anthropic",
		APIKey: "key", Model: "m", MaxTokens: 100,
		SystemPrompt: "s", UserPrompt: "u",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse")
}

func TestCallLLM_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(200)
	}))
	defer server.Close()

	_, err := external.CallLLM(context.Background(), external.CallLLMParams{
		Endpoint: server.URL, Provider: "openai",
		APIKey: "key", Model: "m", MaxTokens: 100,
		SystemPrompt: "s", UserPrompt: "u",
		Timeout: 100 * time.Millisecond,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request failed")
}

func TestCallLLM_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	_, err := external.CallLLM(ctx, external.CallLLMParams{
		Endpoint: "http://localhost:99999", Provider: "openai",
		APIKey: "key", Model: "m", MaxTokens: 100,
		SystemPrompt: "s", UserPrompt: "u",
	})
	require.Error(t, err)
}

// =============================================================================
// CONCURRENCY
// =============================================================================

func TestCallLLM_Concurrent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": "ok"},
			},
			"usage": map[string]interface{}{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer server.Close()

	var wg sync.WaitGroup
	errors := make([]error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errors[idx] = external.CallLLM(context.Background(), external.CallLLMParams{
				Endpoint: server.URL, Provider: "anthropic",
				APIKey: "key", Model: "m", MaxTokens: 100,
				SystemPrompt: "s", UserPrompt: "u",
			})
		}(i)
	}

	wg.Wait()
	for i, err := range errors {
		assert.NoError(t, err, "goroutine %d failed", i)
	}
}

// =============================================================================
// DEFAULT TIMEOUT
// =============================================================================

func TestCallLLM_DefaultTimeout(t *testing.T) {
	// Verify that params with zero timeout get the default
	params := external.CallLLMParams{
		Endpoint: "http://localhost", APIKey: "key", Model: "m",
	}
	// We can't call validate() directly, but we can verify the constant
	assert.Equal(t, 60*time.Second, external.DefaultTimeout)
	_ = params // just verifying constant exists
}
