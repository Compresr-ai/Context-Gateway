// Package oauth provides OAuth credential management for Claude Code integration.
//
// This allows Context Gateway to use Claude Code's browser OAuth credentials
// stored in ~/.claude/.credentials.json instead of requiring an ANTHROPIC_API_KEY.
package oauth

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ClaudeCredentials represents the OAuth credentials from Claude Code.
type ClaudeCredentials struct {
	AccessCredential  string   `json:"accessToken"`
	RefreshCredential string   `json:"refreshToken"`
	ExpiresAt         int64    `json:"expiresAt"` // Unix timestamp in milliseconds
	Scopes            []string `json:"scopes"`
	SubscriptionType  string   `json:"subscriptionType"`
}

// credentialsFile represents the structure of ~/.claude/.credentials.json.
type credentialsFile struct {
	ClaudeAiOauth *ClaudeCredentials `json:"claudeAiOauth"`
}

const (
	// refreshBufferMinutes is how many minutes before expiry we consider the token as needing refresh.
	refreshBufferMinutes = 5

	// macOSKeychainService is the service name used by Claude Code in macOS Keychain.
	macOSKeychainService = "Claude Code-credentials"
)

// LoadClaudeCredentials loads OAuth credentials from the appropriate location
// based on the operating system.
//
// On Linux: reads from ~/.claude/.credentials.json
// On macOS: reads from Keychain using `security find-generic-password`
//
// Returns nil, nil if credentials are not found (not an error, just not available).
func LoadClaudeCredentials() (*ClaudeCredentials, error) {
	switch runtime.GOOS {
	case "darwin":
		return loadFromMacOSKeychain()
	default:
		return loadFromCredentialsFile()
	}
}

// loadFromCredentialsFile reads credentials from ~/.claude/.credentials.json.
func loadFromCredentialsFile() (*ClaudeCredentials, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	claudeDir := filepath.Join(homeDir, ".claude")
	credPath := filepath.Join(claudeDir, ".credentials.json")
	if !isPathUnderDir(credPath, claudeDir) {
		return nil, fmt.Errorf("invalid credentials file path")
	}

	data, err := os.ReadFile(credPath) // #nosec G304 -- path validated above
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Credentials not found, not an error
		}
		return nil, fmt.Errorf("failed to read credentials file: %w", err)
	}

	var creds credentialsFile
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse credentials file: %w", err)
	}

	if creds.ClaudeAiOauth == nil {
		return nil, nil // No OAuth credentials in file
	}

	return creds.ClaudeAiOauth, nil
}

// loadFromMacOSKeychain reads credentials from macOS Keychain.
func loadFromMacOSKeychain() (*ClaudeCredentials, error) {

	cmd := exec.Command("security", "find-generic-password",
		"-s", macOSKeychainService,
		"-w", // Output only the password (JSON data)
	)

	output, err := cmd.Output()
	if err != nil {
		// If the keychain item doesn't exist, security returns an error.
		// Try falling back to the file-based credentials.
		return loadFromCredentialsFile()
	}

	var creds credentialsFile
	if err := json.Unmarshal(output, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse keychain credentials: %w", err)
	}

	if creds.ClaudeAiOauth == nil {
		return nil, nil
	}

	return creds.ClaudeAiOauth, nil
}

// IsExpired returns true if the access token has already expired.
func (c *ClaudeCredentials) IsExpired() bool {
	if c == nil || c.ExpiresAt == 0 {
		return true
	}
	// ExpiresAt is in milliseconds
	return time.Now().UnixMilli() >= c.ExpiresAt
}

// NeedsRefresh returns true if the access token is expired or will expire soon.
func (c *ClaudeCredentials) NeedsRefresh() bool {
	if c == nil || c.ExpiresAt == 0 {
		return true
	}
	// Add buffer time before expiry
	bufferMs := int64(refreshBufferMinutes * 60 * 1000)
	return time.Now().UnixMilli() >= (c.ExpiresAt - bufferMs)
}

// TimeUntilExpiry returns the duration until the token expires.
// Returns 0 if already expired.
func (c *ClaudeCredentials) TimeUntilExpiry() time.Duration {
	if c == nil || c.ExpiresAt == 0 {
		return 0
	}
	expiryTime := time.UnixMilli(c.ExpiresAt)
	remaining := time.Until(expiryTime)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// CredentialsFilePath returns the path to the credentials file.
func CredentialsFilePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	claudeDir := filepath.Join(homeDir, ".claude")
	credPath := filepath.Join(claudeDir, ".credentials.json")
	if !isPathUnderDir(credPath, claudeDir) {
		return "", fmt.Errorf("invalid credentials file path")
	}
	return credPath, nil
}

// SaveCredentials saves credentials to the credentials file.
// Note: This only updates the file, not the macOS Keychain.
func SaveCredentials(creds *ClaudeCredentials) error {
	credPath, err := CredentialsFilePath()
	if err != nil {
		return err
	}

	// Read existing file to preserve other fields
	existingData, err := os.ReadFile(credPath) // #nosec G304 -- path from CredentialsFilePath()
	var existing credentialsFile
	if err == nil {
		_ = json.Unmarshal(existingData, &existing)
	}

	existing.ClaudeAiOauth = creds

	data, err := json.MarshalIndent(&existing, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(credPath), 0700); err != nil {
		return fmt.Errorf("failed to create credentials directory: %w", err)
	}

	// Write with restrictive permissions
	if err := os.WriteFile(credPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write credentials file: %w", err)
	}

	return nil
}

func isPathUnderDir(path, dir string) bool {
	cleanPath := filepath.Clean(path)
	cleanDir := filepath.Clean(dir)
	rel, err := filepath.Rel(cleanDir, cleanPath)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && rel != "")
}
