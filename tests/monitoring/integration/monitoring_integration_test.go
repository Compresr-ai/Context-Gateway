// Monitoring Integration Tests
//
// Tests telemetry event recording, compression metrics tracking,
// and the savings report endpoint.
//
// Run with: go test ./tests/monitoring/integration/... -v
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/compresr/context-gateway/internal/monitoring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// HELPERS
// =============================================================================

func createGateway(cfg *config.Config) *httptest.Server {
	gw := gateway.New(cfg)
	return httptest.NewServer(gw.Handler())
}

func passthroughConfig() *config.Config {
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
			LogLevel:  "disabled",
			LogFormat: "json",
			LogOutput:  "discard",
		},
	}
}

func newMockLLM() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"id":   "msg_test_001",
			"type": "message",
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": "Test response from mock LLM.",
				},
			},
			"stop_reason": "end_turn",
			"usage": map[string]interface{}{
				"input_tokens":  100,
				"output_tokens": 50,
			},
		}
		data, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
}

// =============================================================================
// TESTS
// =============================================================================

// TestIntegration_Monitoring_RequestLogged sends a request through the gateway
// and verifies that the request is processed successfully (telemetry event would
// be logged internally by the gateway handler).
func TestIntegration_Monitoring_RequestLogged(t *testing.T) {
	mockLLM := newMockLLM()
	defer mockLLM.Close()

	cfg := passthroughConfig()
	gw := createGateway(cfg)
	defer gw.Close()

	body := map[string]interface{}{
		"model":      "claude-3-haiku-20240307",
		"max_tokens": 500,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Hello, test request for monitoring."},
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", gw.URL+"/v1/messages", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "sk-ant-test-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("X-Target-URL", mockLLM.URL+"/v1/messages")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// Verify the request was successfully processed
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"expected 200 OK, got %d: %s", resp.StatusCode, string(respBody))

	// Parse response to verify it came through the gateway
	var respMap map[string]interface{}
	err = json.Unmarshal(respBody, &respMap)
	require.NoError(t, err)
	assert.Equal(t, "message", respMap["type"])
}

// TestIntegration_Monitoring_CompressionMetrics verifies that the SavingsTracker
// correctly records compression metrics when tool output compression is reported.
func TestIntegration_Monitoring_CompressionMetrics(t *testing.T) {
	tracker := monitoring.NewSavingsTracker()
	defer tracker.Stop()

	sessionID := "test-session-001"

	// Record a compression event
	comparison := monitoring.CompressionComparison{
		RequestID:        "req-001",
		ProviderModel:    "claude-haiku-4-5",
		OriginalTokens:   2000,
		CompressedTokens: 500,
		CompressionRatio: 0.25,
		Status:           "compressed",
	}
	tracker.RecordToolOutputCompression(comparison, sessionID, true)

	// Record a request event with token usage
	event := &monitoring.RequestEvent{
		RequestID:       "req-001",
		Timestamp:       time.Now(),
		Provider:        "anthropic",
		Model:           "claude-haiku-4-5",
		StatusCode:      200,
		CompressionUsed: true,
		InputTokens:     500,
		OutputTokens:    100,
		PipeType:        monitoring.PipeToolOutput,
		Success:         true,
		IsMainAgent:     true,
	}
	tracker.RecordRequest(event, sessionID)

	// Verify global report
	report := tracker.GetReport()
	assert.Equal(t, 1, report.TotalRequests)
	assert.Equal(t, 1, report.CompressedRequests)
	assert.True(t, report.TokensSaved > 0, "expected tokens saved > 0, got %d", report.TokensSaved)
	assert.True(t, report.OriginalTokens > 0, "expected original tokens > 0")

	// Verify session report
	sessionReport := tracker.GetReportForSession(sessionID)
	assert.Equal(t, 1, sessionReport.TotalRequests)
	assert.Equal(t, 1, sessionReport.CompressedRequests)
	assert.True(t, sessionReport.TokensSaved > 0)
}

// TestIntegration_Monitoring_SavingsReport verifies that the /api/savings endpoint
// returns valid JSON with the expected structure.
func TestIntegration_Monitoring_SavingsReport(t *testing.T) {
	cfg := passthroughConfig()
	gw := createGateway(cfg)
	defer gw.Close()

	req, err := http.NewRequest("GET", gw.URL+"/api/savings", nil)
	require.NoError(t, err)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// The /api/savings endpoint returns a text-formatted report (not JSON)
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"expected 200 OK from /api/savings, got %d: %s", resp.StatusCode, string(respBody))

	// Verify the response contains savings report content
	bodyStr := string(respBody)
	assert.NotEmpty(t, bodyStr, "savings response should not be empty")
	assert.Contains(t, bodyStr, "Savings Report", "response should contain savings report header")
	assert.Contains(t, bodyStr, "Requests:", "response should contain request count")
}

// TestIntegration_Monitoring_TelemetryTracker verifies that the Tracker correctly
// writes request events to a JSONL file.
func TestIntegration_Monitoring_TelemetryTracker(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "telemetry.jsonl")

	tracker, err := monitoring.NewTracker(monitoring.TelemetryConfig{
		Enabled: true,
		LogPath: logPath,
	})
	require.NoError(t, err)
	defer tracker.Close()

	// Record a request event
	event := &monitoring.RequestEvent{
		RequestID:   "req-telemetry-001",
		Timestamp:   time.Now(),
		Method:      "POST",
		Path:        "/v1/messages",
		Provider:    "anthropic",
		Model:       "claude-haiku-4-5",
		StatusCode:  200,
		TokensSaved: 500,
		PipeType:    monitoring.PipeToolOutput,
		Success:     true,
	}
	tracker.RecordRequest(event)

	// Read the log file and verify the event was written
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)

	lines := strings.TrimSpace(string(data))
	require.NotEmpty(t, lines, "telemetry log should not be empty")

	// Parse the JSONL line
	var logged monitoring.RequestEvent
	err = json.Unmarshal([]byte(lines), &logged)
	require.NoError(t, err)
	assert.Equal(t, "req-telemetry-001", logged.RequestID)
	assert.Equal(t, "anthropic", logged.Provider)
	assert.Equal(t, 500, logged.TokensSaved)
	assert.True(t, logged.Success)
}

// TestIntegration_Monitoring_ToolDiscoveryMetrics verifies that tool discovery
// compression events are properly recorded in the savings tracker.
func TestIntegration_Monitoring_ToolDiscoveryMetrics(t *testing.T) {
	tracker := monitoring.NewSavingsTracker()
	defer tracker.Stop()

	// Record a tool discovery event
	allTools := make([]string, 20)
	selectedTools := make([]string, 8)
	for i := 0; i < 20; i++ {
		allTools[i] = fmt.Sprintf("tool_%03d", i)
	}
	for i := 0; i < 8; i++ {
		selectedTools[i] = fmt.Sprintf("tool_%03d", i)
	}

	comparison := monitoring.CompressionComparison{
		RequestID:        "req-disc-001",
		ProviderModel:    "claude-haiku-4-5",
		OriginalTokens:   2500,
		CompressedTokens: 1000,
		AllTools:         allTools,
		SelectedTools:    selectedTools,
		Status:           "compressed",
	}
	tracker.RecordToolDiscovery(comparison, "session-disc-001", true)

	report := tracker.GetReport()
	assert.Equal(t, 1, report.ToolDiscoveryRequests)
	assert.Equal(t, 20, report.OriginalToolCount)
	assert.Equal(t, 8, report.KeptToolCount)
	assert.True(t, report.ToolDiscoveryTokens > 0, "expected tool discovery tokens saved > 0")
}
