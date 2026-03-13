// Store Integration Tests
//
// Tests the MemoryStore end-to-end: shadow put/get, TTL expiry,
// and compressed cache behaviour with dual-TTL semantics.
//
// Run with: go test ./tests/store/integration/... -v
package integration

import (
	"testing"
	"time"

	"github.com/compresr/context-gateway/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_Store_ShadowPutGet stores shadow content via Set,
// retrieves it via Get, and verifies the value matches.
func TestIntegration_Store_ShadowPutGet(t *testing.T) {
	s := store.NewMemoryStore(1 * time.Hour)
	defer s.Close()

	key := "shadow-ref-001"
	original := "This is the full original tool output content that was compressed."

	// Store original content
	err := s.Set(key, original)
	require.NoError(t, err)

	// Retrieve and verify
	got, found := s.Get(key)
	require.True(t, found, "expected shadow content to be found")
	assert.Equal(t, original, got)

	// Non-existent key should return false
	_, found = s.Get("nonexistent-key")
	assert.False(t, found)
}

// TestIntegration_Store_TTLExpiry stores content with a very short TTL,
// waits for it to expire, then verifies it is no longer retrievable.
func TestIntegration_Store_TTLExpiry(t *testing.T) {
	// Use a very short TTL so the test completes quickly
	shortTTL := 50 * time.Millisecond
	s := store.NewMemoryStoreWithDualTTL(shortTTL, 1*time.Hour)
	defer s.Close()

	key := "shadow-ref-ttl"
	err := s.Set(key, "ephemeral content")
	require.NoError(t, err)

	// Verify it exists immediately
	_, found := s.Get(key)
	require.True(t, found, "content should exist before TTL expires")

	// Wait for TTL to expire
	time.Sleep(80 * time.Millisecond)

	// Should be expired now (Get checks expiresAt)
	_, found = s.Get(key)
	assert.False(t, found, "content should be expired after TTL")
}

// TestIntegration_Store_CompressedCacheHit exercises the dual-TTL design:
// - Original content has a short TTL
// - Compressed content has a long TTL (for KV-cache preservation)
// Verifies that compressed content survives after original content expires.
func TestIntegration_Store_CompressedCacheHit(t *testing.T) {
	originalTTL := 50 * time.Millisecond
	compressedTTL := 1 * time.Hour
	s := store.NewMemoryStoreWithDualTTL(originalTTL, compressedTTL)
	defer s.Close()

	key := "shadow-ref-dual"
	originalContent := "Full original tool output with all the details and verbose logs."
	compressedContent := "Compressed: key points extracted."

	// Store both original and compressed
	err := s.Set(key, originalContent)
	require.NoError(t, err)
	err = s.SetCompressed(key, compressedContent)
	require.NoError(t, err)

	// Both should be available immediately
	gotOrig, found := s.Get(key)
	require.True(t, found)
	assert.Equal(t, originalContent, gotOrig)

	gotComp, found := s.GetCompressed(key)
	require.True(t, found)
	assert.Equal(t, compressedContent, gotComp)

	// Wait for original TTL to expire
	time.Sleep(80 * time.Millisecond)

	// Original should be gone
	_, found = s.Get(key)
	assert.False(t, found, "original content should have expired")

	// Compressed should still be available (long TTL)
	gotComp, found = s.GetCompressed(key)
	assert.True(t, found, "compressed content should survive with long TTL")
	assert.Equal(t, compressedContent, gotComp)

	// Verify cache metrics recorded a hit
	hits := s.Metrics.CompressedHits.Load()
	assert.True(t, hits >= 2, "expected at least 2 compressed cache hits, got %d", hits)
}

// TestIntegration_Store_ExpansionRecord verifies that expansion records
// (expand_context interactions) can be stored and retrieved correctly.
func TestIntegration_Store_ExpansionRecord(t *testing.T) {
	s := store.NewMemoryStore(1 * time.Hour)
	defer s.Close()

	key := "shadow-ref-expand"
	record := &store.ExpansionRecord{
		AssistantMessage:  []byte(`{"role":"assistant","content":[{"type":"tool_use","name":"expand_context"}]}`),
		ToolResultMessage: []byte(`{"role":"user","content":[{"type":"tool_result","content":"full content"}]}`),
	}

	// Store expansion record
	err := s.SetExpansion(key, record)
	require.NoError(t, err)

	// Retrieve and verify
	got, found := s.GetExpansion(key)
	require.True(t, found)
	assert.Equal(t, string(record.AssistantMessage), string(got.AssistantMessage))
	assert.Equal(t, string(record.ToolResultMessage), string(got.ToolResultMessage))

	// Non-existent should return nil
	got, found = s.GetExpansion("nonexistent")
	assert.False(t, found)
	assert.Nil(t, got)
}

// TestIntegration_Store_DeleteCleansUp verifies that Delete removes both
// original and compressed entries for the same key.
func TestIntegration_Store_DeleteCleansUp(t *testing.T) {
	s := store.NewMemoryStore(1 * time.Hour)
	defer s.Close()

	key := "shadow-ref-delete"
	_ = s.Set(key, "original")
	_ = s.SetCompressed(key, "compressed")

	// Delete removes both
	err := s.Delete(key)
	require.NoError(t, err)

	_, found := s.Get(key)
	assert.False(t, found)
	_, found = s.GetCompressed(key)
	assert.False(t, found)
}

// TestIntegration_Store_ResetClearsAll verifies that Reset clears all data.
func TestIntegration_Store_ResetClearsAll(t *testing.T) {
	s := store.NewMemoryStore(1 * time.Hour)
	defer s.Close()

	_ = s.Set("k1", "v1")
	_ = s.SetCompressed("k1", "c1")
	_ = s.SetExpansion("k1", &store.ExpansionRecord{})

	s.Reset()

	_, found := s.Get("k1")
	assert.False(t, found)
	_, found = s.GetCompressed("k1")
	assert.False(t, found)
	got, found := s.GetExpansion("k1")
	assert.False(t, found)
	assert.Nil(t, got)
}
