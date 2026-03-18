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
	"container/list"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/compresr/context-gateway/internal/formats"
)

// V2: Default TTL values - re-exported from config for backward compatibility.
const (
	DefaultOriginalTTL   = 5 * time.Hour  // TTL for original content (expand_context)
	DefaultCompressedTTL = 24 * time.Hour // Long TTL for compressed (KV-cache)

	// MaxCompressedEntries is the maximum number of compressed cache entries.
	// OPTIMIZED: Reduced from 10K to 2K to lower memory footprint.
	// At ~2KB avg per entry, 2K entries ≈ 4MB (was 20MB).
	MaxCompressedEntries = 2_000

	// MaxOriginalEntries caps the original content map.
	// OPTIMIZED: Reduced from 5K to 1K to lower memory footprint.
	// Original content is larger (~5KB avg), so 1K entries ≈ 5MB (was 25MB).
	MaxOriginalEntries = 1_000

	// MaxExpansionEntries caps the expansion records map.
	// At ~1KB avg per entry, 1K entries ≈ 1MB.
	MaxExpansionEntries = 1_000

	// MaxFieldRefEntries caps the field reference map.
	// At ~512B avg per entry, 1K entries ≈ 512KB.
	MaxFieldRefEntries = 1_000
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
// V3: Adds field-level compression refs for structured data expansion.
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

	// V3: Field-level compression refs (uses formats.FieldRef as canonical type)
	// SetFieldRef stores a field reference for expansion.
	SetFieldRef(ref *formats.FieldRef) error

	// GetFieldRef retrieves a field reference by ID.
	GetFieldRef(refID string) (*formats.FieldRef, bool)

	// DeleteFieldRef removes a field reference.
	DeleteFieldRef(refID string) error

	// SetFieldRefs stores multiple field references at once.
	SetFieldRefs(refs []*formats.FieldRef) error

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
// V3: Adds field-level compression refs for structured data.
type MemoryStore struct {
	data          map[string]entry
	dataOrder     *list.List                // insertion order for O(1) eviction
	compressed    map[string]entry          // Cache for compressed versions
	compOrder     *list.List                // insertion order for O(1) eviction
	expansions    map[string]expansionEntry // Cache for expansion records
	expansOrder   *list.List                // insertion order for O(1) eviction
	fieldRefs     map[string]fieldRefEntry  // V3: Field-level compression refs
	fieldRefOrder *list.List                // insertion order for O(1) eviction
	mu            sync.RWMutex
	originalTTL   time.Duration // V2: Short TTL for original
	compressedTTL time.Duration // V2: Long TTL for compressed
	stopChan      chan struct{}
	stopped       bool
	wg            sync.WaitGroup // Waits for cleanup goroutine to exit

	maxCompressed int          // Max entries in compressed cache (0 = unlimited)
	maxExpansions int          // Max entries in expansions cache
	maxFieldRefs  int          // Max entries in fieldRefs cache
	Metrics       CacheMetrics // Observable cache statistics
}

type entry struct {
	value     string
	expiresAt time.Time
	element   *list.Element // pointer into order list for O(1) MoveToBack/Remove
}

type expansionEntry struct {
	record    *ExpansionRecord
	expiresAt time.Time
	element   *list.Element // pointer into order list for O(1) MoveToBack/Remove
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
		dataOrder:     list.New(),
		compressed:    make(map[string]entry),
		compOrder:     list.New(),
		expansions:    make(map[string]expansionEntry),
		expansOrder:   list.New(),
		fieldRefs:     make(map[string]fieldRefEntry),
		fieldRefOrder: list.New(),
		originalTTL:   originalTTL,
		compressedTTL: compressedTTL,
		stopChan:      make(chan struct{}),
		maxCompressed: MaxCompressedEntries,
		maxExpansions: MaxExpansionEntries,
		maxFieldRefs:  MaxFieldRefEntries,
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

	// If key exists: refresh TTL and move to back — no new list node needed.
	if existing, ok := s.data[key]; ok {
		s.dataOrder.MoveToBack(existing.element)
		s.data[key] = entry{value: value, expiresAt: time.Now().Add(s.originalTTL), element: existing.element}
		return nil
	}

	// Cap original entries to prevent unbounded growth — O(1) eviction via insertion order list.
	if len(s.data) >= MaxOriginalEntries {
		s.evictOldestData()
	}

	elem := s.dataOrder.PushBack(key)
	s.data[key] = entry{value: value, expiresAt: time.Now().Add(s.originalTTL), element: elem}
	return nil
}

// Get retrieves a value if it exists and hasn't expired.
func (s *MemoryStore) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// enforce "no access after close" contract consistently with Set/Delete
	if s.stopped {
		return "", false
	}

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
	if e, ok := s.data[key]; ok {
		s.dataOrder.Remove(e.element)
		delete(s.data, key)
	}
	if e, ok := s.compressed[key]; ok {
		s.compOrder.Remove(e.element)
		delete(s.compressed, key)
	}
	return nil
}

// SetCompressed stores compressed content with long TTL (V2: KV-cache preservation).
func (s *MemoryStore) SetCompressed(key, compressed string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return nil
	}

	// If key exists: refresh TTL and move to back — no new list node needed.
	if existing, ok := s.compressed[key]; ok {
		s.compOrder.MoveToBack(existing.element)
		s.compressed[key] = entry{value: compressed, expiresAt: time.Now().Add(s.compressedTTL), element: existing.element}
		return nil
	}

	// Evict oldest entry if at capacity.
	if s.maxCompressed > 0 && len(s.compressed) >= s.maxCompressed {
		s.evictOldestCompressed()
	}

	elem := s.compOrder.PushBack(key)
	s.compressed[key] = entry{value: compressed, expiresAt: time.Now().Add(s.compressedTTL), element: elem}
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
	if e, ok := s.compressed[key]; ok {
		s.compOrder.Remove(e.element)
		delete(s.compressed, key)
	}
	return nil
}

// SetExpansion stores an expansion record for a shadow ID.
func (s *MemoryStore) SetExpansion(key string, expansion *ExpansionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return nil
	}

	// If key exists: refresh and move to back — no new list node needed.
	if existing, ok := s.expansions[key]; ok {
		s.expansOrder.MoveToBack(existing.element)
		s.expansions[key] = expansionEntry{record: expansion, expiresAt: time.Now().Add(s.compressedTTL), element: existing.element}
		return nil
	}

	// Cap expansions entries to prevent unbounded growth.
	if len(s.expansions) >= s.maxExpansions {
		s.evictOldestExpansion()
	}

	elem := s.expansOrder.PushBack(key)
	s.expansions[key] = expansionEntry{record: expansion, expiresAt: time.Now().Add(s.compressedTTL), element: elem}
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
	if e, ok := s.expansions[key]; ok {
		s.expansOrder.Remove(e.element)
		delete(s.expansions, key)
	}
	return nil
}

// evictOldestData removes the oldest data entry (called with lock held).
func (s *MemoryStore) evictOldestData() {
	for s.dataOrder.Len() > 0 {
		front := s.dataOrder.Front()
		k := front.Value.(string)
		s.dataOrder.Remove(front)
		if _, exists := s.data[k]; exists {
			delete(s.data, k)
			return
		}
	}
}

// evictOldestCompressed removes the oldest-inserted entry — O(1) via insertion order list (called with lock held).
func (s *MemoryStore) evictOldestCompressed() {
	for s.compOrder.Len() > 0 {
		front := s.compOrder.Front()
		k := front.Value.(string)
		s.compOrder.Remove(front)
		if _, exists := s.compressed[k]; exists {
			delete(s.compressed, k)
			s.Metrics.CompressedEvictions.Add(1)
			return
		}
	}
}

// evictOldestExpansion removes the oldest expansion entry (called with lock held).
func (s *MemoryStore) evictOldestExpansion() {
	for s.expansOrder.Len() > 0 {
		front := s.expansOrder.Front()
		k := front.Value.(string)
		s.expansOrder.Remove(front)
		if _, exists := s.expansions[k]; exists {
			delete(s.expansions, k)
			return
		}
	}
}

// evictOldestFieldRef removes the oldest field ref entry (called with lock held).
func (s *MemoryStore) evictOldestFieldRef() {
	for s.fieldRefOrder.Len() > 0 {
		front := s.fieldRefOrder.Front()
		k := front.Value.(string)
		s.fieldRefOrder.Remove(front)
		if _, exists := s.fieldRefs[k]; exists {
			delete(s.fieldRefs, k)
			return
		}
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
	s.dataOrder.Init()
	s.compressed = make(map[string]entry)
	s.compOrder.Init()
	s.expansions = make(map[string]expansionEntry)
	s.expansOrder.Init()
	s.fieldRefs = make(map[string]fieldRefEntry)
	s.fieldRefOrder.Init()
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
	s.fieldRefs = nil
	s.mu.Unlock()

	return nil
}

// cleanup periodically removes expired entries.
// OPTIMIZED: Runs every 10 minutes (was 5) and processes in batches to reduce lock contention.
func (s *MemoryStore) cleanup() {
	defer s.wg.Done()
	ticker := time.NewTicker(10 * time.Minute) // Reduced frequency from 5min to 10min
	defer ticker.Stop()

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			// Process cleanup in smaller batches to reduce lock hold time
			s.cleanupBatch()
		}
	}
}

// cleanupBatch performs a single cleanup pass with batched deletes.
// Each map gets its own independent batch limit to avoid one map monopolising the budget.
func (s *MemoryStore) cleanupBatch() {
	const maxDeletesPerMap = 25 // 4 maps × 25 = 100 max deletes per cycle

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return
	}

	now := time.Now()
	deleteCount := 0

	// Cleanup original data
	for key, e := range s.data {
		if deleteCount >= maxDeletesPerMap {
			break
		}
		if now.After(e.expiresAt) {
			s.dataOrder.Remove(e.element)
			delete(s.data, key)
			deleteCount++
		}
	}

	// Cleanup compressed data
	deleteCount = 0
	for key, e := range s.compressed {
		if deleteCount >= maxDeletesPerMap {
			break
		}
		if now.After(e.expiresAt) {
			s.compOrder.Remove(e.element)
			delete(s.compressed, key)
			deleteCount++
		}
	}

	// Cleanup expansions
	deleteCount = 0
	for key, e := range s.expansions {
		if deleteCount >= maxDeletesPerMap {
			break
		}
		if now.After(e.expiresAt) {
			s.expansOrder.Remove(e.element)
			delete(s.expansions, key)
			deleteCount++
		}
	}

	// Cleanup field refs
	deleteCount = 0
	for key, e := range s.fieldRefs {
		if deleteCount >= maxDeletesPerMap {
			break
		}
		if now.After(e.expiresAt) {
			s.fieldRefOrder.Remove(e.element)
			delete(s.fieldRefs, key)
			deleteCount++
		}
	}
}

// Ensure MemoryStore implements Store
var _ Store = (*MemoryStore)(nil)
