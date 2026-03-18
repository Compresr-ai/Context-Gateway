// helpers.go contains small shared utilities used across multiple adapters.
package adapters

import "strings"

// getString safely extracts a string value from a map by key.
// Returns "" if the key is missing or the value is not a string.
func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// extractStringContent extracts a text string from flexible content shapes:
//   - plain string → returned as-is
//   - {"text": "..."} map → returns the "text" field
//   - []any of {"text": "..."} blocks → concatenates all non-empty text blocks
func extractStringContent(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if m, ok := v.(map[string]any); ok {
		if s, ok := m["text"].(string); ok {
			return s
		}
	}
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if s, ok := m["text"].(string); ok && s != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(s)
		}
	}
	return b.String()
}
