package common

import (
	"testing"

	"github.com/compresr/context-gateway/internal/utils"
	"github.com/stretchr/testify/assert"
)

func TestMaskKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "empty key", key: "", want: "(empty)"},
		{name: "short key under 16", key: "abc123", want: "****"},
		{name: "exactly 15 chars", key: "123456789012345", want: "****"},
		{name: "exactly 16 chars", key: "1234567890123456", want: "12345678...3456"},
		{name: "long key", key: "sk-ant-api03-abcdefghijklmnop", want: "sk-ant-a...mnop"},
		{name: "32 char key", key: "abcdefghijklmnopqrstuvwxyz012345", want: "abcdefgh...2345"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := utils.MaskKey(tt.key)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMaskKeyShort(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "empty key", key: "", want: "****"},
		{name: "short key 4 chars", key: "abcd", want: "****"},
		{name: "exactly 8 chars", key: "12345678", want: "****"},
		{name: "9 char key", key: "123456789", want: "1234...6789"},
		{name: "long key", key: "sk-ant-api03-abcdefghijklmnop", want: "sk-a...mnop"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := utils.MaskKeyShort(tt.key)
			assert.Equal(t, tt.want, got)
		})
	}
}
