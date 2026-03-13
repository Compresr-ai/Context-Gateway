package unit

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryStore_TTLExpiry(t *testing.T) {
	s := store.NewMemoryStore(10 * time.Millisecond)
	defer s.Close()

	require.NoError(t, s.Set("key1", "value1"))
	val, ok := s.Get("key1")
	require.True(t, ok)
	assert.Equal(t, "value1", val)

	time.Sleep(20 * time.Millisecond)

	_, ok = s.Get("key1")
	assert.False(t, ok, "should expire after TTL")
}

func TestMemoryStore_ConcurrentReadWrite(t *testing.T) {
	s := store.NewMemoryStore(1 * time.Hour)
	defer s.Close()

	var wg sync.WaitGroup

	// 100 writers
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("key_%d", i)
			_ = s.Set(key, fmt.Sprintf("value_%d", i))
			_ = s.SetCompressed(key, fmt.Sprintf("compressed_%d", i))
		}(i)
	}

	// 100 readers
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("key_%d", i)
			s.Get(key)
			s.GetCompressed(key)
		}(i)
	}

	wg.Wait()
}

func TestMemoryStore_SetAfterClose(t *testing.T) {
	s := store.NewMemoryStore(1 * time.Hour)
	require.NoError(t, s.Close())

	// Should not panic after close
	err := s.Set("key", "value")
	assert.NoError(t, err)

	_, ok := s.Get("key")
	assert.False(t, ok, "should not retrieve after close")
}

func TestMemoryStore_DualTTL_OriginalExpiresBeforeCompressed(t *testing.T) {
	s := store.NewMemoryStoreWithDualTTL(10*time.Millisecond, 1*time.Hour)
	defer s.Close()

	require.NoError(t, s.Set("shadow_123", "original content"))
	require.NoError(t, s.SetCompressed("shadow_123", "compressed content"))

	// Both available immediately
	_, ok := s.Get("shadow_123")
	require.True(t, ok)
	_, ok = s.GetCompressed("shadow_123")
	require.True(t, ok)

	// Wait for original TTL to expire
	time.Sleep(20 * time.Millisecond)

	_, ok = s.Get("shadow_123")
	assert.False(t, ok, "original should expire with short TTL")

	val, ok := s.GetCompressed("shadow_123")
	assert.True(t, ok, "compressed should still be available with long TTL")
	assert.Equal(t, "compressed content", val)
}

func TestMemoryStore_Reset_WhileConcurrentAccess(t *testing.T) {
	s := store.NewMemoryStore(1 * time.Hour)
	defer s.Close()

	// Pre-populate
	for i := 0; i < 50; i++ {
		_ = s.Set(fmt.Sprintf("key_%d", i), "value")
	}

	var wg sync.WaitGroup

	// Concurrent readers
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.Get(fmt.Sprintf("key_%d", i))
		}(i)
	}

	// Reset in the middle
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Reset()
	}()

	// Concurrent writers
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = s.Set(fmt.Sprintf("new_key_%d", i), "new_value")
		}(i)
	}

	wg.Wait()
	// No panics = success
}

func TestMemoryStore_ExpansionRecord_RoundTrip(t *testing.T) {
	s := store.NewMemoryStore(1 * time.Hour)
	defer s.Close()

	record := &store.ExpansionRecord{
		AssistantMessage:  json.RawMessage(`{"role":"assistant","content":"expand"}`),
		ToolResultMessage: json.RawMessage(`{"role":"tool","content":"full content here"}`),
	}

	require.NoError(t, s.SetExpansion("shadow_abc", record))

	got, ok := s.GetExpansion("shadow_abc")
	require.True(t, ok)
	assert.Equal(t, record.AssistantMessage, got.AssistantMessage)
	assert.Equal(t, record.ToolResultMessage, got.ToolResultMessage)
}

func TestMemoryStore_OverwriteExistingKey(t *testing.T) {
	s := store.NewMemoryStore(1 * time.Hour)
	defer s.Close()

	require.NoError(t, s.Set("key", "first"))
	require.NoError(t, s.Set("key", "second"))

	val, ok := s.Get("key")
	require.True(t, ok)
	assert.Equal(t, "second", val)
}

func TestMemoryStore_DeleteRemovesBothOriginalAndCompressed(t *testing.T) {
	s := store.NewMemoryStore(1 * time.Hour)
	defer s.Close()

	require.NoError(t, s.Set("key", "original"))
	require.NoError(t, s.SetCompressed("key", "compressed"))

	require.NoError(t, s.Delete("key"))

	_, ok := s.Get("key")
	assert.False(t, ok)
	_, ok = s.GetCompressed("key")
	assert.False(t, ok)
}

func TestMemoryStore_GetNonexistentKey(t *testing.T) {
	s := store.NewMemoryStore(1 * time.Hour)
	defer s.Close()

	_, ok := s.Get("nonexistent")
	assert.False(t, ok)

	_, ok = s.GetCompressed("nonexistent")
	assert.False(t, ok)

	_, ok = s.GetExpansion("nonexistent")
	assert.False(t, ok)
}

func TestMemoryStore_CloseWaitsForCleanup(t *testing.T) {
	// Verify Close() doesn't panic and properly waits for cleanup goroutine.
	s := store.NewMemoryStoreWithDualTTL(10*time.Millisecond, 10*time.Millisecond)

	// Add some data
	for i := 0; i < 100; i++ {
		_ = s.Set(fmt.Sprintf("k%d", i), "v")
		_ = s.SetCompressed(fmt.Sprintf("k%d", i), "c")
	}

	// Close should not panic
	require.NoError(t, s.Close())
}

func TestMemoryStore_DeleteAfterClose(t *testing.T) {
	s := store.NewMemoryStore(1 * time.Hour)
	_ = s.Set("key", "value")
	_ = s.SetCompressed("key", "compressed")
	require.NoError(t, s.Close())

	// Delete operations after close should not panic
	assert.NoError(t, s.Delete("key"))
	assert.NoError(t, s.DeleteCompressed("key"))
	assert.NoError(t, s.DeleteExpansion("key"))
}

func TestMemoryStore_ConcurrentCloseAndOperations(t *testing.T) {
	s := store.NewMemoryStore(1 * time.Hour)

	// Pre-populate
	for i := 0; i < 50; i++ {
		_ = s.Set(fmt.Sprintf("k%d", i), "v")
		_ = s.SetCompressed(fmt.Sprintf("k%d", i), "c")
	}

	var wg sync.WaitGroup

	// Concurrent operations
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("k%d", i)
			s.Get(key)
			s.GetCompressed(key)
			_ = s.Delete(key)
		}(i)
	}

	// Close concurrently
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = s.Close()
	}()

	wg.Wait()
	// No panics = success
}

func TestMemoryStore_CompressedEviction(t *testing.T) {
	s := store.NewMemoryStoreWithDualTTL(1*time.Hour, 1*time.Hour)
	defer s.Close()

	// Fill beyond MaxCompressedEntries
	for i := 0; i < store.MaxCompressedEntries+100; i++ {
		_ = s.SetCompressed(fmt.Sprintf("key_%d", i), fmt.Sprintf("value_%d", i))
	}

	// Should be at or below max
	assert.LessOrEqual(t, s.CompressedSize(), store.MaxCompressedEntries)

	// Evictions should have occurred
	assert.Greater(t, s.Metrics.CompressedEvictions.Load(), int64(0))
}

func TestMemoryStore_CompressedMetrics(t *testing.T) {
	s := store.NewMemoryStoreWithDualTTL(1*time.Hour, 1*time.Hour)
	defer s.Close()

	// Miss on nonexistent key
	_, ok := s.GetCompressed("nonexistent")
	assert.False(t, ok)
	assert.Equal(t, int64(1), s.Metrics.CompressedMisses.Load())
	assert.Equal(t, int64(0), s.Metrics.CompressedHits.Load())

	// Set and hit
	_ = s.SetCompressed("key1", "compressed1")
	val, ok := s.GetCompressed("key1")
	assert.True(t, ok)
	assert.Equal(t, "compressed1", val)
	assert.Equal(t, int64(1), s.Metrics.CompressedHits.Load())
	assert.Equal(t, int64(1), s.Metrics.CompressedMisses.Load())

	// Expired key counts as miss
	s2 := store.NewMemoryStoreWithDualTTL(1*time.Hour, 10*time.Millisecond)
	defer s2.Close()
	_ = s2.SetCompressed("expiring", "val")
	time.Sleep(20 * time.Millisecond)
	_, ok = s2.GetCompressed("expiring")
	assert.False(t, ok)
	assert.Equal(t, int64(1), s2.Metrics.CompressedMisses.Load())
}

func TestMemoryStore_OverwriteDoesNotEvict(t *testing.T) {
	s := store.NewMemoryStoreWithDualTTL(1*time.Hour, 1*time.Hour)
	defer s.Close()

	// Fill to capacity
	for i := 0; i < store.MaxCompressedEntries; i++ {
		_ = s.SetCompressed(fmt.Sprintf("key_%d", i), "value")
	}
	assert.Equal(t, store.MaxCompressedEntries, s.CompressedSize())

	// Overwriting existing key should NOT trigger eviction
	evictionsBefore := s.Metrics.CompressedEvictions.Load()
	_ = s.SetCompressed("key_0", "updated_value")
	assert.Equal(t, evictionsBefore, s.Metrics.CompressedEvictions.Load())
	assert.Equal(t, store.MaxCompressedEntries, s.CompressedSize())
}
