// Package store provides shadow context storage for expand_context.
//
// V2 DESIGN: When tool outputs are compressed, we use dual TTL:
//   - Original content: 5 hour TTL - needed for expand_context during session
//   - Compressed content: 24 hour TTL - preserves KV-cache across sessions
//
// This optimizes memory while maintaining KV-cache consistency.
//
// Currently only MemoryStore is implemented. For multi-instance deployments,
// implement Store interface with Redis or similar.
package store

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

// V2: Default TTL values - re-exported from config for backward compatibility.
const (
	DefaultOriginalTTL   = 5 * time.Hour  // TTL for original content (expand_context)
	DefaultCompressedTTL = 24 * time.Hour // Long TTL for compressed (KV-cache)

	// MaxCompressedEntries is the maximum number of compressed cache entries.
	// Prevents unbounded memory growth in long-running sessions.
	// At ~2KB avg per entry, 10K entries ≈ 20MB.
	MaxCompressedEntries = 10_000

	// MaxOriginalEntries caps the original content map.
	// Original content is larger (~5KB avg), so 5K entries ≈ 25MB.
	MaxOriginalEntries = 5_000
)

// Note: These match config.DefaultOriginalTTL and config.DefaultCompressedTTL.
// Kept here for package-local usage without import cycles.

// ExpansionRecord stores the expand_context interaction that happened during a request.
// This is used to reconstruct history for KV-cache preservation.
type ExpansionRecord struct {
	// AssistantMessage is the assistant's expand_context tool call (JSON serialized)
	AssistantMessage json.RawMessage `json:"assistant_message"`
	// ToolResultMessage is the tool result with the expanded content (JSON serialized)
	ToolResultMessage json.RawMessage `json:"tool_result_message"`
}

// Store defines the interface for shadow context storage.
// V2: Supports dual TTL for original (short) and compressed (long) content.
type Store interface {
	// Set stores original content with short TTL.
	Set(key, value string) error

	// Get retrieves original content by key.
	Get(key string) (string, bool)

	// Delete removes original content by key.
	Delete(key string) error

	// SetCompressed stores compressed content with long TTL (KV-cache preservation).
	SetCompressed(key, compressed string) error

	// GetCompressed retrieves the cached compressed version.
	GetCompressed(key string) (string, bool)

	// DeleteCompressed removes only the compressed version.
	DeleteCompressed(key string) error

	// SetExpansion stores an expansion record for a shadow ID.
	// This is called when the LLM requests expand_context and we provide the original content.
	SetExpansion(key string, expansion *ExpansionRecord) error

	// GetExpansion retrieves the expansion record for a shadow ID.
	// Returns nil if no expansion has happened for this shadow ID.
	GetExpansion(key string) (*ExpansionRecord, bool)

	// DeleteExpansion removes the expansion record.
	DeleteExpansion(key string) error

	// Close cleans up resources.
	Close() error
}

// CacheMetrics tracks cache hit/miss/eviction statistics.
type CacheMetrics struct {
	CompressedHits      atomic.Int64
	CompressedMisses    atomic.Int64
	CompressedEvictions atomic.Int64
}

// MemoryStore is a simple in-memory implementation of Store.
// V2: Supports dual TTL for original and compressed content.
type MemoryStore struct {
	data          map[string]entry
	compressed    map[string]entry          // Cache for compressed versions
	expansions    map[string]expansionEntry // Cache for expansion records
	mu            sync.RWMutex
	originalTTL   time.Duration // V2: Short TTL for original
	compressedTTL time.Duration // V2: Long TTL for compressed
	stopChan      chan struct{}
	stopped       bool
	wg            sync.WaitGroup // Waits for cleanup goroutine to exit

	maxCompressed int          // Max entries in compressed cache (0 = unlimited)
	Metrics       CacheMetrics // Observable cache statistics
}

type entry struct {
	value     string
	expiresAt time.Time
}

type expansionEntry struct {
	record    *ExpansionRecord
	expiresAt time.Time
}

// NewMemoryStore creates a new in-memory store with default TTLs.
// V2: Uses dual TTL (5 hour original, 24 hour compressed).
func NewMemoryStore(ttl time.Duration) *MemoryStore {
	return NewMemoryStoreWithDualTTL(ttl, ttl)
}

// NewMemoryStoreWithDualTTL creates a store with separate TTLs (V2).
func NewMemoryStoreWithDualTTL(originalTTL, compressedTTL time.Duration) *MemoryStore {
	s := &MemoryStore{
		data:          make(map[string]entry),
		compressed:    make(map[string]entry),
		expansions:    make(map[string]expansionEntry),
		originalTTL:   originalTTL,
		compressedTTL: compressedTTL,
		stopChan:      make(chan struct{}),
		maxCompressed: MaxCompressedEntries,
	}

	// Start cleanup goroutine
	s.wg.Add(1)
	go s.cleanup()

	return s
}

// Set stores original content with short TTL (V2).
func (s *MemoryStore) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return nil
	}

	// Cap original entries to prevent unbounded growth
	if len(s.data) >= MaxOriginalEntries {
		// Evict oldest entry
		var oldestKey string
		var oldestTime time.Time
		for k, e := range s.data {
			if oldestKey == "" || e.expiresAt.Before(oldestTime) {
				oldestKey = k
				oldestTime = e.expiresAt
			}
		}
		if oldestKey != "" {
			delete(s.data, oldestKey)
		}
	}

	s.data[key] = entry{
		value:     value,
		expiresAt: time.Now().Add(s.originalTTL),
	}
	return nil
}

// Get retrieves a value if it exists and hasn't expired.
func (s *MemoryStore) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, exists := s.data[key]
	if !exists {
		return "", false
	}

	if time.Now().After(e.expiresAt) {
		return "", false
	}

	return e.value, true
}

// Delete removes a value.
func (s *MemoryStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return nil
	}
	delete(s.data, key)
	delete(s.compressed, key)
	return nil
}

// SetCompressed stores compressed content with long TTL (V2: KV-cache preservation).
func (s *MemoryStore) SetCompressed(key, compressed string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return nil
	}

	// Evict oldest entries if at capacity (skip if updating existing key)
	if s.maxCompressed > 0 && len(s.compressed) >= s.maxCompressed {
		if _, exists := s.compressed[key]; !exists {
			s.evictOldestCompressed()
		}
	}

	s.compressed[key] = entry{
		value:     compressed,
		expiresAt: time.Now().Add(s.compressedTTL),
	}
	return nil
}

// GetCompressed retrieves the cached compressed version.
func (s *MemoryStore) GetCompressed(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, exists := s.compressed[key]
	if !exists {
		s.Metrics.CompressedMisses.Add(1)
		return "", false
	}

	if time.Now().After(e.expiresAt) {
		s.Metrics.CompressedMisses.Add(1)
		return "", false
	}

	s.Metrics.CompressedHits.Add(1)
	return e.value, true
}

// DeleteCompressed removes only the compressed version cache entry.
func (s *MemoryStore) DeleteCompressed(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return nil
	}
	delete(s.compressed, key)
	return nil
}

// SetExpansion stores an expansion record for a shadow ID.
func (s *MemoryStore) SetExpansion(key string, expansion *ExpansionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return nil
	}

	s.expansions[key] = expansionEntry{
		record:    expansion,
		expiresAt: time.Now().Add(s.compressedTTL), // V2: Use long TTL for expansions
	}
	return nil
}

// GetExpansion retrieves the expansion record for a shadow ID.
func (s *MemoryStore) GetExpansion(key string) (*ExpansionRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, exists := s.expansions[key]
	if !exists {
		return nil, false
	}

	if time.Now().After(e.expiresAt) {
		return nil, false
	}

	return e.record, true
}

// DeleteExpansion removes the expansion record.
func (s *MemoryStore) DeleteExpansion(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return nil
	}
	delete(s.expansions, key)
	return nil
}

// evictOldestCompressed removes the entry with the earliest expiry (called with lock held).
func (s *MemoryStore) evictOldestCompressed() {
	var oldestKey string
	var oldestTime time.Time
	first := true
	for k, e := range s.compressed {
		if first || e.expiresAt.Before(oldestTime) {
			oldestKey = k
			oldestTime = e.expiresAt
			first = false
		}
	}
	if oldestKey != "" {
		delete(s.compressed, oldestKey)
		s.Metrics.CompressedEvictions.Add(1)
	}
}

// CompressedSize returns the number of entries in the compressed cache.
func (s *MemoryStore) CompressedSize() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.compressed)
}

// Reset clears all cached data without stopping the cleanup goroutine.
// Call this when starting a new session to ensure a clean slate.
func (s *MemoryStore) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data = make(map[string]entry)
	s.compressed = make(map[string]entry)
	s.expansions = make(map[string]expansionEntry)
}

// Close stops the cleanup goroutine and clears data.
func (s *MemoryStore) Close() error {
	s.mu.Lock()
	if !s.stopped {
		s.stopped = true
		close(s.stopChan)
	}
	s.mu.Unlock()

	// Wait for cleanup goroutine to exit before niling maps.
	s.wg.Wait()

	s.mu.Lock()
	s.data = nil
	s.compressed = nil
	s.expansions = nil
	s.mu.Unlock()

	return nil
}

// cleanup periodically removes expired entries.
func (s *MemoryStore) cleanup() {
	defer s.wg.Done()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.mu.Lock()
			if !s.stopped {
				now := time.Now()
				for key, e := range s.data {
					if now.After(e.expiresAt) {
						delete(s.data, key)
					}
				}
				for key, e := range s.compressed {
					if now.After(e.expiresAt) {
						delete(s.compressed, key)
					}
				}
				for key, e := range s.expansions {
					if now.After(e.expiresAt) {
						delete(s.expansions, key)
					}
				}
			}
			s.mu.Unlock()
		}
	}
}

// Ensure MemoryStore implements Store
var _ Store = (*MemoryStore)(nil)
