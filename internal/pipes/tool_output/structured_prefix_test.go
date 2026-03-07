package tooloutput

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// DetectStructuredFormat
// =============================================================================

func TestDetectStructuredFormat_JSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		format  string
		startAt int
	}{
		{"object", `{"key": "value"}`, "json", 0},
		{"array", `[{"key": "value"}]`, "json", 0},
		{"whitespace prefix", "  \n\t{\"key\": 1}", "json", 4},
		{"nested array", `[1, 2, 3]`, "json", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			format, start := DetectStructuredFormat(tt.input)
			assert.Equal(t, tt.format, format)
			assert.Equal(t, tt.startAt, start)
		})
	}
}

func TestDetectStructuredFormat_YAML(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		format string
	}{
		{"document separator", "---\nkey: value", "yaml"},
		{"key value", "status: ok\ncount: 5", "yaml"},
		{"with whitespace", "  ---\nkey: value", "yaml"},
		{"hyphenated key", "my-key: value", "yaml"},
		{"underscore key", "my_key: value", "yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			format, _ := DetectStructuredFormat(tt.input)
			assert.Equal(t, tt.format, format)
		})
	}
}

func TestDetectStructuredFormat_XML(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		format string
	}{
		{"xml declaration", "<?xml version=\"1.0\"?>", "xml"},
		{"root element", "<root><child/></root>", "xml"},
		{"with whitespace", "\n  <items>\n  <item>1</item>\n</items>", "xml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			format, _ := DetectStructuredFormat(tt.input)
			assert.Equal(t, tt.format, format)
		})
	}
}

func TestDetectStructuredFormat_Unstructured(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"plain text", "Hello, world!"},
		{"number first", "42 items found"},
		{"path", "/usr/local/bin/go"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			format, _ := DetectStructuredFormat(tt.input)
			assert.Equal(t, "", format)
		})
	}
}

// =============================================================================
// ExtractVerbatimPrefix
// =============================================================================

func TestExtractVerbatimPrefix_Passthrough(t *testing.T) {
	// Content <= prefixBytes*2 → passthrough
	content := `{"a":1,"b":2,"c":3}`
	verbatim, rest := ExtractVerbatimPrefix(content, "json", 100)
	assert.Equal(t, content, verbatim)
	assert.Equal(t, "", rest)
}

func TestExtractVerbatimPrefix_JSON_CommaBoundary(t *testing.T) {
	// Build JSON: {"item0":"val0","item1":"val1",...}
	var parts []string
	for i := 0; i < 50; i++ {
		parts = append(parts, `"item`+strings.Repeat("x", 10)+`":"value"`)
	}
	content := "{" + strings.Join(parts, ",") + "}"
	prefixBytes := 100

	verbatim, rest := ExtractVerbatimPrefix(content, "json", prefixBytes)

	require.NotEmpty(t, rest, "should have a rest portion")
	// Verbatim should end at a comma (boundary)
	lastChar := verbatim[len(verbatim)-1]
	assert.Contains(t, ",}]", string(lastChar), "should cut at JSON boundary")
	// Combined should equal original
	assert.Equal(t, content, verbatim+rest)
}

func TestExtractVerbatimPrefix_YAML_NewlineBoundary(t *testing.T) {
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = "key" + strings.Repeat("x", 20) + ": value"
	}
	content := strings.Join(lines, "\n")
	prefixBytes := 100

	verbatim, rest := ExtractVerbatimPrefix(content, "yaml", prefixBytes)

	require.NotEmpty(t, rest)
	// Verbatim should end right after a newline
	assert.True(t, strings.HasSuffix(verbatim, "\n"), "should cut at newline boundary")
	assert.Equal(t, content, verbatim+rest)
}

func TestExtractVerbatimPrefix_XML_NewlineBoundary(t *testing.T) {
	var b strings.Builder
	b.WriteString("<root>\n")
	for i := 0; i < 50; i++ {
		b.WriteString("  <item>value" + strings.Repeat("x", 20) + "</item>\n")
	}
	b.WriteString("</root>")
	content := b.String()
	prefixBytes := 100

	verbatim, rest := ExtractVerbatimPrefix(content, "xml", prefixBytes)

	require.NotEmpty(t, rest)
	assert.True(t, strings.HasSuffix(verbatim, "\n"), "should cut at newline boundary")
	assert.Equal(t, content, verbatim+rest)
}

func TestExtractVerbatimPrefix_NoBoundary_FallbackToExact(t *testing.T) {
	// Single long JSON string with no separators in the search zone
	content := `{"key":"` + strings.Repeat("a", 500) + `"}`
	prefixBytes := 100

	verbatim, rest := ExtractVerbatimPrefix(content, "json", prefixBytes)

	require.NotEmpty(t, rest)
	// Should cut at exactly prefixBytes since no boundary found in last 25%
	assert.Equal(t, prefixBytes, len(verbatim))
	assert.Equal(t, content, verbatim+rest)
}

func TestExtractVerbatimPrefix_ContentEqualsPrefixBytes(t *testing.T) {
	content := strings.Repeat("a", 100)
	// content (100) <= prefixBytes*2 (200) → passthrough
	verbatim, rest := ExtractVerbatimPrefix(content, "json", 100)
	assert.Equal(t, content, verbatim)
	assert.Equal(t, "", rest)
}

func TestExtractVerbatimPrefix_PrefixBytesExceedsContent(t *testing.T) {
	content := `{"small": true}`
	verbatim, rest := ExtractVerbatimPrefix(content, "json", 1000)
	assert.Equal(t, content, verbatim)
	assert.Equal(t, "", rest)
}

func TestExtractVerbatimPrefix_JSON_ClosingBraceBoundary(t *testing.T) {
	// Nested objects that create } boundaries
	// Each ,"x":{"n":0} is 12 chars, so we need enough prefixBytes to have } in the search zone
	content := `{"a":{"nested":1},"b":{"nested":2},"c":{"nested":3}` +
		strings.Repeat(`,"x":{"n":0}`, 30) + "}"
	prefixBytes := 80 // enough so } boundaries fall within search zone (last 25%)

	verbatim, rest := ExtractVerbatimPrefix(content, "json", prefixBytes)

	require.NotEmpty(t, rest)
	lastChar := verbatim[len(verbatim)-1]
	assert.Contains(t, ",}]", string(lastChar), "should cut at JSON boundary, got: %q (len=%d)", string(lastChar), len(verbatim))
	assert.Equal(t, content, verbatim+rest)
}
