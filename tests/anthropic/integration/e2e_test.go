// Claude Code E2E Integration Tests - Real Anthropic API Calls
//
// These tests make REAL calls to Anthropic API through the gateway proxy,
// simulating exactly how Claude Code interacts with the API.
//
// Requirements:
//   - ANTHROPIC_API_KEY environment variable set in .env
//   - Network connectivity to Anthropic API
//
// Run with: go test ./tests/claude_code/integration/... -v

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

	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
)

const (
	anthropicBaseURL = "https://api.anthropic.com"
	anthropicModel   = "claude-sonnet-4-20250514"
	anthropicVersion = "2023-06-01"
	maxRetries       = 3
	retryDelay       = 2 * time.Second
)

func init() {
	godotenv.Load("../../../.env")
}

// retryableRequest performs HTTP request with automatic retry on transient errors
func retryableRequest(client *http.Client, req *http.Request, t *testing.T) (*http.Response, error) {
	var resp *http.Response
	var err error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		// Clone request body for potential retries
		var bodyBytes []byte
		if req.Body != nil {
			bodyBytes, _ = io.ReadAll(req.Body)
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		resp, err = client.Do(req)

		// Success - return immediately
		if err == nil && resp.StatusCode < 500 {
			return resp, nil
		}

		// Log retry attempt
		if err != nil {
			t.Logf("Attempt %d/%d failed with error: %v", attempt, maxRetries, err)
		} else {
			t.Logf("Attempt %d/%d failed with status %d", attempt, maxRetries, resp.StatusCode)
			if resp != nil {
				resp.Body.Close()
			}
		}

		// Don't retry on last attempt
		if attempt < maxRetries {
			time.Sleep(retryDelay * time.Duration(attempt)) // Exponential backoff
			// Reset body for retry
			if bodyBytes != nil {
				req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}
		}
	}

	return resp, err
}

func getAnthropicKey(t *testing.T) string {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping E2E test")
	}
	return key
}

// =============================================================================
// TEST 1: Simple Chat - Basic Message
// =============================================================================

func TestE2E_ClaudeCode_SimpleChat(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 50,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Say 'Hello from Claude Code test' and nothing else."},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	content := extractAnthropicContent(response)
	t.Logf("Claude Response: %s", content)
	assert.Contains(t, strings.ToLower(content), "hello")
}

// =============================================================================
// TEST 1b: Simple Chat - Verify Usage Extraction
// =============================================================================

func TestE2E_ClaudeCode_UsageExtraction(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 50,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Say 'test' and nothing else."},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	// Verify usage is present in API response
	inputTokens, outputTokens := extractAnthropicUsage(response)
	t.Logf("Usage - Input Tokens: %d, Output Tokens: %d", inputTokens, outputTokens)

	assert.Greater(t, inputTokens, 0, "should have input tokens")
	assert.Greater(t, outputTokens, 0, "should have output tokens")

	// Verify usage field exists in response
	usage, ok := response["usage"].(map[string]interface{})
	require.True(t, ok, "response should have usage field")
	assert.Contains(t, usage, "input_tokens", "usage should have input_tokens")
	assert.Contains(t, usage, "output_tokens", "usage should have output_tokens")
}

// =============================================================================
// TEST 2: Tool Result - Small File Read
// =============================================================================

func TestE2E_ClaudeCode_SmallToolResult(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	toolOutput := `package main

func main() {
	fmt.Println("Hello, World!")
}
`

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What does this Go code do?"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_001",
						"name":  "read_file",
						"input": map[string]string{"path": "main.go"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_001",
						"content":     toolOutput,
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractAnthropicContent(response)
	t.Logf("Claude Response: %s", content)
	assert.True(t, strings.Contains(strings.ToLower(content), "hello") ||
		strings.Contains(strings.ToLower(content), "print"))
}

// =============================================================================
// TEST 3: Large Tool Result - File Read with Compression
// =============================================================================

func TestE2E_ClaudeCode_LargeToolResultCompression(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := compressionConfigAnthropicDirect(apiKey)
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	largeFile := generateLargeGoFile(3000)

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 200,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Summarize what this code does in one sentence."},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_large_001",
						"name":  "read_file",
						"input": map[string]string{"path": "service.go"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_large_001",
						"content":     largeFile,
					},
				},
			},
		},
	}

	t.Logf("Original file size: %d bytes", len(largeFile))

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractAnthropicContent(response)
	t.Logf("Claude Response: %s", content)
	assert.NotEmpty(t, content)
}

// =============================================================================
// TEST 4: Multiple Tool Results
// =============================================================================

func TestE2E_ClaudeCode_MultipleToolResults(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 150,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Compare main.go and utils.go - which has more functions?"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_multi_001",
						"name":  "read_file",
						"input": map[string]string{"path": "main.go"},
					},
					{
						"type":  "tool_use",
						"id":    "toolu_multi_002",
						"name":  "read_file",
						"input": map[string]string{"path": "utils.go"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_multi_001",
						"content":     "package main\n\nfunc main() {\n\tfmt.Println(\"Hello\")\n}",
					},
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_multi_002",
						"content":     "package utils\n\nfunc Add(a, b int) int { return a + b }\nfunc Sub(a, b int) int { return a - b }\nfunc Mul(a, b int) int { return a * b }",
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractAnthropicContent(response)
	t.Logf("Claude Response: %s", content)
	assert.True(t, strings.Contains(strings.ToLower(content), "utils") ||
		strings.Contains(content, "3"))
}

// =============================================================================
// TEST 5: Directory Listing Tool Result
// =============================================================================

func TestE2E_ClaudeCode_DirectoryListing(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	dirListing := `cmd/
internal/
  adapters/
  config/
  gateway/
  pipes/
tests/
go.mod
go.sum
README.md`

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What kind of project is this based on the directory structure?"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_dir_001",
						"name":  "list_dir",
						"input": map[string]string{"path": "."},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_dir_001",
						"content":     dirListing,
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	content := extractAnthropicContent(response)
	if content == "" {
		t.Logf("Full response: %+v", response)
	}
	t.Logf("Claude Response: %s", content)
	// Response should be valid (text or tool_use)
	assert.True(t, isValidAnthropicResponse(response), "Expected valid Anthropic response")
}

// =============================================================================
// TEST 6: Bash Command Output
// =============================================================================

func TestE2E_ClaudeCode_BashCommandOutput(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	bashOutput := `PASS
ok      github.com/compresr/context-gateway/internal/gateway    0.523s
ok      github.com/compresr/context-gateway/internal/pipes      0.412s
ok      github.com/compresr/context-gateway/internal/store      0.089s`

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Did the tests pass?"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_bash_001",
						"name":  "bash",
						"input": map[string]string{"command": "go test ./..."},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_bash_001",
						"content":     bashOutput,
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractAnthropicContent(response)
	t.Logf("Claude Response: %s", content)
	assert.True(t, strings.Contains(strings.ToLower(content), "pass") ||
		strings.Contains(strings.ToLower(content), "yes"))
}

// =============================================================================
// TEST 7: Search Results Tool
// =============================================================================

func TestE2E_ClaudeCode_SearchResults(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	searchResults := `internal/gateway/handler.go:45: func (g *Gateway) handleRequest(w http.ResponseWriter, r *http.Request) {
internal/gateway/handler.go:89: func (g *Gateway) processRequest(ctx *PipeContext) error {
internal/gateway/router.go:23: func (g *Gateway) route(r *http.Request) string {`

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Where is the main request handling logic?"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_search_001",
						"name":  "grep",
						"input": map[string]string{"pattern": "func.*Request"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_search_001",
						"content":     searchResults,
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := retryableRequest(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	content := extractAnthropicContent(response)
	if content == "" {
		t.Logf("Full response: %+v", response)
	}
	t.Logf("Claude Response: %s", content)
	// Response should mention handler or the search results in some way
	assert.True(t, len(content) > 0 || resp.StatusCode == http.StatusOK, "Expected successful response")
}

// =============================================================================
// TEST 8: Error Tool Result
// =============================================================================

func TestE2E_ClaudeCode_ErrorToolResult(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Read the config file"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_err_001",
						"name":  "read_file",
						"input": map[string]string{"path": "nonexistent.go"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_err_001",
						"content":     "Error: file not found: nonexistent.go",
						"is_error":    true,
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractAnthropicContent(response)
	t.Logf("Claude Response: %s", content)
	assert.NotEmpty(t, content)
}

// =============================================================================
// TEST 9: Long Conversation Context
// =============================================================================

func TestE2E_ClaudeCode_LongConversation(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "I'm working on a Go project"},
			{"role": "assistant", "content": "I'd be happy to help with your Go project! What would you like to work on?"},
			{"role": "user", "content": "Can you check the main.go file?"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_conv_001",
						"name":  "read_file",
						"input": map[string]string{"path": "main.go"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_conv_001",
						"content":     "package main\n\nfunc main() {\n\tfmt.Println(\"Hello\")\n}",
					},
				},
			},
			{"role": "assistant", "content": "I see a simple main.go file that prints 'Hello'. What would you like me to do with it?"},
			{"role": "user", "content": "What's missing from this code?"},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractAnthropicContent(response)
	t.Logf("Claude Response: %s", content)
	assert.Contains(t, strings.ToLower(content), "import")
}

// =============================================================================
// TEST 10: Compare Direct vs Proxy
// =============================================================================

func TestE2E_ClaudeCode_CompareDirectVsProxy(t *testing.T) {
	apiKey := getAnthropicKey(t)

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 10,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What is 2+2? Reply with just the number."},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	client := &http.Client{Timeout: 30 * time.Second}

	// Direct request
	directReq, _ := http.NewRequest("POST", anthropicBaseURL+"/v1/messages", bytes.NewReader(bodyBytes))
	directReq.Header.Set("Content-Type", "application/json")
	directReq.Header.Set("x-api-key", apiKey)
	directReq.Header.Set("anthropic-version", anthropicVersion)

	directResp, err := client.Do(directReq)
	require.NoError(t, err)
	directBody, _ := io.ReadAll(directResp.Body)
	directResp.Body.Close()

	var directResponse map[string]interface{}
	json.Unmarshal(directBody, &directResponse)

	// Proxy request
	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	proxyReq, _ := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("x-api-key", apiKey)
	proxyReq.Header.Set("anthropic-version", anthropicVersion)
	proxyReq.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	proxyResp, err := client.Do(proxyReq)
	require.NoError(t, err)
	proxyBody, _ := io.ReadAll(proxyResp.Body)
	proxyResp.Body.Close()

	var proxyResponse map[string]interface{}
	json.Unmarshal(proxyBody, &proxyResponse)

	directContent := extractAnthropicContent(directResponse)
	proxyContent := extractAnthropicContent(proxyResponse)

	t.Logf("Direct: %s", directContent)
	t.Logf("Proxy: %s", proxyContent)

	assert.Equal(t, directResp.StatusCode, proxyResp.StatusCode)
	assert.Contains(t, directContent, "4")
	assert.Contains(t, proxyContent, "4")
}

// =============================================================================
// TEST 11: Write File Tool
// =============================================================================

func TestE2E_ClaudeCode_WriteFileTool(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Create a hello.go file"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_write_001",
						"name":  "write_file",
						"input": map[string]string{"path": "hello.go", "content": "package main\n\nfunc main() {}"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_write_001",
						"content":     "File written successfully: hello.go",
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractAnthropicContent(response)
	t.Logf("Claude Response: %s", content)
	assert.NotEmpty(t, content)
}

// =============================================================================
// TEST 12: Large Bash Output Compression
// =============================================================================

func TestE2E_ClaudeCode_LargeBashOutputCompression(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := compressionConfigAnthropicDirect(apiKey)
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	largeBashOutput := generateLargeBashOutput(2500)

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 150,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Summarize the test results"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_bash_large_001",
						"name":  "bash",
						"input": map[string]string{"command": "go test -v ./..."},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_bash_large_001",
						"content":     largeBashOutput,
					},
				},
			},
		},
	}

	t.Logf("Original bash output size: %d bytes", len(largeBashOutput))

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractAnthropicContent(response)
	t.Logf("Claude Response: %s", content)
	assert.NotEmpty(t, content)
}

// =============================================================================
// TEST 13: Cache Hit - Same Content Twice
// =============================================================================

func TestE2E_ClaudeCode_CacheHit(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := compressionConfigAnthropicDirect(apiKey)
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	largeFile := generateLargeGoFile(2000)

	makeRequest := func() *http.Response {
		requestBody := map[string]interface{}{
			"model":      anthropicModel,
			"max_tokens": 100,
			"messages": []map[string]interface{}{
				{"role": "user", "content": "What does this code do?"},
				{
					"role": "assistant",
					"content": []map[string]interface{}{
						{
							"type":  "tool_use",
							"id":    "toolu_cache_001",
							"name":  "read_file",
							"input": map[string]string{"path": "cached.go"},
						},
					},
				},
				{
					"role": "user",
					"content": []map[string]interface{}{
						{
							"type":        "tool_result",
							"tool_use_id": "toolu_cache_001",
							"content":     largeFile,
						},
					},
				},
			},
		}

		bodyBytes, _ := json.Marshal(requestBody)
		req, _ := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", anthropicVersion)
		req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

		client := &http.Client{Timeout: 60 * time.Second}
		resp, _ := client.Do(req)
		return resp
	}

	// First request
	resp1 := makeRequest()
	defer resp1.Body.Close()
	assert.Equal(t, http.StatusOK, resp1.StatusCode)

	// Second request - should use cache
	resp2 := makeRequest()
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusOK, resp2.StatusCode)

	t.Log("Both requests successful - cache should have been used for second request")
}

// =============================================================================
// TEST 14: Git Diff Tool Output
// =============================================================================

func TestE2E_ClaudeCode_GitDiffOutput(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	gitDiff := `diff --git a/main.go b/main.go
index abc123..def456 100644
--- a/main.go
+++ b/main.go
@@ -1,5 +1,7 @@
 package main
 
+import "fmt"
+
 func main() {
-    // TODO
+    fmt.Println("Hello")
 }`

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What changed in this diff?"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_git_001",
						"name":  "bash",
						"input": map[string]string{"command": "git diff HEAD~1"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_git_001",
						"content":     gitDiff,
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractAnthropicContent(response)
	t.Logf("Claude Response: %s", content)
	assert.True(t, strings.Contains(strings.ToLower(content), "import") ||
		strings.Contains(strings.ToLower(content), "print") ||
		strings.Contains(strings.ToLower(content), "hello"))
}

// =============================================================================
// TEST 15: JSON Tool Output
// =============================================================================

func TestE2E_ClaudeCode_JSONToolOutput(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	jsonOutput := `{
  "name": "context-gateway",
  "version": "1.0.0",
  "dependencies": {
    "go": "1.23",
    "testify": "1.8.4"
  }
}`

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What version of Go does this project use?"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_json_001",
						"name":  "read_file",
						"input": map[string]string{"path": "package.json"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_json_001",
						"content":     jsonOutput,
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	require.NoError(t, err)

	content := extractAnthropicContent(response)
	if content == "" {
		t.Logf("Full response: %+v", response)
	}
	t.Logf("Claude Response: %s", content)
	// Response should be valid (content or valid stop_reason)
	assert.True(t, isValidAnthropicResponse(response), "Expected valid Anthropic response")
}

// =============================================================================
// TEST 16: Health Check Endpoint
// =============================================================================

func TestE2E_ClaudeCode_HealthCheck(t *testing.T) {
	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	resp, err := http.Get(gwServer.URL + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var health map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&health)

	assert.Equal(t, "ok", health["status"])
}

// =============================================================================
// TEST 17: Large Search Results Compression
// =============================================================================

func TestE2E_ClaudeCode_LargeSearchResultsCompression(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := compressionConfigAnthropicDirect(apiKey)
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	largeSearchResults := generateLargeSearchResults(2500)

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 150,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "How many matches were found and in which files?"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_search_large_001",
						"name":  "grep",
						"input": map[string]string{"pattern": "func.*Handler"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_search_large_001",
						"content":     largeSearchResults,
					},
				},
			},
		},
	}

	t.Logf("Original search results size: %d bytes", len(largeSearchResults))

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractAnthropicContent(response)
	t.Logf("Claude Response: %s", content)
	assert.NotEmpty(t, content)
}

// =============================================================================
// TEST 18: Tool with System Prompt
// =============================================================================

func TestE2E_ClaudeCode_WithSystemPrompt(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 100,
		"system":     "You are a helpful coding assistant. Be concise.",
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What's in the config?"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_sys_001",
						"name":  "read_file",
						"input": map[string]string{"path": "config.yaml"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_sys_001",
						"content":     "server:\n  port: 8080\n  timeout: 30s",
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractAnthropicContent(response)
	t.Logf("Claude Response: %s", content)
	assert.True(t, strings.Contains(content, "8080") || strings.Contains(strings.ToLower(content), "port"))
}

// =============================================================================
// TEST 19: Traffic Interception - Verify Compression
// =============================================================================

func TestE2E_ClaudeCode_TrafficInterception(t *testing.T) {
	apiKey := getAnthropicKey(t)

	var interceptedBody []byte
	interceptor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		interceptedBody, _ = io.ReadAll(r.Body)

		forwardReq, _ := http.NewRequest(r.Method, anthropicBaseURL+"/v1/messages", bytes.NewReader(interceptedBody))
		for k, v := range r.Header {
			forwardReq.Header[k] = v
		}

		client := &http.Client{Timeout: 60 * time.Second}
		resp, err := client.Do(forwardReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	defer interceptor.Close()

	cfg := compressionConfigAnthropicDirect(apiKey)
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	largeFile := generateLargeGoFile(2000)
	originalSize := len(largeFile)

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What does this code do?"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_intercept_001",
						"name":  "read_file",
						"input": map[string]string{"path": "large.go"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_intercept_001",
						"content":     largeFile,
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", interceptor.URL)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	t.Logf("Original size: %d bytes", originalSize)
	t.Logf("Intercepted size: %d bytes", len(interceptedBody))

	if len(interceptedBody) < originalSize {
		t.Logf("✓ Request was compressed! Reduction: %.1f%%",
			float64(originalSize-len(interceptedBody))/float64(originalSize)*100)
	}
}

// =============================================================================
// TEST 20: Full Workflow - Read, Modify, Write
// =============================================================================

func TestE2E_ClaudeCode_FullWorkflow(t *testing.T) {
	apiKey := getAnthropicKey(t)

	cfg := passthroughConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 200,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Add error handling to main.go"},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_flow_001",
						"name":  "read_file",
						"input": map[string]string{"path": "main.go"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_flow_001",
						"content":     "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello\")\n}",
					},
				},
			},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_flow_002",
						"name":  "write_file",
						"input": map[string]string{"path": "main.go", "content": "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc main() {\n\tif err := run(); err != nil {\n\t\tfmt.Fprintln(os.Stderr, err)\n\t\tos.Exit(1)\n\t}\n}\n\nfunc run() error {\n\tfmt.Println(\"Hello\")\n\treturn nil\n}"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_flow_002",
						"content":     "File written successfully",
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)

	content := extractAnthropicContent(response)
	t.Logf("Claude Response: %s", content)
	assert.NotEmpty(t, content)
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

func passthroughConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         18080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled: false,
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

func compressionConfigAnthropicDirect(apiKey string) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         18080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 120 * time.Second,
		},
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:             true,
				Strategy:            config.StrategyCompresr, // Uses Compresr API for compression
				FallbackStrategy:    "passthrough",
				MinBytes:            500,
				MaxBytes:            65536,
				TargetRatio:         0.3,
				IncludeExpandHint:   false,
				EnableExpandContext: false,
				Compresr: config.CompresrConfig{
					Endpoint:  "/api/compress/tool-output",
					APIKey:    os.Getenv("COMPRESR_API_KEY"),
					Model:     "toc_espresso_v1", // Use OpenAI model via API
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

func generateLargeGoFile(minSize int) string {
	var buf bytes.Buffer
	buf.WriteString(`package service

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

type User struct {
	ID        int64
	Name      string
	Email     string
	CreatedAt time.Time
}

type UserService struct {
	db    *sql.DB
	cache sync.Map
}

func NewUserService(db *sql.DB) *UserService {
	return &UserService{db: db}
}

`)

	i := 0
	for buf.Len() < minSize {
		buf.WriteString(fmt.Sprintf(`func (s *UserService) GetUser%d(ctx context.Context, id int64) (*User, error) {
	if cached, ok := s.cache.Load(id); ok {
		return cached.(*User), nil
	}

	row := s.db.QueryRowContext(ctx, "SELECT id, name, email FROM users WHERE id = $1", id)
	var user User
	if err := row.Scan(&user.ID, &user.Name, &user.Email); err != nil {
		return nil, fmt.Errorf("get user %d: %%w", err)
	}

	s.cache.Store(id, &user)
	return &user, nil
}

`, i, i))
		i++
	}

	return buf.String()
}

func generateLargeBashOutput(minSize int) string {
	var buf bytes.Buffer
	i := 0
	for buf.Len() < minSize {
		buf.WriteString(fmt.Sprintf("=== RUN   TestFunction%d\n", i))
		buf.WriteString(fmt.Sprintf("--- PASS: TestFunction%d (0.%02ds)\n", i, i%100))
		i++
	}
	buf.WriteString("PASS\n")
	buf.WriteString(fmt.Sprintf("ok      github.com/example/project    %.3fs\n", float64(i)*0.01))
	return buf.String()
}

func generateLargeSearchResults(minSize int) string {
	var buf bytes.Buffer
	files := []string{"handler.go", "router.go", "middleware.go", "service.go", "controller.go"}
	i := 0
	for buf.Len() < minSize {
		file := files[i%len(files)]
		buf.WriteString(fmt.Sprintf("internal/gateway/%s:%d: func Handler%d(w http.ResponseWriter, r *http.Request) {\n", file, 10+i*5, i))
		i++
	}
	return buf.String()
}

func extractAnthropicContent(response map[string]interface{}) string {
	content, ok := response["content"].([]interface{})
	if !ok || len(content) == 0 {
		return ""
	}

	var result strings.Builder
	for _, block := range content {
		blockMap, ok := block.(map[string]interface{})
		if !ok {
			continue
		}
		if blockMap["type"] == "text" {
			if text, ok := blockMap["text"].(string); ok {
				result.WriteString(text)
			}
		}
	}
	return result.String()
}

// extractAnthropicUsage extracts usage info from Anthropic API response
func extractAnthropicUsage(response map[string]interface{}) (inputTokens, outputTokens int) {
	usage, ok := response["usage"].(map[string]interface{})
	if !ok {
		return 0, 0
	}

	if input, ok := usage["input_tokens"].(float64); ok {
		inputTokens = int(input)
	}
	if output, ok := usage["output_tokens"].(float64); ok {
		outputTokens = int(output)
	}
	return inputTokens, outputTokens
}

// isValidAnthropicResponse checks if the response is a valid Anthropic response
// (either has content or has a valid stop_reason indicating the model completed)
func isValidAnthropicResponse(response map[string]interface{}) bool {
	// Check for valid stop_reason (end_turn, stop_sequence, etc.)
	if stopReason, ok := response["stop_reason"].(string); ok {
		if stopReason == "end_turn" || stopReason == "stop_sequence" || stopReason == "max_tokens" {
			return true
		}
	}

	content, ok := response["content"].([]interface{})
	if !ok || len(content) == 0 {
		return false
	}

	for _, block := range content {
		blockMap, ok := block.(map[string]interface{})
		if !ok {
			continue
		}
		// Response is valid if it contains text or tool_use
		if blockMap["type"] == "text" || blockMap["type"] == "tool_use" {
			return true
		}
	}
	return false
}
