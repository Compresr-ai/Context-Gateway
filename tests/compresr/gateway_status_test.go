// GatewayStatus API client unit tests
//
// Tests the GetGatewayStatus method of the Compresr Go client.
// Covers: success, error, unauthorized, and response parsing.

package compresr_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/compresr/context-gateway/internal/compresr"
)

func TestGetGatewayStatus_Success(t *testing.T) {
	mockResp := compresr.APIResponse[compresr.GatewayStatus]{
		Success: true,
		Data: compresr.GatewayStatus{
			Tier:                 "pro",
			CreditsRemainingUSD:  42.5,
			CreditsUsedThisMonth: 7.5,
			MonthlyBudgetUSD:     50.0,
			UsagePercent:         15.0,
			RequestsToday:        12,
			RequestsThisMonth:    100,
			DailyRequestLimit:    nil,
			IsAdmin:              false,
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/gateway/status" {
			t.Errorf("expected /api/gateway/status, got %s", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "test-key" {
			t.Errorf("expected X-API-Key header to be set")
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(mockResp)
	}))
	defer server.Close()

	client := compresr.NewClient(server.URL, "test-key")
	status, err := client.GetGatewayStatus()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Tier != "pro" {
		t.Errorf("expected tier 'pro', got %q", status.Tier)
	}
	if status.CreditsRemainingUSD != 42.5 {
		t.Errorf("expected credits_remaining_usd 42.5, got %v", status.CreditsRemainingUSD)
	}
	if status.UsagePercent != 15.0 {
		t.Errorf("expected usage_percent 15.0, got %v", status.UsagePercent)
	}
}

func TestGetGatewayStatus_APIError(t *testing.T) {
	mockResp := compresr.APIResponse[compresr.GatewayStatus]{
		Success: false,
		Message: "Failed to get status",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(mockResp)
	}))
	defer server.Close()

	client := compresr.NewClient(server.URL, "test-key")
	_, err := client.GetGatewayStatus()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "API error: Failed to get status" {
		t.Errorf("expected error 'API error: Failed to get status', got %q", err.Error())
	}
}

func TestGetGatewayStatus_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	}))
	defer server.Close()

	client := compresr.NewClient(server.URL, "test-key")
	_, err := client.GetGatewayStatus()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "invalid API key" {
		t.Errorf("expected unauthorized error, got %q", err.Error())
	}
}

func TestGetGatewayStatus_NoAPIKey(t *testing.T) {
	client := compresr.NewClient("http://localhost", "")
	_, err := client.GetGatewayStatus()
	if err == nil {
		t.Error("expected error when no API key configured")
	}
}
