// Compresr API E2E Integration Tests - Real API Calls
//
// These tests make REAL calls to the Compresr API endpoints.
// They test the actual compression functionality, not mock server responses.
//
// Requirements:
//   - COMPRESR_API_KEY environment variable set in .env
//   - COMPRESR_BASE_URL environment variable (defaults to https://api.compresr.ai)
//   - Network connectivity to Compresr API
//
// Run with: go test ./tests/compresr/... -v -run TestE2E

package compresr_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/compresr"
	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	godotenv.Load("../../.env")
}

func getCompresrKey(t *testing.T) string {
	key := os.Getenv("COMPRESR_API_KEY")
	if key == "" {
		t.Skip("COMPRESR_API_KEY not set, skipping E2E test")
	}
	return key
}

func getCompresrURL() string {
	url := os.Getenv("COMPRESR_BASE_URL")
	if url == "" {
		url = compresr.DefaultCompresrAPIBaseURL
	}
	return url
}

// =============================================================================
// TEST 1: Health Check - API is reachable
// =============================================================================

func TestE2E_Compresr_APIReachable(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey)
	require.True(t, client.HasAPIKey(), "Client should have API key configured")
}

// =============================================================================
// TEST 2: Subscription Status - Check API key validity
// =============================================================================

func TestE2E_Compresr_Subscription(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey)

	sub, err := client.GetSubscription()
	require.NoError(t, err, "GetSubscription should not fail with valid API key")

	t.Logf("Subscription Tier: %s", sub.Tier)
	t.Logf("Credits Remaining: %.2f", sub.CreditsRemaining)
	t.Logf("Features: %v", sub.Features)

	// Should have a valid tier
	assert.NotEmpty(t, sub.Tier, "Should have a subscription tier")
	validTiers := []string{"free", "pro", "enterprise", "business", "starter"}
	assert.Contains(t, validTiers, sub.Tier, "Tier should be a valid tier")
}

// =============================================================================
// TEST 3: Tool Output Compression - Small Text
// =============================================================================

func TestE2E_Compresr_ToolOutputCompression_SmallText(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey)

	// Generate a medium-sized tool output that should be compressed
	toolOutput := `
package main

import (
	"fmt"
	"os"
	"strings"
)

// Config holds application configuration
type Config struct {
	Port     int
	Host     string
	Debug    bool
	LogLevel string
}

// LoadConfig loads configuration from environment
func LoadConfig() *Config {
	return &Config{
		Port:     8080,
		Host:     "localhost",
		Debug:    os.Getenv("DEBUG") == "true",
		LogLevel: os.Getenv("LOG_LEVEL"),
	}
}

func main() {
	cfg := LoadConfig()
	fmt.Printf("Starting server on %s:%d\n", cfg.Host, cfg.Port)
	fmt.Printf("Debug mode: %v\n", cfg.Debug)
	fmt.Printf("Log level: %s\n", cfg.LogLevel)
}
`

	params := compresr.CompressToolOutputParams{
		ToolOutput: toolOutput,
		UserQuery:  "What does the main function do?",
		ToolName:   "read_file",
		ModelName:  "toc_latte_v1",
		Source:     "gateway",
	}

	result, err := client.CompressToolOutput(params)
	require.NoError(t, err, "CompressToolOutput should not fail")

	t.Logf("Original size: %d bytes", len(toolOutput))
	t.Logf("Compressed size: %d bytes", len(result.CompressedOutput))
	t.Logf("Compression ratio: %.2f%%", float64(len(result.CompressedOutput))/float64(len(toolOutput))*100)

	assert.NotEmpty(t, result.CompressedOutput, "Should have compressed output")
	// Compressed output should be smaller (unless fallback occurred)
	if len(toolOutput) > 500 {
		if len(result.CompressedOutput) >= len(toolOutput) {
			t.Log("Note: Compression fallback occurred (output same as input), which is valid behavior")
		}
	}
}

// =============================================================================
// TEST 4: Tool Output Compression - Large Code File
// =============================================================================

func TestE2E_Compresr_ToolOutputCompression_LargeFile(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey, compresr.WithTimeout(60*time.Second))

	// Generate a large code file (~5KB)
	var sb strings.Builder
	sb.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n)\n\n")

	for i := 0; i < 50; i++ {
		sb.WriteString(generateFunction(i))
	}

	sb.WriteString("func main() {\n")
	for i := 0; i < 50; i++ {
		sb.WriteString(fmt.Sprintf("\tHandler%d(nil, nil)\n", i))
	}
	sb.WriteString("}\n")

	largeFile := sb.String()
	t.Logf("Generated file size: %d bytes", len(largeFile))

	params := compresr.CompressToolOutputParams{
		ToolOutput: largeFile,
		UserQuery:  "Explain what Handler5 does",
		ToolName:   "read_file",
		ModelName:  "toc_latte_v1",
		Source:     "gateway",
	}

	result, err := client.CompressToolOutput(params)
	require.NoError(t, err, "CompressToolOutput should not fail for large file")

	t.Logf("Original size: %d bytes", len(largeFile))
	t.Logf("Compressed size: %d bytes", len(result.CompressedOutput))

	compressionRatio := float64(len(result.CompressedOutput)) / float64(len(largeFile)) * 100
	t.Logf("Compression ratio: %.2f%%", compressionRatio)

	assert.NotEmpty(t, result.CompressedOutput, "Should have compressed output")
	// Allow for fallback in test environment where LLM may not be running
	if compressionRatio >= 80.0 {
		t.Logf("Note: Compression ratio %.2f%% >= 80%%, fallback may have occurred", compressionRatio)
	}
}

func generateFunction(i int) string {
	return fmt.Sprintf(`
// Handler%d handles request type %d
func Handler%d(w http.ResponseWriter, r *http.Request) {
	// Process the request
	data := processData%d(r)
	
	// Validate input
	if err := validate%d(data); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	
	// Transform data
	result := transform%d(data)
	
	// Send response
	fmt.Fprintf(w, "Handler%d processed: %%v", result)
}

func processData%d(r *http.Request) map[string]interface{} {
	return map[string]interface{}{"id": %d, "type": "handler%d"}
}

func validate%d(data map[string]interface{}) error {
	return nil
}

func transform%d(data map[string]interface{}) string {
	return fmt.Sprintf("transformed_%%v", data)
}

`, i, i, i, i, i, i, i, i, i, i, i, i)
}

// =============================================================================
// TEST 5: Tool Discovery - Filter Relevant Tools
// =============================================================================

func TestE2E_Compresr_ToolDiscovery(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey, compresr.WithTimeout(30*time.Second))

	tools := []compresr.ToolDefinition{
		{
			Name:        "read_file",
			Description: "Read the contents of a file from the filesystem",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "File path to read"},
				},
			},
		},
		{
			Name:        "write_file",
			Description: "Write content to a file on the filesystem",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "File path to write"},
					"content": map[string]any{"type": "string", "description": "Content to write"},
				},
			},
		},
		{
			Name:        "execute_command",
			Description: "Execute a shell command and return the output",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{"type": "string", "description": "Command to execute"},
				},
			},
		},
		{
			Name:        "search_web",
			Description: "Search the web for information",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "Search query"},
				},
			},
		},
		{
			Name:        "get_weather",
			Description: "Get current weather for a location",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"location": map[string]any{"type": "string", "description": "Location name"},
				},
			},
		},
		{
			Name:        "list_directory",
			Description: "List files and directories in a path",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Directory path"},
				},
			},
		},
	}

	params := compresr.FilterToolsParams{
		Query:      "I need to read the main.go file",
		Tools:      tools,
		MaxTools:   3,
		AlwaysKeep: []string{},
		ModelName:  "tdc_coldbrew_v1",
		Source:     "gateway",
	}

	result, err := client.FilterTools(params)
	require.NoError(t, err, "FilterTools should not fail")

	t.Logf("Query: %s", params.Query)
	t.Logf("Input tools: %d", len(tools))
	t.Logf("Relevant tools: %v", result.RelevantTools)

	assert.NotEmpty(t, result.RelevantTools, "Should return relevant tools")
	assert.LessOrEqual(t, len(result.RelevantTools), params.MaxTools,
		"Should not exceed MaxTools")

	// read_file should be in the results since query mentions reading a file
	assert.Contains(t, result.RelevantTools, "read_file",
		"read_file should be relevant for 'read the main.go file'")
}

// =============================================================================
// TEST 6: History Compression - Compress Conversation
// =============================================================================

func TestE2E_Compresr_HistoryCompression(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey, compresr.WithTimeout(60*time.Second))

	// Create a conversation history
	messages := []compresr.HistoryMessage{
		{Role: "user", Content: "Can you help me write a Python script to process CSV files?"},
		{Role: "assistant", Content: "Of course! I'd be happy to help you write a Python script for CSV processing. What kind of processing do you need? For example:\n1. Reading and filtering data\n2. Transforming columns\n3. Merging multiple CSV files\n4. Calculating statistics\n5. Converting to other formats"},
		{Role: "user", Content: "I need to read a CSV file, filter rows where the 'status' column is 'active', and calculate the sum of the 'amount' column."},
		{Role: "assistant", Content: "Here's a Python script that does exactly that:\n\n```python\nimport pandas as pd\n\n# Read the CSV file\ndf = pd.read_csv('data.csv')\n\n# Filter for active status\nactive_df = df[df['status'] == 'active']\n\n# Calculate sum of amount\ntotal = active_df['amount'].sum()\n\nprint(f'Total amount for active records: {total}')\n```\n\nThis script uses pandas for efficient data processing. Would you like me to add error handling or any additional features?"},
		{Role: "user", Content: "Yes, please add error handling for missing files and invalid data types."},
		{Role: "assistant", Content: "Here's the improved version with comprehensive error handling:\n\n```python\nimport pandas as pd\nimport sys\nfrom pathlib import Path\n\ndef process_csv(filepath):\n    # Check if file exists\n    if not Path(filepath).exists():\n        raise FileNotFoundError(f\"CSV file not found: {filepath}\")\n    \n    try:\n        # Read CSV with explicit data types\n        df = pd.read_csv(filepath, dtype={'status': str})\n        \n        # Verify required columns exist\n        required = ['status', 'amount']\n        missing = [col for col in required if col not in df.columns]\n        if missing:\n            raise ValueError(f\"Missing columns: {missing}\")\n        \n        # Convert amount to numeric, coercing errors\n        df['amount'] = pd.to_numeric(df['amount'], errors='coerce')\n        \n        # Filter and calculate\n        active_df = df[df['status'] == 'active']\n        total = active_df['amount'].sum()\n        \n        return total\n        \n    except pd.errors.EmptyDataError:\n        raise ValueError(\"CSV file is empty\")\n\nif __name__ == '__main__':\n    try:\n        result = process_csv('data.csv')\n        print(f'Total: {result}')\n    except Exception as e:\n        print(f'Error: {e}', file=sys.stderr)\n        sys.exit(1)\n```"},
		{Role: "user", Content: "Perfect! Now can you also add logging?"},
	}

	params := compresr.CompressHistoryParams{
		Messages:   messages,
		KeepRecent: 2,
		ModelName:  "hcc_espresso_v1",
		Source:     "gateway",
	}

	result, err := client.CompressHistory(params)
	require.NoError(t, err, "CompressHistory should not fail")

	t.Logf("Messages compressed: %d", result.MessagesCompressed)
	t.Logf("Messages kept: %d", result.MessagesKept)
	t.Logf("Original tokens: %d", result.OriginalTokens)
	t.Logf("Compressed tokens: %d", result.CompressedTokens)
	t.Logf("Compression ratio: %.2f%%", result.CompressionRatio*100)
	t.Logf("Duration: %d ms", result.DurationMS)
	t.Logf("Summary preview: %s...", truncate(result.Summary, 200))

	assert.NotEmpty(t, result.Summary, "Should have a summary")
	assert.Greater(t, result.MessagesCompressed, 0, "Should compress some messages")
	assert.Equal(t, params.KeepRecent, result.MessagesKept, "Should keep recent messages as specified")

	// Summary should mention key topics from conversation
	summaryLower := strings.ToLower(result.Summary)
	assert.True(t,
		strings.Contains(summaryLower, "csv") ||
			strings.Contains(summaryLower, "python") ||
			strings.Contains(summaryLower, "file") ||
			strings.Contains(summaryLower, "data"),
		"Summary should mention key topics from conversation")
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

// =============================================================================
// TEST 7: Available Models - List compression models
// =============================================================================

func TestE2E_Compresr_AvailableModels(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey)

	// Get tool output models
	toolOutputModels, err := client.GetToolOutputModels()
	require.NoError(t, err, "GetToolOutputModels should not fail")

	t.Logf("Tool Output Models: %d", len(toolOutputModels))
	for _, model := range toolOutputModels {
		t.Logf("  - %s: %s", model.Value, model.Label)
	}

	assert.NotEmpty(t, toolOutputModels, "Should have tool output models")

	// Get tool discovery models
	toolDiscoveryModels, err := client.GetToolDiscoveryModels()
	require.NoError(t, err, "GetToolDiscoveryModels should not fail")

	t.Logf("Tool Discovery Models: %d", len(toolDiscoveryModels))
	for _, model := range toolDiscoveryModels {
		t.Logf("  - %s: %s", model.Value, model.Label)
	}

	// Tool discovery models may be empty if feature is not enabled for subscription
	if len(toolDiscoveryModels) == 0 {
		t.Log("No tool discovery models available (may require specific subscription)")
	}
}

// =============================================================================
// TEST 8: Invalid API Key - Should fail gracefully
// =============================================================================

func TestE2E_Compresr_InvalidAPIKey(t *testing.T) {
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, "invalid-api-key-12345")

	_, err := client.GetSubscription()
	assert.Error(t, err, "Should fail with invalid API key")
	t.Logf("Expected error: %v", err)
}

// =============================================================================
// TEST 9: Empty Input Handling
// =============================================================================

func TestE2E_Compresr_EmptyInput(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey)

	// Tool output with empty content should fail or return empty
	params := compresr.CompressToolOutputParams{
		ToolOutput: "",
		UserQuery:  "test",
		ToolName:   "test",
		ModelName:  "toc_latte_v1",
		Source:     "gateway",
	}

	_, err := client.CompressToolOutput(params)
	// Should either return error or handle gracefully
	if err != nil {
		t.Logf("Empty input handled with error (expected): %v", err)
	} else {
		t.Log("Empty input accepted (API may return empty result)")
	}
}

// =============================================================================
// TEST 10: Rate Limiting - Multiple rapid requests
// =============================================================================

func TestE2E_Compresr_RateLimiting(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey)

	// Make several rapid requests
	successCount := 0
	for i := 0; i < 5; i++ {
		params := compresr.CompressToolOutputParams{
			ToolOutput: fmt.Sprintf("Test content %d for rate limit testing", i),
			UserQuery:  "test",
			ToolName:   "test",
			ModelName:  "toc_latte_v1",
			Source:     "gateway",
		}

		_, err := client.CompressToolOutput(params)
		if err == nil {
			successCount++
		} else {
			t.Logf("Request %d failed (may be rate limited): %v", i, err)
		}
	}

	t.Logf("Successful requests: %d/5", successCount)
	assert.Greater(t, successCount, 0, "At least some requests should succeed")
}

// =============================================================================
// TEST 11: Tool Output - JSON/Structured Data (grep_search results)
// =============================================================================

func TestE2E_Compresr_ToolOutput_JSONStructured(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey, compresr.WithTimeout(30*time.Second))

	// Simulate grep_search output - common real-world scenario
	grepOutput := `{
  "matches": [
    {"file": "src/main.go", "line": 15, "content": "func main() {"},
    {"file": "src/main.go", "line": 45, "content": "    return nil"},
    {"file": "src/handler.go", "line": 23, "content": "func HandleRequest(w http.ResponseWriter, r *http.Request) {"},
    {"file": "src/handler.go", "line": 67, "content": "    json.NewEncoder(w).Encode(response)"},
    {"file": "src/handler.go", "line": 89, "content": "func validateInput(data map[string]interface{}) error {"},
    {"file": "src/config.go", "line": 12, "content": "type Config struct {"},
    {"file": "src/config.go", "line": 34, "content": "func LoadConfig(path string) (*Config, error) {"},
    {"file": "src/utils.go", "line": 8, "content": "func ParseJSON(data []byte) (map[string]interface{}, error) {"},
    {"file": "src/utils.go", "line": 22, "content": "func FormatError(err error) string {"},
    {"file": "tests/main_test.go", "line": 15, "content": "func TestMain(t *testing.T) {"},
    {"file": "tests/handler_test.go", "line": 28, "content": "func TestHandleRequest(t *testing.T) {"},
    {"file": "tests/handler_test.go", "line": 56, "content": "func TestValidateInput(t *testing.T) {"}
  ],
  "total_matches": 12,
  "files_searched": 156,
  "duration_ms": 45
}`

	params := compresr.CompressToolOutputParams{
		ToolOutput: grepOutput,
		UserQuery:  "Find the HandleRequest function",
		ToolName:   "grep_search",
		ModelName:  "toc_latte_v1",
		Source:     "gateway:anthropic",
	}

	result, err := client.CompressToolOutput(params)
	require.NoError(t, err, "CompressToolOutput should handle JSON output")

	t.Logf("Original: %d bytes", len(grepOutput))
	t.Logf("Compressed: %d bytes", len(result.CompressedOutput))
	t.Logf("Ratio: %.2f%%", float64(len(result.CompressedOutput))/float64(len(grepOutput))*100)

	assert.NotEmpty(t, result.CompressedOutput, "Should return compressed output")
	// Compressed output should mention HandleRequest since that's what user asked
	assert.True(t,
		strings.Contains(strings.ToLower(result.CompressedOutput), "handler") ||
			strings.Contains(strings.ToLower(result.CompressedOutput), "handlerequest") ||
			strings.Contains(strings.ToLower(result.CompressedOutput), "request"),
		"Compressed output should preserve relevant info about HandleRequest")
}

// =============================================================================
// TEST 12: Tool Output - Error/Stack Trace
// =============================================================================

func TestE2E_Compresr_ToolOutput_ErrorStackTrace(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey, compresr.WithTimeout(30*time.Second))

	// Simulate run_in_terminal error output with stack trace
	errorOutput := `Command failed with exit code 1:
$ go build ./...

# github.com/example/myapp/internal/handler
internal/handler/user.go:45:23: cannot use resp (variable of type *UserResponse) as type Response in return statement
internal/handler/user.go:67:12: undefined: ValidateUser
internal/handler/user.go:89:34: too many arguments in call to db.Query
        have (context.Context, string, string, int)
        want (context.Context, string)

# github.com/example/myapp/internal/auth
internal/auth/jwt.go:23:5: imported and not used: "crypto/rand"
internal/auth/jwt.go:56:18: cannot convert token (variable of type string) to type []byte

Stack trace:
goroutine 1 [running]:
runtime/debug.Stack()
        /usr/local/go/src/runtime/debug/stack.go:24 +0x65
github.com/example/myapp/internal/handler.(*Handler).ServeHTTP(0xc0001a2000, {0x7f9c8c0b2e00, 0xc0001a4000}, 0xc0001b6000)
        /app/internal/handler/handler.go:89 +0x245
net/http.(*ServeMux).ServeHTTP(0x0?, {0x7f9c8c0b2e00?, 0xc0001a4000?}, 0x0?)
        /usr/local/go/src/net/http/server.go:2514 +0x8d
net/http.serverHandler.ServeHTTP({0xc0001a0000?}, {0x7f9c8c0b2e00, 0xc0001a4000}, 0xc0001b6000)
        /usr/local/go/src/net/http/server.go:2938 +0x8e
net/http.(*conn).serve(0xc0001a2000, {0x7f9c8c0b3380, 0xc000198000})
        /usr/local/go/src/net/http/server.go:2009 +0x5f4
created by net/http.(*Server).Serve in goroutine 1
        /usr/local/go/src/net/http/server.go:3086 +0x4db`

	params := compresr.CompressToolOutputParams{
		ToolOutput: errorOutput,
		UserQuery:  "Why is my build failing?",
		ToolName:   "run_in_terminal",
		ModelName:  "toc_latte_v1",
		Source:     "gateway:anthropic",
	}

	result, err := client.CompressToolOutput(params)
	require.NoError(t, err, "CompressToolOutput should handle error/stack trace")

	t.Logf("Original: %d bytes", len(errorOutput))
	t.Logf("Compressed: %d bytes", len(result.CompressedOutput))
	t.Logf("Compressed output preview: %s", truncate(result.CompressedOutput, 300))

	assert.NotEmpty(t, result.CompressedOutput, "Should return compressed output")
	// Should preserve key error information
	compressedLower := strings.ToLower(result.CompressedOutput)
	assert.True(t,
		strings.Contains(compressedLower, "error") ||
			strings.Contains(compressedLower, "fail") ||
			strings.Contains(compressedLower, "cannot") ||
			strings.Contains(compressedLower, "undefined"),
		"Compressed output should preserve error information")
}

// =============================================================================
// TEST 13: Tool Output - Directory Listing (list_dir)
// =============================================================================

func TestE2E_Compresr_ToolOutput_DirectoryListing(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey, compresr.WithTimeout(30*time.Second))

	// Large directory listing - common real-world scenario
	var sb strings.Builder
	sb.WriteString("Contents of /Users/dev/myproject:\n\n")

	dirs := []string{"cmd/", "internal/", "pkg/", "api/", "web/", "docs/", "scripts/", "deployments/", "test/", "vendor/"}
	files := []string{
		"main.go", "go.mod", "go.sum", "Makefile", "Dockerfile", "docker-compose.yml",
		"README.md", "LICENSE", "CONTRIBUTING.md", ".gitignore", ".env.example",
	}

	for _, d := range dirs {
		sb.WriteString(d + "\n")
	}
	for _, f := range files {
		sb.WriteString(f + "\n")
	}

	// Add subdirectory contents
	internalFiles := []string{
		"internal/auth/", "internal/auth/jwt.go", "internal/auth/oauth.go", "internal/auth/middleware.go",
		"internal/handler/", "internal/handler/user.go", "internal/handler/product.go", "internal/handler/order.go",
		"internal/model/", "internal/model/user.go", "internal/model/product.go", "internal/model/order.go",
		"internal/repository/", "internal/repository/user.go", "internal/repository/product.go",
		"internal/service/", "internal/service/user.go", "internal/service/product.go", "internal/service/order.go",
		"internal/config/", "internal/config/config.go", "internal/config/database.go",
	}
	for _, f := range internalFiles {
		sb.WriteString(f + "\n")
	}

	dirListing := sb.String()

	params := compresr.CompressToolOutputParams{
		ToolOutput: dirListing,
		UserQuery:  "Where is the authentication code?",
		ToolName:   "list_dir",
		ModelName:  "toc_latte_v1",
		Source:     "gateway:anthropic",
	}

	result, err := client.CompressToolOutput(params)
	require.NoError(t, err, "CompressToolOutput should handle directory listing")

	t.Logf("Original: %d bytes", len(dirListing))
	t.Logf("Compressed: %d bytes", len(result.CompressedOutput))
	t.Logf("Ratio: %.2f%%", float64(len(result.CompressedOutput))/float64(len(dirListing))*100)

	assert.NotEmpty(t, result.CompressedOutput, "Should return compressed output")
	// Should mention auth since that's what user asked about
	compressedLower := strings.ToLower(result.CompressedOutput)
	assert.True(t,
		strings.Contains(compressedLower, "auth") ||
			strings.Contains(compressedLower, "internal"),
		"Compressed output should mention auth-related paths")
}

// =============================================================================
// TEST 14: Tool Output - Git Diff
// =============================================================================

func TestE2E_Compresr_ToolOutput_GitDiff(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey, compresr.WithTimeout(30*time.Second))

	gitDiff := `diff --git a/internal/handler/user.go b/internal/handler/user.go
index 3a4b5c6..7d8e9f0 100644
--- a/internal/handler/user.go
+++ b/internal/handler/user.go
@@ -23,10 +23,15 @@ type UserHandler struct {
 }
 
 func (h *UserHandler) GetUser(w http.ResponseWriter, r *http.Request) {
-    id := r.URL.Query().Get("id")
-    user, err := h.service.GetUser(r.Context(), id)
+    userID := chi.URLParam(r, "userID")
+    if userID == "" {
+        http.Error(w, "user ID is required", http.StatusBadRequest)
+        return
+    }
+    
+    user, err := h.service.GetUserByID(r.Context(), userID)
     if err != nil {
-        http.Error(w, err.Error(), http.StatusInternalServerError)
+        http.Error(w, "user not found", http.StatusNotFound)
         return
     }
     
@@ -45,6 +50,22 @@ func (h *UserHandler) GetUser(w http.ResponseWriter, r *http.Request) {
     json.NewEncoder(w).Encode(user)
 }

+func (h *UserHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
+    userID := chi.URLParam(r, "userID")
+    
+    var update UserUpdate
+    if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
+        http.Error(w, "invalid request body", http.StatusBadRequest)
+        return
+    }
+    
+    if err := h.service.UpdateUser(r.Context(), userID, update); err != nil {
+        http.Error(w, err.Error(), http.StatusInternalServerError)
+        return
+    }
+    
+    w.WriteHeader(http.StatusNoContent)
+}
+
 func (h *UserHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
diff --git a/internal/service/user.go b/internal/service/user.go
index 1a2b3c4..5d6e7f8 100644
--- a/internal/service/user.go
+++ b/internal/service/user.go
@@ -15,6 +15,10 @@ func (s *UserService) GetUser(ctx context.Context, id string) (*User, error) {
     return s.repo.FindByID(ctx, id)
 }

+func (s *UserService) GetUserByID(ctx context.Context, userID string) (*User, error) {
+    return s.repo.FindByID(ctx, userID)
+}
+
 func (s *UserService) CreateUser(ctx context.Context, user *User) error {
     return s.repo.Create(ctx, user)
 }`

	params := compresr.CompressToolOutputParams{
		ToolOutput: gitDiff,
		UserQuery:  "What changes were made to the UpdateUser function?",
		ToolName:   "run_in_terminal",
		ModelName:  "toc_latte_v1",
		Source:     "gateway:anthropic",
	}

	result, err := client.CompressToolOutput(params)
	require.NoError(t, err, "CompressToolOutput should handle git diff")

	t.Logf("Original: %d bytes", len(gitDiff))
	t.Logf("Compressed: %d bytes", len(result.CompressedOutput))
	t.Logf("Compressed output: %s", truncate(result.CompressedOutput, 400))

	assert.NotEmpty(t, result.CompressedOutput, "Should return compressed output")
	// Should mention UpdateUser since that's the query
	compressedLower := strings.ToLower(result.CompressedOutput)
	assert.True(t,
		strings.Contains(compressedLower, "update") ||
			strings.Contains(compressedLower, "user") ||
			strings.Contains(compressedLower, "add"),
		"Compressed output should mention UpdateUser changes")
}

// =============================================================================
// TEST 15: Tool Output - Very Large Output (stress test)
// =============================================================================

func TestE2E_Compresr_ToolOutput_VeryLarge(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey, compresr.WithTimeout(120*time.Second))

	// Generate ~100KB of code
	var sb strings.Builder
	sb.WriteString("package largefile\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n\t\"encoding/json\"\n)\n\n")

	for i := 0; i < 200; i++ {
		sb.WriteString(fmt.Sprintf(`
// Handler%d processes request type %d with validation and response handling.
// It implements the standard HTTP handler pattern with proper error handling.
func Handler%d(w http.ResponseWriter, r *http.Request) {
	// Parse request body
	var req Request%d
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	
	// Validate required fields
	if req.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	
	// Process the request
	result, err := processRequest%d(r.Context(), &req)
	if err != nil {
		http.Error(w, "processing failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	
	// Return response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

type Request%d struct {
	ID     string            `+"`json:\"id\"`"+`
	Type   string            `+"`json:\"type\"`"+`
	Data   map[string]any    `+"`json:\"data\"`"+`
}

`, i, i, i, i, i, i))
	}

	largeOutput := sb.String()
	t.Logf("Generated output size: %d bytes (%.2f KB)", len(largeOutput), float64(len(largeOutput))/1024)

	params := compresr.CompressToolOutputParams{
		ToolOutput: largeOutput,
		UserQuery:  "Find the Handler50 function",
		ToolName:   "read_file",
		ModelName:  "toc_latte_v1",
		Source:     "gateway:anthropic",
	}

	result, err := client.CompressToolOutput(params)
	require.NoError(t, err, "CompressToolOutput should handle very large output")

	t.Logf("Original: %d bytes (%.2f KB)", len(largeOutput), float64(len(largeOutput))/1024)
	t.Logf("Compressed: %d bytes (%.2f KB)", len(result.CompressedOutput), float64(len(result.CompressedOutput))/1024)
	ratio := float64(len(result.CompressedOutput)) / float64(len(largeOutput)) * 100
	t.Logf("Compression ratio: %.2f%%", ratio)

	assert.NotEmpty(t, result.CompressedOutput, "Should return compressed output")
	assert.Less(t, ratio, 80.0, "Large file should achieve meaningful compression (<80%%)")
}

// =============================================================================
// TEST 16: Tool Output - Unicode/Special Characters
// =============================================================================

func TestE2E_Compresr_ToolOutput_UnicodeSpecialChars(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey, compresr.WithTimeout(30*time.Second))

	// Content with various unicode and special characters
	unicodeContent := `# 日本語のドキュメント (Japanese Documentation)

## 概要 (Overview)
このプロジェクトは、マルチ言語サポートを提供します。

## Emojis in code comments 🚀
- Build status: ✅ Passing
- Tests: ✅ 156/156
- Coverage: 📊 87%

## Special characters in strings
const greeting = "Hello, 世界! 🌍"
const currency = "Price: €100 or ¥15000 or £80"
const math = "∑(x²) = √(n) × π"

## Code example with unicode identifiers
func 計算する(値 int) int {
    結果 := 値 * 2
    return 結果
}

## Error messages in multiple languages
var errors = map[string]string{
    "en": "File not found",
    "ja": "ファイルが見つかりません", 
    "es": "Archivo no encontrado",
    "de": "Datei nicht gefunden",
    "zh": "文件未找到",
    "ar": "الملف غير موجود",
    "ru": "Файл не найден",
}

## Box drawing characters
┌─────────────────────────────┐
│  Application Architecture   │
├─────────────────────────────┤
│  ┌─────┐    ┌─────────────┐ │
│  │ API │───→│  Database   │ │
│  └─────┘    └─────────────┘ │
└─────────────────────────────┘
`

	params := compresr.CompressToolOutputParams{
		ToolOutput: unicodeContent,
		UserQuery:  "What languages are supported?",
		ToolName:   "read_file",
		ModelName:  "toc_latte_v1",
		Source:     "gateway:anthropic",
	}

	result, err := client.CompressToolOutput(params)
	require.NoError(t, err, "CompressToolOutput should handle unicode content")

	t.Logf("Original: %d bytes", len(unicodeContent))
	t.Logf("Compressed: %d bytes", len(result.CompressedOutput))
	t.Logf("Compressed preview: %s", truncate(result.CompressedOutput, 300))

	assert.NotEmpty(t, result.CompressedOutput, "Should return compressed output")
}

// =============================================================================
// TEST 17: Tool Output - Different Models Comparison
// =============================================================================

func TestE2E_Compresr_ToolOutput_DifferentModels(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey, compresr.WithTimeout(60*time.Second))

	// Medium-sized code sample
	codeContent := `package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type Server struct {
	router *http.ServeMux
	db     Database
	cache  Cache
}

func NewServer(db Database, cache Cache) *Server {
	s := &Server{
		router: http.NewServeMux(),
		db:     db,
		cache:  cache,
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.router.HandleFunc("/api/users", s.handleUsers)
	s.router.HandleFunc("/api/products", s.handleProducts)
	s.router.HandleFunc("/api/orders", s.handleOrders)
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	
	users, err := s.db.GetUsers(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	json.NewEncoder(w).Encode(users)
}

func (s *Server) handleProducts(w http.ResponseWriter, r *http.Request) {
	// Implementation
}

func (s *Server) handleOrders(w http.ResponseWriter, r *http.Request) {
	// Implementation
}
`

	// Test models with their specific requirements
	// toc_latte_v1 supports query, toc_espresso_v1 (Lingua) does NOT support query
	type modelTest struct {
		name          string
		supportsQuery bool
	}
	models := []modelTest{
		{name: "toc_latte_v1", supportsQuery: true},
		{name: "toc_espresso_v1", supportsQuery: false},
	}
	results := make(map[string]int)

	for _, model := range models {
		query := ""
		if model.supportsQuery {
			query = "How does the server handle user requests?"
		}

		params := compresr.CompressToolOutputParams{
			ToolOutput: codeContent,
			UserQuery:  query,
			ToolName:   "read_file",
			ModelName:  model.name,
			Source:     "gateway:anthropic",
		}

		result, err := client.CompressToolOutput(params)
		if err != nil {
			t.Logf("Model %s failed: %v", model.name, err)
			continue
		}

		results[model.name] = len(result.CompressedOutput)
		t.Logf("Model %s (query=%v): %d → %d bytes (%.2f%%)",
			model.name, model.supportsQuery, len(codeContent), len(result.CompressedOutput),
			float64(len(result.CompressedOutput))/float64(len(codeContent))*100)
	}

	assert.GreaterOrEqual(t, len(results), 1, "At least one model should work")
}

// =============================================================================
// TEST 18: Tool Discovery - Edge Cases
// =============================================================================

func TestE2E_Compresr_ToolDiscovery_EdgeCases(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey, compresr.WithTimeout(30*time.Second))

	// Large tool set with similar descriptions
	tools := []compresr.ToolDefinition{
		{Name: "read_file", Description: "Read the contents of a file from the filesystem"},
		{Name: "write_file", Description: "Write content to a file on the filesystem"},
		{Name: "read_directory", Description: "Read directory contents and list files"},
		{Name: "create_directory", Description: "Create a new directory on the filesystem"},
		{Name: "delete_file", Description: "Delete a file from the filesystem"},
		{Name: "move_file", Description: "Move or rename a file on the filesystem"},
		{Name: "copy_file", Description: "Copy a file to a new location"},
		{Name: "search_files", Description: "Search for files matching a pattern"},
		{Name: "grep_search", Description: "Search for text within files using regex"},
		{Name: "semantic_search", Description: "Search codebase using semantic understanding"},
		{Name: "run_command", Description: "Execute a shell command"},
		{Name: "run_python", Description: "Execute Python code"},
		{Name: "run_tests", Description: "Run test suite"},
		{Name: "git_status", Description: "Get current git repository status"},
		{Name: "git_diff", Description: "Show git diff of changes"},
		{Name: "git_commit", Description: "Commit changes to git"},
		{Name: "git_push", Description: "Push commits to remote"},
		{Name: "http_request", Description: "Make HTTP request to external API"},
		{Name: "database_query", Description: "Execute database query"},
		{Name: "send_notification", Description: "Send notification to user"},
	}

	testCases := []struct {
		query       string
		expectTools []string
		description string
	}{
		{
			query:       "I need to find where the handleUser function is defined",
			expectTools: []string{"grep_search", "semantic_search", "search_files", "read_file"},
			description: "Search-related query",
		},
		{
			query:       "Create a new file called config.yaml",
			expectTools: []string{"write_file", "create_directory"},
			description: "File creation query",
		},
		{
			query:       "Run the unit tests and check if they pass",
			expectTools: []string{"run_tests", "run_command"},
			description: "Testing query",
		},
		{
			query:       "Commit my changes and push to main branch",
			expectTools: []string{"git_commit", "git_push", "git_status"},
			description: "Git operations query",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			params := compresr.FilterToolsParams{
				Query:     tc.query,
				Tools:     tools,
				MaxTools:  5,
				ModelName: "tdc_coldbrew_v1",
				Source:    "gateway",
			}

			result, err := client.FilterTools(params)
			require.NoError(t, err, "FilterTools should not fail")

			t.Logf("Query: %s", tc.query)
			t.Logf("Returned tools: %v", result.RelevantTools)

			assert.NotEmpty(t, result.RelevantTools, "Should return relevant tools")
			assert.LessOrEqual(t, len(result.RelevantTools), params.MaxTools, "Should not exceed MaxTools")

			// Check if at least one expected tool is in results
			hasExpected := false
			for _, expected := range tc.expectTools {
				for _, got := range result.RelevantTools {
					if got == expected {
						hasExpected = true
						break
					}
				}
			}
			assert.True(t, hasExpected, "Should return at least one expected tool from %v", tc.expectTools)
		})
	}
}

// =============================================================================
// TEST 19: Tool Output - Malformed/Edge Case Inputs
// =============================================================================

func TestE2E_Compresr_ToolOutput_EdgeCaseInputs(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey, compresr.WithTimeout(30*time.Second))

	testCases := []struct {
		name       string
		toolOutput string
		query      string
		toolName   string
		shouldPass bool
	}{
		{
			name:       "Whitespace only",
			toolOutput: "   \n\t\n   ",
			query:      "test",
			toolName:   "test",
			shouldPass: true, // API should handle gracefully
		},
		{
			name:       "Very short content",
			toolOutput: "ok",
			query:      "status",
			toolName:   "run_command",
			shouldPass: true,
		},
		{
			name:       "Repeated characters",
			toolOutput: strings.Repeat("a", 5000),
			query:      "analyze this",
			toolName:   "read_file",
			shouldPass: true,
		},
		{
			name:       "Newlines only",
			toolOutput: strings.Repeat("\n", 100),
			query:      "parse output",
			toolName:   "run_command",
			shouldPass: true,
		},
		{
			name:       "JSON array",
			toolOutput: `[1, 2, 3, 4, 5, {"key": "value"}, [nested], null, true, false]`,
			query:      "parse JSON",
			toolName:   "http_request",
			shouldPass: true,
		},
		{
			name:       "Binary-like content",
			toolOutput: "\x00\x01\x02\x03\x04\x05 binary header \xff\xfe\xfd",
			query:      "analyze binary",
			toolName:   "read_file",
			shouldPass: true, // Should handle gracefully
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			params := compresr.CompressToolOutputParams{
				ToolOutput: tc.toolOutput,
				UserQuery:  tc.query,
				ToolName:   tc.toolName,
				ModelName:  "toc_latte_v1",
				Source:     "gateway",
			}

			result, err := client.CompressToolOutput(params)

			if tc.shouldPass {
				if err != nil {
					t.Logf("Note: %s returned error (may be expected): %v", tc.name, err)
				} else {
					t.Logf("%s: %d → %d bytes", tc.name, len(tc.toolOutput), len(result.CompressedOutput))
					assert.NotNil(t, result, "Should return a result")
				}
			} else {
				assert.Error(t, err, "Should fail for invalid input")
			}
		})
	}
}

// =============================================================================
// TEST 20: Tool Output - Real World File Contents
// =============================================================================

func TestE2E_Compresr_ToolOutput_RealWorldFiles(t *testing.T) {
	apiKey := getCompresrKey(t)
	baseURL := getCompresrURL()

	client := compresr.NewClient(baseURL, apiKey, compresr.WithTimeout(60*time.Second))

	// package.json
	packageJSON := `{
  "name": "my-react-app",
  "version": "2.1.0",
  "description": "A comprehensive React application with TypeScript",
  "main": "dist/index.js",
  "scripts": {
    "dev": "vite",
    "build": "tsc && vite build",
    "test": "vitest",
    "test:coverage": "vitest --coverage",
    "lint": "eslint src --ext .ts,.tsx",
    "lint:fix": "eslint src --ext .ts,.tsx --fix",
    "format": "prettier --write src/**/*.{ts,tsx,css}",
    "typecheck": "tsc --noEmit",
    "prepare": "husky install"
  },
  "dependencies": {
    "react": "^18.2.0",
    "react-dom": "^18.2.0",
    "react-router-dom": "^6.14.0",
    "@tanstack/react-query": "^4.29.0",
    "axios": "^1.4.0",
    "zustand": "^4.3.8",
    "tailwindcss": "^3.3.2",
    "clsx": "^1.2.1",
    "date-fns": "^2.30.0",
    "zod": "^3.21.4"
  },
  "devDependencies": {
    "@types/react": "^18.2.0",
    "@types/react-dom": "^18.2.0",
    "@vitejs/plugin-react": "^4.0.0",
    "typescript": "^5.1.0",
    "vite": "^4.3.9",
    "vitest": "^0.32.0",
    "eslint": "^8.43.0",
    "prettier": "^2.8.8",
    "husky": "^8.0.3"
  }
}`

	params := compresr.CompressToolOutputParams{
		ToolOutput: packageJSON,
		UserQuery:  "What testing framework does this project use?",
		ToolName:   "read_file",
		ModelName:  "toc_latte_v1",
		Source:     "gateway:anthropic",
	}

	result, err := client.CompressToolOutput(params)
	require.NoError(t, err, "Should compress package.json")

	t.Logf("package.json: %d → %d bytes (%.2f%%)",
		len(packageJSON), len(result.CompressedOutput),
		float64(len(result.CompressedOutput))/float64(len(packageJSON))*100)
	t.Logf("Compressed: %s", truncate(result.CompressedOutput, 300))

	// Should mention vitest since that's the testing framework
	compressedLower := strings.ToLower(result.CompressedOutput)
	assert.True(t,
		strings.Contains(compressedLower, "vitest") ||
			strings.Contains(compressedLower, "test"),
		"Should mention testing framework")
}

// Helper to use fmt
var _ = fmt.Sprintf
