package tooloutput

import (
	"strings"

	"github.com/compresr/context-gateway/internal/formats"
	"github.com/compresr/context-gateway/internal/tokenizer"
)

// DetectStructuredFormat checks content to determine if it's structured data.
// Returns format ("json", "yaml", "xml", "") and the byte position where content starts.
// Uses the centralized formats.Detect() as the single source of truth.
func DetectStructuredFormat(content string) (format string, start int) {
	// Find first non-whitespace position
	for i, c := range content {
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		// Use centralized detector - single source of truth
		result := formats.Detect(content)
		switch result.Format {
		case formats.FormatJSON:
			return "json", i
		case formats.FormatYAML:
			return "yaml", i
		case formats.FormatXML:
			return "xml", i
		default:
			// For other formats (text, markdown, code), return empty
			// These are handled as single "content" field in structured approach
			return "", i
		}
	}
	return "", 0
}

// ExtractVerbatimPrefix splits structured content into a verbatim prefix and a remainder
// to be compressed. prefixTokens is the target token count for the prefix (uses tiktoken).
//
// If content <= prefixTokens*2 tokens, returns the whole content as verbatim (no point compressing a stub).
// Otherwise, searches backward from the byte position corresponding to prefixTokens for the nearest structural boundary:
//   - JSON: nearest , } or ]
//   - YAML/XML: nearest \n
//
// If no boundary is found within the last 25% of the prefix zone, cuts at the estimated position.
func ExtractVerbatimPrefix(content, format string, prefixTokens int) (verbatim, rest string) {
	contentTokens := tokenizer.CountTokens(content)
	if contentTokens <= prefixTokens*2 {
		return content, ""
	}

	// Estimate byte position for prefixTokens using tiktoken ratio
	// Average ~4 bytes per token, but adjust based on actual content
	bytesPerToken := float64(len(content)) / float64(contentTokens)
	targetBytes := int(float64(prefixTokens) * bytesPerToken)

	// Clamp to content length
	if targetBytes >= len(content) {
		return content, ""
	}

	cutPos := findBoundary(content, format, targetBytes)
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
