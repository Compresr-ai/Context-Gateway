package pipes

import "strings"

// NormalizeEndpointURL joins a base URL and path, cleaning double slashes
// while preserving the scheme (e.g. "https://").
func NormalizeEndpointURL(base, path string) string {
	u := base + path
	u = strings.Replace(u, "://", "::SCHEME::", 1)
	u = strings.ReplaceAll(u, "//", "/")
	u = strings.Replace(u, "::SCHEME::", "://", 1)
	return u
}
