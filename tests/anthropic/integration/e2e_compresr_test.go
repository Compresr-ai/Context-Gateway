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
	"github.com/compresr/context-gateway/internal/preemptive"
	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	godotenv.Load("../../../.env")
}

func getCompresrKey(t *testing.T) string {
	key := os.Getenv("COMPRESR_API_KEY")
	if key == "" {
		t.Skip("COMPRESR_API_KEY not set, skipping E2E test")
	}
	return key
}

// =============================================================================
// E2E CONFIG: All three pipes enabled with REAL Compresr API
// =============================================================================

func e2eFullConfig() *config.Config {
	compresrKey := os.Getenv("COMPRESR_API_KEY")
	compresrURL := os.Getenv("COMPRESR_API_URL")
	if compresrURL == "" {
		compresrURL = "https://api.compresr.ai"
	}

	return &config.Config{
		Server: config.ServerConfig{
			Port:         18080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 180 * time.Second,
		},
		URLs: config.URLsConfig{
			Compresr: compresrURL,
		},
		Pipes: config.PipesConfig{
			// PIPE 1: Tool Output Compression via Compresr API
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:             true,
				Strategy:            "api",
				FallbackStrategy:    "passthrough",
				MinBytes:            500,
				MaxBytes:            100000,
				TargetRatio:         0.3,
				IncludeExpandHint:   true,
				EnableExpandContext: true,
				API: config.APIConfig{
					Endpoint:  "/api/compress/tool-output/",
					APISecret: compresrKey,
					Model:     "toc_latte_v1",
					Timeout:   30 * time.Second,
				},
			},
			// PIPE 2: Tool Discovery via Compresr API
			ToolDiscovery: config.ToolDiscoveryPipeConfig{
				Enabled:              true,
				Strategy:             "api",
				MinTools:             3,
				MaxTools:             10,
				TargetRatio:          0.5,
				EnableSearchFallback: true,
				API: config.APIConfig{
					Endpoint:  "/api/compress/tool-discovery/",
					APISecret: compresrKey,
					Model:     "tdc_coldbrew_v1",
					Timeout:   30 * time.Second,
				},
			},
		},
		// PIPE 3: History Compression (Preemptive) via Compresr API
		Preemptive: preemptive.Config{
			Enabled:          true,
			TriggerThreshold: 50.0, // Trigger early for testing
			Summarizer: preemptive.SummarizerConfig{
				Strategy: "api",
				API: &preemptive.APIConfig{
					Endpoint: "/api/compress/history/",
					APIKey:   compresrKey,
					Model:    "hcc_espresso_v1",
					Timeout:  60 * time.Second,
				},
				TokenEstimateRatio: 4,
				KeepRecentCount:    2,
			},
			Session: preemptive.SessionConfig{
				SummaryTTL:       10 * time.Minute,
				HashMessageCount: 3,
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

// =============================================================================
// E2E TEST: Tool Output Compression via Real Compresr API
// =============================================================================

// TestE2E_ToolOutputCompression_RealCompresrAPI tests tool output compression
// with a REAL call to the Compresr API (toc_latte_v1 model).
func TestE2E_ToolOutputCompression_RealCompresrAPI(t *testing.T) {
	apiKey := getAnthropicKey(t)
	_ = getCompresrKey(t)

	cfg := e2eFullConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Generate a large tool output (~3KB) that will trigger compression
	largeOutput := generateLargeCodeFile(3000)

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 300,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Analyze this Go code and tell me what the main function does."},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_e2e_001",
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
						"tool_use_id": "toolu_e2e_001",
						"content":     largeOutput,
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	t.Logf("Tool output size: %d bytes (should trigger Compresr API compression)", len(largeOutput))

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := retryableRequest(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	bodyBytes, _ = io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "Response: %s", string(bodyBytes))

	var response map[string]interface{}
	json.Unmarshal(bodyBytes, &response)

	content := extractAnthropicContent(response)
	t.Logf("Claude Response (compressed via Compresr): %s", content)

	assert.NotEmpty(t, content)
	// Should have meaningful response about the code
	contentLower := strings.ToLower(content)
	assert.True(t,
		strings.Contains(contentLower, "function") ||
			strings.Contains(contentLower, "main") ||
			strings.Contains(contentLower, "code") ||
			strings.Contains(contentLower, "process"),
		"Response should mention code elements")
}

// TestE2E_ToolOutputCompression_MultipleTools tests compression of multiple
// tool outputs in a single request via Compresr API.
func TestE2E_ToolOutputCompression_MultipleTools(t *testing.T) {
	apiKey := getAnthropicKey(t)
	_ = getCompresrKey(t)

	cfg := e2eFullConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Three large outputs - all should be compressed via Compresr API
	output1 := generateLargeCodeFile(2000)   // Go code
	output2 := generateLargeLogOutput(2000)  // Server logs
	output3 := generateLargeJSONResponse(50) // API response

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 400,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "I need a summary of these three files: the Go code, the server logs, and the API response."},
			{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    "toolu_multi_001",
						"name":  "read_file",
						"input": map[string]string{"path": "handler.go"},
					},
					{
						"type":  "tool_use",
						"id":    "toolu_multi_002",
						"name":  "read_file",
						"input": map[string]string{"path": "server.log"},
					},
					{
						"type":  "tool_use",
						"id":    "toolu_multi_003",
						"name":  "curl",
						"input": map[string]string{"url": "https://api.example.com/users"},
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_multi_001",
						"content":     output1,
					},
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_multi_002",
						"content":     output2,
					},
					{
						"type":        "tool_result",
						"tool_use_id": "toolu_multi_003",
						"content":     output3,
					},
				},
			},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	t.Logf("Tool output sizes: %d, %d, %d bytes", len(output1), len(output2), len(output3))

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := retryableRequest(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	bodyBytes, _ = io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "Response: %s", string(bodyBytes))

	var response map[string]interface{}
	json.Unmarshal(bodyBytes, &response)

	content := extractAnthropicContent(response)
	t.Logf("Claude Response (3 tools compressed): %s", content)
	assert.NotEmpty(t, content)
}

// =============================================================================
// E2E TEST: Tool Discovery via Real Compresr API
// =============================================================================

// TestE2E_ToolDiscovery_RealCompresrAPI tests tool discovery/filtering
// with a REAL call to the Compresr API (tdc_coldbrew_v1 model).
func TestE2E_ToolDiscovery_RealCompresrAPI(t *testing.T) {
	apiKey := getAnthropicKey(t)
	_ = getCompresrKey(t)

	cfg := e2eFullConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Define 20+ tools - Compresr should filter to most relevant
	tools := make([]map[string]interface{}, 0, 25)
	for i := 0; i < 25; i++ {
		tools = append(tools, map[string]interface{}{
			"name":        fmt.Sprintf("tool_%d", i),
			"description": getToolDescription(i),
			"input_schema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		})
	}

	// User query about reading files - should select file-related tools
	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 200,
		"tools":      tools,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Read the contents of config.yaml file"},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	t.Logf("Sending request with %d tools (Compresr should filter to ~10)", len(tools))

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := retryableRequest(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	bodyBytes, _ = io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "Response: %s", string(bodyBytes))

	var response map[string]interface{}
	json.Unmarshal(bodyBytes, &response)

	// Check if Claude made a tool call (should select file reading tool)
	content := extractAnthropicContent(response)
	t.Logf("Claude Response (from filtered tools): %s", content)
	// Response should be meaningful
	assert.True(t, len(content) > 0 || hasToolUse(response), "Should have content or tool use")
}

// TestE2E_ToolDiscovery_ManyTools tests tool discovery with 50+ tools
func TestE2E_ToolDiscovery_ManyTools(t *testing.T) {
	apiKey := getAnthropicKey(t)
	_ = getCompresrKey(t)

	cfg := e2eFullConfig()
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Define 50 tools
	tools := make([]map[string]interface{}, 0, 50)
	for i := 0; i < 50; i++ {
		tools = append(tools, map[string]interface{}{
			"name":        fmt.Sprintf("tool_%02d_%s", i, getCategoryName(i)),
			"description": getToolDescription(i),
			"input_schema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		})
	}

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 200,
		"tools":      tools,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Run the unit tests in the project"},
		},
	}

	bodyBytes, _ := json.Marshal(requestBody)
	t.Logf("Sending request with %d tools (heavy tool discovery test)", len(tools))

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := retryableRequest(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	bodyBytes, _ = io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "Response: %s", string(bodyBytes))
}

// =============================================================================
// E2E TEST: History Compression via Real Compresr API
// =============================================================================

// TestE2E_HistoryCompression_RealCompresrAPI tests history compression
// with a REAL call to the Compresr API (hcc_espresso_v1 model).
func TestE2E_HistoryCompression_RealCompresrAPI(t *testing.T) {
	apiKey := getAnthropicKey(t)
	_ = getCompresrKey(t)

	cfg := e2eFullConfig()
	cfg.Preemptive.TriggerThreshold = 30.0          // Lower threshold for testing
	cfg.Preemptive.TestContextWindowOverride = 4000 // Small window to trigger compression
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Build a long conversation history that should trigger compression
	messages := []map[string]interface{}{
		{"role": "user", "content": "Let's have a detailed discussion about software architecture."},
		{"role": "assistant", "content": generateLongAssistantResponse(500)},
		{"role": "user", "content": "What about microservices vs monolith? Can you explain the tradeoffs in detail?"},
		{"role": "assistant", "content": generateLongAssistantResponse(600)},
		{"role": "user", "content": "How does Kubernetes help with microservices deployment?"},
		{"role": "assistant", "content": generateLongAssistantResponse(500)},
		{"role": "user", "content": "What database strategies work best for microservices?"},
		{"role": "assistant", "content": generateLongAssistantResponse(500)},
		// Final question that should trigger history compression
		{"role": "user", "content": "Given everything we discussed, what's your recommendation for a new project?"},
	}

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 300,
		"messages":   messages,
	}

	totalHistorySize := 0
	for _, m := range messages {
		if content, ok := m["content"].(string); ok {
			totalHistorySize += len(content)
		}
	}
	t.Logf("Total history size: %d bytes (should trigger history compression)", totalHistorySize)

	bodyBytes, _ := json.Marshal(requestBody)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := retryableRequest(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	bodyBytes, _ = io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "Response: %s", string(bodyBytes))

	var response map[string]interface{}
	json.Unmarshal(bodyBytes, &response)

	content := extractAnthropicContent(response)
	t.Logf("Claude Response (with history compression): %s", content)
	assert.NotEmpty(t, content)
	// Should have contextual answer based on previous discussion
	contentLower := strings.ToLower(content)
	assert.True(t,
		strings.Contains(contentLower, "microservice") ||
			strings.Contains(contentLower, "architecture") ||
			strings.Contains(contentLower, "recommend") ||
			strings.Contains(contentLower, "project"),
		"Response should be contextual to the conversation")
}

// =============================================================================
// E2E TEST: Combined - All Three Pipes Active
// =============================================================================

// TestE2E_AllPipesActive tests a complex scenario with all three compression
// pipes active: tool output, tool discovery, and history compression.
func TestE2E_AllPipesActive(t *testing.T) {
	apiKey := getAnthropicKey(t)
	_ = getCompresrKey(t)

	cfg := e2eFullConfig()
	cfg.Preemptive.TriggerThreshold = 40.0
	cfg.Preemptive.TestContextWindowOverride = 8000
	gw := gateway.New(cfg)
	gwServer := httptest.NewServer(gw.Handler())
	defer gwServer.Close()

	// Define tools (tool discovery will filter)
	tools := make([]map[string]interface{}, 0, 20)
	for i := 0; i < 20; i++ {
		tools = append(tools, map[string]interface{}{
			"name":        fmt.Sprintf("tool_%d", i),
			"description": getToolDescription(i),
			"input_schema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		})
	}

	// Large tool output (tool output compression will compress)
	largeOutput := generateLargeCodeFile(3000)

	// Long conversation history (history compression may trigger)
	messages := []map[string]interface{}{
		{"role": "user", "content": "I need help understanding this codebase."},
		{"role": "assistant", "content": generateLongAssistantResponse(300)},
		{"role": "user", "content": "Can you read the main.go file and explain it?"},
		{
			"role": "assistant",
			"content": []map[string]interface{}{
				{
					"type":  "tool_use",
					"id":    "toolu_combined_001",
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
					"tool_use_id": "toolu_combined_001",
					"content":     largeOutput,
				},
			},
		},
	}

	requestBody := map[string]interface{}{
		"model":      anthropicModel,
		"max_tokens": 400,
		"tools":      tools,
		"messages":   messages,
	}

	t.Logf("E2E test: %d tools, %d bytes tool output, %d messages",
		len(tools), len(largeOutput), len(messages))

	bodyBytes, _ := json.Marshal(requestBody)

	req, err := http.NewRequest("POST", gwServer.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("X-Target-URL", anthropicBaseURL+"/v1/messages")

	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := retryableRequest(client, req, t)
	require.NoError(t, err)
	defer resp.Body.Close()

	bodyBytes, _ = io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "Response: %s", string(bodyBytes))

	var response map[string]interface{}
	json.Unmarshal(bodyBytes, &response)

	content := extractAnthropicContent(response)
	t.Logf("Claude Response (all pipes active): %s", content)
	assert.NotEmpty(t, content)
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

func generateLargeCodeFile(minBytes int) string {
	var buf strings.Builder
	buf.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n)\n\n")
	i := 0
	for buf.Len() < minBytes {
		buf.WriteString(fmt.Sprintf(`// Handler%d processes requests for endpoint %d.
// It validates input, performs business logic, and returns JSON.
func Handler%d(w http.ResponseWriter, r *http.Request) {
	// Validate request method
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Process request
	result := process%d(r)
	fmt.Fprintf(w, "Processed: %%v\n", result)
}

func process%d(r *http.Request) string {
	return fmt.Sprintf("request_%%d_processed", %d)
}

`, i, i, i, i, i, i))
		i++
	}
	buf.WriteString("\nfunc main() {\n\thttp.HandleFunc(\"/\", Handler0)\n\thttp.ListenAndServe(\":8080\", nil)\n}\n")
	return buf.String()
}

func generateLargeLogOutput(minBytes int) string {
	var buf strings.Builder
	levels := []string{"INFO", "DEBUG", "WARN", "ERROR"}
	i := 0
	for buf.Len() < minBytes {
		level := levels[i%len(levels)]
		buf.WriteString(fmt.Sprintf("[2026-02-24T%02d:%02d:%02d] %s server: Processing request #%d from client 192.168.1.%d\n",
			i%24, i%60, i%60, level, i, i%256))
		i++
	}
	return buf.String()
}

func generateLargeJSONResponse(numItems int) string {
	items := make([]map[string]interface{}, 0, numItems)
	for i := 0; i < numItems; i++ {
		items = append(items, map[string]interface{}{
			"id":         i + 1,
			"name":       fmt.Sprintf("User %d", i+1),
			"email":      fmt.Sprintf("user%d@example.com", i+1),
			"active":     i%2 == 0,
			"created_at": "2026-02-24T12:00:00Z",
		})
	}
	data, _ := json.MarshalIndent(map[string]interface{}{"users": items, "total": numItems}, "", "  ")
	return string(data)
}

func generateLongAssistantResponse(minBytes int) string {
	paragraphs := []string{
		"Software architecture is a crucial aspect of system design that involves making high-level structural choices. ",
		"These decisions affect performance, scalability, maintainability, and overall system quality. ",
		"Modern architectures often embrace microservices for their flexibility and independent deployment capabilities. ",
		"However, monolithic architectures can be simpler and more efficient for smaller teams and applications. ",
		"The choice between these patterns depends on team size, project complexity, and scaling requirements. ",
	}
	var buf strings.Builder
	i := 0
	for buf.Len() < minBytes {
		buf.WriteString(paragraphs[i%len(paragraphs)])
		i++
	}
	return buf.String()
}

func getToolDescription(i int) string {
	descriptions := []string{
		"Reads file contents from the filesystem",
		"Executes shell commands in bash",
		"Searches for patterns using grep",
		"Lists directory contents",
		"Writes content to a file",
		"Runs unit tests",
		"Builds the project",
		"Deploys to production",
		"Monitors system metrics",
		"Manages database connections",
		"Handles HTTP requests",
		"Processes JSON data",
		"Validates input schemas",
		"Generates reports",
		"Sends notifications",
	}
	return descriptions[i%len(descriptions)]
}

func getCategoryName(i int) string {
	categories := []string{"file", "shell", "search", "network", "database", "test", "build", "deploy"}
	return categories[i%len(categories)]
}

func hasToolUse(response map[string]interface{}) bool {
	content, ok := response["content"].([]interface{})
	if !ok {
		return false
	}
	for _, c := range content {
		if block, ok := c.(map[string]interface{}); ok {
			if block["type"] == "tool_use" {
				return true
			}
		}
	}
	return false
}
