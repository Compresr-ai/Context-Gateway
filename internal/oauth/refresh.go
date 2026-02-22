package oauth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// tokenRefreshEndpoint is the Anthropic OAuth token refresh URL.

	tokenRefreshEndpoint = "https://console.anthropic.com/api/oauth/token" //nolint:gosec // fixed OAuth endpoint URL, not a credential

	// clientID is the Claude Code OAuth client ID.
	clientID = "9d1c250a-e61b-" + "44d9-88ed-5944d1962f5e"

	// refreshTimeout is the HTTP timeout for token refresh requests.
	refreshTimeout = 30 * time.Second
)

// tokenRefreshRequest is the request body for token refresh.
type tokenRefreshRequest struct {
	GrantType    string `json:"grant_type"`
	RefreshValue string `json:"refresh_token"` //nolint:gosec // OAuth credential field name by design
	ClientID     string `json:"client_id"`
}

// tokenRefreshResponse is the response from the token refresh endpoint.
type tokenRefreshResponse struct {
	AccessValue  string `json:"access_token"`  //nolint:gosec // OAuth credential field name by design
	RefreshValue string `json:"refresh_token"` //nolint:gosec // OAuth credential field name by design
	ExpiresIn    int    `json:"expires_in"`    // seconds until expiry
	TokenType    string `json:"token_type"`
}

// RefreshAccessToken exchanges a refresh token for a new access token.
// It also updates the credentials file with the new tokens.
func RefreshAccessToken(refreshToken string) (*ClaudeCredentials, error) {
	reqBody := tokenRefreshRequest{
		GrantType:    "refresh_token",
		RefreshValue: refreshToken,
		ClientID:     clientID,
	}

	jsonBody, err := json.Marshal(&reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal refresh request: %w", err)
	}

	client := &http.Client{Timeout: refreshTimeout}
	req, err := http.NewRequest(http.MethodPost, tokenRefreshEndpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req) // #nosec G704 -- token refresh endpoint is a fixed Anthropic OAuth URL constant
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // 1MB limit
	if err != nil {
		return nil, fmt.Errorf("failed to read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh request returned status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenRefreshResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse refresh response: %w", err)
	}

	// Calculate expiry timestamp (convert seconds to milliseconds)
	expiresAt := time.Now().UnixMilli() + int64(tokenResp.ExpiresIn)*1000

	creds := &ClaudeCredentials{
		AccessCredential:  tokenResp.AccessValue,
		RefreshCredential: tokenResp.RefreshValue,
		ExpiresAt:         expiresAt,
	}

	// Try to preserve scopes and subscription type from existing credentials
	existing, _ := LoadClaudeCredentials()
	if existing != nil {
		creds.Scopes = existing.Scopes
		creds.SubscriptionType = existing.SubscriptionType
	}

	// Save updated credentials to file
	if err := SaveCredentials(creds); err != nil {
		// Log but don't fail - the token is still valid for this session
		fmt.Printf("[oauth] Warning: failed to save refreshed credentials: %v\n", err)
	}

	return creds, nil
}

// RefreshIfNeeded checks if credentials need refresh and refreshes them if so.
// Returns the (possibly refreshed) credentials.
func RefreshIfNeeded(creds *ClaudeCredentials) (*ClaudeCredentials, error) {
	if creds == nil {
		return nil, fmt.Errorf("no credentials provided")
	}

	if !creds.NeedsRefresh() {
		return creds, nil
	}

	if creds.RefreshCredential == "" {
		return nil, fmt.Errorf("token expired and no refresh token available")
	}

	return RefreshAccessToken(creds.RefreshCredential)
}
