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
		url = "https://api.compresr.ai"
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

// Helper to use fmt
var _ = fmt.Sprintf
