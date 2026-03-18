package formats

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// FieldRefPrefix is used to mark compressed field references.
const FieldRefPrefix = "[REF:"

// FieldRefSuffix closes the reference marker.
const FieldRefSuffix = "]"

// Security limits to prevent DoS attacks
const (
	// MaxNestingDepth is the maximum recursion depth for JSON traversal
	MaxNestingDepth = 100
	// MaxFieldCount is the maximum number of fields to extract from a single JSON
	MaxFieldCount = 1000
	// MaxFieldSize is the maximum size of a single field value in bytes (1MB)
	MaxFieldSize = 1024 * 1024
	// MaxTotalFieldSize is the maximum total size of all extracted fields (10MB)
	MaxTotalFieldSize = 10 * 1024 * 1024
)

// FieldExtractor handles field-level extraction and compression for structured data.
type FieldExtractor struct {
	// TokenThreshold is the minimum tokens for a field to be compressed
	TokenThreshold int
	// TokenCounter is a function to count tokens (injected to avoid circular deps)
	TokenCounter func(string) int
	// MaxFields limits the number of extracted fields (0 = MaxFieldCount)
	MaxFields int
	// MaxDepth limits recursion depth (0 = MaxNestingDepth)
	MaxDepth int
}

// NewFieldExtractor creates a new field extractor with defaults.
func NewFieldExtractor(tokenCounter func(string) int) *FieldExtractor {
	threshold := 50 // Default: compress fields with 50+ tokens
	if tokenCounter == nil {
		// Fallback: rough estimate of ~4 chars per token
		tokenCounter = func(s string) int { return len(s) / 4 }
	}
	return &FieldExtractor{
		TokenThreshold: threshold,
		TokenCounter:   tokenCounter,
		MaxFields:      MaxFieldCount,
		MaxDepth:       MaxNestingDepth,
	}
}

// ExtractedField represents a field that can be compressed.
type ExtractedField struct {
	Path     string // JSON path like "description" or "items.0.content"
	Value    string // The field value
	Tokens   int    // Token count
	ParentID string // Parent object ID (e.g., tool name)
}

// ExtractLargeFields extracts fields exceeding the token threshold from JSON.
// Returns a list of fields that should be compressed.
// Enforces MaxDepth, MaxFields, and MaxFieldSize limits.
func (e *FieldExtractor) ExtractLargeFields(jsonContent string, parentID string) ([]ExtractedField, error) {
	var data any
	if err := json.Unmarshal([]byte(jsonContent), &data); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	maxDepth := e.MaxDepth
	if maxDepth <= 0 {
		maxDepth = MaxNestingDepth
	}
	maxFields := e.MaxFields
	if maxFields <= 0 {
		maxFields = MaxFieldCount
	}

	var fields []ExtractedField
	var totalSize int
	e.extractFieldsRecursive(data, "", parentID, &fields, 0, maxDepth, maxFields, &totalSize)
	return fields, nil
}

// extractFieldsRecursive walks the JSON structure and extracts large string fields.
// Respects depth and field count limits to prevent DoS.
func (e *FieldExtractor) extractFieldsRecursive(data any, path string, parentID string, fields *[]ExtractedField, depth, maxDepth, maxFields int, totalSize *int) {
	// Check depth limit
	if depth >= maxDepth {
		return
	}
	// Check field count limit
	if len(*fields) >= maxFields {
		return
	}
	// Check total size limit
	if *totalSize >= MaxTotalFieldSize {
		return
	}

	switch v := data.(type) {
	case map[string]any:
		for key, val := range v {
			if len(*fields) >= maxFields {
				return
			}
			newPath := key
			if path != "" {
				newPath = path + "." + key
			}
			e.extractFieldsRecursive(val, newPath, parentID, fields, depth+1, maxDepth, maxFields, totalSize)
		}
	case []any:
		for i, item := range v {
			if len(*fields) >= maxFields {
				return
			}
			newPath := fmt.Sprintf("%s.%d", path, i)
			if path == "" {
				newPath = fmt.Sprintf("%d", i)
			}
			e.extractFieldsRecursive(item, newPath, parentID, fields, depth+1, maxDepth, maxFields, totalSize)
		}
	case string:
		// Skip fields that are too large
		if len(v) > MaxFieldSize {
			return
		}
		tokens := e.TokenCounter(v)
		if tokens >= e.TokenThreshold {
			*fields = append(*fields, ExtractedField{
				Path:     path,
				Value:    v,
				Tokens:   tokens,
				ParentID: parentID,
			})
			*totalSize += len(v)
		}
	}
}

// GenerateFieldRefID creates a unique reference ID for a field.
func GenerateFieldRefID(parentID, path, value string) string {
	h := sha256.New()
	h.Write([]byte(parentID))
	h.Write([]byte(":"))
	h.Write([]byte(path))
	h.Write([]byte(":"))
	h.Write([]byte(value))
	hash := hex.EncodeToString(h.Sum(nil))[:12]
	return fmt.Sprintf("field_%s", hash)
}

// CompressedFieldResult is the result of compressing a single field.
type CompressedFieldResult struct {
	Field      ExtractedField
	RefID      string
	Compressed string // The compressed summary
}

// validatePath checks if a path is valid and doesn't contain malicious content.
func validatePath(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	parts := strings.Split(path, ".")
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("invalid path: empty segment")
		}
		// Check for control characters
		for _, c := range part {
			if c < 32 || c == 127 {
				return fmt.Errorf("invalid path: control characters not allowed")
			}
		}
	}
	return nil
}

// ReplaceFieldWithRef replaces a field value in JSON with a reference marker.
// Returns the modified JSON with the field replaced.
// Validates the path to prevent injection attacks.
func ReplaceFieldWithRef(jsonContent string, path string, refID string, summary string) (string, error) {
	// Validate path first
	if err := validatePath(path); err != nil {
		return "", fmt.Errorf("path validation failed: %w", err)
	}

	var data any
	if err := json.Unmarshal([]byte(jsonContent), &data); err != nil {
		return "", fmt.Errorf("invalid JSON: %w", err)
	}

	// Format: [REF:field_xxx] summary...
	replacement := fmt.Sprintf("%s%s%s %s", FieldRefPrefix, refID, FieldRefSuffix, summary)

	if err := replaceAtPath(data, strings.Split(path, "."), replacement); err != nil {
		return "", err
	}

	result, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

// replaceAtPath replaces the value at the given path in the data structure.
func replaceAtPath(data any, pathParts []string, replacement string) error {
	if len(pathParts) == 0 {
		return fmt.Errorf("empty path")
	}

	switch v := data.(type) {
	case map[string]any:
		if len(pathParts) == 1 {
			v[pathParts[0]] = replacement
			return nil
		}
		next, ok := v[pathParts[0]]
		if !ok {
			return fmt.Errorf("path not found: %s", pathParts[0])
		}
		return replaceAtPath(next, pathParts[1:], replacement)
	case []any:
		var idx int
		if _, err := fmt.Sscanf(pathParts[0], "%d", &idx); err != nil {
			return fmt.Errorf("invalid array index: %s", pathParts[0])
		}
		if idx < 0 || idx >= len(v) {
			return fmt.Errorf("array index out of bounds: %d", idx)
		}
		if len(pathParts) == 1 {
			v[idx] = replacement
			return nil
		}
		return replaceAtPath(v[idx], pathParts[1:], replacement)
	default:
		return fmt.Errorf("cannot navigate into %T", data)
	}
}

// ExtractRefID extracts a field reference ID from a reference marker.
// Returns the ref ID and true if found, or empty string and false if not.
func ExtractRefID(text string) (string, bool) {
	start := strings.Index(text, FieldRefPrefix)
	if start == -1 {
		return "", false
	}
	start += len(FieldRefPrefix)
	end := strings.Index(text[start:], FieldRefSuffix)
	if end == -1 {
		return "", false
	}
	return text[start : start+end], true
}

// HasFieldRef checks if text contains a field reference marker.
func HasFieldRef(text string) bool {
	return strings.Contains(text, FieldRefPrefix)
}

// ToolSchemaCompressor handles compression of tool schemas (Stage 2).
// It compresses only the description field of each tool while preserving structure.
type ToolSchemaCompressor struct {
	Extractor *FieldExtractor
}

// NewToolSchemaCompressor creates a compressor for tool schemas.
func NewToolSchemaCompressor(tokenCounter func(string) int) *ToolSchemaCompressor {
	return &ToolSchemaCompressor{
		Extractor: NewFieldExtractor(tokenCounter),
	}
}

// ToolSchema represents a tool definition to be compressed.
type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
	// Other fields preserved as-is
}

// CompressToolSchemas compresses the descriptions of multiple tools.
// Returns the compressed schemas and the field refs for expansion.
func (c *ToolSchemaCompressor) CompressToolSchemas(
	schemas []map[string]any,
	compressFunc func(content, instruction string) (string, error),
) ([]map[string]any, []FieldRef, error) {
	fieldRefs := make([]FieldRef, 0, len(schemas))
	result := make([]map[string]any, len(schemas))

	for i, schema := range schemas {
		// Copy the schema
		compressed := make(map[string]any)
		for k, v := range schema {
			compressed[k] = v
		}

		// Get tool name
		name, _ := schema["name"].(string)
		if name == "" {
			name = fmt.Sprintf("tool_%d", i)
		}

		// Check description field
		desc, ok := schema["description"].(string)
		if !ok || desc == "" {
			result[i] = compressed
			continue
		}

		// Check if description needs compression
		tokens := c.Extractor.TokenCounter(desc)
		if tokens < c.Extractor.TokenThreshold {
			result[i] = compressed
			continue
		}

		// Compress the description
		compressedDesc, err := compressFunc(desc, "Summarize this tool description concisely while preserving key functionality")
		if err != nil {
			// On error, keep original
			result[i] = compressed
			continue
		}

		// Generate ref ID
		refID := GenerateFieldRefID(name, "description", desc)

		// Store field ref
		fieldRefs = append(fieldRefs, FieldRef{
			ID:         refID,
			ParentID:   name,
			Field:      "description",
			Original:   desc,
			Compressed: compressedDesc,
		})

		// Replace description with ref + summary
		compressed["description"] = fmt.Sprintf("%s%s%s %s", FieldRefPrefix, refID, FieldRefSuffix, compressedDesc)

		result[i] = compressed
	}

	return result, fieldRefs, nil
}

// ExtractFieldRefsFromJSON finds all field references in a JSON string.
func ExtractFieldRefsFromJSON(jsonContent string) []string {
	var refs []string
	start := 0
	for {
		idx := strings.Index(jsonContent[start:], FieldRefPrefix)
		if idx == -1 {
			break
		}
		pos := start + idx + len(FieldRefPrefix)
		endIdx := strings.Index(jsonContent[pos:], FieldRefSuffix)
		if endIdx == -1 {
			break
		}
		refID := jsonContent[pos : pos+endIdx]
		refs = append(refs, refID)
		start = pos + endIdx
	}
	return refs
}

// WrapAsStructured converts any content to a unified structured format.
// - JSON content stays as-is with fields extracted
// - Plain text/markdown/code becomes {"content": "..."}
// This enables unified field-level compression for ALL content types.
func WrapAsStructured(content string) StructuredContent {
	result := Detect(content)

	sc := StructuredContent{
		Original:  content,
		Format:    result.Format,
		Fields:    make(map[string]string),
		IsWrapped: false,
	}

	switch result.Format {
	case FormatJSON:
		// Extract all string fields from JSON
		var data map[string]any
		if err := json.Unmarshal([]byte(content), &data); err == nil {
			extractStringFieldsWithLimits(data, "", sc.Fields, 0, MaxNestingDepth, MaxFieldCount)
		} else {
			// JSON array or invalid - treat as single content field
			sc.Fields[ContentField] = content
			sc.IsWrapped = true
		}
	default:
		// Text, Markdown, YAML, Code - wrap as single content field
		sc.Fields[ContentField] = content
		sc.IsWrapped = true
	}

	return sc
}

// extractStringFieldsWithLimits recursively extracts string fields from a JSON object.
// Respects depth and field count limits to prevent DoS.
func extractStringFieldsWithLimits(data map[string]any, prefix string, fields map[string]string, depth, maxDepth, maxFields int) {
	if depth >= maxDepth || len(fields) >= maxFields {
		return
	}

	for key, val := range data {
		if len(fields) >= maxFields {
			return
		}
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		switch v := val.(type) {
		case string:
			// Skip very large strings
			if len(v) <= MaxFieldSize {
				fields[path] = v
			}
		case map[string]any:
			extractStringFieldsWithLimits(v, path, fields, depth+1, maxDepth, maxFields)
		case []any:
			extractArrayStrings(v, path, fields, depth+1, maxDepth, maxFields)
		}
	}
}

// extractArrayStrings extracts strings from a JSON array with limits.
func extractArrayStrings(arr []any, prefix string, fields map[string]string, depth, maxDepth, maxFields int) {
	if depth >= maxDepth || len(fields) >= maxFields {
		return
	}

	for i, item := range arr {
		if len(fields) >= maxFields {
			return
		}
		path := fmt.Sprintf("%s.%d", prefix, i)
		switch v := item.(type) {
		case map[string]any:
			extractStringFieldsWithLimits(v, path, fields, depth+1, maxDepth, maxFields)
		case string:
			if len(v) <= MaxFieldSize {
				fields[path] = v
			}
		}
	}
}

// UnwrapStructured converts structured content back to original format.
// If content was wrapped (plain text), returns just the content field.
// If content was JSON, reconstructs the JSON with updated field values.
// Note: Field updates are best-effort; invalid paths are silently skipped.
func UnwrapStructured(sc StructuredContent) string {
	if sc.IsWrapped {
		// Return the single content field
		if content, ok := sc.Fields[ContentField]; ok {
			return content
		}
		return sc.Original
	}

	// Reconstruct JSON with updated fields
	var data map[string]any
	if err := json.Unmarshal([]byte(sc.Original), &data); err != nil {
		return sc.Original
	}

	// Update fields (best-effort, errors logged but not fatal)
	for path, value := range sc.Fields {
		if err := validatePath(path); err != nil {
			continue // Skip invalid paths
		}
		_ = setFieldAtPathSafe(data, strings.Split(path, "."), value, 0, MaxNestingDepth)
	}

	result, err := json.Marshal(data)
	if err != nil {
		return sc.Original
	}
	return string(result)
}

// setFieldAtPathSafe sets a value at the given path in a data structure.
// Returns an error if the path is invalid or cannot be navigated.
// Respects depth limits to prevent stack overflow.
func setFieldAtPathSafe(data any, pathParts []string, value string, depth, maxDepth int) error {
	if depth >= maxDepth {
		return fmt.Errorf("max depth exceeded")
	}
	if len(pathParts) == 0 {
		return fmt.Errorf("empty path")
	}

	switch v := data.(type) {
	case map[string]any:
		if len(pathParts) == 1 {
			v[pathParts[0]] = value
			return nil
		}
		next, ok := v[pathParts[0]]
		if !ok {
			return fmt.Errorf("path not found: %s", pathParts[0])
		}
		return setFieldAtPathSafe(next, pathParts[1:], value, depth+1, maxDepth)
	case []any:
		var idx int
		if _, err := fmt.Sscanf(pathParts[0], "%d", &idx); err != nil {
			return fmt.Errorf("invalid array index: %s", pathParts[0])
		}
		if idx < 0 || idx >= len(v) {
			return fmt.Errorf("array index out of bounds: %d", idx)
		}
		if len(pathParts) == 1 {
			v[idx] = value
			return nil
		}
		return setFieldAtPathSafe(v[idx], pathParts[1:], value, depth+1, maxDepth)
	default:
		return fmt.Errorf("cannot navigate into %T", data)
	}
}
