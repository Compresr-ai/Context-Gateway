package compresr_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/compresr/context-gateway/internal/compresr"
)

// =============================================================================
// TOOL OUTPUT COMPRESSION TESTS
// =============================================================================

func TestCompressToolOutput(t *testing.T) {
	tests := []struct {
		name           string
		params         compresr.CompressToolOutputParams
		serverResponse compresr.APIResponse[compresr.CompressToolOutputResponse]
		serverStatus   int
		expectError    bool
		expectOutput   string
	}{
		{
			name: "successful compression with gemfilter model",
			params: compresr.CompressToolOutputParams{
				ToolOutput: "This is a very long tool output that needs compression",
				UserQuery:  "What is the file content?",
				ToolName:   "read_file",
				ModelName:  "toc_latte_v1",
				Source:     "gateway:anthropic",
			},
			serverResponse: compresr.APIResponse[compresr.CompressToolOutputResponse]{
				Success: true,
				Data: compresr.CompressToolOutputResponse{
					CompressedOutput: "Compressed: file content",
				},
			},
			serverStatus: http.StatusOK,
			expectError:  false,
			expectOutput: "Compressed: file content",
		},
		{
			name: "successful compression with sat model",
			params: compresr.CompressToolOutputParams{
				ToolOutput: "Large tool output content",
				UserQuery:  "What does this do?",
				ToolName:   "grep_search",
				ModelName:  "toc_espresso_v1",
				Source:     "gateway:openai",
			},
			serverResponse: compresr.APIResponse[compresr.CompressToolOutputResponse]{
				Success: true,
				Data: compresr.CompressToolOutputResponse{
					CompressedOutput: "Compressed: search results",
				},
			},
			serverStatus: http.StatusOK,
			expectError:  false,
			expectOutput: "Compressed: search results",
		},
		{
			name: "successful compression with default model",
			params: compresr.CompressToolOutputParams{
				ToolOutput: "Tool output content",
				UserQuery:  "",
				ToolName:   "list_dir",
				ModelName:  "", // Should default to toc_latte_v1
				Source:     "gateway:openai",
			},
			serverResponse: compresr.APIResponse[compresr.CompressToolOutputResponse]{
				Success: true,
				Data: compresr.CompressToolOutputResponse{
					CompressedOutput: "Compressed output",
				},
			},
			serverStatus: http.StatusOK,
			expectError:  false,
			expectOutput: "Compressed output",
		},
		{
			name: "api returns error",
			params: compresr.CompressToolOutputParams{
				ToolOutput: "content",
				ToolName:   "test_tool",
			},
			serverResponse: compresr.APIResponse[compresr.CompressToolOutputResponse]{
				Success: false,
				Message: "compression failed",
			},
			serverStatus: http.StatusOK,
			expectError:  true,
		},
		{
			name: "server returns HTTP error",
			params: compresr.CompressToolOutputParams{
				ToolOutput: "content",
				ToolName:   "test_tool",
			},
			serverStatus: http.StatusInternalServerError,
			expectError:  true,
		},
		{
			name: "unauthorized - invalid API key",
			params: compresr.CompressToolOutputParams{
				ToolOutput: "content",
				ToolName:   "test_tool",
			},
			serverStatus: http.StatusUnauthorized,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != "/api/compress/tool-output/" {
					t.Errorf("expected /api/compress/tool-output/, got %s", r.URL.Path)
				}
				if r.Header.Get("X-API-Key") == "" {
					t.Error("expected X-API-Key header")
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
				}

				// Verify request body
				var reqBody struct {
					ToolOutput string `json:"tool_output"`
					Query      string `json:"query"`
					ToolName   string `json:"tool_name"`
					ModelName  string `json:"compression_model_name"`
					Source     string `json:"source"`
				}
				if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
					t.Errorf("failed to decode request body: %v", err)
				}
				if reqBody.ToolOutput != tt.params.ToolOutput {
					t.Errorf("expected ToolOutput %q, got %q", tt.params.ToolOutput, reqBody.ToolOutput)
				}
				if reqBody.ToolName != tt.params.ToolName {
					t.Errorf("expected ToolName %q, got %q", tt.params.ToolName, reqBody.ToolName)
				}

				// Model should default to toc_latte_v1 if not specified
				expectedModel := tt.params.ModelName
				if expectedModel == "" {
					expectedModel = "toc_latte_v1"
				}
				if reqBody.ModelName != expectedModel {
					t.Errorf("expected ModelName %q, got %q", expectedModel, reqBody.ModelName)
				}

				w.WriteHeader(tt.serverStatus)
				if tt.serverStatus == http.StatusOK {
					json.NewEncoder(w).Encode(tt.serverResponse)
				}
			}))
			defer server.Close()

			// Create client with test server
			client := compresr.NewClient(server.URL, "test-api-key")

			// Make request
			result, err := client.CompressToolOutput(tt.params)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result.CompressedOutput != tt.expectOutput {
				t.Errorf("expected output %q, got %q", tt.expectOutput, result.CompressedOutput)
			}
		})
	}
}

func TestCompressToolOutput_NoAPIKey(t *testing.T) {
	client := compresr.NewClient("http://localhost", "")
	_, err := client.CompressToolOutput(compresr.CompressToolOutputParams{
		ToolOutput: "content",
		ToolName:   "test",
	})
	if err == nil {
		t.Error("expected error when no API key configured")
	}
}

// =============================================================================
// TOOL OUTPUT VALIDATION TESTS
// =============================================================================

func TestCompressToolOutput_ValidationErrors(t *testing.T) {
	client := compresr.NewClient("http://localhost", "test-key")

	tests := []struct {
		name        string
		params      compresr.CompressToolOutputParams
		expectedErr string
	}{
		{
			name: "empty tool_output",
			params: compresr.CompressToolOutputParams{
				ToolOutput: "",
				ToolName:   "test",
			},
			expectedErr: "tool_output is required",
		},
		{
			name: "empty tool_name",
			params: compresr.CompressToolOutputParams{
				ToolOutput: "content",
				ToolName:   "",
			},
			expectedErr: "tool_name is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.CompressToolOutput(tt.params)
			if err == nil {
				t.Error("expected error, got nil")
				return
			}
			if err.Error() != tt.expectedErr {
				t.Errorf("expected error %q, got %q", tt.expectedErr, err.Error())
			}
		})
	}
}

// =============================================================================
// TOOL DISCOVERY (FILTER TOOLS) TESTS
// =============================================================================

func TestFilterTools(t *testing.T) {
	tests := []struct {
		name           string
		params         compresr.FilterToolsParams
		serverResponse compresr.APIResponse[compresr.FilterToolsResponse]
		serverStatus   int
		expectError    bool
		expectTools    []string
	}{
		{
			name: "successful filtering",
			params: compresr.FilterToolsParams{
				Query:      "read a file",
				AlwaysKeep: []string{"submit_answer"},
				Tools: []compresr.ToolDefinition{
					{Name: "read_file", Description: "Read file contents"},
					{Name: "write_file", Description: "Write file contents"},
					{Name: "list_dir", Description: "List directory"},
				},
			},
			serverResponse: compresr.APIResponse[compresr.FilterToolsResponse]{
				Success: true,
				Data: compresr.FilterToolsResponse{
					RelevantTools: []string{"read_file", "submit_answer"},
				},
			},
			serverStatus: http.StatusOK,
			expectError:  false,
			expectTools:  []string{"read_file", "submit_answer"},
		},
		{
			name: "filtering with parameters",
			params: compresr.FilterToolsParams{
				Query: "search for code",
				Tools: []compresr.ToolDefinition{
					{
						Name:        "grep_search",
						Description: "Search for patterns",
						Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
					},
				},
			},
			serverResponse: compresr.APIResponse[compresr.FilterToolsResponse]{
				Success: true,
				Data: compresr.FilterToolsResponse{
					RelevantTools: []string{"grep_search"},
				},
			},
			serverStatus: http.StatusOK,
			expectError:  false,
			expectTools:  []string{"grep_search"},
		},
		{
			name: "api returns error",
			params: compresr.FilterToolsParams{
				Query: "test",
				Tools: []compresr.ToolDefinition{{Name: "test"}},
			},
			serverResponse: compresr.APIResponse[compresr.FilterToolsResponse]{
				Success: false,
				Message: "filtering failed",
			},
			serverStatus: http.StatusOK,
			expectError:  true,
		},
		{
			name: "server returns HTTP error",
			params: compresr.FilterToolsParams{
				Query: "test",
				Tools: []compresr.ToolDefinition{{Name: "test"}},
			},
			serverStatus: http.StatusInternalServerError,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != "/api/compress/tool-discovery/" {
					t.Errorf("expected /api/compress/tool-discovery/, got %s", r.URL.Path)
				}
				if r.Header.Get("X-API-Key") == "" {
					t.Error("expected X-API-Key header")
				}

				// Verify request body
				var reqBody struct {
					Query                string                    `json:"query"`
					AlwaysKeep           []string                  `json:"always_keep"`
					Tools                []compresr.ToolDefinition `json:"tools"`
					MaxTools             int                       `json:"max_tools"`
					CompressionModelName string                    `json:"compression_model_name"`
				}
				if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
					t.Errorf("failed to decode request body: %v", err)
				}
				if reqBody.Query != tt.params.Query {
					t.Errorf("expected Query %q, got %q", tt.params.Query, reqBody.Query)
				}
				if len(reqBody.Tools) != len(tt.params.Tools) {
					t.Errorf("expected %d tools, got %d", len(tt.params.Tools), len(reqBody.Tools))
				}
				// Verify compression_model_name defaults to tdc_espresso
				expectedModel := tt.params.ModelName
				if expectedModel == "" {
					expectedModel = "tdc_coldbrew_v1"
				}
				if reqBody.CompressionModelName != expectedModel {
					t.Errorf("expected CompressionModelName %q, got %q", expectedModel, reqBody.CompressionModelName)
				}
				// Verify max_tools defaults to 5 (backend default)
				expectedMaxTools := tt.params.MaxTools
				if expectedMaxTools <= 0 {
					expectedMaxTools = 5
				}
				if reqBody.MaxTools != expectedMaxTools {
					t.Errorf("expected MaxTools %d, got %d", expectedMaxTools, reqBody.MaxTools)
				}

				w.WriteHeader(tt.serverStatus)
				if tt.serverStatus == http.StatusOK {
					json.NewEncoder(w).Encode(tt.serverResponse)
				}
			}))
			defer server.Close()

			// Create client with test server
			client := compresr.NewClient(server.URL, "test-api-key")

			// Make request
			result, err := client.FilterTools(tt.params)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if len(result.RelevantTools) != len(tt.expectTools) {
				t.Errorf("expected %d tools, got %d", len(tt.expectTools), len(result.RelevantTools))
				return
			}
			for i, tool := range result.RelevantTools {
				if tool != tt.expectTools[i] {
					t.Errorf("tool[%d]: expected %q, got %q", i, tt.expectTools[i], tool)
				}
			}
		})
	}
}

func TestFilterTools_NoAPIKey(t *testing.T) {
	client := compresr.NewClient("http://localhost", "")
	_, err := client.FilterTools(compresr.FilterToolsParams{
		Query: "test",
		Tools: []compresr.ToolDefinition{{Name: "test"}},
	})
	if err == nil {
		t.Error("expected error when no API key configured")
	}
}

// =============================================================================
// CLIENT CONFIGURATION TESTS
// =============================================================================

func TestNewClient_DefaultBaseURL(t *testing.T) {
	// Test that default base URL is used when none provided
	client := compresr.NewClient("", "test-key")
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	// Note: We can't directly check baseURL since it's private,
	// but we know from implementation it defaults to "https://api.compresr.ai"
}

func TestClient_HasAPIKey(t *testing.T) {
	// Save and clear env var to test empty key behavior
	savedKey := os.Getenv("COMPRESR_API_KEY")
	os.Unsetenv("COMPRESR_API_KEY")
	defer os.Setenv("COMPRESR_API_KEY", savedKey)

	clientWithKey := compresr.NewClient("http://localhost", "my-key")
	if !clientWithKey.HasAPIKey() {
		t.Error("expected HasAPIKey to return true")
	}

	clientWithoutKey := compresr.NewClient("http://localhost", "")
	if clientWithoutKey.HasAPIKey() {
		t.Error("expected HasAPIKey to return false")
	}
}

func TestClient_SetAPIKey(t *testing.T) {
	// Save and clear env var to test empty key behavior
	savedKey := os.Getenv("COMPRESR_API_KEY")
	os.Unsetenv("COMPRESR_API_KEY")
	defer os.Setenv("COMPRESR_API_KEY", savedKey)

	client := compresr.NewClient("http://localhost", "")
	if client.HasAPIKey() {
		t.Error("expected no API key initially")
	}

	client.SetAPIKey("new-key")
	if !client.HasAPIKey() {
		t.Error("expected HasAPIKey to return true after SetAPIKey")
	}
}

// =============================================================================
// TOOL OUTPUT QUERY FIELD TESTS
// =============================================================================

func TestCompressToolOutput_QueryFieldMapping(t *testing.T) {
	// This test verifies that the UserQuery param is sent as "query" in JSON
	// to match backend schema (was previously sent as "user_query" by mistake)
	tests := []struct {
		name         string
		userQuery    string
		expectInJSON string
		omitIfEmpty  bool
	}{
		{
			name:         "query with content",
			userQuery:    "What is the file content?",
			expectInJSON: "What is the file content?",
			omitIfEmpty:  false,
		},
		{
			name:         "empty query for lingua model",
			userQuery:    "",
			expectInJSON: "",
			omitIfEmpty:  true, // Empty query should be omitted from JSON
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var reqBody map[string]any
				json.NewDecoder(r.Body).Decode(&reqBody)

				// Verify "query" field (NOT "user_query")
				query, hasQuery := reqBody["query"]
				if tt.omitIfEmpty && tt.userQuery == "" {
					// Empty query should still be present but empty
					if hasQuery && query != "" {
						t.Errorf("expected empty query, got %v", query)
					}
				} else {
					if query != tt.expectInJSON {
						t.Errorf("expected query %q, got %v", tt.expectInJSON, query)
					}
				}

				// Verify "user_query" is NOT present (old incorrect field name)
				if _, hasOld := reqBody["user_query"]; hasOld {
					t.Error("should not have user_query field - use query instead")
				}

				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(compresr.APIResponse[compresr.CompressToolOutputResponse]{
					Success: true,
					Data:    compresr.CompressToolOutputResponse{CompressedOutput: "test"},
				})
			}))
			defer server.Close()

			client := compresr.NewClient(server.URL, "test-key")
			_, _ = client.CompressToolOutput(compresr.CompressToolOutputParams{
				ToolOutput: "test content",
				UserQuery:  tt.userQuery,
				ToolName:   "test_tool",
			})
		})
	}
}

// =============================================================================
// TOOL DISCOVERY MODEL AND MAX_TOOLS DEFAULTS TESTS
// =============================================================================

func TestFilterTools_DefaultsApplied(t *testing.T) {
	tests := []struct {
		name        string
		modelName   string
		maxTools    int
		expectModel string
		expectMax   int
	}{
		{
			name:        "defaults applied when not specified",
			modelName:   "",
			maxTools:    0,
			expectModel: "tdc_coldbrew_v1",
			expectMax:   5, // Backend default is 5
		},
		{
			name:        "custom model name",
			modelName:   "custom_discovery_model",
			maxTools:    0,
			expectModel: "custom_discovery_model",
			expectMax:   5,
		},
		{
			name:        "custom max tools",
			modelName:   "",
			maxTools:    25,
			expectModel: "tdc_coldbrew_v1",
			expectMax:   25,
		},
		{
			name:        "both custom",
			modelName:   "custom_model",
			maxTools:    15,
			expectModel: "custom_model",
			expectMax:   15,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var reqBody struct {
					CompressionModelName string `json:"compression_model_name"`
					MaxTools             int    `json:"max_tools"`
				}
				json.NewDecoder(r.Body).Decode(&reqBody)

				if reqBody.CompressionModelName != tt.expectModel {
					t.Errorf("expected compression_model_name %q, got %q", tt.expectModel, reqBody.CompressionModelName)
				}
				if reqBody.MaxTools != tt.expectMax {
					t.Errorf("expected max_tools %d, got %d", tt.expectMax, reqBody.MaxTools)
				}

				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(compresr.APIResponse[compresr.FilterToolsResponse]{
					Success: true,
					Data:    compresr.FilterToolsResponse{RelevantTools: []string{"test"}},
				})
			}))
			defer server.Close()

			client := compresr.NewClient(server.URL, "test-key")
			_, _ = client.FilterTools(compresr.FilterToolsParams{
				Query:     "test query",
				Tools:     []compresr.ToolDefinition{{Name: "test"}},
				ModelName: tt.modelName,
				MaxTools:  tt.maxTools,
			})
		})
	}
}

// =============================================================================
// EDGE CASE TESTS
// =============================================================================

func TestCompressToolOutput_LargeContent(t *testing.T) {
	// Test handling of large tool outputs
	largeContent := make([]byte, 100*1024) // 100KB
	for i := range largeContent {
		largeContent[i] = 'x'
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]any
		json.NewDecoder(r.Body).Decode(&reqBody)

		content, _ := reqBody["tool_output"].(string)
		if len(content) != len(largeContent) {
			t.Errorf("expected content length %d, got %d", len(largeContent), len(content))
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(compresr.APIResponse[compresr.CompressToolOutputResponse]{
			Success: true,
			Data:    compresr.CompressToolOutputResponse{CompressedOutput: "compressed"},
		})
	}))
	defer server.Close()

	client := compresr.NewClient(server.URL, "test-key")
	result, err := client.CompressToolOutput(compresr.CompressToolOutputParams{
		ToolOutput: string(largeContent),
		UserQuery:  "summarize",
		ToolName:   "large_tool",
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result.CompressedOutput != "compressed" {
		t.Errorf("expected compressed output, got %q", result.CompressedOutput)
	}
}

func TestFilterTools_ManyTools(t *testing.T) {
	// Test handling of many tools
	tools := make([]compresr.ToolDefinition, 100)
	for i := range tools {
		tools[i] = compresr.ToolDefinition{
			Name:        fmt.Sprintf("tool_%d", i),
			Description: fmt.Sprintf("Description for tool %d", i),
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody struct {
			Tools []compresr.ToolDefinition `json:"tools"`
		}
		json.NewDecoder(r.Body).Decode(&reqBody)

		if len(reqBody.Tools) != 100 {
			t.Errorf("expected 100 tools, got %d", len(reqBody.Tools))
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(compresr.APIResponse[compresr.FilterToolsResponse]{
			Success: true,
			Data:    compresr.FilterToolsResponse{RelevantTools: []string{"tool_0", "tool_1"}},
		})
	}))
	defer server.Close()

	client := compresr.NewClient(server.URL, "test-key")
	result, err := client.FilterTools(compresr.FilterToolsParams{
		Query:    "test",
		Tools:    tools,
		MaxTools: 10,
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(result.RelevantTools) != 2 {
		t.Errorf("expected 2 relevant tools, got %d", len(result.RelevantTools))
	}
}

func TestFilterTools_WithAlwaysKeep(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody struct {
			AlwaysKeep []string `json:"always_keep"`
		}
		json.NewDecoder(r.Body).Decode(&reqBody)

		if len(reqBody.AlwaysKeep) != 2 {
			t.Errorf("expected 2 always_keep tools, got %d", len(reqBody.AlwaysKeep))
		}
		if reqBody.AlwaysKeep[0] != "submit_answer" || reqBody.AlwaysKeep[1] != "ask_user" {
			t.Errorf("unexpected always_keep values: %v", reqBody.AlwaysKeep)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(compresr.APIResponse[compresr.FilterToolsResponse]{
			Success: true,
			Data:    compresr.FilterToolsResponse{RelevantTools: []string{"submit_answer", "ask_user", "read_file"}},
		})
	}))
	defer server.Close()

	client := compresr.NewClient(server.URL, "test-key")
	result, err := client.FilterTools(compresr.FilterToolsParams{
		Query:      "read a file",
		Tools:      []compresr.ToolDefinition{{Name: "read_file"}, {Name: "write_file"}},
		AlwaysKeep: []string{"submit_answer", "ask_user"},
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(result.RelevantTools) != 3 {
		t.Errorf("expected 3 relevant tools, got %d", len(result.RelevantTools))
	}
}

func TestCompressToolOutput_SourceTracking(t *testing.T) {
	// Test that source field is properly sent for analytics
	sources := []string{
		"gateway:anthropic",
		"gateway:openai",
		"sdk:python",
		"sdk:go",
		"extension:vscode",
	}

	for _, source := range sources {
		t.Run(source, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var reqBody map[string]any
				json.NewDecoder(r.Body).Decode(&reqBody)

				gotSource, _ := reqBody["source"].(string)
				if gotSource != source {
					t.Errorf("expected source %q, got %q", source, gotSource)
				}

				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(compresr.APIResponse[compresr.CompressToolOutputResponse]{
					Success: true,
					Data:    compresr.CompressToolOutputResponse{CompressedOutput: "test"},
				})
			}))
			defer server.Close()

			client := compresr.NewClient(server.URL, "test-key")
			_, _ = client.CompressToolOutput(compresr.CompressToolOutputParams{
				ToolOutput: "content",
				ToolName:   "test",
				Source:     source,
			})
		})
	}
}

func TestFilterTools_SourceTracking(t *testing.T) {
	// Test that source field is properly sent for tool discovery analytics
	sources := []string{
		"gateway:anthropic",
		"gateway:openai",
		"sdk:python",
	}

	for _, source := range sources {
		t.Run(source, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var reqBody map[string]any
				json.NewDecoder(r.Body).Decode(&reqBody)

				gotSource, _ := reqBody["source"].(string)
				if gotSource != source {
					t.Errorf("expected source %q, got %q", source, gotSource)
				}

				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(compresr.APIResponse[compresr.FilterToolsResponse]{
					Success: true,
					Data:    compresr.FilterToolsResponse{RelevantTools: []string{"test"}},
				})
			}))
			defer server.Close()

			client := compresr.NewClient(server.URL, "test-key")
			_, _ = client.FilterTools(compresr.FilterToolsParams{
				Query:  "test query",
				Tools:  []compresr.ToolDefinition{{Name: "test"}},
				Source: source,
			})
		})
	}
}

// =============================================================================
// FILTER TOOLS VALIDATION TESTS
// =============================================================================

func TestFilterTools_ValidationErrors(t *testing.T) {
	client := compresr.NewClient("http://localhost", "test-key")

	tests := []struct {
		name        string
		params      compresr.FilterToolsParams
		expectedErr string
	}{
		{
			name: "empty query",
			params: compresr.FilterToolsParams{
				Query: "",
				Tools: []compresr.ToolDefinition{{Name: "test"}},
			},
			expectedErr: "query is required",
		},
		{
			name: "empty tools list",
			params: compresr.FilterToolsParams{
				Query: "test query",
				Tools: []compresr.ToolDefinition{},
			},
			expectedErr: "tools list is required and must not be empty",
		},
		{
			name: "nil tools list",
			params: compresr.FilterToolsParams{
				Query: "test query",
				Tools: nil,
			},
			expectedErr: "tools list is required and must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.FilterTools(tt.params)
			if err == nil {
				t.Error("expected error, got nil")
				return
			}
			if err.Error() != tt.expectedErr {
				t.Errorf("expected error %q, got %q", tt.expectedErr, err.Error())
			}
		})
	}
}

// =============================================================================
// HTTP HEADER TESTS
// =============================================================================

func TestClient_UserAgentHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userAgent := r.Header.Get("User-Agent")
		if userAgent != "compresr-gateway/1.0" {
			t.Errorf("expected User-Agent 'compresr-gateway/1.0', got %q", userAgent)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(compresr.APIResponse[compresr.CompressToolOutputResponse]{
			Success: true,
			Data:    compresr.CompressToolOutputResponse{CompressedOutput: "test"},
		})
	}))
	defer server.Close()

	client := compresr.NewClient(server.URL, "test-key")
	_, _ = client.CompressToolOutput(compresr.CompressToolOutputParams{
		ToolOutput: "content",
		ToolName:   "test",
	})
}

func TestClient_ContentTypeHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType := r.Header.Get("Content-Type")
		if contentType != "application/json" {
			t.Errorf("expected Content-Type 'application/json', got %q", contentType)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(compresr.APIResponse[compresr.CompressToolOutputResponse]{
			Success: true,
			Data:    compresr.CompressToolOutputResponse{CompressedOutput: "test"},
		})
	}))
	defer server.Close()

	client := compresr.NewClient(server.URL, "test-key")
	_, _ = client.CompressToolOutput(compresr.CompressToolOutputParams{
		ToolOutput: "content",
		ToolName:   "test",
	})
}

// =============================================================================
// PARAMETERS FIELD TESTS (Backend expects 'parameters', not 'definition')
// =============================================================================

func TestFilterTools_ParametersField(t *testing.T) {
	// This test verifies that tool parameters are sent as 'parameters' (not 'definition')
	// to match backend schema
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody struct {
			Tools []struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				Parameters  map[string]any `json:"parameters"`
				Definition  map[string]any `json:"definition"` // Should be absent
			} `json:"tools"`
		}
		json.NewDecoder(r.Body).Decode(&reqBody)

		if len(reqBody.Tools) != 1 {
			t.Errorf("expected 1 tool, got %d", len(reqBody.Tools))
			return
		}

		tool := reqBody.Tools[0]
		if tool.Parameters == nil {
			t.Error("expected 'parameters' field to be present")
		}
		if tool.Definition != nil {
			t.Error("'definition' field should not be present - use 'parameters' instead")
		}
		if tool.Parameters["type"] != "object" {
			t.Errorf("expected parameters.type='object', got %v", tool.Parameters["type"])
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(compresr.APIResponse[compresr.FilterToolsResponse]{
			Success: true,
			Data:    compresr.FilterToolsResponse{RelevantTools: []string{"test_tool"}},
		})
	}))
	defer server.Close()

	client := compresr.NewClient(server.URL, "test-key")
	_, err := client.FilterTools(compresr.FilterToolsParams{
		Query: "test query",
		Tools: []compresr.ToolDefinition{
			{
				Name:        "test_tool",
				Description: "A test tool",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"arg1": map[string]any{"type": "string"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// =============================================================================
// ERROR RESPONSE TESTS
// =============================================================================

func TestCompressToolOutput_APIErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(compresr.APIResponse[compresr.CompressToolOutputResponse]{
			Success: false,
			Message: "rate limit exceeded",
		})
	}))
	defer server.Close()

	client := compresr.NewClient(server.URL, "test-key")
	_, err := client.CompressToolOutput(compresr.CompressToolOutputParams{
		ToolOutput: "content",
		ToolName:   "test",
	})

	if err == nil {
		t.Error("expected error, got nil")
		return
	}
	if err.Error() != "API error: rate limit exceeded" {
		t.Errorf("expected error 'API error: rate limit exceeded', got %q", err.Error())
	}
}

func TestFilterTools_APIErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(compresr.APIResponse[compresr.FilterToolsResponse]{
			Success: false,
			Message: "invalid model",
		})
	}))
	defer server.Close()

	client := compresr.NewClient(server.URL, "test-key")
	_, err := client.FilterTools(compresr.FilterToolsParams{
		Query: "test",
		Tools: []compresr.ToolDefinition{{Name: "test"}},
	})

	if err == nil {
		t.Error("expected error, got nil")
		return
	}
	if err.Error() != "API error: invalid model" {
		t.Errorf("expected error 'API error: invalid model', got %q", err.Error())
	}
}

func TestClient_HTTPStatusCodes(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		expectErr  string
	}{
		{
			name:       "400 Bad Request",
			statusCode: http.StatusBadRequest,
			expectErr:  "unexpected status 400",
		},
		{
			name:       "401 Unauthorized",
			statusCode: http.StatusUnauthorized,
			expectErr:  "invalid API key",
		},
		{
			name:       "403 Forbidden",
			statusCode: http.StatusForbidden,
			expectErr:  "unexpected status 403",
		},
		{
			name:       "429 Too Many Requests",
			statusCode: http.StatusTooManyRequests,
			expectErr:  "unexpected status 429",
		},
		{
			name:       "500 Internal Server Error",
			statusCode: http.StatusInternalServerError,
			expectErr:  "unexpected status 500",
		},
		{
			name:       "502 Bad Gateway",
			statusCode: http.StatusBadGateway,
			expectErr:  "unexpected status 502",
		},
		{
			name:       "503 Service Unavailable",
			statusCode: http.StatusServiceUnavailable,
			expectErr:  "unexpected status 503",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				w.Write([]byte("error response body"))
			}))
			defer server.Close()

			client := compresr.NewClient(server.URL, "test-key")
			_, err := client.CompressToolOutput(compresr.CompressToolOutputParams{
				ToolOutput: "content",
				ToolName:   "test",
			})

			if err == nil {
				t.Error("expected error, got nil")
				return
			}
			if err.Error() != tt.expectErr && !contains(err.Error(), tt.expectErr) {
				t.Errorf("expected error containing %q, got %q", tt.expectErr, err.Error())
			}
		})
	}
}

// contains checks if s contains substr
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// =============================================================================
// UNICODE AND SPECIAL CHARACTERS TESTS
// =============================================================================

func TestCompressToolOutput_UnicodeContent(t *testing.T) {
	unicodeContent := "Hello 世界! 🌍 Привет мир! مرحبا بالعالم"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]any
		json.NewDecoder(r.Body).Decode(&reqBody)

		content, _ := reqBody["tool_output"].(string)
		if content != unicodeContent {
			t.Errorf("unicode content mismatch: expected %q, got %q", unicodeContent, content)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(compresr.APIResponse[compresr.CompressToolOutputResponse]{
			Success: true,
			Data:    compresr.CompressToolOutputResponse{CompressedOutput: "compressed unicode"},
		})
	}))
	defer server.Close()

	client := compresr.NewClient(server.URL, "test-key")
	result, err := client.CompressToolOutput(compresr.CompressToolOutputParams{
		ToolOutput: unicodeContent,
		ToolName:   "unicode_tool",
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result.CompressedOutput != "compressed unicode" {
		t.Errorf("expected 'compressed unicode', got %q", result.CompressedOutput)
	}
}

func TestFilterTools_SpecialCharactersInToolNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody struct {
			Tools []compresr.ToolDefinition `json:"tools"`
		}
		json.NewDecoder(r.Body).Decode(&reqBody)

		expectedNames := []string{
			"tool-with-dashes",
			"tool_with_underscores",
			"tool.with.dots",
			"CamelCaseTool",
		}

		for i, tool := range reqBody.Tools {
			if tool.Name != expectedNames[i] {
				t.Errorf("tool[%d]: expected name %q, got %q", i, expectedNames[i], tool.Name)
			}
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(compresr.APIResponse[compresr.FilterToolsResponse]{
			Success: true,
			Data:    compresr.FilterToolsResponse{RelevantTools: []string{"tool-with-dashes"}},
		})
	}))
	defer server.Close()

	client := compresr.NewClient(server.URL, "test-key")
	_, err := client.FilterTools(compresr.FilterToolsParams{
		Query: "test",
		Tools: []compresr.ToolDefinition{
			{Name: "tool-with-dashes"},
			{Name: "tool_with_underscores"},
			{Name: "tool.with.dots"},
			{Name: "CamelCaseTool"},
		},
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
