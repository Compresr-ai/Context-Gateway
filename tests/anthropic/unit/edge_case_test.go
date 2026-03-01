// Edge Case Tests
//
// Tests critical edge cases:
// - API failure fallback (graceful degradation)
// - Oversized content handling (skip compression)
// - First request cache miss (cold start)
// - Stream buffer phantom tool suppression
// - Content hash determinism
package unit

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/config"
	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
	"github.com/compresr/context-gateway/tests/anthropic/fixtures"
)

// TestE1_CompressionAPIFailure verifies graceful fallback on API error.
func TestE1_CompressionAPIFailure(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyCompresr, 50, true)
	cfg.Pipes.ToolOutput.Compresr.Endpoint = "http://invalid-endpoint:9999/fail"
	cfg.Pipes.ToolOutput.Compresr.Timeout = 100 * time.Millisecond
	st := fixtures.TestStore()

	pipe := tooloutput.New(cfg, st)
	content := strings.Repeat("content to compress ", 50)
	reqBody := fixtures.RequestWithSingleToolOutput(content)
	ctx := fixtures.TestPipeContext(reqBody)

	result, err := pipe.Process(ctx)
	// Should not error - should fallback gracefully
	assert.NoError(t, err, "E1: should fallback on API failure, not error")
	// Result should be valid (either compressed or original)
	assert.NotNil(t, result)
}

// TestE3_ContentTooLarge verifies oversized content skips compression.
func TestE3_ContentTooLarge(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyCompresr, 50, true)
	cfg.Pipes.ToolOutput.MaxBytes = 1000 // Small max for test
	st := fixtures.TestStore()

	pipe := tooloutput.New(cfg, st)
	content := strings.Repeat("x", 2000) // Exceeds max
	reqBody := fixtures.RequestWithSingleToolOutput(content)
	ctx := fixtures.TestPipeContext(reqBody)

	result, err := pipe.Process(ctx)
	require.NoError(t, err)

	// Content should pass through unchanged (skipped compression)
	assert.Equal(t, reqBody, result, "E3: oversized content should pass through unchanged")
}

// TestE4_CacheMissOnFirstRequest verifies cold start behavior.
func TestE4_CacheMissOnFirstRequest(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyCompresr, 50, true)
	st := fixtures.TestStore() // Empty store

	pipe := tooloutput.New(cfg, st)
	content := strings.Repeat("new content ", 50)
	reqBody := fixtures.RequestWithSingleToolOutput(content)
	ctx := fixtures.TestPipeContext(reqBody)

	_, _ = pipe.Process(ctx)
	metrics := pipe.GetMetrics()
	assert.GreaterOrEqual(t, metrics.CacheMisses, int64(1),
		"E4: first request should be a cache miss")
}

// TestE14_StreamBufferSuppressExpandContext verifies phantom tool filtering.
func TestE14_StreamBufferSuppressExpandContext(t *testing.T) {
	buffer := tooloutput.NewStreamBuffer()
	chunk := []byte(`data: {"type":"content_block_start","content_block":{"type":"tool_use","id":"tu1","name":"expand_context"}}`)

	output, err := buffer.ProcessChunk(chunk)
	assert.NoError(t, err)
	assert.Nil(t, output, "E14: expand_context chunk should be suppressed")
}

// TestE15_StreamBufferPassthroughNormalTools verifies normal tools pass through.
func TestE15_StreamBufferPassthroughNormalTools(t *testing.T) {
	buffer := tooloutput.NewStreamBuffer()
	chunk := []byte(`data: {"type":"content_block_start","content_block":{"type":"tool_use","id":"tu1","name":"read_file"}}`)

	output, err := buffer.ProcessChunk(chunk)
	assert.NoError(t, err)
	assert.NotNil(t, output, "E15: normal tool should pass through")
}

// TestE22_ContentHashDeterminism verifies hashing is consistent.
// Note: contentHash is a private method on Pipe, so we test via behavior
func TestE22_ContentHashDeterminism(t *testing.T) {
	// Test that same content produces same shadow ID (via cache behavior)
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 50, true)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	// Same content should produce consistent behavior
	content := strings.Repeat("test content ", 50)
	reqBody := fixtures.RequestWithSingleToolOutput(content)

	ctx1 := fixtures.TestPipeContext(reqBody)
	result1, err1 := pipe.Process(ctx1)
	require.NoError(t, err1)

	ctx2 := fixtures.TestPipeContext(reqBody)
	result2, err2 := pipe.Process(ctx2)
	require.NoError(t, err2)

	// Same input should produce same output
	assert.Equal(t, result1, result2, "E22: same content should produce consistent results")
}

// TestE24_PrefixFormat verifies prefix format constant.
func TestE24_PrefixFormat(t *testing.T) {
	// PrefixFormat is a constant, verify it has expected format
	assert.Contains(t, tooloutput.PrefixFormat, "<<<SHADOW:")
	assert.Contains(t, tooloutput.PrefixFormat, ">>>")
}

// TestBelowMinThreshold verifies content below minBytes is not compressed.
func TestBelowMinThreshold(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyCompresr, 500, true) // 500 byte min
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	smallContent := strings.Repeat("x", 100) // Below 500
	reqBody := fixtures.RequestWithSingleToolOutput(smallContent)
	ctx := fixtures.TestPipeContext(reqBody)

	result, err := pipe.Process(ctx)
	require.NoError(t, err)
	assert.Equal(t, reqBody, result, "content below min threshold should pass through")
}

// TestPassthroughStrategy verifies passthrough mode.
func TestPassthroughStrategy(t *testing.T) {
	cfg := fixtures.TestConfig(config.StrategyPassthrough, 50, false)
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	content := strings.Repeat("content ", 100)
	reqBody := fixtures.RequestWithSingleToolOutput(content)
	ctx := fixtures.TestPipeContext(reqBody)

	result, err := pipe.Process(ctx)
	require.NoError(t, err)
	assert.Equal(t, reqBody, result, "passthrough should not modify content")
	assert.False(t, ctx.OutputCompressed)
}

// TestDisabledPipe verifies disabled pipe returns unchanged content.
func TestDisabledPipe(t *testing.T) {
	cfg := fixtures.DisabledConfig()
	pipe := tooloutput.New(cfg, fixtures.TestStore())

	content := strings.Repeat("content ", 100)
	reqBody := fixtures.RequestWithSingleToolOutput(content)
	ctx := fixtures.TestPipeContext(reqBody)

	result, err := pipe.Process(ctx)
	require.NoError(t, err)
	assert.Equal(t, reqBody, result, "disabled pipe should not modify content")
}
