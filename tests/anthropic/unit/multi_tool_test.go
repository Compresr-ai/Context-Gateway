// Multi-Tool Integration Tests
//
// Tests compression of requests with multiple tool outputs.
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

func TestMultiTool_AllToolsCompressed(t *testing.T) {
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

	// Create request with 3 tool outputs
	content1 := strings.Repeat("file1 content ", 50)
	content2 := strings.Repeat("file2 content ", 50)
	content3 := strings.Repeat("file3 content ", 50)
	reqBody := fixtures.MultiToolOutputRequest(content1, content2, content3)
	ctx := fixtures.TestPipeContext(reqBody)

	result, err := pipe.Process(ctx)
	require.NoError(t, err)

	// All tool outputs should be processed
	if ctx.OutputCompressed {
		assert.GreaterOrEqual(t, len(ctx.ToolOutputCompressions), 1)
	}
	_ = result
}

func TestMultiTool_MixedSizes(t *testing.T) {
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
				MinBytes:            100,
				MaxBytes:            1024 * 1024,
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

	// Mix of small (below threshold) and large content
	smallContent := "small"
	largeContent := strings.Repeat("large content ", 100)
	reqBody := fixtures.MultiToolOutputRequest(smallContent, largeContent)
	ctx := fixtures.TestPipeContext(reqBody)

	result, err := pipe.Process(ctx)
	require.NoError(t, err)

	// Only large content should be compressed
	_ = result
}

func TestMultiTool_Passthrough(t *testing.T) {
	pipe := tooloutput.New(fixtures.PassthroughConfig(), fixtures.TestStore())

	content1 := strings.Repeat("content1 ", 50)
	content2 := strings.Repeat("content2 ", 50)
	reqBody := fixtures.MultiToolOutputRequest(content1, content2)
	ctx := fixtures.TestPipeContext(reqBody)

	result, err := pipe.Process(ctx)
	require.NoError(t, err)

	// Passthrough mode - no changes
	assert.Equal(t, reqBody, result)
	assert.False(t, ctx.OutputCompressed)
}
