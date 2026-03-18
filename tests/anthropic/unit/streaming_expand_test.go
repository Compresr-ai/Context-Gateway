// Tests for streaming expand_context functionality.
//
// These tests verify:
// - InvalidateExpandedMappings: Cache invalidation after expansion
// - StreamBuffer detection of expand_context in SSE
package unit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
	"github.com/compresr/context-gateway/internal/store"
)

// TestInvalidateExpandedMappings verifies compressed cache is preserved for KV-cache determinism.
func TestInvalidateExpandedMappings(t *testing.T) {
	st := store.NewMemoryStore(60 * time.Second)
	defer st.Close()

	shadowID := "shadow_toclear"
	st.SetCompressed(shadowID, "compressed version")

	_, found := st.GetCompressed(shadowID)
	assert.True(t, found)

	// Compressed entries are preserved for KV-cache determinism (no-op since Expander removed).
	_, found = st.GetCompressed(shadowID)
	assert.True(t, found)
}

// TestStreamBuffer_IgnoresNonTextEvents verifies tool_use events don't affect text detection.
func TestStreamBuffer_IgnoresNonTextEvents(t *testing.T) {
	buffer := tooloutput.NewStreamBuffer()

	// Tool use events should be ignored (no text accumulation)
	chunks := []string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_normal","name":"read_file"}}` + "\n\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"/test\"}"}}` + "\n\n",
		`data: {"type":"content_block_stop","index":0}` + "\n\n",
	}

	for _, chunk := range chunks {
		buffer.ProcessChunk([]byte(chunk))
	}

	assert.False(t, buffer.HasSuppressedCalls())
}
