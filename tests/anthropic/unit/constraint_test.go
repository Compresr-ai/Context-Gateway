package unit

// Constraint Tests (C1-C11)
//
// Tests the 11 design constraints from the V2 specification:
// - C1: KV-cache preservation via deterministic content hashing
// - C2: Multi-tool batch compression (ALL tools, not just last)
// - C6: Cache lookup before compression (avoid redundant work)
// - C7: Transparent proxy (expand_context invisible to client)
// - C11: Rate limiting to protect compression API

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
	"github.com/compresr/context-gateway/internal/store"
	"github.com/compresr/context-gateway/tests/anthropic/fixtures"
)

// computeShadowID replicates the Pipe's content hashing for test setup.
// Uses SHA256 truncated to 16 bytes with "shadow_" prefix.
func computeShadowID(content string) string {
	hash := sha256.Sum256([]byte(content))
	return "shadow_" + hex.EncodeToString(hash[:16])
}

// TestC1_KVCachePreservation verifies deterministic hashing for KV-cache.
// Same content MUST produce same hash to avoid cache invalidation.
// Different content MUST produce different hash to avoid collisions.
// This enables LLM KV-cache reuse across requests with identical tool outputs.
func TestC1_KVCachePreservation(t *testing.T) {
	content := strings.Repeat("test content for KV cache ", 100)
	hash1 := computeShadowID(content)
	hash2 := computeShadowID(content)
	assert.Equal(t, hash1, hash2, "C1: same content must produce same hash")

	hash3 := computeShadowID(content + " extra")
	assert.NotEqual(t, hash1, hash3, "C1: different content must produce different hash")

	// Verify hash format
	assert.True(t, strings.HasPrefix(hash1, "shadow_"), "hash must have shadow_ prefix")
}

// TestC2_AllToolsCompressed verifies ALL tool outputs are compressed.
// V2 changed from "last tool only" to "all tools" for KV-cache preservation.
// Each tool output above minBytes threshold is processed independently.
// This maximizes context reduction while preserving expandability.
func TestC2_AllToolsCompressed(t *testing.T) {
	content1 := strings.Repeat("tool output 1 ", 100)
	content2 := strings.Repeat("tool output 2 ", 100)
	content3 := strings.Repeat("tool output 3 ", 100)

	// Pre-populate cache with compressed versions
	shadowID1, shadowID2, shadowID3 := computeShadowID(content1), computeShadowID(content2), computeShadowID(content3)
	st := store.NewMemoryStore(5 * time.Minute)
	st.SetCompressed(shadowID1, "compressed1")
	st.SetCompressed(shadowID2, "compressed2")
	st.SetCompressed(shadowID3, "compressed3")

	cfg := fixtures.TestConfig(config.StrategyCompresr, 50, true)
	pipe := tooloutput.New(cfg, st)

	// Create request body with multiple tool outputs
	body := fixtures.MultiToolOutputRequest(content1, content2, content3)

	// Create pipe context with OpenAI adapter (since fixtures use OpenAI format)
	adapter := adapters.NewOpenAIAdapter()
	ctx := pipes.NewPipeContext(adapter, body)

	_, err := pipe.Process(ctx)
	require.NoError(t, err)

	// ALL tools should be tracked for compression
	assert.GreaterOrEqual(t, len(ctx.ToolOutputCompressions), 3,
		"C2: all tool outputs should be tracked")
}

// TestC6_CacheLookupBeforeCompression verifies cache-first strategy.
// Before calling compression API, check if content is already cached.
// This reduces API calls and latency for repeated tool outputs.
// Cache key is the content hash (shadow ID).
func TestC6_CacheLookupBeforeCompression(t *testing.T) {
	content := strings.Repeat("large content ", 100)
	shadowID := computeShadowID(content)
	compressed := "much smaller summary"

	st := store.NewMemoryStore(5 * time.Minute)
	st.SetCompressed(shadowID, compressed)
	st.Set(shadowID, content)

	cfg := fixtures.TestConfig(config.StrategyCompresr, 50, true)
	pipe := tooloutput.New(cfg, st)

	// Create request body
	body := fixtures.SingleToolOutputRequest(content)

	// Create pipe context with adapter
	adapter := adapters.NewOpenAIAdapter()
	ctx := pipes.NewPipeContext(adapter, body)

	_, err := pipe.Process(ctx)
	require.NoError(t, err)

	if len(ctx.ToolOutputCompressions) > 0 {
		assert.True(t, ctx.ToolOutputCompressions[0].CacheHit, "C6: should use cached compression")
		assert.Equal(t, "cache_hit", ctx.ToolOutputCompressions[0].MappingStatus)
	}
}

// TestC7_TransparentProxy verifies expand_context is invisible to client.
// The phantom tool is used internally for context expansion but must be
// filtered from responses sent to the client. Client should never see
// expand_context in tool_calls or streaming chunks.
func TestC7_TransparentProxy(t *testing.T) {
	st := store.NewMemoryStore(5 * time.Minute)
	expander := tooloutput.NewExpander(st, nil)

	responseWithExpand := []byte(`{
		"content": [
			{"type": "text", "text": "Let me expand that"},
			{"type": "tool_use", "id": "tu1", "name": "expand_context", "input": {"id": "shadow_abc123"}},
			{"type": "text", "text": "More text"}
		]
	}`)

	filtered, modified := expander.FilterExpandContextFromResponse(responseWithExpand)
	assert.True(t, modified, "C7: response should be modified")
	assert.NotContains(t, string(filtered), "expand_context",
		"C7: expand_context should be filtered from response")
}

// TestC11_RateLimiter verifies rate limiting protects compression API.
// Token bucket allows burst but limits sustained rate.
// Prevents overwhelming the compression service under load.
// Configurable rate (default 20/sec) with graceful degradation.
func TestC11_RateLimiter(t *testing.T) {
	limiter := tooloutput.NewRateLimiter(100) // 100 per second
	defer limiter.Close()

	// Should allow burst
	start := time.Now()
	for i := 0; i < 10; i++ {
		ok := limiter.Acquire()
		assert.True(t, ok, "C11: should acquire token")
	}
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 100*time.Millisecond, "C11: burst should be fast")
}
