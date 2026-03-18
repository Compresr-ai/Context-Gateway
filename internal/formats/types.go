// Package formats provides centralized content format detection and field-level compression.
//
// Design Principles:
//   - Unified Structured Approach: ALL content is treated as structured data
//   - Plain text becomes {content: "..."} with one field
//   - JSON/YAML keeps its structure with multiple fields
//   - Field-Level Compression: Replace large fields with reference IDs
//   - Reference-Based Expansion: Each ID expands to only its original value
//   - Single Detection Path: formats.Detect() is the only detection source
//
// This package is the SINGLE SOURCE OF TRUTH for:
//   - Format detection (Detect function)
//   - Field extraction (FieldExtractor)
//   - Reference ID generation (GenerateFieldRefID)
//   - Content wrapping (WrapAsStructured)
package formats

// Format represents detected content format.
type Format string

const (
	FormatJSON     Format = "json"
	FormatYAML     Format = "yaml"
	FormatXML      Format = "xml"
	FormatCode     Format = "code"
	FormatMarkdown Format = "markdown"
	FormatText     Format = "text"
)

// InputMode determines how content is processed.
type InputMode string

const (
	// InputModeRaw triggers auto-detection of format.
	InputModeRaw InputMode = "raw"
	// InputModeStructured means the caller specifies the format.
	InputModeStructured InputMode = "structured"
)

// DetectionResult from format detection.
type DetectionResult struct {
	Format     Format  `json:"format"`
	Language   string  `json:"language,omitempty"` // For code: "python", "go", etc.
	Confidence float64 `json:"confidence"`         // 0.0-1.0
}

// FieldRef represents a compressed field that can be expanded.
// Used for field-level compression in structured data.
type FieldRef struct {
	// ID is the unique reference like "field_abc123"
	ID string `json:"id"`
	// ParentID identifies the parent object (e.g., tool name)
	ParentID string `json:"parent_id,omitempty"`
	// Field is the field name (e.g., "description", "content")
	Field string `json:"field"`
	// Original is the full uncompressed value
	Original string `json:"original"`
	// Compressed is the compressed value (summary)
	Compressed string `json:"compressed,omitempty"`
}

// FieldCompressionResult is returned after compressing a field.
type FieldCompressionResult struct {
	// RefID is the reference ID to use in place of the original
	RefID string `json:"ref_id"`
	// Compressed is the compressed content with the ref marker
	Compressed string `json:"compressed"`
	// OriginalTokens is the token count of the original
	OriginalTokens int `json:"original_tokens"`
	// CompressedTokens is the token count after compression
	CompressedTokens int `json:"compressed_tokens"`
}

// CompressionInput is the unified input for compression operations.
type CompressionInput struct {
	// Content is the raw content to compress
	Content string `json:"content"`
	// Mode is raw (auto-detect) or structured (caller-specified)
	Mode InputMode `json:"mode"`
	// Format is required if Mode is structured
	Format Format `json:"format,omitempty"`
	// Language is for code format (e.g., "python", "go")
	Language string `json:"language,omitempty"`
	// Instruction is the query for relevance-based compression
	Instruction string `json:"instruction,omitempty"`
	// PreserveKeys are field paths to keep verbatim (JSON/YAML)
	PreserveKeys []string `json:"preserve_keys,omitempty"`
	// ExcludeKeys are field paths to remove entirely (JSON/YAML)
	ExcludeKeys []string `json:"exclude_keys,omitempty"`
}

// CompressionOutput is the unified output from compression.
type CompressionOutput struct {
	// Compressed is the final compressed content
	Compressed string `json:"compressed"`
	// Format is the detected or specified format
	Format Format `json:"format"`
	// OriginalTokens is the token count before compression
	OriginalTokens int `json:"original_tokens"`
	// CompressedTokens is the token count after compression
	CompressedTokens int `json:"compressed_tokens"`
	// FieldRefs are the field references created during compression
	FieldRefs []FieldRef `json:"field_refs,omitempty"`
	// CacheHit indicates if the result came from cache
	CacheHit bool `json:"cache_hit"`
}

// StructuredContent wraps any content as structured data for unified processing.
// Plain text becomes {"content": "..."}, JSON stays as-is.
type StructuredContent struct {
	// Original is the raw input content
	Original string `json:"original"`
	// Format is the detected format
	Format Format `json:"format"`
	// Fields are the extractable fields (for JSON: all string fields, for text: single "content" field)
	Fields map[string]string `json:"fields"`
	// IsWrapped is true if the content was wrapped (plain text → JSON)
	IsWrapped bool `json:"is_wrapped"`
}

// ContentField is the field name used when wrapping plain text as structured.
const ContentField = "content"
