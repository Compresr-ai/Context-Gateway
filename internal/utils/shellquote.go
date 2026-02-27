package utils

import "strings"

// ShellQuote safely wraps a single shell argument in single quotes.
func ShellQuote(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
}
