package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// isLoopback
// ---------------------------------------------------------------------------

func TestIsLoopback(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		want       bool
	}{
		{name: "ipv4 loopback", remoteAddr: "127.0.0.1:12345", want: true},
		{name: "ipv6 loopback", remoteAddr: "[::1]:12345", want: true},
		{name: "external ip", remoteAddr: "192.168.1.1:12345", want: false},
		{name: "public ip", remoteAddr: "8.8.8.8:443", want: false},
		{name: "empty string", remoteAddr: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isLoopback(tt.remoteAddr)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// isAllowedOrigin
// ---------------------------------------------------------------------------

func TestIsAllowedOrigin(t *testing.T) {
	g := &Gateway{}
	tests := []struct {
		name   string
		origin string
		want   bool
	}{
		{name: "localhost", origin: "http://localhost", want: true},
		{name: "localhost with port", origin: "http://localhost:3000", want: true},
		{name: "127.0.0.1", origin: "http://127.0.0.1", want: true},
		{name: "127.0.0.1 with port", origin: "http://127.0.0.1:8080", want: true},
		{name: "external origin", origin: "http://example.com", want: false},
		{name: "https external", origin: "https://evil.com", want: false},
		{name: "empty origin", origin: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := g.isAllowedOrigin(tt.origin)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// isAllowedHost
// ---------------------------------------------------------------------------

func TestIsAllowedHost(t *testing.T) {
	g := &Gateway{}
	tests := []struct {
		name string
		host string
		want bool
	}{
		{name: "anthropic api", host: "api.anthropic.com", want: true},
		{name: "openai api", host: "api.openai.com", want: true},
		{name: "unknown host", host: "evil.example.com", want: false},
		{name: "empty host", host: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := g.isAllowedHost(tt.host)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// getClientIP
// ---------------------------------------------------------------------------

func TestGetClientIP_DirectConnection(t *testing.T) {
	g := &Gateway{}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.168.1.100:12345"

	ip := g.getClientIP(r)
	assert.Equal(t, "192.168.1.100", ip)
}

func TestGetClientIP_XForwardedFor_FromLocalhost(t *testing.T) {
	g := &Gateway{}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("X-Forwarded-For", "10.0.0.5")

	ip := g.getClientIP(r)
	// Should trust X-Forwarded-For from localhost
	assert.Equal(t, "10.0.0.5", ip)
}

func TestGetClientIP_XRealIP_FromLocalhost(t *testing.T) {
	g := &Gateway{}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("X-Real-IP", "10.0.0.10")

	ip := g.getClientIP(r)
	assert.Equal(t, "10.0.0.10", ip)
}

func TestGetClientIP_IgnoresHeadersFromExternal(t *testing.T) {
	g := &Gateway{}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.168.1.100:12345"
	r.Header.Set("X-Forwarded-For", "10.0.0.5")

	ip := g.getClientIP(r)
	// Should NOT trust X-Forwarded-For from non-localhost
	assert.Equal(t, "192.168.1.100", ip)
}

// ---------------------------------------------------------------------------
// responseWriter
// ---------------------------------------------------------------------------

func TestResponseWriter_CapturesStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rec, status: http.StatusOK}

	rw.WriteHeader(http.StatusNotFound)
	assert.Equal(t, http.StatusNotFound, rw.status)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestResponseWriter_DefaultStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rec, status: http.StatusOK}

	// Without calling WriteHeader, status should stay at default
	assert.Equal(t, http.StatusOK, rw.status)
}

// ---------------------------------------------------------------------------
// rateLimiter
// ---------------------------------------------------------------------------

func TestRateLimiter_AllowsWithinRate(t *testing.T) {
	rl := newRateLimiter(100) // 100 req/sec
	defer rl.Stop()

	for i := 0; i < 50; i++ {
		assert.True(t, rl.allow("192.168.1.1"), "request %d should be allowed", i)
	}
}

func TestRateLimiter_BlocksExcess(t *testing.T) {
	rl := newRateLimiter(5) // Very low rate
	defer rl.Stop()

	blocked := false
	for i := 0; i < 100; i++ {
		if !rl.allow("192.168.1.1") {
			blocked = true
			break
		}
	}
	assert.True(t, blocked, "rate limiter should block excessive requests")
}

func TestRateLimiter_IndependentPerIP(t *testing.T) {
	rl := newRateLimiter(10)
	defer rl.Stop()

	// Both IPs should independently be allowed
	assert.True(t, rl.allow("192.168.1.1"))
	assert.True(t, rl.allow("192.168.1.2"))
}

func TestRateLimiter_RefillsOverTime(t *testing.T) {
	rl := newRateLimiter(10)
	defer rl.Stop()

	// Exhaust tokens
	for i := 0; i < 20; i++ {
		rl.allow("192.168.1.1")
	}

	// Wait for refill
	time.Sleep(200 * time.Millisecond)

	// Should be allowed again
	assert.True(t, rl.allow("192.168.1.1"))
}

// ---------------------------------------------------------------------------
// isNonLLMEndpoint
// ---------------------------------------------------------------------------

func TestIsNonLLMEndpoint(t *testing.T) {
	g := &Gateway{}
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "event logging", path: "/api/event_logging", want: true},
		{name: "telemetry", path: "/api/telemetry", want: true},
		{name: "analytics", path: "/api/analytics", want: true},
		{name: "analytics with subpath", path: "/api/analytics/events", want: true},
		{name: "messages endpoint", path: "/v1/messages", want: false},
		{name: "chat completions", path: "/v1/chat/completions", want: false},
		{name: "root path", path: "/", want: false},
		{name: "empty path", path: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := g.isNonLLMEndpoint(tt.path)
			assert.Equal(t, tt.want, got)
		})
	}
}
