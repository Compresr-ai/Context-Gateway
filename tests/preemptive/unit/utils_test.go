package preemptive_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/preemptive"
)

// =============================================================================
// ComputeSessionID
// =============================================================================

func TestComputeSessionID_ValidRequest(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"messages": [
			{"role": "user", "content": "Hello, how are you?"},
			{"role": "assistant", "content": "I'm fine!"},
			{"role": "user", "content": "Tell me a joke"}
		]
	}`)

	id := preemptive.ComputeSessionID(body)
	assert.NotEmpty(t, id)
	assert.Len(t, id, 16) // SHA256 hex, first 16 chars
}

func TestComputeSessionID_Deterministic(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"consistent task"}]}`)

	id1 := preemptive.ComputeSessionID(body)
	id2 := preemptive.ComputeSessionID(body)
	assert.Equal(t, id1, id2, "same input should produce same session ID")
}

func TestComputeSessionID_DifferentContent(t *testing.T) {
	body1 := []byte(`{"messages":[{"role":"user","content":"task A"}]}`)
	body2 := []byte(`{"messages":[{"role":"user","content":"task B"}]}`)

	id1 := preemptive.ComputeSessionID(body1)
	id2 := preemptive.ComputeSessionID(body2)
	assert.NotEqual(t, id1, id2, "different content should produce different session IDs")
}

func TestComputeSessionID_EmptyBody(t *testing.T) {
	id := preemptive.ComputeSessionID([]byte{})
	assert.Empty(t, id)
}

func TestComputeSessionID_InvalidJSON(t *testing.T) {
	id := preemptive.ComputeSessionID([]byte("not json"))
	assert.Empty(t, id)
}

func TestComputeSessionID_NoMessages(t *testing.T) {
	body := []byte(`{"model": "claude-sonnet-4-20250514"}`)
	id := preemptive.ComputeSessionID(body)
	assert.Empty(t, id)
}

func TestComputeSessionID_EmptyMessages(t *testing.T) {
	body := []byte(`{"messages":[]}`)
	id := preemptive.ComputeSessionID(body)
	assert.Empty(t, id)
}

func TestComputeSessionID_NoUserMessage(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":"hello"}]}`)
	id := preemptive.ComputeSessionID(body)
	assert.Empty(t, id)
}

func TestComputeSessionID_UsesFirstUserMessage(t *testing.T) {
	// Same first user message, different later messages → same ID
	body1 := []byte(`{"messages":[
		{"role":"user","content":"task"},
		{"role":"assistant","content":"response A"}
	]}`)
	body2 := []byte(`{"messages":[
		{"role":"user","content":"task"},
		{"role":"assistant","content":"response B"},
		{"role":"user","content":"follow up"}
	]}`)

	id1 := preemptive.ComputeSessionID(body1)
	id2 := preemptive.ComputeSessionID(body2)
	assert.Equal(t, id1, id2, "session ID should be based on first user message only")
}

// =============================================================================
// ParseMessages
// =============================================================================

func TestParseMessages_Valid(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`)
	msgs, err := preemptive.ParseMessages(body)
	require.NoError(t, err)
	assert.Len(t, msgs, 2)
}

func TestParseMessages_EmptyMessages(t *testing.T) {
	body := []byte(`{"messages":[]}`)
	msgs, err := preemptive.ParseMessages(body)
	require.NoError(t, err)
	assert.Len(t, msgs, 0)
}

func TestParseMessages_NoMessagesField(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-20250514"}`)
	msgs, err := preemptive.ParseMessages(body)
	require.NoError(t, err)
	assert.Nil(t, msgs)
}

func TestParseMessages_InvalidJSON(t *testing.T) {
	_, err := preemptive.ParseMessages([]byte("bad json"))
	assert.Error(t, err)
}

// =============================================================================
// ExtractText
// =============================================================================

func TestExtractText_String(t *testing.T) {
	result := preemptive.ExtractText("hello world")
	assert.Equal(t, "hello world", result)
}

func TestExtractText_Nil(t *testing.T) {
	result := preemptive.ExtractText(nil)
	assert.Empty(t, result)
}

func TestExtractText_ContentBlocks(t *testing.T) {
	// Simulate Anthropic content blocks as []interface{}
	blocks := []interface{}{
		map[string]interface{}{"type": "text", "text": "hello "},
		map[string]interface{}{"type": "text", "text": "world"},
	}
	result := preemptive.ExtractText(blocks)
	assert.Contains(t, result, "hello")
	assert.Contains(t, result, "world")
}

// =============================================================================
// ExtractContentString
// =============================================================================

func TestExtractContentString_String(t *testing.T) {
	result := preemptive.ExtractContentString("simple text")
	assert.Equal(t, "simple text", result)
}

func TestExtractContentString_Nil(t *testing.T) {
	result := preemptive.ExtractContentString(nil)
	assert.Empty(t, result)
}

func TestExtractContentString_ContentBlocks(t *testing.T) {
	blocks := []interface{}{
		map[string]interface{}{"type": "text", "text": "block1"},
		map[string]interface{}{"type": "tool_use", "name": "read_file"},
	}
	result := preemptive.ExtractContentString(blocks)
	assert.Contains(t, result, "block1")
}

// =============================================================================
// JoinNonEmpty
// =============================================================================

func TestJoinNonEmpty(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
		sep   string
		want  string
	}{
		{name: "all non-empty", parts: []string{"a", "b", "c"}, sep: ", ", want: "a, b, c"},
		{name: "with empty strings", parts: []string{"a", "", "c"}, sep: ", ", want: "a, c"},
		{name: "all empty", parts: []string{"", "", ""}, sep: ", ", want: ""},
		{name: "single element", parts: []string{"only"}, sep: ", ", want: "only"},
		{name: "nil slice", parts: nil, sep: ", ", want: ""},
		{name: "empty slice", parts: []string{}, sep: ", ", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := preemptive.JoinNonEmpty(tt.parts, tt.sep)
			assert.Equal(t, tt.want, got)
		})
	}
}

// =============================================================================
// WithDefaults
// =============================================================================

func TestWithDefaults_AppliesDefaults(t *testing.T) {
	cfg := preemptive.WithDefaults(preemptive.Config{})

	assert.Equal(t, 90*time.Second, cfg.PendingJobTimeout)
	assert.Equal(t, 2*time.Minute, cfg.SyncTimeout)
}

func TestWithDefaults_PreservesExisting(t *testing.T) {
	cfg := preemptive.WithDefaults(preemptive.Config{
		PendingJobTimeout: 30 * time.Second,
		SyncTimeout:       1 * time.Minute,
	})

	assert.Equal(t, 30*time.Second, cfg.PendingJobTimeout)
	assert.Equal(t, 1*time.Minute, cfg.SyncTimeout)
}

// =============================================================================
// FormatMessages
// =============================================================================

func TestFormatMessages(t *testing.T) {
	msgs := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"Hello"}`),
		json.RawMessage(`{"role":"assistant","content":"Hi there!"}`),
	}

	result := preemptive.FormatMessages(msgs)
	assert.Contains(t, result, "user")
	assert.Contains(t, result, "Hello")
	assert.Contains(t, result, "assistant")
}

func TestFormatMessages_Empty(t *testing.T) {
	result := preemptive.FormatMessages(nil)
	assert.Empty(t, result)
}
