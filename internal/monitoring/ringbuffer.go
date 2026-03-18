// Package monitoring - ringbuffer.go provides a generic thread-safe ring buffer.
package monitoring

import "sync"

// RingBuffer is a thread-safe, bounded ring buffer for recent event tracking.
// Uses a circular buffer (head index) for O(1) Record, avoiding slice copies.
type RingBuffer[T any] struct {
	mu     sync.RWMutex
	buf    []T
	head   int // index of the oldest entry when full
	size   int // number of valid entries currently stored
	capVal int // maximum capacity
}

// NewRingBuffer creates a new ring buffer with the given capacity.
func NewRingBuffer[T any](maxSize int) *RingBuffer[T] {
	if maxSize <= 0 {
		maxSize = 1
	}
	return &RingBuffer[T]{
		buf:    make([]T, maxSize),
		capVal: maxSize,
	}
}

// Reset clears all entries.
func (rb *RingBuffer[T]) Reset() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.head = 0
	rb.size = 0
	// Zero out slots to allow GC of pointer-bearing T values.
	var zero T
	for i := range rb.buf {
		rb.buf[i] = zero
	}
}

// Record adds an entry to the buffer, overwriting the oldest entry when full.
// O(1) — no slice copies.
func (rb *RingBuffer[T]) Record(entry T) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.size < rb.capVal {
		// Buffer not yet full: write at (head+size) % cap.
		rb.buf[(rb.head+rb.size)%rb.capVal] = entry
		rb.size++
	} else {
		// Buffer full: overwrite oldest slot (head) and advance head.
		rb.buf[rb.head] = entry
		rb.head = (rb.head + 1) % rb.capVal
	}
}

// Recent returns the most recent n entries (newest first).
func (rb *RingBuffer[T]) Recent(n int) []T {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if n <= 0 || rb.size == 0 {
		return nil
	}
	if n > rb.size {
		n = rb.size
	}

	result := make([]T, n)
	for i := 0; i < n; i++ {
		// newest-first: index from the tail backwards
		idx := (rb.head + rb.size - 1 - i + rb.capVal) % rb.capVal
		result[i] = rb.buf[idx]
	}
	return result
}

// Count returns the number of entries.
func (rb *RingBuffer[T]) Count() int {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.size
}

// RecentWhere returns the most recent n entries (newest first) that satisfy match.
func (rb *RingBuffer[T]) RecentWhere(n int, match func(T) bool) []T {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if n <= 0 || rb.size == 0 {
		return nil
	}

	var result []T
	for i := 0; i < rb.size && len(result) < n; i++ {
		idx := (rb.head + rb.size - 1 - i + rb.capVal) % rb.capVal
		if match(rb.buf[idx]) {
			result = append(result, rb.buf[idx])
		}
	}
	return result
}

// All returns a copy of all entries (oldest first). Useful for aggregation.
func (rb *RingBuffer[T]) All() []T {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if rb.size == 0 {
		return nil
	}
	result := make([]T, rb.size)
	for i := 0; i < rb.size; i++ {
		result[i] = rb.buf[(rb.head+i)%rb.capVal]
	}
	return result
}
