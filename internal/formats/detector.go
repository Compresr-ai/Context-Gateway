package formats

import (
	"encoding/json"
	"strings"
)

// Detect performs fast heuristic format detection.
// This matches the compresr-filters fallback logic for consistency.
// Detection is lightweight (~100µs) for routing and caching decisions.
func Detect(content string) DetectionResult {
	trimmed := strings.TrimSpace(content)
	if len(trimmed) == 0 {
		return DetectionResult{Format: FormatText, Confidence: 0.1}
	}

	// JSON - fastest check (most common in tool outputs)
	if isJSON(trimmed) {
		return DetectionResult{Format: FormatJSON, Confidence: 0.9}
	}

	// XML - check for XML markers
	if isXML(trimmed) {
		return DetectionResult{Format: FormatXML, Confidence: 0.85}
	}

	// YAML - check for YAML markers
	if isYAML(trimmed) {
		return DetectionResult{Format: FormatYAML, Confidence: 0.7}
	}

	// Markdown - check for markdown signals
	if isMarkdown(trimmed) {
		return DetectionResult{Format: FormatMarkdown, Confidence: 0.7}
	}

	// Code - check for language keywords
	if lang, conf := detectCodeLanguage(trimmed); conf > 0.5 {
		return DetectionResult{Format: FormatCode, Language: lang, Confidence: conf}
	}

	return DetectionResult{Format: FormatText, Confidence: 0.3}
}

// isJSON checks if content is valid JSON.
func isJSON(content string) bool {
	// Quick prefix check
	if !strings.HasPrefix(content, "{") && !strings.HasPrefix(content, "[") {
		return false
	}
	// Quick suffix check
	if !strings.HasSuffix(content, "}") && !strings.HasSuffix(content, "]") {
		return false
	}
	// Validate JSON
	return json.Valid([]byte(content))
}

// isXML checks for XML content.
func isXML(content string) bool {
	// Check for XML declaration
	if strings.HasPrefix(content, "<?xml") {
		return true
	}
	// Check for XML tag at start
	if !strings.HasPrefix(content, "<") {
		return false
	}
	// Must have matching close tag or self-closing
	// Check for closing tag pattern
	if strings.Contains(content, "</") || strings.Contains(content, "/>") {
		// Additional validation: should end with >
		if strings.HasSuffix(strings.TrimSpace(content), ">") {
			return true
		}
	}
	return false
}

// isYAML checks for YAML markers.
func isYAML(content string) bool {
	// YAML document start
	if strings.HasPrefix(content, "---") {
		return true
	}
	// Look for key: value patterns without JSON braces
	if strings.HasPrefix(content, "{") {
		return false
	}
	// Check for YAML-style key: value in first 500 chars
	sample := content
	if len(sample) > 500 {
		sample = content[:500]
	}
	lines := strings.Split(sample, "\n")
	yamlPatterns := 0
	for _, line := range lines {
		trimLine := strings.TrimSpace(line)
		if trimLine == "" || strings.HasPrefix(trimLine, "#") {
			continue
		}
		// Look for "key: value" pattern
		if colonIdx := strings.Index(trimLine, ":"); colonIdx > 0 && colonIdx < len(trimLine)-1 {
			key := strings.TrimSpace(trimLine[:colonIdx])
			// Key should be simple identifier (no quotes, no special chars)
			if isSimpleIdentifier(key) {
				yamlPatterns++
			}
		}
	}
	return yamlPatterns >= 2
}

// isMarkdown checks for markdown signals.
func isMarkdown(content string) bool {
	signals := 0

	// Check first 1000 chars for efficiency
	sample := content
	if len(sample) > 1000 {
		sample = content[:1000]
	}

	// Headers (# through ######)
	lines := strings.Split(sample, "\n")
	for _, line := range lines {
		trimLine := strings.TrimSpace(line)
		if len(trimLine) > 0 && trimLine[0] == '#' {
			// Count consecutive #
			hashCount := 0
			for _, c := range trimLine {
				if c == '#' {
					hashCount++
				} else {
					break
				}
			}
			if hashCount >= 1 && hashCount <= 6 && len(trimLine) > hashCount && trimLine[hashCount] == ' ' {
				signals++
			}
		}
	}

	// Code blocks
	if strings.Count(sample, "```") >= 2 {
		signals++
	}

	// Links [text](url)
	if strings.Contains(sample, "](") && strings.Contains(sample, "[") {
		signals++
	}

	// Bold/italic
	if strings.Contains(sample, "**") || strings.Contains(sample, "__") {
		signals++
	}

	// Lists - count list items; 2+ list items counts as 2 signals
	listItemCount := 0
	for _, line := range lines {
		trimLine := strings.TrimSpace(line)
		if strings.HasPrefix(trimLine, "- ") || strings.HasPrefix(trimLine, "* ") {
			listItemCount++
		} else if len(trimLine) >= 2 && trimLine[0] >= '0' && trimLine[0] <= '9' && trimLine[1] == '.' {
			listItemCount++
		}
	}
	if listItemCount >= 2 {
		signals += 2 // Multiple list items is strong markdown signal
	} else if listItemCount == 1 {
		signals++
	}

	return signals >= 2
}

// detectCodeLanguage attempts to detect programming language.
func detectCodeLanguage(content string) (string, float64) {
	sample := content
	if len(sample) > 2000 {
		sample = content[:2000]
	}

	// Language-specific patterns (check more of the content)
	type langPattern struct {
		lang     string
		patterns []string
		minMatch int // minimum matches required
	}

	checks := []langPattern{
		{"python", []string{"def ", "import ", "from ", "class ", "if __name__", "elif ", "self.", "print("}, 2},
		{"javascript", []string{"function ", "const ", "let ", "var ", "=>", "require(", "module.exports"}, 2},
		{"typescript", []string{"interface ", "type ", ": string", ": number", ": boolean", "export "}, 2},
		{"go", []string{"func ", "package ", "import (", "import \"", "type ", "struct {", "fmt."}, 2},
		{"rust", []string{"fn ", "let mut ", "impl ", "pub fn ", "use ", "mod ", "::"}, 2},
		{"java", []string{"public class ", "private ", "void ", "String ", "System.out", "import java"}, 2},
		{"shell", []string{"#!/bin/bash", "#!/bin/sh", "echo ", "export ", "$(", "if [", "fi"}, 2},
		{"sql", []string{"SELECT ", "FROM ", "WHERE ", "INSERT ", "UPDATE ", "CREATE TABLE", "JOIN "}, 2},
	}

	bestLang := ""
	bestScore := 0.0

	for _, lp := range checks {
		matches := 0
		for _, pattern := range lp.patterns {
			if containsIgnoreCase(sample, pattern) {
				matches++
			}
		}
		if matches >= lp.minMatch {
			score := float64(matches) / float64(len(lp.patterns))
			// Boost score if we have many matches
			if matches >= 3 {
				score += 0.2
			}
			if score > bestScore {
				bestScore = score
				bestLang = lp.lang
			}
		}
	}

	// Cap at 0.8 to leave room for higher confidence formats
	if bestScore > 0.8 {
		bestScore = 0.8
	}

	return bestLang, bestScore
}

// containsIgnoreCase checks if s contains substr (case-insensitive for keywords like SELECT).
func containsIgnoreCase(s, substr string) bool {
	// For most patterns, exact match is fine
	if strings.Contains(s, substr) {
		return true
	}
	// For SQL keywords, also check uppercase
	if strings.Contains(strings.ToUpper(s), strings.ToUpper(substr)) {
		return true
	}
	return false
}

// isSimpleIdentifier checks if a string looks like a YAML key.
func isSimpleIdentifier(s string) bool {
	if len(s) == 0 || len(s) > 100 {
		return false
	}
	for i, c := range s {
		if i == 0 {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '_' {
				return false
			}
		} else {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' {
				return false
			}
		}
	}
	return true
}
