// Package circuitbreaker provides a simple circuit breaker pattern implementation
// to prevent repeated calls to failing services.
package circuitbreaker

import (
	"sync"
	"time"
)

// DefaultMaxFailures is the default number of consecutive failures before the circuit opens.
const DefaultMaxFailures = 5

// DefaultOpenDuration is the default time the circuit stays open before allowing a retry.
const DefaultOpenDuration = 30 * time.Second

// CircuitBreaker prevents repeated calls to a failing API.
// When consecutiveFailures reaches maxFailures, the circuit opens
// for openDuration. During that time, calls are immediately rejected.
// After the duration, a probe request is allowed through (half-open state).
// On success, the circuit resets to closed. On failure, it stays open.
type CircuitBreaker struct {
	mu                  sync.Mutex
	consecutiveFailures int
	openUntil           time.Time
	maxFailures         int
	openDuration        time.Duration
}

// Option configures a CircuitBreaker.
type Option func(*CircuitBreaker)

// WithMaxFailures sets the failure threshold before the circuit opens.
func WithMaxFailures(n int) Option {
	return func(cb *CircuitBreaker) {
		cb.maxFailures = n
	}
}

// WithOpenDuration sets how long the circuit stays open.
func WithOpenDuration(d time.Duration) Option {
	return func(cb *CircuitBreaker) {
		cb.openDuration = d
	}
}

// New creates a new CircuitBreaker with the given options.
func New(opts ...Option) *CircuitBreaker {
	cb := &CircuitBreaker{
		maxFailures:  DefaultMaxFailures,
		openDuration: DefaultOpenDuration,
	}
	for _, opt := range opts {
		opt(cb)
	}
	return cb
}

// Allow returns true when a call is permitted (circuit closed or half-open).
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.consecutiveFailures >= cb.maxFailures {
		if time.Now().Before(cb.openUntil) {
			return false // circuit open — reject immediately
		}
		// Half-open: allow one probe request through
	}
	return true
}

// RecordSuccess resets the circuit breaker to closed state.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveFailures = 0
	cb.openUntil = time.Time{}
}

// RecordFailure increments the failure count and opens the circuit if threshold is reached.
// In half-open state (openUntil already passed), a failed probe does NOT extend openUntil —
// it keeps the circuit tripped until the next probe window expires naturally.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveFailures++
	if cb.consecutiveFailures >= cb.maxFailures && cb.openUntil.IsZero() {
		// Only set openUntil on the first time we hit the threshold, not on repeated probes.
		cb.openUntil = time.Now().Add(cb.openDuration)
	}
}

// IsOpen returns true if the circuit is currently open (rejecting calls).
func (cb *CircuitBreaker) IsOpen() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.consecutiveFailures >= cb.maxFailures && time.Now().Before(cb.openUntil)
}

// Reset manually resets the circuit breaker to closed state.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveFailures = 0
	cb.openUntil = time.Time{}
}
