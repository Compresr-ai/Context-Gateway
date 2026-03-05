// Package monitoring - ringbuffer.go provides a generic thread-safe ring buffer.
package monitoring

import "sync"

// RingBuffer is a thread-safe, bounded ring buffer for recent event tracking.
type RingBuffer[T any] struct {
	mu      sync.RWMutex
	entries []T
	maxSize int
}

// NewRingBuffer creates a new ring buffer with the given capacity.
func NewRingBuffer[T any](maxSize int) *RingBuffer[T] {
	return &RingBuffer[T]{
		entries: make([]T, 0, maxSize),
		maxSize: maxSize,
	}
}

// Reset clears all entries.
func (rb *RingBuffer[T]) Reset() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.entries = make([]T, 0, rb.maxSize)
}

// Record adds an entry to the buffer, dropping the oldest if full.
func (rb *RingBuffer[T]) Record(entry T) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if len(rb.entries) >= rb.maxSize {
		copy(rb.entries, rb.entries[1:])
		rb.entries[len(rb.entries)-1] = entry
	} else {
		rb.entries = append(rb.entries, entry)
	}
}

// Recent returns the most recent n entries (newest first).
func (rb *RingBuffer[T]) Recent(n int) []T {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if n <= 0 || len(rb.entries) == 0 {
		return nil
	}
	if n > len(rb.entries) {
		n = len(rb.entries)
	}

	result := make([]T, n)
	for i := 0; i < n; i++ {
		result[i] = rb.entries[len(rb.entries)-1-i]
	}
	return result
}

// Count returns the number of entries.
func (rb *RingBuffer[T]) Count() int {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return len(rb.entries)
}

// All returns a copy of all entries (oldest first). Useful for aggregation.
func (rb *RingBuffer[T]) All() []T {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if len(rb.entries) == 0 {
		return nil
	}
	result := make([]T, len(rb.entries))
	copy(result, rb.entries)
	return result
}
