// Package retry provides simple retry with exponential backoff for HTTP calls.
//
// Usage:
//
//	for attempt := 0; attempt < retry.MaxAttempts; attempt++ {
//	    if attempt > 0 {
//	        time.Sleep(retry.Backoff(attempt - 1))
//	    }
//	    resp, err := doRequest()
//	    if err != nil {
//	        if retry.IsTransientErr(err) { lastErr = err; continue }
//	        return err
//	    }
//	    if retry.IsTransientStatus(resp.StatusCode) { lastErr = ...; continue }
//	    // success
//	}
package retry

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const (
	// MaxAttempts is the total number of attempts (1 initial + 2 retries).
	MaxAttempts = 3

	// baseDelay is the initial backoff duration.
	baseDelay = 100 * time.Millisecond
)

// IsTransientErr returns true if the error is a transient network error that
// should be retried. Returns false for context cancellation/deadline exceeded.
func IsTransientErr(err error) bool {
	return err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded)
}

// IsTransientStatus returns true if the HTTP status code warrants a retry.
// Retries on 429 (rate limit) and any 5xx (server error).
func IsTransientStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= http.StatusInternalServerError
}

// Backoff returns the exponential backoff duration for the given retry index (0-based).
// attempt=0 → 100ms, attempt=1 → 200ms.
func Backoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 20 { // cap to prevent overflow: max ~104 seconds
		attempt = 20
	}
	return baseDelay * time.Duration(1<<uint(attempt)) //nolint:gosec // G115: overflow prevented by bounds check above
}
