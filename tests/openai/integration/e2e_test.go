// OpenAI E2E Integration Tests - Real OpenAI API Calls
//
// These tests make REAL calls to OpenAI API through the gateway proxy,
// simulating exactly how GPT-based tools interact with the API.
//
// Requirements:
//   - OPENAI_API_KEY environment variable set in .env
//   - Network connectivity to OpenAI API
//
// Run with: go test ./tests/openai/integration/... -v -run TestE2E

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
	e2eOpenaiBaseURL = "https://api.openai.com"
	e2eOpenaiModel   = "gpt-4o-mini"
	maxRetriesE2E    = 3
	retryDelayE2E    = 2 * time.Second
)

func init() {
	godotenv.Load("../../../.env")
}

func getOpenAIKeyE2E(t *testing.T) string {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY not set, skipping E2E test")
	}
	return key
}

// =============================================================================
// TEST 1: Simple Chat - Basic Message
// =============================================================================

func TestE2E_OpenAI_SimpleChat(t *testing.T) {
	apiKey := getOpenAIKeyE2E(t)

	cfg := passthroughConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"model":      e2eOpenaiModel,
		"max_tokens": 50,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Say 'Hello from OpenAI test' and nothing else."},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", e2eOpenaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	content := extractOpenAIContentE2E(response)
	t.Logf("GPT Response: %s", content)
	assert.Contains(t, strings.ToLower(content), "hello")
}

// =============================================================================
// TEST 2: Simple Chat - Verify Usage Extraction
// =============================================================================

func TestE2E_OpenAI_UsageExtraction(t *testing.T) {
	apiKey := getOpenAIKeyE2E(t)

	cfg := passthroughConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"model":      e2eOpenaiModel,
		"max_tokens": 50,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Say 'test' and nothing else."},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", e2eOpenaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	// Verify usage is present in API response
	usage, ok := response["usage"].(map[string]interface{})
	require.True(t, ok, "response should have usage field")

	promptTokens, _ := usage["prompt_tokens"].(float64)
	completionTokens, _ := usage["completion_tokens"].(float64)
	t.Logf("Usage - Prompt Tokens: %.0f, Completion Tokens: %.0f", promptTokens, completionTokens)

	assert.Greater(t, promptTokens, float64(0), "should have prompt tokens")
	assert.Greater(t, completionTokens, float64(0), "should have completion tokens")
}

// =============================================================================
// TEST 3: Tool Result - Small File Read
// =============================================================================

func TestE2E_OpenAI_SmallToolResult(t *testing.T) {
	apiKey := getOpenAIKeyE2E(t)

	cfg := passthroughConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	toolOutput := `package main
func main() {
	fmt.Println("Hello, World!")
}`

	requestBody := map[string]interface{}{
		"model":      e2eOpenaiModel,
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What does this Go code do?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_001",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "main.go"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_001", "content": toolOutput},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", e2eOpenaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractOpenAIContentE2E(response)
	t.Logf("GPT Response: %s", content)
	assert.True(t, strings.Contains(strings.ToLower(content), "hello") ||
		strings.Contains(strings.ToLower(content), "print"))
}

// =============================================================================
// TEST 4: Large Tool Result - File Read with Compression
// =============================================================================

func TestE2E_OpenAI_LargeToolResultCompression(t *testing.T) {
	apiKey := getOpenAIKeyE2E(t)

	cfg := compressionConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	largeFile := generateLargeGoFileE2E(3000)
	t.Logf("Original file size: %d bytes", len(largeFile))

	requestBody := map[string]interface{}{
		"model":      e2eOpenaiModel,
		"max_tokens": 200,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Summarize what this code does in one sentence."},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_large_001",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "service.go"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_large_001", "content": largeFile},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", e2eOpenaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := retryableRequestE2E(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractOpenAIContentE2E(response)
	t.Logf("GPT Response: %s", content)
	assert.NotEmpty(t, content)
}

// =============================================================================
// TEST 5: Multiple Tool Results
// =============================================================================

func TestE2E_OpenAI_MultipleToolResults(t *testing.T) {
	apiKey := getOpenAIKeyE2E(t)

	cfg := passthroughConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"model":      e2eOpenaiModel,
		"max_tokens": 150,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What are these two files about?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_multi_001",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "README.md"}`,
						},
					},
					{
						"id":   "call_multi_002",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "main.go"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_multi_001", "content": "# My Project\nA simple Go application."},
			{"role": "tool", "tool_call_id": "call_multi_002", "content": "package main\n\nfunc main() { }"},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", e2eOpenaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractOpenAIContentE2E(response)
	t.Logf("GPT Response: %s", content)
	assert.NotEmpty(t, content)
}

// =============================================================================
// TEST 6: Directory Listing
// =============================================================================

func TestE2E_OpenAI_DirectoryListing(t *testing.T) {
	apiKey := getOpenAIKeyE2E(t)

	cfg := compressionConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	dirListing := generateLargeDirListingE2E(100)
	t.Logf("Directory listing size: %d bytes", len(dirListing))

	requestBody := map[string]interface{}{
		"model":      e2eOpenaiModel,
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "How many Go files are in this directory? Just give me the count."},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_dir_001",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "list_dir",
							"arguments": `{"path": "."}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_dir_001", "content": dirListing},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", e2eOpenaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := retryableRequestE2E(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractOpenAIContentE2E(response)
	t.Logf("GPT Response: %s", content)
	assert.NotEmpty(t, content)
}

// =============================================================================
// TEST 7: Bash Command Output
// =============================================================================

func TestE2E_OpenAI_BashCommandOutput(t *testing.T) {
	apiKey := getOpenAIKeyE2E(t)

	cfg := compressionConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	bashOutput := generateLargeBashOutputE2E(2000)
	t.Logf("Bash output size: %d bytes", len(bashOutput))

	requestBody := map[string]interface{}{
		"model":      e2eOpenaiModel,
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Were there any errors in the build output?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_bash_001",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "bash",
							"arguments": `{"command": "make build"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_bash_001", "content": bashOutput},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", e2eOpenaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := retryableRequestE2E(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractOpenAIContentE2E(response)
	t.Logf("GPT Response: %s", content)
	assert.NotEmpty(t, content)
}

// =============================================================================
// TEST 8: Error Tool Result
// =============================================================================

func TestE2E_OpenAI_ErrorToolResult(t *testing.T) {
	apiKey := getOpenAIKeyE2E(t)

	cfg := passthroughConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"model":      e2eOpenaiModel,
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Read the file please."},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_error_001",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "nonexistent.txt"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_error_001", "content": "Error: ENOENT: no such file or directory, open 'nonexistent.txt'"},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", e2eOpenaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractOpenAIContentE2E(response)
	t.Logf("GPT Response: %s", content)

	contentLower := strings.ToLower(content)
	assert.True(t,
		strings.Contains(contentLower, "error") ||
			strings.Contains(contentLower, "not found") ||
			strings.Contains(contentLower, "doesn't exist") ||
			strings.Contains(contentLower, "does not exist") ||
			strings.Contains(contentLower, "can't") ||
			strings.Contains(contentLower, "cannot"))
}

// =============================================================================
// TEST 9: Git Diff Output
// =============================================================================

func TestE2E_OpenAI_GitDiffOutput(t *testing.T) {
	apiKey := getOpenAIKeyE2E(t)

	cfg := compressionConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	gitDiff := generateLargeGitDiffE2E(2000)
	t.Logf("Git diff size: %d bytes", len(gitDiff))

	requestBody := map[string]interface{}{
		"model":      e2eOpenaiModel,
		"max_tokens": 150,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Summarize the changes in this diff."},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_diff_001",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "bash",
							"arguments": `{"command": "git diff HEAD~1"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_diff_001", "content": gitDiff},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", e2eOpenaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := retryableRequestE2E(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractOpenAIContentE2E(response)
	t.Logf("GPT Response: %s", content)
	assert.NotEmpty(t, content)
}

// =============================================================================
// TEST 10: JSON Tool Output
// =============================================================================

func TestE2E_OpenAI_JSONToolOutput(t *testing.T) {
	apiKey := getOpenAIKeyE2E(t)

	cfg := compressionConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	jsonOutput := generateLargeJSONOutputE2E(50)
	t.Logf("JSON output size: %d bytes", len(jsonOutput))

	requestBody := map[string]interface{}{
		"model":      e2eOpenaiModel,
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "How many users are there and what's the average age?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_json_001",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "api_request",
							"arguments": `{"endpoint": "/users"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_json_001", "content": jsonOutput},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", e2eOpenaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := retryableRequestE2E(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractOpenAIContentE2E(response)
	t.Logf("GPT Response: %s", content)
	assert.NotEmpty(t, content)
}

// =============================================================================
// TEST 11: Health Check
// =============================================================================

func TestE2E_OpenAI_HealthCheck(t *testing.T) {
	cfg := passthroughConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	resp, err := http.Get(gwServer.URL + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// =============================================================================
// TEST 12: With System Prompt
// =============================================================================

func TestE2E_OpenAI_WithSystemPrompt(t *testing.T) {
	apiKey := getOpenAIKeyE2E(t)

	cfg := passthroughConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"model":      e2eOpenaiModel,
		"max_tokens": 50,
		"messages": []map[string]interface{}{
			{"role": "system", "content": "You are a helpful assistant. Always respond in ALL CAPS."},
			{"role": "user", "content": "Say hello."},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", e2eOpenaiBaseURL+"/v1/chat/completions")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractOpenAIContentE2E(response)
	t.Logf("GPT Response: %s", content)
	assert.NotEmpty(t, content)
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

func retryableRequestE2E(client *http.Client, req *http.Request, t *testing.T) (*http.Response, error) {
	var resp *http.Response
	var err error

	for attempt := 1; attempt <= maxRetriesE2E; attempt++ {
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
			t.Logf("Attempt %d/%d failed with error: %v", attempt, maxRetriesE2E, err)
		} else {
			t.Logf("Attempt %d/%d failed with status %d", attempt, maxRetriesE2E, resp.StatusCode)
			if resp != nil {
				resp.Body.Close()
			}
		}

		if attempt < maxRetriesE2E {
			time.Sleep(retryDelayE2E * time.Duration(attempt))
			if bodyBytes != nil {
				req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}
		}
	}

	return resp, err
}

func extractOpenAIContentE2E(response map[string]interface{}) string {
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

func passthroughConfigOpenAI() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         18080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:          false,
				Strategy:         "passthrough",
				FallbackStrategy: "passthrough",
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

func compressionConfigOpenAI() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         18080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:             true,
				Strategy:            config.StrategyCompresr,
				FallbackStrategy:    "passthrough",
				MinBytes:            500,
				MaxBytes:            65536,
				TargetRatio:         0.3,
				IncludeExpandHint:   true,
				EnableExpandContext: true,
				Compresr: config.CompresrConfig{
					Endpoint:  os.Getenv("COMPRESR_API_URL") + "/api/compress/tool-output",
					APIKey: os.Getenv("COMPRESR_API_KEY"),
					Model:     "toc_espresso_v1",
					Timeout:   30 * time.Second,
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

func generateLargeGoFileE2E(minSize int) string {
	var buf strings.Builder
	buf.WriteString("package main\n\nimport \"fmt\"\n\n")

	i := 0
	for buf.Len() < minSize {
		buf.WriteString(fmt.Sprintf(`
func handler%d(w http.ResponseWriter, r *http.Request) {
	// Handle request %d
	data := r.URL.Query().Get("data")
	result := processData%d(data)
	json.NewEncoder(w).Encode(result)
}
`, i, i, i))
		i++
	}

	buf.WriteString("\nfunc main() {\n\tfmt.Println(\"Server starting...\")\n}\n")
	return buf.String()
}

func generateLargeDirListingE2E(numFiles int) string {
	var buf strings.Builder
	for i := 0; i < numFiles; i++ {
		ext := []string{".go", ".md", ".json", ".yaml", ".txt"}[i%5]
		buf.WriteString(fmt.Sprintf("file_%03d%s\n", i, ext))
	}
	return buf.String()
}

func generateLargeBashOutputE2E(minSize int) string {
	var buf strings.Builder
	i := 0
	for buf.Len() < minSize {
		buf.WriteString(fmt.Sprintf("==> Building package %d...\n", i))
		buf.WriteString(fmt.Sprintf("    Compiling module%d.go\n", i))
		buf.WriteString(fmt.Sprintf("    Linking dependencies for module%d\n", i))
		if i%5 == 0 {
			buf.WriteString(fmt.Sprintf("    Warning: deprecated function in module%d\n", i))
		}
		buf.WriteString(fmt.Sprintf("    Module %d completed successfully\n", i))
		i++
	}
	buf.WriteString("\n==> Build completed successfully!\n")
	return buf.String()
}

func generateLargeGitDiffE2E(minSize int) string {
	var buf strings.Builder
	i := 0
	for buf.Len() < minSize {
		buf.WriteString(fmt.Sprintf(`diff --git a/file%d.go b/file%d.go
index abc123..def456 100644
--- a/file%d.go
+++ b/file%d.go
@@ -10,6 +10,8 @@ func handler%d() {
-    oldCode := "old"
+    newCode := "new"
+    additionalCode := "added"
`, i, i, i, i, i))
		i++
	}
	return buf.String()
}

func generateLargeJSONOutputE2E(numUsers int) string {
	var users []map[string]interface{}
	for i := 0; i < numUsers; i++ {
		users = append(users, map[string]interface{}{
			"id":     i + 1,
			"name":   fmt.Sprintf("User %d", i+1),
			"email":  fmt.Sprintf("user%d@example.com", i+1),
			"age":    20 + (i % 50),
			"active": i%3 != 0,
		})
	}
	data, _ := json.MarshalIndent(map[string]interface{}{"users": users}, "", "  ")
	return string(data)
}
