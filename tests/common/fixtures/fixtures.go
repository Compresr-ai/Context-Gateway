// Package fixtures provides test data and helpers for expand_context tests.
// These fixtures work for both Anthropic and OpenAI providers.
package fixtures

import (
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/store"
)

func init() {
	godotenv.Load("../../../.env")
	// Silence logs during tests
	zerolog.SetGlobalLevel(zerolog.Disabled)
	log.Logger = zerolog.New(io.Discard)
}

// =============================================================================
// TEST DATA
// =============================================================================

// LargeToolOutput is a large content that will be compressed aggressively.
// The simple strategy keeps only first N words, making it easy to verify expand_context.
const LargeToolOutput = `This is important information about the system configuration. It contains critical error details that need to be preserved. The log shows debugging information that might be useful later. Stack trace for the error that occurred shows the resolution steps that were taken. Additional context about the environment shows the final outcome of the debugging session. This contains metadata about the execution time and resources used.

Line 1: CRITICAL ERROR - Database connection failed with timeout after 30 seconds. Stack trace shows connection pool exhausted.
Line 2: WARNING - Memory usage at 95%, triggering garbage collection.
Line 3: INFO - Retry attempt 1 of 3 for database connection.
Line 4: ERROR - SSL certificate validation failed for external API endpoint.
Line 5: DEBUG - Request payload size: 2.3MB, response time: 450ms.
Line 6: INFO - Successfully reconnected to database after 45 seconds downtime.
Line 7: WARNING - Deprecated API endpoint called from legacy client version 2.1.
Line 8: ERROR - Rate limit exceeded for user_id=12345, blocking for 60 seconds.`

// SmallToolOutput is too small to compress (below threshold).
const SmallToolOutput = `{"status": "ok", "count": 42}`

// =============================================================================
// STORE HELPERS
// =============================================================================

// TestStore creates an in-memory store for testing.
func TestStore() store.Store {
	return store.NewMemoryStore(1 * time.Hour)
}

// PreloadedStore creates a store with pre-populated shadow content.
func PreloadedStore(content map[string]string) store.Store {
	st := store.NewMemoryStore(1 * time.Hour)
	for shadowID, c := range content {
		st.Set(shadowID, c)
	}
	return st
}

// =============================================================================
// CONFIG HELPERS
// =============================================================================

// SimpleCompressionConfig creates a config using simple compression strategy.
// This is for testing expand_context behavior with minimal compression.
func SimpleCompressionConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         18080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:             true,
				Strategy:            "simple", // Uses first-N-words compression
				FallbackStrategy:    "passthrough",
				MinBytes:            100, // Low threshold to trigger compression
				MaxBytes:            65536,
				TargetRatio:         0.1, // Aggressive compression
				IncludeExpandHint:   true,
				EnableExpandContext: true, // Key: enable expand_context
			},
			ToolDiscovery: config.ToolDiscoveryPipeConfig{
				Enabled: false,
			},
		},
		Store: config.StoreConfig{
			Type: "memory",
			TTL:  1 * time.Hour,
		},
		Monitoring: config.MonitoringConfig{
			LogLevel:  "debug",
			LogFormat: "json",
			LogOutput: "stdout",
		},
	}
}

// SimpleCompressionConfigNoExpand creates a config with compression but NO expand_context.
func SimpleCompressionConfigNoExpand() *config.Config {
	cfg := SimpleCompressionConfig()
	cfg.Pipes.ToolOutput.EnableExpandContext = false
	cfg.Pipes.ToolOutput.IncludeExpandHint = false
	return cfg
}

// PassthroughConfig creates a passthrough-only config (no compression).
func PassthroughConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         18080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:             false,
				Strategy:            "passthrough",
				FallbackStrategy:    "passthrough",
				MinBytes:            256,
				EnableExpandContext: false,
			},
			ToolDiscovery: config.ToolDiscoveryPipeConfig{
				Enabled: false,
			},
		},
		Store: config.StoreConfig{
			Type: "memory",
			TTL:  1 * time.Hour,
		},
		Monitoring: config.MonitoringConfig{
			LogLevel:  "error",
			LogFormat: "json",
			LogOutput: "stdout",
		},
	}
}

// =============================================================================
// API KEY HELPERS
// =============================================================================

// GetAnthropicKey returns the Anthropic API key or empty if not set.
func GetAnthropicKey() string {
	return os.Getenv("ANTHROPIC_API_KEY")
}

// GetOpenAIKey returns the OpenAI API key or empty if not set.
func GetOpenAIKey() string {
	return os.Getenv("OPENAI_API_KEY")
}

// =============================================================================
// REQUEST BUILDERS
// =============================================================================

// AnthropicToolResultRequest creates an Anthropic request with a tool result.
func AnthropicToolResultRequest(model string, toolOutput string) []byte {
	req := map[string]interface{}{
		"model":      model,
		"max_tokens": 1000,
		"messages": []map[string]interface{}{
			{
				"role":    "user",
				"content": "What are the key points from the log file?",
			},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_01test123",
						"name":  "read_file",
						"input": map[string]string{"path": "system.log"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_01test123",
						"content":     toolOutput,
					},
				},
			},
		},
	}
	data, _ := json.Marshal(req)
	return data
}

// OpenAIToolResultRequest creates an OpenAI Chat Completions request with a tool result.
func OpenAIToolResultRequest(model string, toolOutput string) []byte {
	req := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{
			{
				"role":    "user",
				"content": "What are the key points from the log file?",
			},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_test123",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "system.log"}`,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": "call_test123",
				"content":      toolOutput,
			},
		},
		"max_completion_tokens": 1000,
	}
	data, _ := json.Marshal(req)
	return data
}

// =============================================================================
// RESPONSE BUILDERS
// =============================================================================

// AnthropicResponseWithExpandCall creates an Anthropic response with expand_context tool call.
func AnthropicResponseWithExpandCall(toolUseID, shadowID string) []byte {
	resp := map[string]interface{}{
		"id":   "msg_001",
		"type": "message",
		"role": "assistant",
		"content": []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": "I need to see the full content to provide a proper analysis.",
			},
			map[string]interface{}{
				"type":  "tool_use",
				"id":    toolUseID,
				"name":  "expand_context",
				"input": map[string]interface{}{"id": shadowID},
			},
		},
		"stop_reason": "tool_use",
		"usage": map[string]interface{}{
			"input_tokens":  100,
			"output_tokens": 50,
		},
	}
	data, _ := json.Marshal(resp)
	return data
}

// OpenAIResponseWithExpandCall creates an OpenAI response with expand_context tool call.
func OpenAIResponseWithExpandCall(toolCallID, shadowID string) []byte {
	resp := map[string]interface{}{
		"id":      "chatcmpl-001",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "gpt-4",
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []interface{}{
						map[string]interface{}{
							"id":   toolCallID,
							"type": "function",
							"function": map[string]interface{}{
								"name":      "expand_context",
								"arguments": `{"id":"` + shadowID + `"}`,
							},
						},
					},
				},
				"finish_reason": "tool_calls",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     100,
			"completion_tokens": 50,
			"total_tokens":      150,
		},
	}
	data, _ := json.Marshal(resp)
	return data
}

// AnthropicFinalResponse creates an Anthropic final response (no expand calls).
func AnthropicFinalResponse(content string) []byte {
	resp := map[string]interface{}{
		"id":   "msg_002",
		"type": "message",
		"role": "assistant",
		"content": []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": content,
			},
		},
		"stop_reason": "end_turn",
		"usage": map[string]interface{}{
			"input_tokens":  200,
			"output_tokens": 100,
		},
	}
	data, _ := json.Marshal(resp)
	return data
}

// OpenAIFinalResponse creates an OpenAI final response (no expand calls).
func OpenAIFinalResponse(content string) []byte {
	resp := map[string]interface{}{
		"id":      "chatcmpl-002",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "gpt-4",
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     200,
			"completion_tokens": 100,
			"total_tokens":      300,
		},
	}
	data, _ := json.Marshal(resp)
	return data
}
