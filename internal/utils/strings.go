// Package utils provides common utility functions.
package utils

// MaskKey masks an API key for safe logging (shows first 8 and last 4 chars).
// Use this to avoid logging sensitive credentials in plain text.
func MaskKey(key string) string {
	if key == "" {
		return "(empty)"
	}
	if len(key) < 16 {
		return "****"
	}
	return key[:8] + "..." + key[len(key)-4:]
}

// MaskKeyShort masks an API key showing only first 4 and last 4 chars.
// Use this for more compact display in TUI elements.
func MaskKeyShort(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}
