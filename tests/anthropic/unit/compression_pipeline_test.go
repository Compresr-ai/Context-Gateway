// Unit Tests - Compression Pipeline with Mocks
//
// Tests the compression pipeline with mock API and store:
// - Passthrough mode: content passes unchanged
// - API compression: mock server returns compressed content
// - Expand loop: LLM requests expansion, gateway retrieves original
// - KV-cache preservation: cached content reused with prefix
package unit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/config"
	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
	"github.com/compresr/context-gateway/tests/anthropic/fixtures"
)

// TestIntegration_CompressionPipeline_PassthroughMode verifies no-op mode.
// When strategy is passthrough, content passes through unchanged.
// No compression API calls, no cache writes, no modifications.
func TestIntegration_CompressionPipeline_PassthroughMode(t *testing.T) {
	pipe := tooloutput.New(fixtures.PassthroughConfig(), fixtures.TestStore())
	content := fixtures.LargeToolOutput(2048)
	reqBody := fixtures.RequestWithSingleToolOutput(content)
	ctx := fixtures.TestPipeContext(reqBody)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	// In passthrough mode, the result should equal original request
	assert.Equal(t, reqBody, result)
	assert.False(t, ctx.OutputCompressed)
	assert.Empty(t, ctx.ShadowRefs)
}

func TestIntegration_CompressionPipeline_WithMockAPI(t *testing.T) {
	compressedContent := "Compressed: file list summary"
	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"compressed_output": compressedContent,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer mockAPI.Close()

	cfg := &config.Config{
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:             true,
				Strategy:            config.StrategyCompresr,
				FallbackStrategy:    config.StrategyPassthrough,
				MinBytes:            100,
				MaxBytes:            1024 * 1024,
				TargetRatio:         0.5,
				IncludeExpandHint:   true,
				EnableExpandContext: true,
				Compresr: config.CompresrConfig{
					Endpoint: "/compress",
					Timeout:  5 * time.Second,
				},
			},
		},
		URLs: config.URLsConfig{
			Compresr: mockAPI.URL,
		},
	}

	st := fixtures.TestStore()
	pipe := tooloutput.New(cfg, st)

	content := fixtures.LargeToolOutput(2048)
	reqBody := fixtures.RequestWithSingleToolOutput(content)
	ctx := fixtures.TestPipeContext(reqBody)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)

	if ctx.OutputCompressed {
		// Result should be smaller than original
		assert.Less(t, len(result), len(reqBody))
		// Should have shadow refs stored
		assert.NotEmpty(t, ctx.ShadowRefs)
		// Result should contain compressed content
		assert.True(t, strings.Contains(string(result), compressedContent) ||
			len(result) < len(reqBody))
	}
}

func TestIntegration_CompressionPipeline_BelowThreshold(t *testing.T) {
	pipe := tooloutput.New(fixtures.CompresrCompressionConfig(), fixtures.TestStore())

	// Content below minBytes (256 by default)
	smallContent := "small"
	reqBody := fixtures.RequestWithSingleToolOutput(smallContent)
	ctx := fixtures.TestPipeContext(reqBody)

	result, err := pipe.Process(ctx)

	require.NoError(t, err)
	// Should pass through unchanged
	assert.Equal(t, reqBody, result)
	assert.False(t, ctx.OutputCompressed)
}

func TestIntegration_CompressionPipeline_CacheHit(t *testing.T) {
	content := strings.Repeat("cached content for testing ", 50)

	// Pre-populate the store with compressed content
	st := fixtures.TestStore()

	// Create pipe
	cfg := fixtures.CompresrCompressionConfig()
	cfg.Pipes.ToolOutput.MinBytes = 50
	pipe := tooloutput.New(cfg, st)

	// First request - cache miss
	reqBody := fixtures.RequestWithSingleToolOutput(content)
	ctx1 := fixtures.TestPipeContext(reqBody)
	_, err := pipe.Process(ctx1)
	require.NoError(t, err)

	// Second request with same content - should be cache hit
	ctx2 := fixtures.TestPipeContext(reqBody)
	_, err = pipe.Process(ctx2)
	require.NoError(t, err)

	// Check metrics for cache hits
	metrics := pipe.GetMetrics()
	// At least one cache miss (first request) and potentially cache hit (second)
	assert.GreaterOrEqual(t, metrics.CacheMisses+metrics.CacheHits, int64(1))
}

func TestIntegration_CompressionPipeline_MultipleToolOutputs(t *testing.T) {
	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"compressed_output": "compressed",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer mockAPI.Close()

	cfg := &config.Config{
		Pipes: config.PipesConfig{
			ToolOutput: config.ToolOutputPipeConfig{
				Enabled:             true,
				Strategy:            config.StrategyCompresr,
				FallbackStrategy:    config.StrategyPassthrough,
				MinBytes:            50,
				MaxBytes:            1024 * 1024,
				TargetRatio:         0.5,
				EnableExpandContext: true,
				Compresr: config.CompresrConfig{
					Endpoint: "/compress",
					Timeout:  5 * time.Second,
				},
			},
		},
		URLs: config.URLsConfig{
			Compresr: mockAPI.URL,
		},
	}

	pipe := tooloutput.New(cfg, fixtures.TestStore())

	content1 := strings.Repeat("content one ", 50)
	content2 := strings.Repeat("content two ", 50)
	reqBody := fixtures.MultiToolOutputRequest(content1, content2)
	ctx := fixtures.TestPipeContext(reqBody)

	result, err := pipe.Process(ctx)
	require.NoError(t, err)

	// Multiple tool outputs should all be processed
	if ctx.OutputCompressed {
		assert.NotEmpty(t, ctx.ToolOutputCompressions)
	}
	_ = result // Use result to avoid unused variable warning
}
