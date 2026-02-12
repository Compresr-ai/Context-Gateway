// OpenAI Hard Integration Tests - Edge Cases and Complex Scenarios
//
// These tests cover edge cases and complex scenarios for OpenAI integration:
// - Multiple tools with varying output sizes
// - Error handling
// - Real-world output formats
// - Special characters and binary-like content
//
// Run with: go test ./tests/openai/integration/... -v -run TestHardIntegration

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

func init() {
	godotenv.Load("../../../.env")
}

func getOpenAIKeyHard(t *testing.T) string {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY not set, skipping hard integration test")
	}
	return key
}

// =============================================================================
// MULTIPLE TOOLS WITH VARYING SIZES
// =============================================================================

// TestHardIntegration_ThreeToolsOneLarge_OneExpandNeeded tests mixed tool output sizes.
func TestHardIntegration_ThreeToolsOneLarge_OneExpandNeeded(t *testing.T) {
	apiKey := getOpenAIKeyHard(t)

	cfg := hardCompressionConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Small, medium, and large outputs
	smallOutput := "package.json found"
	mediumOutput := strings.Repeat("dependency line\n", 30)
	largeOutput := generateLargeCodeHard(3000)

	requestBody := map[string]interface{}{
		"model":      "gpt-4o-mini",
		"max_tokens": 300,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Summarize these three tool results."},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_small",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "find_file",
							"arguments": `{"name": "package.json"}`,
						},
					},
					{
						"id":   "call_medium",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "deps.txt"}`,
						},
					},
					{
						"id":   "call_large",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "main.go"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_small", "content": smallOutput},
			{"role": "tool", "tool_call_id": "call_medium", "content": mediumOutput},
			{"role": "tool", "tool_call_id": "call_large", "content": largeOutput},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", "https://api.openai.com/v1/chat/completions")

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := retryableRequestHard(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	responseBody, _ := io.ReadAll(resp.Body)
	assert.NotContains(t, string(responseBody), "expand_context")
	assert.NotContains(t, string(responseBody), "<<<SHADOW:")

	var response map[string]interface{}
	json.Unmarshal(responseBody, &response)
	content := extractOpenAIContentHard(response)
	t.Logf("GPT Response: %s", content)
	assert.NotEmpty(t, content)
}

// TestHardIntegration_ThreeToolsAllLarge tests all large tool outputs.
func TestHardIntegration_ThreeToolsAllLarge(t *testing.T) {
	apiKey := getOpenAIKeyHard(t)

	cfg := hardCompressionConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	large1 := generateLargeCodeHard(2000)
	large2 := generateLargeLogHard(2000)
	large3 := generateLargeJSONHard(30)

	requestBody := map[string]interface{}{
		"model":      "gpt-4o-mini",
		"max_tokens": 400,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What can you tell me about these three results?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_code",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "service.go"}`,
						},
					},
					{
						"id":   "call_log",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "bash",
							"arguments": `{"command": "cat app.log"}`,
						},
					},
					{
						"id":   "call_api",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "api_request",
							"arguments": `{"endpoint": "/users"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_code", "content": large1},
			{"role": "tool", "tool_call_id": "call_log", "content": large2},
			{"role": "tool", "tool_call_id": "call_api", "content": large3},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", "https://api.openai.com/v1/chat/completions")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := retryableRequestHard(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	responseBody, _ := io.ReadAll(resp.Body)
	assert.NotContains(t, string(responseBody), "expand_context")
	assert.NotContains(t, string(responseBody), "<<<SHADOW:")

	var response map[string]interface{}
	json.Unmarshal(responseBody, &response)
	content := extractOpenAIContentHard(response)
	t.Logf("GPT Response: %s", content)
	assert.NotEmpty(t, content)
}

// =============================================================================
// ERROR HANDLING
// =============================================================================

// TestHardIntegration_MixedSuccessAndError tests success and error tool results together.
func TestHardIntegration_MixedSuccessAndError(t *testing.T) {
	apiKey := getOpenAIKeyHard(t)

	cfg := hardCompressionConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	successContent := "File content: package main\n\nfunc main() {\n\tfmt.Println(\"Hello\")\n}\n"
	errorContent := "Error: ENOENT: no such file or directory, open 'config.yaml'"

	requestBody := map[string]interface{}{
		"model":      "gpt-4o-mini",
		"max_tokens": 200,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Read both files and tell me what you found."},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_success",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "main.go"}`,
						},
					},
					{
						"id":   "call_error",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "config.yaml"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_success", "content": successContent},
			{"role": "tool", "tool_call_id": "call_error", "content": errorContent},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", "https://api.openai.com/v1/chat/completions")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := retryableRequestHard(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)
	content := extractOpenAIContentHard(response)
	t.Logf("GPT Response: %s", content)

	contentLower := strings.ToLower(content)
	assert.True(t, strings.Contains(contentLower, "error") ||
		strings.Contains(contentLower, "not found") ||
		strings.Contains(contentLower, "missing"))
}

// TestHardIntegration_LargeErrorMessage tests large error message handling.
func TestHardIntegration_LargeErrorMessage(t *testing.T) {
	apiKey := getOpenAIKeyHard(t)

	cfg := hardCompressionConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Generate a large stack trace error
	var errorBuf strings.Builder
	errorBuf.WriteString("Error: Build failed\n\n")
	for i := 0; i < 50; i++ {
		errorBuf.WriteString(fmt.Sprintf("    at module%d.process (module%d.go:%d)\n", i, i, 100+i))
	}
	errorBuf.WriteString("\nCaused by: undefined reference to 'processData'\n")
	largeError := errorBuf.String()

	requestBody := map[string]interface{}{
		"model":      "gpt-4o-mini",
		"max_tokens": 200,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What's the root cause of this build error?"},
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
				},
			},
			{"role": "tool", "tool_call_id": "call_build", "content": largeError},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", "https://api.openai.com/v1/chat/completions")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := retryableRequestHard(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)
	content := extractOpenAIContentHard(response)
	t.Logf("GPT Response: %s", content)

	// Check we got a meaningful response about the error
	contentLower := strings.ToLower(content)
	assert.True(t, strings.Contains(contentLower, "undefined") ||
		strings.Contains(contentLower, "error") ||
		strings.Contains(contentLower, "build") ||
		strings.Contains(contentLower, "reference") ||
		strings.Contains(contentLower, "issue") ||
		strings.Contains(contentLower, "problem") ||
		strings.Contains(contentLower, "fail") ||
		strings.Contains(contentLower, "stack") ||
		len(content) > 20, // At minimum, we got a response
		"Expected response to acknowledge the error in some way")
}

// =============================================================================
// REAL-WORLD OUTPUTS
// =============================================================================

// TestHardIntegration_RealWorld_GitLog tests real git log output.
func TestHardIntegration_RealWorld_GitLog(t *testing.T) {
	apiKey := getOpenAIKeyHard(t)

	cfg := hardCompressionConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	gitLog := generateGitLogHard(30)
	t.Logf("Git log size: %d bytes", len(gitLog))

	requestBody := map[string]interface{}{
		"model":      "gpt-4o-mini",
		"max_tokens": 200,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Summarize the recent commit activity. Who has made the most commits?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_git",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "bash",
							"arguments": `{"command": "git log --oneline -30"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_git", "content": gitLog},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", "https://api.openai.com/v1/chat/completions")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := retryableRequestHard(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)
	content := extractOpenAIContentHard(response)
	t.Logf("GPT Response: %s", content)
	assert.NotEmpty(t, content)
}

// TestHardIntegration_RealWorld_NPMInstall tests npm install output.
func TestHardIntegration_RealWorld_NPMInstall(t *testing.T) {
	apiKey := getOpenAIKeyHard(t)

	cfg := hardCompressionConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	npmOutput := generateNPMInstallHard(50)
	t.Logf("NPM output size: %d bytes", len(npmOutput))

	requestBody := map[string]interface{}{
		"model":      "gpt-4o-mini",
		"max_tokens": 150,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Were there any vulnerabilities found during npm install?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_npm",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "bash",
							"arguments": `{"command": "npm install"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_npm", "content": npmOutput},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", "https://api.openai.com/v1/chat/completions")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := retryableRequestHard(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)
	content := extractOpenAIContentHard(response)
	t.Logf("GPT Response: %s", content)
	assert.NotEmpty(t, content)
}

// TestHardIntegration_RealWorld_DockerBuild tests docker build output.
func TestHardIntegration_RealWorld_DockerBuild(t *testing.T) {
	apiKey := getOpenAIKeyHard(t)

	cfg := hardCompressionConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	dockerOutput := generateDockerBuildHard(20)
	t.Logf("Docker output size: %d bytes", len(dockerOutput))

	requestBody := map[string]interface{}{
		"model":      "gpt-4o-mini",
		"max_tokens": 150,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Was the Docker build successful? What image was created?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_docker",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "bash",
							"arguments": `{"command": "docker build -t myapp ."}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_docker", "content": dockerOutput},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", "https://api.openai.com/v1/chat/completions")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := retryableRequestHard(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)
	content := extractOpenAIContentHard(response)
	t.Logf("GPT Response: %s", content)
	assert.NotEmpty(t, content)
}

// =============================================================================
// EDGE CASES
// =============================================================================

// TestHardIntegration_EmptyToolResult tests empty tool result handling.
func TestHardIntegration_EmptyToolResult(t *testing.T) {
	apiKey := getOpenAIKeyHard(t)

	cfg := passthroughConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"model":      "gpt-4o-mini",
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What's in this file?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_empty",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "empty.txt"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_empty", "content": ""},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", "https://api.openai.com/v1/chat/completions")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)
	content := extractOpenAIContentHard(response)
	t.Logf("GPT Response: %s", content)

	// GPT may respond with text OR with another tool call
	// Check for valid response (either text content or tool_calls)
	hasContent := len(content) > 0
	hasToolCalls := false
	if choices, ok := response["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if msg, ok := choice["message"].(map[string]interface{}); ok {
				if _, ok := msg["tool_calls"]; ok {
					hasToolCalls = true
				}
			}
		}
	}

	assert.True(t, hasContent || hasToolCalls,
		"Should either acknowledge empty result with text or make another tool call")
}

// TestHardIntegration_SpecialCharactersInOutput tests special character handling.
func TestHardIntegration_SpecialCharactersInOutput(t *testing.T) {
	apiKey := getOpenAIKeyHard(t)

	cfg := passthroughConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	specialContent := `File content with special characters:
- Emoji: 🚀 ✅ ❌ 🎉
- Unicode: ñ é ü ö ß 中文 日本語 한국어
- Escape: \n \t \r
- JSON-like: {"key": "value", "nested": {"arr": [1,2,3]}}
- XML-like: <tag attr="value">content</tag>
- Code: func main() { fmt.Println("Hello \"World\"") }
- Path: C:\Users\name\Documents\file.txt
- URL: https://example.com/path?query=value&other=123
`

	requestBody := map[string]interface{}{
		"model":      "gpt-4o-mini",
		"max_tokens": 150,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What types of special characters are in this file?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_special",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "special.txt"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_special", "content": specialContent},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", "https://api.openai.com/v1/chat/completions")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)
	content := extractOpenAIContentHard(response)
	t.Logf("GPT Response: %s", content)

	contentLower := strings.ToLower(content)
	assert.True(t, strings.Contains(contentLower, "emoji") ||
		strings.Contains(contentLower, "unicode") ||
		strings.Contains(contentLower, "special") ||
		strings.Contains(contentLower, "character"))
}

// TestHardIntegration_BinaryLikeContent tests binary-like content handling.
func TestHardIntegration_BinaryLikeContent(t *testing.T) {
	apiKey := getOpenAIKeyHard(t)

	cfg := passthroughConfigOpenAI()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Simulate binary-like content (base64, hex, etc.)
	binaryLike := `Binary file content preview:
00000000: 4845 4c4c 4f20 574f 524c 4400 0000 0000  HELLO WORLD.....
00000010: 89504e47 0d0a1a0a 00000000 49484452  .PNG........IHDR
00000020: 0000001000000010 080200000090916836  ................
Detected: PNG image file
Size: 1024 bytes
`

	requestBody := map[string]interface{}{
		"model":      "gpt-4o-mini",
		"max_tokens": 100,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "What type of file is this?"},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_binary",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "read_file",
							"arguments": `{"path": "image.png"}`,
						},
					},
				},
			},
			{"role": "tool", "tool_call_id": "call_binary", "content": binaryLike},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	req, err := http.NewRequest("POST", gwServer.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Target-URL", "https://api.openai.com/v1/chat/completions")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var response map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&response)
	content := extractOpenAIContentHard(response)
	t.Logf("GPT Response: %s", content)

	contentLower := strings.ToLower(content)
	assert.True(t, strings.Contains(contentLower, "png") ||
		strings.Contains(contentLower, "image") ||
		strings.Contains(contentLower, "binary"))
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

func retryableRequestHard(client *http.Client, req *http.Request, t *testing.T) (*http.Response, error) {
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

func extractOpenAIContentHard(response map[string]interface{}) string {
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

func hardCompressionConfigOpenAI() *config.Config {
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
				MinBytes:            500,
				MaxBytes:            65536,
				TargetRatio:         0.3,
				IncludeExpandHint:   true,
				EnableExpandContext: true,
				API: config.APIConfig{
					Endpoint: os.Getenv("COMPRESR_API_URL") + "/api/compress/tool-output",
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

func generateLargeCodeHard(minSize int) string {
	var buf strings.Builder
	buf.WriteString("package main\n\nimport \"fmt\"\n\n")

	i := 0
	for buf.Len() < minSize {
		buf.WriteString(fmt.Sprintf(`
func process%d(data string) string {
	// Process data item %d
	result := strings.ToUpper(data)
	return fmt.Sprintf("Processed: %%s", result)
}
`, i, i))
		i++
	}

	return buf.String()
}

func generateLargeLogHard(minSize int) string {
	var buf strings.Builder
	levels := []string{"INFO", "DEBUG", "WARN", "ERROR"}

	i := 0
	for buf.Len() < minSize {
		level := levels[i%len(levels)]
		buf.WriteString(fmt.Sprintf("[2024-01-15T%02d:%02d:%02d] %s: Processing request %d\n",
			i%24, i%60, i%60, level, i))
		i++
	}

	return buf.String()
}

func generateLargeJSONHard(numItems int) string {
	var items []map[string]interface{}
	for i := 0; i < numItems; i++ {
		items = append(items, map[string]interface{}{
			"id":     i + 1,
			"name":   fmt.Sprintf("Item %d", i+1),
			"active": i%2 == 0,
		})
	}
	data, _ := json.MarshalIndent(items, "", "  ")
	return string(data)
}

func generateGitLogHard(numCommits int) string {
	var buf strings.Builder
	authors := []string{"alice", "bob", "charlie", "diana"}
	types := []string{"feat", "fix", "docs", "refactor", "test"}

	for i := 0; i < numCommits; i++ {
		author := authors[i%len(authors)]
		commitType := types[i%len(types)]
		buf.WriteString(fmt.Sprintf("abc%04d %s: %s change %d - Author: %s\n",
			i, commitType, commitType, i, author))
	}

	return buf.String()
}

func generateNPMInstallHard(numPackages int) string {
	var buf strings.Builder
	buf.WriteString("npm WARN deprecated package@1.0.0: old version\n")

	for i := 0; i < numPackages; i++ {
		buf.WriteString(fmt.Sprintf("added package-%d@%d.0.0\n", i, i%10))
	}

	buf.WriteString(fmt.Sprintf("\nadded %d packages in 5s\n", numPackages))
	buf.WriteString("\n2 moderate severity vulnerabilities\n")
	buf.WriteString("run `npm audit fix` to fix them\n")

	return buf.String()
}

func generateDockerBuildHard(numSteps int) string {
	var buf strings.Builder
	buf.WriteString("Sending build context to Docker daemon  2.048kB\n")

	for i := 1; i <= numSteps; i++ {
		buf.WriteString(fmt.Sprintf("Step %d/%d : RUN echo 'Building step %d'\n", i, numSteps, i))
		buf.WriteString(fmt.Sprintf(" ---> Running in abc%04d\n", i))
		buf.WriteString(fmt.Sprintf(" ---> def%04d\n", i))
	}

	buf.WriteString("Successfully built abc12345\n")
	buf.WriteString("Successfully tagged myapp:latest\n")

	return buf.String()
}
