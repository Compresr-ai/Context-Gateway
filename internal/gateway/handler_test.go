package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// sanitizeModelName
// ---------------------------------------------------------------------------

func TestSanitizeModelName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantModel string
	}{
		{
			name:      "anthropic prefix stripped",
			input:     `{"model":"anthropic/claude-3-sonnet","messages":[]}`,
			wantModel: "claude-3-sonnet",
		},
		{
			name:      "openai prefix stripped",
			input:     `{"model":"openai/gpt-4","messages":[]}`,
			wantModel: "gpt-4",
		},
		{
			name:      "google prefix stripped",
			input:     `{"model":"google/gemini-pro","messages":[]}`,
			wantModel: "gemini-pro",
		},
		{
			name:      "meta prefix stripped",
			input:     `{"model":"meta/llama-3","messages":[]}`,
			wantModel: "llama-3",
		},
		{
			name:      "no prefix unchanged",
			input:     `{"model":"claude-3-sonnet","messages":[]}`,
			wantModel: "claude-3-sonnet",
		},
		{
			name:      "unknown prefix unchanged",
			input:     `{"model":"mistral/mixtral","messages":[]}`,
			wantModel: "mistral/mixtral",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeModelName([]byte(tt.input))
			var parsed map[string]interface{}
			err := json.Unmarshal(result, &parsed)
			require.NoError(t, err)
			assert.Equal(t, tt.wantModel, parsed["model"])
		})
	}
}

func TestSanitizeModelName_NoModelField(t *testing.T) {
	input := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	result := sanitizeModelName(input)
	assert.Equal(t, input, result)
}

func TestSanitizeModelName_InvalidJSON(t *testing.T) {
	input := []byte(`not json at all`)
	result := sanitizeModelName(input)
	assert.Equal(t, input, result)
}

func TestSanitizeModelName_EmptyBody(t *testing.T) {
	input := []byte(``)
	result := sanitizeModelName(input)
	assert.Equal(t, input, result)
}

// ---------------------------------------------------------------------------
// mergeForwardAuthMeta
// ---------------------------------------------------------------------------

func TestMergeForwardAuthMeta_NilDst(t *testing.T) {
	// Should not panic
	mergeForwardAuthMeta(nil, forwardAuthMeta{InitialMode: "key"})
}

func TestMergeForwardAuthMeta_CopiesNonEmpty(t *testing.T) {
	dst := &forwardAuthMeta{}
	src := forwardAuthMeta{
		InitialMode:   "key",
		EffectiveMode: "oauth",
		FallbackUsed:  true,
	}
	mergeForwardAuthMeta(dst, src)
	assert.Equal(t, "key", dst.InitialMode)
	assert.Equal(t, "oauth", dst.EffectiveMode)
	assert.True(t, dst.FallbackUsed)
}

func TestMergeForwardAuthMeta_DoesNotOverwriteWithEmpty(t *testing.T) {
	dst := &forwardAuthMeta{
		InitialMode:   "key",
		EffectiveMode: "oauth",
	}
	src := forwardAuthMeta{} // all zero values
	mergeForwardAuthMeta(dst, src)
	assert.Equal(t, "key", dst.InitialMode)
	assert.Equal(t, "oauth", dst.EffectiveMode)
	assert.False(t, dst.FallbackUsed)
}

func TestMergeForwardAuthMeta_FallbackSticksTrue(t *testing.T) {
	dst := &forwardAuthMeta{FallbackUsed: true}
	src := forwardAuthMeta{FallbackUsed: false}
	mergeForwardAuthMeta(dst, src)
	// FallbackUsed should stay true (only sets to true, never resets)
	assert.True(t, dst.FallbackUsed)
}

// ---------------------------------------------------------------------------
// writeError (requires a Gateway instance)
// ---------------------------------------------------------------------------

func TestWriteError(t *testing.T) {
	g := &Gateway{}
	rec := httptest.NewRecorder()

	g.writeError(rec, "something went wrong", http.StatusBadRequest)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var body map[string]interface{}
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	require.NoError(t, err)

	errObj, ok := body["error"].(map[string]interface{})
	require.True(t, ok, "error should be an object")
	assert.Equal(t, "something went wrong", errObj["message"])
	assert.Equal(t, "gateway_error", errObj["type"])
}

func TestWriteError_InternalServerError(t *testing.T) {
	g := &Gateway{}
	rec := httptest.NewRecorder()

	g.writeError(rec, "internal error", http.StatusInternalServerError)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	var body map[string]interface{}
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	require.NoError(t, err)

	errObj, ok := body["error"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "internal error", errObj["message"])
}
