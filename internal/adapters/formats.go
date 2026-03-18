// formats.go defines content format types and detection logic for tool output compression.
// Binary content (images, audio, video) is already filtered by adapters before extraction.
package adapters

import (
	"github.com/compresr/context-gateway/internal/formats"
	"github.com/rs/zerolog/log"
)

// ContentFormat identifies the format of extracted tool output content.
type ContentFormat string

const (
	// FormatText is plain text (also covers code: shell, Python, Go, etc.)
	FormatText ContentFormat = "text"

	// FormatJSON is JSON-structured content (objects or arrays)
	FormatJSON ContentFormat = "json"

	// FormatMarkdown is Markdown-formatted content
	FormatMarkdown ContentFormat = "markdown"

	// FormatUnknown means the format could not be classified; always passes through without compression.
	FormatUnknown ContentFormat = "unknown"
)

// DefaultCompressibleFormats is the set of formats the text compressor can safely handle.
// Operators can narrow it via allowed/forbidden config.
var DefaultCompressibleFormats = map[ContentFormat]bool{
	FormatText:     true,
	FormatJSON:     true,
	FormatMarkdown: true,
}

// DetectContentFormat classifies a text string into a canonical ContentFormat.
// Returns FormatUnknown for empty strings.
// Uses the centralized formats.Detect() for detection.
func DetectContentFormat(content string) ContentFormat {
	if content == "" {
		return FormatUnknown
	}
	result := formats.Detect(content)
	return mapFormat(result.Format)
}

// mapFormat converts formats.Format to adapters.ContentFormat.
func mapFormat(f formats.Format) ContentFormat {
	switch f {
	case formats.FormatJSON:
		return FormatJSON
	case formats.FormatMarkdown:
		return FormatMarkdown
	case formats.FormatYAML, formats.FormatCode, formats.FormatText:
		return FormatText
	default:
		return FormatText
	}
}

// BuildEffectiveFormats resolves allowed/forbidden lists against DefaultCompressibleFormats.
// allowed narrows the default set; forbidden removes formats; forbidden takes precedence over allowed.
func BuildEffectiveFormats(allowed, forbidden []string) map[ContentFormat]bool {
	effective := make(map[ContentFormat]bool, len(DefaultCompressibleFormats))
	for f := range DefaultCompressibleFormats {
		effective[f] = true
	}

	if len(allowed) > 0 {
		filtered := make(map[ContentFormat]bool, len(allowed))
		for _, s := range allowed {
			f := ContentFormat(s)
			if DefaultCompressibleFormats[f] {
				filtered[f] = true
			} else {
				log.Warn().
					Str("format", s).
					Msg("tool_output: allowed content_format not in DefaultCompressibleFormats — ignored (compressor only handles text formats)")
			}
		}
		effective = filtered
	}

	for _, s := range forbidden {
		delete(effective, ContentFormat(s))
	}

	return effective
}

// IsCompressible returns true if the format is in the effective compressible set.
// FormatUnknown always returns false.
func IsCompressible(format ContentFormat, effective map[ContentFormat]bool) bool {
	if format == FormatUnknown {
		return false
	}
	return effective[format]
}
