package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/compresr/context-gateway/internal/adapters"
	"github.com/compresr/context-gateway/internal/auth"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// sanitizeModelName
// ---------------------------------------------------------------------------

func TestSanitizeModelName(t *testing.T) {
	tests := []struct {
		name      string
		input     string
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

func TestSetupRoutes_RegistersModelsAlias(t *testing.T) {
	g := &Gateway{}
	mux := http.NewServeMux()

	g.registerModelRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/models?client_version=0.114.0", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var body codexModelsResponse
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	require.NoError(t, err)
	assert.NotEmpty(t, body.Models)
}

func TestHandleModels_V1ReturnsOpenAICompatibleList(t *testing.T) {
	g := &Gateway{}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	g.handleModels(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var body modelsResponse
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	require.NoError(t, err)
	assert.Equal(t, "list", body.Object)
	assert.NotEmpty(t, body.Data)
}

func TestHandleModels_ProxiesUpstreamWhenAuthContextPresent(t *testing.T) {
	EnableLocalHostsForTesting()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/models", r.URL.Path)
		assert.Equal(t, "Bearer subscription-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.4"}]}`))
	}))
	defer upstream.Close()

	g := &Gateway{
		registry:     adapters.NewRegistry(),
		authRegistry: auth.NewRegistry(),
		httpClient:   upstream.Client(),
		configReloader: config.NewReloader(&config.Config{
			Server:  config.ServerConfig{Port: 18081},
			Bedrock: config.BedrockConfig{Enabled: false},
		}, ""),
	}

	req := httptest.NewRequest(http.MethodGet, "/models?client_version=0.114.0", nil)
	req.Header.Set("Authorization", "Bearer subscription-token")
	req.Header.Set(HeaderTargetURL, upstream.URL)
	rec := httptest.NewRecorder()

	g.handleModels(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"models":[{"slug":"gpt-5.4"}]}`, rec.Body.String())
}

func TestHandleModels_ChatGPTSubscriptionUsesSyntheticList(t *testing.T) {
	g := &Gateway{}

	req := httptest.NewRequest(http.MethodGet, "/models?client_version=0.114.0", nil)
	req.Header.Set("Authorization", "Bearer subscription-token")
	rec := httptest.NewRecorder()

	g.handleModels(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var body codexModelsResponse
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	require.NoError(t, err)
	assert.NotEmpty(t, body.Models)
}

func TestHandleProxy_WebSocketUpgradeProxiesResponses(t *testing.T) {
	EnableLocalHostsForTesting()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/responses", r.URL.Path)
		assert.Equal(t, "Bearer subscription-token", r.Header.Get("Authorization"))

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		require.NoError(t, err)
		defer func() { _ = conn.CloseNow() }()

		ctx := context.Background()
		msgType, payload, err := conn.Read(ctx)
		require.NoError(t, err)
		assert.Equal(t, websocket.MessageText, msgType)
		assert.Equal(t, "ping", string(payload))
		require.NoError(t, conn.Write(ctx, msgType, []byte("pong")))
	}))
	defer upstream.Close()

	g := &Gateway{}
	gatewayServer := httptest.NewServer(http.HandlerFunc(g.handleProxy))
	defer gatewayServer.Close()

	headers := http.Header{}
	headers.Set("Authorization", "Bearer subscription-token")
	headers.Set(HeaderTargetURL, upstream.URL)

	ctx := context.Background()
	conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(gatewayServer.URL, "http")+"/responses", &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte("ping")))
	msgType, payload, err := conn.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, websocket.MessageText, msgType)
	assert.Equal(t, "pong", string(payload))
}
