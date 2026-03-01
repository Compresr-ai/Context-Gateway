package utils

import (
	"bytes"
	"encoding/json"
)

// MarshalNoEscape marshals JSON without HTML escaping.
// This avoids inflating payloads by converting characters like '<' into \u003c.
func MarshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encoder adds a trailing newline; remove it for parity with json.Marshal.
	out := bytes.TrimSuffix(buf.Bytes(), []byte{'\n'})
	return out, nil
}
