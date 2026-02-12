// HTTP middleware for security, logging, and rate limiting.
//
// DESIGN: Middleware chain (applied in order):
//  1. panicRecovery:    Catch panics, return 500, log stack trace
//  2. rateLimit:        Per-IP token bucket rate limiting
//  3. loggingMiddleware: Log request/response with timing
//  4. security:         Security headers, CORS, SSRF protection
package gateway

import (
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/monitoring"
)

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	status int
}

// WriteHeader captures the status code before writing it.
func (w *responseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Flush implements http.Flusher to support streaming responses.
// This delegates to the underlying ResponseWriter if it supports flushing.
func (w *responseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// rateLimiter implements a token bucket rate limiter per IP address.
type rateLimiter struct {
	requests   map[string]*bucket
	mu         sync.RWMutex
	rate       int
	maxBuckets int
}

// bucket holds rate limiting state for a single IP.
type bucket struct {
	tokens    int
	lastCheck time.Time
}

// newRateLimiter creates a new rate limiter with the specified rate per second.
func newRateLimiter(rate int) *rateLimiter {
	rl := &rateLimiter{requests: make(map[string]*bucket), rate: rate, maxBuckets: MaxRateLimitBuckets}
	// Start cleanup goroutine
	go rl.cleanup()
	return rl
}

// allow checks if the given IP is allowed to make a request.
func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, exists := rl.requests[ip]
	if !exists {
		// Enforce max buckets to prevent memory exhaustion
		if len(rl.requests) >= rl.maxBuckets {
			rl.evictOldest()
		}
		rl.requests[ip] = &bucket{tokens: rl.rate - 1, lastCheck: now}
		return true
	}

	elapsed := now.Sub(b.lastCheck).Seconds()
	b.tokens += int(elapsed * float64(rl.rate))
	if b.tokens > rl.rate {
		b.tokens = rl.rate
	}
	b.lastCheck = now

	if b.tokens > 0 {
		b.tokens--
		return true
	}
	return false
}

// evictOldest removes the oldest bucket (called with lock held).
func (rl *rateLimiter) evictOldest() {
	var oldestKey string
	var oldestTime time.Time
	first := true
	for k, b := range rl.requests {
		if first || b.lastCheck.Before(oldestTime) {
			oldestKey = k
			oldestTime = b.lastCheck
			first = false
		}
	}
	if oldestKey != "" {
		delete(rl.requests, oldestKey)
	}
}

// cleanup periodically removes stale buckets.
func (rl *rateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		cutoff := time.Now().Add(-10 * time.Minute)
		for ip, b := range rl.requests {
			if b.lastCheck.Before(cutoff) {
				delete(rl.requests, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// loggingMiddleware logs request details and duration using the structured logging system.
func (g *Gateway) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestID := r.Header.Get(HeaderRequestID)
		if requestID == "" {
			requestID = uuid.New().String()
		}
		w.Header().Set(HeaderRequestID, requestID)

		// Add request ID to context for downstream logging
		ctx := monitoring.WithRequestIDContext(r.Context(), requestID)
		r = r.WithContext(ctx)

		// Log incoming request
		bodySize := int(r.ContentLength)
		if bodySize < 0 {
			bodySize = 0
		}
		reqInfo := monitoring.NewRequestInfo(r, requestID, bodySize)
		g.requestLogger.LogIncoming(reqInfo)

		wrapped := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(wrapped, r)

		// Calculate latency
		latency := time.Since(start)

		// Log response
		g.requestLogger.LogResponse(&monitoring.ResponseInfo{
			RequestID:  requestID,
			StatusCode: wrapped.status,
			Latency:    latency,
		})

		// Record metrics
		success := wrapped.status < 400
		g.metrics.RecordRequest(success, latency)

		// Check for high latency
		g.alerts.FlagHighLatency(requestID, latency, "", r.URL.Path)

		// Legacy log for compatibility
		log.Info().
			Str("id", requestID).
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", wrapped.status).
			Dur("duration", latency).
			Msg("request")
	})
}

// panicRecovery middleware recovers from panics and returns a 500 error.
func (g *Gateway) panicRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				stack := string(debug.Stack())
				requestID := monitoring.RequestIDFromContext(r.Context())

				log.Error().Interface("panic", err).Str("stack", stack).Msg("panic")

				// Alert on panic
				g.alerts.FlagPanic(requestID, err, stack)

				g.writeError(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// rateLimit middleware enforces per-IP rate limiting.
func (g *Gateway) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := g.getClientIP(r)
		if !g.rateLimiter.allow(ip) {
			log.Warn().Str("ip", ip).Msg("rate limit exceeded")
			w.Header().Set("Retry-After", "1")
			g.writeError(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// security middleware adds security headers and handles CORS.
func (g *Gateway) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")

		// CORS: restrict to configured origins (default: none for API-only use)
		origin := r.Header.Get("Origin")
		if origin != "" && g.isAllowedOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Target-URL, X-Provider, X-Request-ID, x-api-key")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isAllowedOrigin checks if origin is permitted for CORS.
func (g *Gateway) isAllowedOrigin(origin string) bool {
	// Default: allow localhost for development
	return strings.HasPrefix(origin, "http://localhost") || strings.HasPrefix(origin, "http://127.0.0.1")
}

// getClientIP extracts the client IP address from the request.
// Trusts X-Forwarded-For and X-Real-IP headers only from localhost.
func (g *Gateway) getClientIP(r *http.Request) string {
	// Only trust X-Forwarded-For from localhost (reverse proxy)
	if remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr); remoteIP == "127.0.0.1" || remoteIP == "::1" {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if idx := strings.Index(xff, ","); idx != -1 {
				return strings.TrimSpace(xff[:idx])
			}
			return strings.TrimSpace(xff)
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return xri
		}
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

// isAllowedHost checks if the host is in the allowlist for SSRF protection.
func (g *Gateway) isAllowedHost(host string) bool {
	// Strip port if present
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)

	// Check static allowlist
	if allowedHosts[host] {
		return true
	}

	// Dynamic check for Bedrock Runtime hosts (any region)
	if strings.HasPrefix(host, "bedrock-runtime.") && strings.HasSuffix(host, ".amazonaws.com") {
		return true
	}

	return false
}
