// Gemini E2E Integration Tests - Real Google AI API Calls
//
// These tests make REAL calls to Google Gemini API through the gateway proxy,
// simulating exactly how Gemini-based tools interact with the API.
//
// Requirements:
//   - GEMINI_API_KEY environment variable set in .env
//   - Network connectivity to Google AI API
//
// Run with: go test ./tests/gemini/integration/... -v -run TestE2E

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
	geminiBaseURL = "https://generativelanguage.googleapis.com"
	geminiModel   = "gemini-2.5-flash"
	maxRetries    = 3
	retryDelay    = 2 * time.Second
)

func init() {
	godotenv.Load("../../../.env")
}

func getGeminiKey(t *testing.T) string {
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		t.Skip("GEMINI_API_KEY not set, skipping E2E test")
	}
	return key
}

// passthroughConfigGemini creates a config with all pipes disabled (pure proxy)
func passthroughConfigGemini() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:         18080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 180 * time.Second,
		},
		URLs: config.URLsConfig{},
		Pipes: config.PipesConfig{
			ToolOutput:    config.ToolOutputPipeConfig{Enabled: false},
			ToolDiscovery: config.ToolDiscoveryPipeConfig{Enabled: false},
		},
		Store: config.StoreConfig{
			Type: "memory",
			TTL:  5 * time.Minute,
		},
		Monitoring: config.MonitoringConfig{
			LogLevel:  "info",
			LogFormat: "json",
			LogOutput: "stdout",
		},
	}
}

// retryableRequestGemini performs HTTP request with automatic retry on transient errors
func retryableRequestGemini(client *http.Client, req *http.Request, t *testing.T) (*http.Response, error) {
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

// extractGeminiContent extracts text from Gemini response format
func extractGeminiContent(response map[string]interface{}) string {
	candidates, ok := response["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return ""
	}

	candidate, ok := candidates[0].(map[string]interface{})
	if !ok {
		return ""
	}

	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		return ""
	}

	parts, ok := content["parts"].([]interface{})
	if !ok || len(parts) == 0 {
		return ""
	}

	part, ok := parts[0].(map[string]interface{})
	if !ok {
		return ""
	}

	text, _ := part["text"].(string)
	return text
}

// =============================================================================
// TEST 1: Simple Chat - Basic Message
// =============================================================================

func TestE2E_Gemini_SimpleChat(t *testing.T) {
	apiKey := getGeminiKey(t)

	cfg := passthroughConfigGemini()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Gemini uses contents[] with parts[] format
	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"role": "user",
				"parts": []map[string]interface{}{
					{"text": "Say 'Hello from Gemini test' and nothing else."},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": 50,
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	// Use header-based auth for Gemini (x-goog-api-key) to avoid URL query param issues
	geminiPath := fmt.Sprintf("/v1beta/models/%s:generateContent", geminiModel)
	targetURL := fmt.Sprintf("%s%s", geminiBaseURL, geminiPath)

	req, err := http.NewRequest("POST", gwServer.URL+geminiPath, bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Target-URL", targetURL)
	req.Header.Set("x-goog-api-key", apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := retryableRequestGemini(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)
	t.Logf("Response status: %d, body: %s", resp.StatusCode, string(responseBody))

	assert.Equal(t, http.StatusOK, resp.StatusCode, "Response: %s", string(responseBody))

	var response map[string]interface{}
	err = json.Unmarshal(responseBody, &response)
	require.NoError(t, err)

	content := extractGeminiContent(response)
	t.Logf("Gemini Response: %s", content)
	assert.Contains(t, strings.ToLower(content), "hello")
}

// =============================================================================
// TEST 2: Simple Chat - Verify Usage Metadata
// =============================================================================

func TestE2E_Gemini_UsageExtraction(t *testing.T) {
	apiKey := getGeminiKey(t)

	cfg := passthroughConfigGemini()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"role": "user",
				"parts": []map[string]interface{}{
					{"text": "Say 'test' and nothing else."},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": 50,
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	// Use header-based auth for Gemini (x-goog-api-key)
	geminiPath := fmt.Sprintf("/v1beta/models/%s:generateContent", geminiModel)
	targetURL := fmt.Sprintf("%s%s", geminiBaseURL, geminiPath)

	req, err := http.NewRequest("POST", gwServer.URL+geminiPath, bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Target-URL", targetURL)
	req.Header.Set("x-goog-api-key", apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := retryableRequestGemini(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "Response: %s", string(responseBody))

	var response map[string]interface{}
	err = json.Unmarshal(responseBody, &response)
	require.NoError(t, err)

	// Gemini returns usageMetadata with promptTokenCount and candidatesTokenCount
	usage, ok := response["usageMetadata"].(map[string]interface{})
	require.True(t, ok, "response should have usageMetadata field")

	promptTokens, _ := usage["promptTokenCount"].(float64)
	candidateTokens, _ := usage["candidatesTokenCount"].(float64)
	t.Logf("Usage - Prompt Tokens: %.0f, Candidate Tokens: %.0f", promptTokens, candidateTokens)

	assert.Greater(t, promptTokens, float64(0), "should have prompt tokens")
	assert.Greater(t, candidateTokens, float64(0), "should have candidate tokens")
}

// =============================================================================
// TEST 3: Multi-turn Conversation
// =============================================================================

func TestE2E_Gemini_MultiTurn(t *testing.T) {
	apiKey := getGeminiKey(t)

	cfg := passthroughConfigGemini()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Multi-turn conversation with history
	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"role": "user",
				"parts": []map[string]interface{}{
					{"text": "My name is Alice."},
				},
			},
			{
				"role": "model",
				"parts": []map[string]interface{}{
					{"text": "Hello Alice! Nice to meet you."},
				},
			},
			{
				"role": "user",
				"parts": []map[string]interface{}{
					{"text": "What is my name?"},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": 50,
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	// Use header-based auth for Gemini (x-goog-api-key)
	geminiPath := fmt.Sprintf("/v1beta/models/%s:generateContent", geminiModel)
	targetURL := fmt.Sprintf("%s%s", geminiBaseURL, geminiPath)

	req, err := http.NewRequest("POST", gwServer.URL+geminiPath, bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Target-URL", targetURL)
	req.Header.Set("x-goog-api-key", apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := retryableRequestGemini(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "Response: %s", string(responseBody))

	var response map[string]interface{}
	json.Unmarshal(responseBody, &response)

	content := extractGeminiContent(response)
	t.Logf("Gemini Response: %s", content)
	assert.Contains(t, strings.ToLower(content), "alice", "Should remember the name from context")
}

// =============================================================================
// TEST 4: Tool Use - Function Calling
// =============================================================================

func TestE2E_Gemini_ToolUse(t *testing.T) {
	apiKey := getGeminiKey(t)

	cfg := passthroughConfigGemini()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Request with tool definitions
	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"role": "user",
				"parts": []map[string]interface{}{
					{"text": "What's the weather in San Francisco?"},
				},
			},
		},
		"tools": []map[string]interface{}{
			{
				"functionDeclarations": []map[string]interface{}{
					{
						"name":        "get_weather",
						"description": "Get current weather for a city",
						"parameters": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"city": map[string]interface{}{
									"type":        "string",
									"description": "The city name",
								},
							},
							"required": []string{"city"},
						},
					},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": 200,
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	// Use header-based auth for Gemini (x-goog-api-key)
	geminiPath := fmt.Sprintf("/v1beta/models/%s:generateContent", geminiModel)
	targetURL := fmt.Sprintf("%s%s", geminiBaseURL, geminiPath)

	req, err := http.NewRequest("POST", gwServer.URL+geminiPath, bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Target-URL", targetURL)
	req.Header.Set("x-goog-api-key", apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := retryableRequestGemini(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "Response: %s", string(responseBody))

	var response map[string]interface{}
	json.Unmarshal(responseBody, &response)

	// Check if model called the tool (functionCall in parts)
	candidates, _ := response["candidates"].([]interface{})
	require.NotEmpty(t, candidates, "should have candidates")

	candidate, _ := candidates[0].(map[string]interface{})
	content, _ := candidate["content"].(map[string]interface{})
	parts, _ := content["parts"].([]interface{})

	// Should either have a functionCall or text response
	foundFunctionCall := false
	foundText := false
	for _, partAny := range parts {
		part, ok := partAny.(map[string]interface{})
		if !ok {
			continue
		}
		if _, ok := part["functionCall"]; ok {
			foundFunctionCall = true
			fnCall := part["functionCall"].(map[string]interface{})
			t.Logf("Gemini called function: %v with args: %v", fnCall["name"], fnCall["args"])
		}
		if _, ok := part["text"]; ok {
			foundText = true
		}
	}

	assert.True(t, foundFunctionCall || foundText, "Response should have either function call or text")
	if foundFunctionCall {
		t.Log("Model correctly chose to call the get_weather tool")
	}
}

// =============================================================================
// TEST 5: Tool Result - Function Response
// =============================================================================

func TestE2E_Gemini_ToolResult(t *testing.T) {
	apiKey := getGeminiKey(t)

	cfg := passthroughConfigGemini()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Conversation with tool call and result
	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"role": "user",
				"parts": []map[string]interface{}{
					{"text": "What's the weather in Tokyo?"},
				},
			},
			{
				"role": "model",
				"parts": []map[string]interface{}{
					{
						"functionCall": map[string]interface{}{
							"name": "get_weather",
							"args": map[string]interface{}{
								"city": "Tokyo",
							},
						},
					},
				},
			},
			{
				"role": "user",
				"parts": []map[string]interface{}{
					{
						"functionResponse": map[string]interface{}{
							"name": "get_weather",
							"response": map[string]interface{}{
								"temperature": "22°C",
								"condition":   "Sunny with clouds",
								"humidity":    "65%",
							},
						},
					},
				},
			},
		},
		"tools": []map[string]interface{}{
			{
				"functionDeclarations": []map[string]interface{}{
					{
						"name":        "get_weather",
						"description": "Get current weather for a city",
						"parameters": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"city": map[string]interface{}{
									"type":        "string",
									"description": "The city name",
								},
							},
							"required": []string{"city"},
						},
					},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": 200,
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	// Use header-based auth for Gemini (x-goog-api-key)
	geminiPath := fmt.Sprintf("/v1beta/models/%s:generateContent", geminiModel)
	targetURL := fmt.Sprintf("%s%s", geminiBaseURL, geminiPath)

	req, err := http.NewRequest("POST", gwServer.URL+geminiPath, bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Target-URL", targetURL)
	req.Header.Set("x-goog-api-key", apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := retryableRequestGemini(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "Response: %s", string(responseBody))

	var response map[string]interface{}
	json.Unmarshal(responseBody, &response)

	content := extractGeminiContent(response)
	t.Logf("Gemini Response after tool result: %s", content)

	// Should reference the weather data
	contentLower := strings.ToLower(content)
	assert.True(t,
		strings.Contains(contentLower, "22") ||
			strings.Contains(contentLower, "sunny") ||
			strings.Contains(contentLower, "tokyo") ||
			strings.Contains(contentLower, "weather"),
		"Response should mention weather data from tool result")
}

// =============================================================================
// TEST 6: Large Tool Result (Testing Gateway Passthrough)
// =============================================================================

func TestE2E_Gemini_LargeToolResult(t *testing.T) {
	apiKey := getGeminiKey(t)

	cfg := passthroughConfigGemini()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Generate a larger response payload
	largeData := make(map[string]interface{})
	for i := 0; i < 50; i++ {
		largeData[fmt.Sprintf("item_%d", i)] = fmt.Sprintf("This is item %d with some additional content to make the payload larger. Additional padding text here.", i)
	}

	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"role": "user",
				"parts": []map[string]interface{}{
					{"text": "List the items you received from the data lookup."},
				},
			},
			{
				"role": "model",
				"parts": []map[string]interface{}{
					{
						"functionCall": map[string]interface{}{
							"name": "lookup_data",
							"args": map[string]interface{}{
								"query": "all items",
							},
						},
					},
				},
			},
			{
				"role": "user",
				"parts": []map[string]interface{}{
					{
						"functionResponse": map[string]interface{}{
							"name":     "lookup_data",
							"response": largeData,
						},
					},
				},
			},
		},
		"tools": []map[string]interface{}{
			{
				"functionDeclarations": []map[string]interface{}{
					{
						"name":        "lookup_data",
						"description": "Look up data items",
						"parameters": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"query": map[string]interface{}{
									"type":        "string",
									"description": "The search query",
								},
							},
							"required": []string{"query"},
						},
					},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": 500,
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	t.Logf("Request size: %d bytes", len(bodyBytes))

	// Use header-based auth for Gemini (x-goog-api-key)
	geminiPath := fmt.Sprintf("/v1beta/models/%s:generateContent", geminiModel)
	targetURL := fmt.Sprintf("%s%s", geminiBaseURL, geminiPath)

	req, err := http.NewRequest("POST", gwServer.URL+geminiPath, bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Target-URL", targetURL)
	req.Header.Set("x-goog-api-key", apiKey)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := retryableRequestGemini(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "Response: %s", string(responseBody))

	var response map[string]interface{}
	json.Unmarshal(responseBody, &response)

	content := extractGeminiContent(response)
	t.Logf("Gemini Response: %s", content)

	// Should have some response about the items
	assert.NotEmpty(t, content, "Should receive a response")
}

// =============================================================================
// TEST 7: Streaming Response
// =============================================================================

func TestE2E_Gemini_Streaming(t *testing.T) {
	apiKey := getGeminiKey(t)

	cfg := passthroughConfigGemini()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"role": "user",
				"parts": []map[string]interface{}{
					{"text": "Count from 1 to 5 slowly."},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": 100,
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	// Use header-based auth for Gemini (x-goog-api-key)
	// Use streamGenerateContent endpoint for streaming
	geminiPath := fmt.Sprintf("/v1beta/models/%s:streamGenerateContent", geminiModel)
	targetURL := fmt.Sprintf("%s%s", geminiBaseURL, geminiPath)

	req, err := http.NewRequest("POST", gwServer.URL+geminiPath, bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Target-URL", targetURL)
	req.Header.Set("x-goog-api-key", apiKey)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := retryableRequestGemini(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Read streaming response
	responseBody, _ := io.ReadAll(resp.Body)
	t.Logf("Streaming response length: %d bytes", len(responseBody))

	// Streaming responses come as JSON array or newline-delimited
	assert.NotEmpty(t, responseBody, "Should receive streaming response")

	// Should contain numbers
	responseStr := string(responseBody)
	hasNumbers := strings.Contains(responseStr, "1") ||
		strings.Contains(responseStr, "2") ||
		strings.Contains(responseStr, "3")
	assert.True(t, hasNumbers, "Response should contain numbers: %s", responseStr)
}

// =============================================================================
// TEST 8: System Instruction
// =============================================================================

func TestE2E_Gemini_SystemInstruction(t *testing.T) {
	apiKey := getGeminiKey(t)

	cfg := passthroughConfigGemini()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Gemini uses systemInstruction at top level
	requestBody := map[string]interface{}{
		"systemInstruction": map[string]interface{}{
			"parts": []map[string]interface{}{
				{"text": "You are a pirate. Always respond in pirate speak."},
			},
		},
		"contents": []map[string]interface{}{
			{
				"role": "user",
				"parts": []map[string]interface{}{
					{"text": "Hello, how are you?"},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": 100,
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	// Use header-based auth for Gemini (x-goog-api-key)
	geminiPath := fmt.Sprintf("/v1beta/models/%s:generateContent", geminiModel)
	targetURL := fmt.Sprintf("%s%s", geminiBaseURL, geminiPath)

	req, err := http.NewRequest("POST", gwServer.URL+geminiPath, bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Target-URL", targetURL)
	req.Header.Set("x-goog-api-key", apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := retryableRequestGemini(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "Response: %s", string(responseBody))

	var response map[string]interface{}
	json.Unmarshal(responseBody, &response)

	content := extractGeminiContent(response)
	t.Logf("Gemini Response: %s", content)

	// Should respond in pirate speak
	contentLower := strings.ToLower(content)
	pirateWords := []string{"ahoy", "matey", "arr", "ye", "aye", "captain", "sailor", "ship", "sea"}
	foundPirateWord := false
	for _, word := range pirateWords {
		if strings.Contains(contentLower, word) {
			foundPirateWord = true
			break
		}
	}
	assert.True(t, foundPirateWord, "Response should contain pirate-speak words")
}

// =============================================================================
// TEST 9: Error Handling - Invalid API Key
// =============================================================================

func TestE2E_Gemini_InvalidAPIKey(t *testing.T) {
	cfg := passthroughConfigGemini()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"role": "user",
				"parts": []map[string]interface{}{
					{"text": "Hello"},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	// Use header-based auth with invalid key
	geminiPath := fmt.Sprintf("/v1beta/models/%s:generateContent", geminiModel)
	targetURL := fmt.Sprintf("%s%s", geminiBaseURL, geminiPath)

	req, err := http.NewRequest("POST", gwServer.URL+geminiPath, bytes.NewReader(bodyBytes))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Target-URL", targetURL)
	req.Header.Set("x-goog-api-key", "invalid-key")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should get 400 or 401 error from Gemini
	assert.True(t, resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 403,
		"Invalid API key should return 400/401/403, got %d", resp.StatusCode)
}
