package tooloutput

import "strings"

// DetectStructuredFormat checks the first non-whitespace character(s) of content
// to determine if it's structured data.
// Returns format ("json", "yaml", "xml", "") and the byte position where content starts.
func DetectStructuredFormat(content string) (format string, start int) {
	for i, c := range content {
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			continue
		case c == '{' || c == '[':
			return "json", i
		case c == '<':
			return "xml", i
		case c == '-' && i+2 < len(content) && content[i:i+3] == "---":
			return "yaml", i
		default:
			// Check for "key: value" YAML pattern (word followed by colon)
			rest := content[i:]
			if idx := strings.IndexByte(rest, ':'); idx > 0 && idx < 64 {
				prefix := rest[:idx]
				if isYAMLKey(prefix) {
					return "yaml", i
				}
			}
			return "", i
		}
	}
	return "", 0
}

// isYAMLKey checks if s looks like a YAML key (alphanumeric, underscores, hyphens).
func isYAMLKey(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// ExtractVerbatimPrefix splits structured content into a verbatim prefix and a remainder
// to be compressed. prefixBytes should be the min_bytes value from config.
//
// If content <= prefixBytes*2, returns the whole content as verbatim (no point compressing a stub).
// Otherwise, searches backward from prefixBytes for the nearest structural boundary:
//   - JSON: nearest , } or ]
//   - YAML/XML: nearest \n
//
// If no boundary is found within the last 25% of the prefix zone, cuts at prefixBytes exactly.
func ExtractVerbatimPrefix(content, format string, prefixBytes int) (verbatim, rest string) {
	if len(content) <= prefixBytes*2 {
		return content, ""
	}

	// Clamp prefixBytes to content length
	if prefixBytes >= len(content) {
		return content, ""
	}

	cutPos := findBoundary(content, format, prefixBytes)
	return content[:cutPos], content[cutPos:]
}

// findBoundary searches backward from pos for a structural separator.
// Returns the cut position (exclusive — the separator is included in the prefix).
func findBoundary(content, format string, pos int) int {
	// Search zone: last 25% of the prefix area
	searchStart := pos - pos/4
	if searchStart < 0 {
		searchStart = 0
	}

	var seps string
	switch format {
	case "json":
		seps = ",}]"
	default: // yaml, xml
		seps = "\n"
	}

	// Search backward from pos
	for i := pos - 1; i >= searchStart; i-- {
		if strings.ContainsRune(seps, rune(content[i])) {
			return i + 1 // include the separator in the prefix
		}
	}

	// No boundary found — cut at pos
	return pos
}
