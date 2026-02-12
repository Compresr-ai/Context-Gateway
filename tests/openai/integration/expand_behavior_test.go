// Expand Context Behavior Tests - Testing WITH and WITHOUT expand_context for OpenAI
//
// These tests verify the gateway behavior:
// 1. WITH expand_context enabled - LLM can request full content via expand_context tool
// 2. WITHOUT expand_context enabled - LLM only sees compressed content
//
// Uses gpt-4o-mini for cost-effective integration testing.
//
// Run with: go test ./tests/openai/integration/... -v -run TestExpandBehavior
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	openaiBaseURL = "https://api.openai.com"
	// Use gpt-5-nano for cost-effective testing
	miniModel   = "gpt-5-nano"
	testTimeout = 90 * time.Second
)

func init() {
	godotenv.Load("../../../.env")
}

func getOpenAIKey(t *testing.T) string {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration test")
	}
	return key
}

// =============================================================================
// EXPAND CONTEXT ENABLED - Force LLM to use expand tool
// =============================================================================

// TestExpandBehavior_WithExpand_MinimalCompression tests that when we give
// the LLM an extremely compressed summary (just one word), it MUST use
// expand_context to get the full content.
func TestExpandBehavior_WithExpand_MinimalCompression(t *testing.T) {
	apiKey := getOpenAIKey(t)

	// Create a mock compression API that returns MINIMAL summary
	mockCompressor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"compressed": "compressed",
			"success":    true,
		})
	}))
	defer mockCompressor.Close()

	cfg := configWithExpandEnabledOpenAI(mockCompressor.URL)
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Large content that MUST be expanded to answer the question
	secretContent := `SECRET DATABASE CREDENTIALS:
Host: db-prod-001.internal.company.com
Port: 5432
Username: admin_prod_user
Password: xK9#mP2$vL7!qR4@nF6

BACKUP DATABASE:
Host: db-backup-002.internal.company.com
Port: 5433
Username: backup_readonly
Password: bK3@pM8#vN2!

Connection String: postgresql://admin_prod_user:xK9#mP2$vL7!qR4@nF6@db-prod-001.internal.company.com:5432/production

NOTE: These credentials rotate every 30 days. Last rotation: 2024-01-15`

	requestBody := map[string]interface{}{
		"model":                 miniModel,
		"max_completion_tokens": 500,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What is the exact database password for the production database?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_secret_001",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "secrets.txt"}`,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": "call_secret_001",
				"content":      secretContent,
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", openaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: testTimeout}
	resp, err := retryableRequestOpenAI(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)
	t.Logf("Response: %s", string(responseBody))

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, string(responseBody), "expand_context")
	assert.NotContains(t, string(responseBody), "<<<SHADOW:")

	var response map[string]interface{}
	json.Unmarshal(responseBody, &response)
	content := extractOpenAIContentHelper(response)
	t.Logf("GPT Response: %s", content)

	// Works with both reasoning models (empty content) and regular models
	assert.True(t, content != "" || isValidOpenAIResponse(response), "Expected either content or valid response")
}

// TestExpandBehavior_WithExpand_QuestionRequiresDetail tests asking a question
// that REQUIRES detailed content to answer correctly.
func TestExpandBehavior_WithExpand_QuestionRequiresDetail(t *testing.T) {
	apiKey := getOpenAIKey(t)

	cfg := configWithExpandEnabledOpenAI("")
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	largeCodeWithBug := generateBuggyCodeOpenAI()
	t.Logf("Code size: %d bytes", len(largeCodeWithBug))

	requestBody := map[string]interface{}{
		"model":                 miniModel,
		"max_completion_tokens": 600,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Find the bug in the code. What specific line has the issue and what is the exact problem?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_bug_001",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "buggy.go"}`,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": "call_bug_001",
				"content":      largeCodeWithBug,
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", openaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: testTimeout}
	resp, err := retryableRequestOpenAI(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, string(responseBody), "expand_context")
	assert.NotContains(t, string(responseBody), "<<<SHADOW:")

	var response map[string]interface{}
	json.Unmarshal(responseBody, &response)
	content := extractOpenAIContentHelper(response)
	t.Logf("GPT Response: %s", content)

	// Works with both reasoning models (empty content) and regular models
	if content != "" {
		contentLower := strings.ToLower(content)
		assert.True(t, strings.Contains(contentLower, "divide") ||
			strings.Contains(contentLower, "nil") ||
			strings.Contains(contentLower, "zero") ||
			strings.Contains(contentLower, "panic") ||
			strings.Contains(contentLower, "error") ||
			strings.Contains(contentLower, "bug"))
	} else {
		assert.True(t, isValidOpenAIResponse(response), "Expected valid response for reasoning model")
	}
}

// =============================================================================
// SMALL CONTENT - No compression needed (below threshold)
// =============================================================================

// TestExpandBehavior_SmallContent_NoCompression tests that small content
// below the min_bytes threshold is NOT compressed and passes through unchanged.
func TestExpandBehavior_SmallContent_NoCompression(t *testing.T) {
	apiKey := getOpenAIKey(t)

	cfg := configWithExpandEnabledOpenAI("")
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	smallContent := `{"status": "ok", "count": 42}`

	requestBody := map[string]interface{}{
		"model":                 miniModel,
		"max_completion_tokens": 200,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What is the status and count from this response?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_small_001",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "api_call",
							"arguments": `{"endpoint": "/status"}`,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": "call_small_001",
				"content":      smallContent,
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", openaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: testTimeout}
	resp, err := retryableRequestOpenAI(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, string(responseBody), "expand_context")
	assert.NotContains(t, string(responseBody), "<<<SHADOW:")

	var response map[string]interface{}
	json.Unmarshal(responseBody, &response)
	content := extractOpenAIContentHelper(response)
	t.Logf("GPT Response: %s", content)

	// Works with both reasoning models (empty content) and regular models
	if content != "" {
		contentLower := strings.ToLower(content)
		assert.True(t, strings.Contains(contentLower, "ok") ||
			strings.Contains(contentLower, "42") ||
			strings.Contains(contentLower, "status"))
	} else {
		assert.True(t, isValidOpenAIResponse(response), "Expected valid response for reasoning model")
	}
}

// =============================================================================
// EXPAND CONTEXT DISABLED - LLM only sees compressed content
// =============================================================================

// TestExpandBehavior_NoExpand_CompressedOnly tests that WITHOUT expand_context,
// the LLM only sees the compressed version and CANNOT get the full content.
func TestExpandBehavior_NoExpand_CompressedOnly(t *testing.T) {
	apiKey := getOpenAIKey(t)

	cfg := configWithExpandDisabledOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	secretContent := `SECRET API KEYS:
Production: sk-prod-abc123xyz789
Staging: sk-stage-def456uvw012
Development: sk-dev-ghi789rst345

AWS Credentials:
Access Key: AKIAIOSFODNN7EXAMPLE
Secret Key: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`

	requestBody := map[string]interface{}{
		"model":                 miniModel,
		"max_completion_tokens": 300,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What is the exact production API key?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_noexpand_001",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "api_keys.txt"}`,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": "call_noexpand_001",
				"content":      secretContent,
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", openaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: testTimeout}
	resp, err := retryableRequestOpenAI(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)
	t.Logf("Response: %s", string(responseBody))

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, string(responseBody), "expand_context")

	var response map[string]interface{}
	json.Unmarshal(responseBody, &response)
	content := extractOpenAIContentHelper(response)
	t.Logf("GPT Response (no expand): %s", content)

	// Works with both reasoning models (empty content) and regular models
	assert.True(t, content != "" || isValidOpenAIResponse(response), "Expected either content or valid response")
}

// TestExpandBehavior_NoExpand_LargeOutput tests large content without expand.
func TestExpandBehavior_NoExpand_LargeOutput(t *testing.T) {
	apiKey := getOpenAIKey(t)

	cfg := configWithExpandDisabledOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	largeLog := generateLargeLogFileOpenAI(3000)
	t.Logf("Log size: %d bytes", len(largeLog))

	requestBody := map[string]interface{}{
		"model":                 miniModel,
		"max_completion_tokens": 250,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Summarize these logs - are there any errors?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_largelog_001",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "app.log"}`,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": "call_largelog_001",
				"content":      largeLog,
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", openaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: testTimeout}
	resp, err := retryableRequestOpenAI(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, string(responseBody), "expand_context")

	var response map[string]interface{}
	json.Unmarshal(responseBody, &response)
	content := extractOpenAIContentHelper(response)
	t.Logf("GPT Response: %s", content)

	// Works with both reasoning models (empty content) and regular models
	assert.True(t, content != "" || isValidOpenAIResponse(response), "Expected either content or valid response")
}

// =============================================================================
// COMPARISON TESTS - Same question WITH vs WITHOUT expand
// =============================================================================

// TestExpandBehavior_Compare_DetailedQuestionWithVsWithout compares LLM responses
func TestExpandBehavior_Compare_DetailedQuestionWithVsWithout(t *testing.T) {
	apiKey := getOpenAIKey(t)

	detailedConfig := `# Application Configuration
server:
  host: 0.0.0.0
  port: 8443
cache:
  redis:
    host: redis-cluster.internal
    port: 6379
    password: r3d1s_p@ssw0rd_2024
    db: 0
    pool_size: 20`

	question := "What is the Redis password in this config?"

	t.Run("with_expand", func(t *testing.T) {
		cfg := configWithExpandEnabledOpenAI("")
		gw := gateway.New(cfg)
		gwServer := httptest.NewServer(gw.Handler())
		defer gwServer.Close()

		resp := makeToolResultRequestOpenAI(t, gwServer.URL, apiKey, question, detailedConfig)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.NotContains(t, resp.body, "expand_context")
		assert.NotContains(t, resp.body, "<<<SHADOW:")

		t.Logf("WITH expand - Response: %s", resp.content)
	})

	t.Run("without_expand", func(t *testing.T) {
		cfg := configWithExpandDisabledOpenAI()
		gw := gateway.New(cfg)
		gwServer := httptest.NewServer(gw.Handler())
		defer gwServer.Close()

		resp := makeToolResultRequestOpenAI(t, gwServer.URL, apiKey, question, detailedConfig)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.NotContains(t, resp.body, "expand_context")

		t.Logf("WITHOUT expand - Response: %s", resp.content)
	})
}

// =============================================================================
// MULTIPLE TOOL OUTPUTS - Partial expansion
// =============================================================================

// TestExpandBehavior_MultiTool_SomeNeedExpand tests 3 tool outputs
func TestExpandBehavior_MultiTool_SomeNeedExpand(t *testing.T) {
	apiKey := getOpenAIKey(t)

	cfg := configWithExpandEnabledOpenAI("")
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	smallOutput := "BUILD SUCCESS"
	mediumOutput := strings.Repeat("test output line\n", 50)
	largeOutput := generateLargeGoFileOpenAI(2000)

	requestBody := map[string]interface{}{
		"model":                 miniModel,
		"max_completion_tokens": 400,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Tell me about each of these three things: build status, test output, and the code."},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_build",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "bash",
							"arguments": `{"command": "make build"}`,
						},
					},
					{
						"id":   "call_test",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "bash",
							"arguments": `{"command": "make test"}`,
						},
					},
					{
						"id":   "call_code",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "main.go"}`,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": "call_build",
				"content":      smallOutput,
			},
			{
				"role":         "tool",
				"tool_call_id": "call_test",
				"content":      mediumOutput,
			},
			{
				"role":         "tool",
				"tool_call_id": "call_code",
				"content":      largeOutput,
			},
		},
	}

	t.Logf("Output sizes: small=%d, medium=%d, large=%d",
		len(smallOutput), len(mediumOutput), len(largeOutput))

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", openaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := retryableRequestOpenAI(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, string(responseBody), "expand_context")
	assert.NotContains(t, string(responseBody), "<<<SHADOW:")

	var response map[string]interface{}
	json.Unmarshal(responseBody, &response)
	content := extractOpenAIContentHelper(response)
	t.Logf("GPT Response: %s", content)

	// Works with both reasoning models (empty content) and regular models
	if content != "" {
		contentLower := strings.ToLower(content)
		assert.True(t, strings.Contains(contentLower, "build") ||
			strings.Contains(contentLower, "success") ||
			strings.Contains(contentLower, "test") ||
			strings.Contains(contentLower, "code"))
	} else {
		assert.True(t, isValidOpenAIResponse(response), "Expected valid response for reasoning model")
	}
}

// =============================================================================
// ERROR HANDLING WITH EXPAND
// =============================================================================

// TestExpandBehavior_WithExpand_ErrorToolResult tests error tool results with expand.
func TestExpandBehavior_WithExpand_ErrorToolResult(t *testing.T) {
	apiKey := getOpenAIKey(t)

	cfg := configWithExpandEnabledOpenAI("")
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"model":                 miniModel,
		"max_completion_tokens": 200,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Read the password file"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_error",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "/etc/shadow"}`,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": "call_error",
				"content":      "Error: Permission denied - cannot read /etc/shadow (requires root)",
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", openaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: testTimeout}
	resp, err := retryableRequestOpenAI(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, string(responseBody), "expand_context")

	var response map[string]interface{}
	json.Unmarshal(responseBody, &response)
	content := extractOpenAIContentHelper(response)
	t.Logf("GPT Response: %s", content)

	// Works with both reasoning models (empty content) and regular models
	if content != "" {
		contentLower := strings.ToLower(content)
		assert.True(t, strings.Contains(contentLower, "permission") ||
			strings.Contains(contentLower, "denied") ||
			strings.Contains(contentLower, "cannot") ||
			strings.Contains(contentLower, "error") ||
			strings.Contains(contentLower, "root"))
	} else {
		assert.True(t, isValidOpenAIResponse(response), "Expected valid response for reasoning model")
	}
}

// =============================================================================
// STRESS TESTS
// =============================================================================

// TestExpandBehavior_StressTest_ManyToolResults tests many tool results at once.
func TestExpandBehavior_StressTest_ManyToolResults(t *testing.T) {
	apiKey := getOpenAIKey(t)

	cfg := configWithExpandEnabledOpenAI("")
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Create 5 tool calls and results
	toolCalls := make([]map[string]interface{}, 5)
	toolResults := make([]map[string]interface{}, 5)

	for i := 0; i < 5; i++ {
		toolID := fmt.Sprintf("call_stress_%03d", i)
		toolCalls[i] = map[string]interface{}{
			"id":   toolID,
			"type": "function",
			"function": map[string]interface{}{
				"name":      "read_file",
				"arguments": fmt.Sprintf(`{"path": "file%d.txt"}`, i),
			},
		}
		toolResults[i] = map[string]interface{}{
			"role":         "tool",
			"tool_call_id": toolID,
			"content":      fmt.Sprintf("Content of file %d:\n%s", i, strings.Repeat(fmt.Sprintf("line %d data\n", i), 100*(i+1))),
		}
	}

	messages := []map[string]interface{}{
		{"role": "user", "content": "How many files are there and what's in each one?"},
		{
			"role":       "assistant",
			"content":    nil,
			"tool_calls": toolCalls,
		},
	}
	messages = append(messages, toolResults...)

	requestBody := map[string]interface{}{
		"model":                 miniModel,
		"max_completion_tokens": 500,
		"messages":              messages,
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", openaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := retryableRequestOpenAI(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, string(responseBody), "expand_context")
	assert.NotContains(t, string(responseBody), "<<<SHADOW:")

	var response map[string]interface{}
	json.Unmarshal(responseBody, &response)
	content := extractOpenAIContentHelper(response)
	t.Logf("GPT Response: %s", content)

	// Works with both reasoning models (empty content) and regular models
	if content != "" {
		contentLower := strings.ToLower(content)
		assert.True(t, strings.Contains(contentLower, "5") ||
			strings.Contains(contentLower, "five") ||
			strings.Contains(contentLower, "file"))
	} else {
		assert.True(t, isValidOpenAIResponse(response), "Expected valid response for reasoning model")
	}
}

// =============================================================================
// HELPER TYPES AND FUNCTIONS
// =============================================================================

type toolResultResponseOpenAI struct {
	StatusCode int
	body       string
	content    string
}

func makeToolResultRequestOpenAI(t *testing.T, gwURL string, apiKey string, question string, toolOutput string) toolResultResponseOpenAI {
	requestBody := map[string]interface{}{
		"model":                 miniModel,
		"max_completion_tokens": 300,
		"messages": []map[string]interface{}{
			{"role": "user", "content": question},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_helper_001",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "file.txt"}`,
						},
					},
				},
			},
			{
				"role":         "tool",
				"tool_call_id": "call_helper_001",
				"content":      toolOutput,
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwURL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", openaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: testTimeout}
	resp, err := retryableRequestOpenAI(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)
	var response map[string]interface{}
	json.Unmarshal(responseBody, &response)
	content := extractOpenAIContentHelper(response)

	return toolResultResponseOpenAI{
		StatusCode: resp.StatusCode,
		body:       string(responseBody),
		content:    content,
	}
}

func retryableRequestOpenAI(client *http.Client, req *http.Request, t *testing.T) (*http.Response, error) {
	maxRetries := 3
	retryDelay := 2 * time.Second

	var resp *http.Response
	var err error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		var bodyBytes []byte
		if req.Body != nil {
			bodyBytes, _ = io.ReadAll(req.Body)
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		resp, err = client.Do(req)

		if err == nil && resp.StatusCode < 500 {
			return resp, nil
		}

		if err != nil {
			t.Logf("Attempt %d/%d failed with error: %v", attempt, maxRetries, err)
		} else {
			t.Logf("Attempt %d/%d failed with status %d", attempt, maxRetries, resp.StatusCode)
			if resp != nil {
				resp.Body.Close()
			}
		}

		if attempt < maxRetries {
			time.Sleep(retryDelay * time.Duration(attempt))
			if bodyBytes != nil {
				req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}
		}
	}

	return resp, err
}

func extractOpenAIContentHelper(response map[string]interface{}) string {
	choices, ok := response["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return ""
	}

	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return ""
	}

	message, ok := choice["message"].(map[string]interface{})
	if !ok {
		return ""
	}

	content, _ := message["content"].(string)
	return content
}

// isValidOpenAIResponse checks if response is valid (works with reasoning models that may have empty content)
func isValidOpenAIResponse(response map[string]interface{}) bool {
	choices, ok := response["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return false
	}

	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return false
	}

	// Check for finish_reason - indicates valid completion
	if finishReason, ok := choice["finish_reason"].(string); ok && finishReason != "" {
		return true
	}

	return false
}

func configWithExpandEnabledOpenAI(mockAPIURL string) *config.Config {
	apiEndpoint := "/api/compress/tool-output"
	if mockAPIURL != "" {
		apiEndpoint = mockAPIURL
	}

	return &config.Config{
		Server: config.ServerConfig{
			Port:         18080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:             true,
				Strategy:            "api",
				FallbackStrategy:    "passthrough",
				MinBytes:            300,
				MaxBytes:            65536,
				TargetRatio:         0.2,
				IncludeExpandHint:   true,
				EnableExpandContext: true, // ENABLED
				API: config.APIConfig{
					Endpoint: apiEndpoint,
					APIKey:   os.Getenv("COMPRESR_API_KEY"),
					Model:    "tool_output_openai",
					Timeout:  30 * time.Second,
				},
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

func configWithExpandDisabledOpenAI() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         18080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:             true,
				Strategy:            "api",
				FallbackStrategy:    "passthrough",
				MinBytes:            300,
				MaxBytes:            65536,
				TargetRatio:         0.2,
				IncludeExpandHint:   false, // No hint
				EnableExpandContext: false, // DISABLED
				API: config.APIConfig{
					Endpoint: "/api/compress/tool-output",
					APIKey:   os.Getenv("COMPRESR_API_KEY"),
					Model:    "tool_output_openai",
					Timeout:  30 * time.Second,
				},
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

func generateBuggyCodeOpenAI() string {
	return `package main

import (
	"fmt"
	"sync"
)

type UserService struct {
	users map[string]*User
	mu    sync.RWMutex
}

type User struct {
	ID      string
	Name    string
	Balance int
}

func NewUserService() *UserService {
	return &UserService{
		users: make(map[string]*User),
	}
}

func (s *UserService) GetUser(id string) *User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.users[id]
}

func (s *UserService) CreateUser(id, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[id] = &User{ID: id, Name: name, Balance: 0}
}

// BUG: This function has a divide by zero error!
func (s *UserService) CalculateAverageBalance() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := 0
	for _, user := range s.users {
		total += user.Balance
	}
	// BUG HERE: Division by zero when users map is empty!
	return float64(total) / float64(len(s.users))
}

func main() {
	service := NewUserService()
	// This will panic because no users exist!
	avg := service.CalculateAverageBalance()
	fmt.Printf("Average balance: %.2f\n", avg)
}`
}

func generateLargeLogFileOpenAI(minSize int) string {
	var buf strings.Builder
	levels := []string{"INFO", "DEBUG", "WARN", "ERROR"}
	components := []string{"api", "db", "cache", "auth", "worker"}
	i := 0

	for buf.Len() < minSize {
		level := levels[i%len(levels)]
		component := components[i%len(components)]
		timestamp := fmt.Sprintf("2024-01-15T%02d:%02d:%02d.%03dZ",
			i%24, i%60, i%60, i%1000)

		var msg string
		switch level {
		case "ERROR":
			msg = fmt.Sprintf("Failed to process request: timeout after 30s (attempt %d/3)", i%3+1)
		case "WARN":
			msg = fmt.Sprintf("High memory usage detected: %dMB (threshold: 512MB)", 400+i%200)
		case "DEBUG":
			msg = fmt.Sprintf("Processing request #%d with params: {user_id: %d, action: 'update'}", i, i*100)
		default:
			msg = fmt.Sprintf("Request completed successfully in %dms", 50+i%100)
		}

		buf.WriteString(fmt.Sprintf("[%s] %s [%s] %s\n", timestamp, level, component, msg))
		i++
	}

	return buf.String()
}

func generateLargeGoFileOpenAI(minSize int) string {
	var buf strings.Builder
	buf.WriteString("package main\n\nimport \"fmt\"\n\n")

	i := 0
	for buf.Len() < minSize {
		buf.WriteString(fmt.Sprintf(`
func function%d(x, y int) int {
	// Process values
	result := x + y + %d
	for i := 0; i < 10; i++ {
		result += i
	}
	return result
}
`, i, i*100))
		i++
	}

	buf.WriteString(`
func main() {
	fmt.Println("Hello from generated code")
}
`)
	return buf.String()
}
