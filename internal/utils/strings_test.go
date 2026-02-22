package utils

import "testing"

func TestMaskKey(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty string", "", "(empty)"},
		{"short key", "sk-ant-123", "****"},
		{"normal key", "sk-ant-api123456789abcdef", "sk-ant-a...cdef"},
		{"long key", "sk-ant-api123456789abcdefghijklmnop", "sk-ant-a...mnop"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MaskKey(tt.input)
			if result != tt.expected {
				t.Errorf("MaskKey(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMaskKeyShort(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty string", "", "****"},
		{"very short key", "abc", "****"},
		{"8 char key", "12345678", "****"},
		{"normal key", "sk-ant-api123", "sk-a...i123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MaskKeyShort(tt.input)
			if result != tt.expected {
				t.Errorf("MaskKeyShort(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
