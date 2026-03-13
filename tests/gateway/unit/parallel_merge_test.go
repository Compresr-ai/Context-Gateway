// Parallel Pipe Merge Tests
//
// Verifies the mergeParallelResults function that combines outputs from
// tool_output (messages[]) and tool_discovery (tools[]) running in parallel.
package unit

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// mergeParallelResults is the same logic as in router.go — duplicated here
// for unit testing without importing the gateway package (which has many deps).
func mergeParallelResults(original, toBody []byte, toErr error, tdBody []byte, tdErr error) []byte {
	if toErr != nil && tdErr != nil {
		return original
	}
	if toErr != nil {
		return tdBody
	}
	if tdErr != nil {
		return toBody
	}
	toolsValue := gjson.GetBytes(tdBody, "tools")
	if !toolsValue.Exists() {
		result, err := sjson.DeleteBytes(toBody, "tools")
		if err != nil {
			return toBody
		}
		return result
	}
	result, err := sjson.SetRawBytes(toBody, "tools", []byte(toolsValue.Raw))
	if err != nil {
		return toBody
	}
	return result
}

// TestMerge_BothSucceed verifies correct merge when both pipes succeed.
func TestMerge_BothSucceed(t *testing.T) {
	original := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"original"}],"tools":[{"name":"t1"},{"name":"t2"},{"name":"t3"}]}`)

	// tool_output compressed messages but kept tools unchanged
	toBody := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"compressed"}],"tools":[{"name":"t1"},{"name":"t2"},{"name":"t3"}]}`)

	// tool_discovery filtered tools but kept messages unchanged
	tdBody := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"original"}],"tools":[{"name":"t1"}]}`)

	result := mergeParallelResults(original, toBody, nil, tdBody, nil)

	assert.True(t, json.Valid(result), "must be valid JSON")

	// Messages should come from tool_output (compressed)
	assert.Equal(t, "compressed", gjson.GetBytes(result, "messages.0.content").String())

	// Tools should come from tool_discovery (filtered)
	assert.Equal(t, int64(1), gjson.GetBytes(result, "tools.#").Int())
	assert.Equal(t, "t1", gjson.GetBytes(result, "tools.0.name").String())
}

// TestMerge_ToolOutputFails verifies graceful degradation when tool_output fails.
func TestMerge_ToolOutputFails(t *testing.T) {
	original := []byte(`{"model":"claude-3","messages":[{"content":"original"}],"tools":[{"name":"t1"},{"name":"t2"}]}`)
	tdBody := []byte(`{"model":"claude-3","messages":[{"content":"original"}],"tools":[{"name":"t1"}]}`)

	result := mergeParallelResults(original, nil, errors.New("compression API timeout"), tdBody, nil)

	// Should use tool_discovery result (tools filtered, messages unchanged)
	assert.Equal(t, int64(1), gjson.GetBytes(result, "tools.#").Int())
	assert.Equal(t, "original", gjson.GetBytes(result, "messages.0.content").String())
}

// TestMerge_ToolDiscoveryFails verifies graceful degradation when tool_discovery fails.
func TestMerge_ToolDiscoveryFails(t *testing.T) {
	original := []byte(`{"model":"claude-3","messages":[{"content":"original"}],"tools":[{"name":"t1"},{"name":"t2"}]}`)
	toBody := []byte(`{"model":"claude-3","messages":[{"content":"compressed"}],"tools":[{"name":"t1"},{"name":"t2"}]}`)

	result := mergeParallelResults(original, toBody, nil, nil, errors.New("scoring failed"))

	// Should use tool_output result (messages compressed, tools unchanged)
	assert.Equal(t, "compressed", gjson.GetBytes(result, "messages.0.content").String())
	assert.Equal(t, int64(2), gjson.GetBytes(result, "tools.#").Int())
}

// TestMerge_BothFail verifies passthrough when both pipes fail.
func TestMerge_BothFail(t *testing.T) {
	original := []byte(`{"model":"claude-3","messages":[{"content":"original"}],"tools":[{"name":"t1"}]}`)

	result := mergeParallelResults(original,
		nil, errors.New("tool_output failed"),
		nil, errors.New("tool_discovery failed"))

	assert.Equal(t, original, result, "must return original body unchanged")
}

// TestMerge_PreservesOtherFields verifies model, max_tokens, etc. survive the merge.
func TestMerge_PreservesOtherFields(t *testing.T) {
	original := []byte(`{"model":"claude-3","max_tokens":4096,"stream":true,"messages":[{"content":"hi"}],"tools":[{"name":"t1"},{"name":"t2"}]}`)

	toBody, _ := sjson.SetBytes(original, "messages.0.content", "compressed")
	tdBody, _ := sjson.SetRawBytes(original, "tools", []byte(`[{"name":"t1"}]`))

	result := mergeParallelResults(original, toBody, nil, tdBody, nil)

	require.True(t, json.Valid(result))
	assert.Equal(t, "claude-3", gjson.GetBytes(result, "model").String())
	assert.Equal(t, int64(4096), gjson.GetBytes(result, "max_tokens").Int())
	assert.Equal(t, true, gjson.GetBytes(result, "stream").Bool())
	assert.Equal(t, "compressed", gjson.GetBytes(result, "messages.0.content").String())
	assert.Equal(t, int64(1), gjson.GetBytes(result, "tools.#").Int())
}

// TestMerge_ToolDiscoveryRemovesAllTools verifies handling when tool_discovery
// removes all tools (e.g., tool-search strategy replaces with empty set).
func TestMerge_ToolDiscoveryRemovesAllTools(t *testing.T) {
	original := []byte(`{"model":"claude-3","messages":[],"tools":[{"name":"t1"}]}`)
	toBody := []byte(`{"model":"claude-3","messages":[],"tools":[{"name":"t1"}]}`)

	// tool_discovery produced a body without tools field
	tdBody := []byte(`{"model":"claude-3","messages":[]}`)

	result := mergeParallelResults(original, toBody, nil, tdBody, nil)

	// Tools should be removed
	assert.False(t, gjson.GetBytes(result, "tools").Exists(), "tools should be removed when tool_discovery removes them")
}
