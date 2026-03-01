package common

import (
	"testing"

	"github.com/compresr/context-gateway/internal/utils"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name string
		arg  string
		want string
	}{
		{name: "simple word", arg: "hello", want: "'hello'"},
		{name: "word with spaces", arg: "fix the bug", want: "'fix the bug'"},
		{name: "single quote", arg: "it's", want: "'it'\\''s'"},
		{name: "double quotes", arg: `say "hello"`, want: `'say "hello"'`},
		{name: "dollar sign", arg: "$HOME", want: "'$HOME'"},
		{name: "empty string", arg: "", want: "''"},
		{name: "flag with value", arg: "-p", want: "'-p'"},
		{name: "long flag", arg: "--verbose", want: "'--verbose'"},
		{name: "multiple single quotes", arg: "don't won't", want: "'don'\\''t won'\\''t'"},
		{name: "backtick", arg: "`whoami`", want: "'`whoami`'"},
		{name: "semicolon", arg: "foo; rm -rf /", want: "'foo; rm -rf /'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := utils.ShellQuote(tt.arg)
			if got != tt.want {
				t.Errorf("ShellQuote(%q) = %q, want %q", tt.arg, got, tt.want)
			}
		})
	}
}
