package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/config"
)

// minimalTestConfig returns a minimal *config.Config suitable for unit tests.
// All optional pipes and features are disabled to avoid external dependencies.
func minimalTestConfig() *config.Config {
	cfg, err := config.LoadFromBytes([]byte(`
server:
  port: 19998
  read_timeout: 30s
  write_timeout: 30s
urls:
  gateway: "http://localhost:19998"
  compresr: "https://api.compresr.ai"
store:
  type: memory
  ttl: 1h
pipes:
  tool_output:
    enabled: false
  tool_discovery:
    enabled: false
preemptive:
  enabled: false
cost_control:
  enabled: false
  session_cap: 0
  global_cap: 0
monitoring:
  log_level: "off"
  log_format: "console"
  log_output: "stdout"
  telemetry_enabled: false
`))
	if err != nil {
		panic("minimalTestConfig: " + err.Error())
	}
	return cfg
}

// newTestGateway builds a minimal Gateway with only the configReloader wired up.
// It does NOT start a server — use g.handleConfigAPI directly in tests.
func newTestGateway() *Gateway {
	cfg := minimalTestConfig()
	return &Gateway{
		config:         cfg,
		configReloader: config.NewReloader(cfg, ""),
	}
}

// ---------------------------------------------------------------------------
// GET /api/config
// ---------------------------------------------------------------------------

func TestHandleGetConfig_ReturnsCurrentConfig(t *testing.T) {
	g := newTestGateway()

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	g.handleConfigAPI(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var resp configResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.False(t, resp.CostControl.Enabled)
}

func TestHandleGetConfig_ForbiddenFromNonLoopback(t *testing.T) {
	g := newTestGateway()

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.RemoteAddr = "203.0.113.1:12345" // non-loopback
	rec := httptest.NewRecorder()

	g.handleConfigAPI(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// ---------------------------------------------------------------------------
// PATCH /api/config — basic functionality
// ---------------------------------------------------------------------------

func TestHandlePatchConfig_UpdatesCostControl(t *testing.T) {
	g := newTestGateway()

	enabled := true
	cap := 25.0
	patch := config.ConfigPatch{
		CostControl: &config.CostControlPatch{
			Enabled:   &enabled,
			GlobalCap: &cap,
		},
	}
	body, err := json.Marshal(patch)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	g.handleConfigAPI(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp configResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.True(t, resp.CostControl.Enabled)
	assert.InDelta(t, 25.0, resp.CostControl.GlobalCap, 0.001)
}

func TestHandlePatchConfig_UpdatesPreemptive(t *testing.T) {
	g := newTestGateway()

	threshold := 90.0
	patch := config.ConfigPatch{
		Preemptive: &config.PreemptivePatch{
			TriggerThreshold: &threshold,
		},
	}
	body, _ := json.Marshal(patch)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	g.handleConfigAPI(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp configResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.InDelta(t, 90.0, resp.Preemptive.TriggerThreshold, 0.001)
}

func TestHandlePatchConfig_InvalidJSONReturns400(t *testing.T) {
	g := newTestGateway()

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewReader([]byte("not json")))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	g.handleConfigAPI(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandlePatchConfig_ForbiddenFromNonLoopback(t *testing.T) {
	g := newTestGateway()

	enabled := true
	body, _ := json.Marshal(config.ConfigPatch{
		CostControl: &config.CostControlPatch{Enabled: &enabled},
	})
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewReader(body))
	req.RemoteAddr = "10.0.0.1:12345"
	rec := httptest.NewRecorder()

	g.handleConfigAPI(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandleConfigAPI_MethodNotAllowed(t *testing.T) {
	g := newTestGateway()

	req := httptest.NewRequest(http.MethodDelete, "/api/config", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	g.handleConfigAPI(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

// ---------------------------------------------------------------------------
// Hot-reload: PATCH /api/config → g.cfg() reflects new values
// ---------------------------------------------------------------------------

func TestHotReload_CfgReflectsAPIUpdate(t *testing.T) {
	g := newTestGateway()

	// Before patch: cost control off.
	assert.False(t, g.cfg().CostControl.Enabled)

	enabled := true
	cap := 50.0
	patch := config.ConfigPatch{
		CostControl: &config.CostControlPatch{Enabled: &enabled, GlobalCap: &cap},
	}
	body, _ := json.Marshal(patch)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	g.handleConfigAPI(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	// After patch: g.cfg() must return the live value without restart.
	assert.True(t, g.cfg().CostControl.Enabled, "g.cfg() should return updated config after PATCH")
	assert.InDelta(t, 50.0, g.cfg().CostControl.GlobalCap, 0.001)
}

func TestHotReload_MultiplePatches(t *testing.T) {
	g := newTestGateway()

	sendPatch := func(patch config.ConfigPatch) {
		body, _ := json.Marshal(patch)
		req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1"
		rec := httptest.NewRecorder()
		g.handleConfigAPI(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
	}

	// Patch 1: enable cost control with cap=10
	enabled := true
	cap1 := 10.0
	sendPatch(config.ConfigPatch{
		CostControl: &config.CostControlPatch{Enabled: &enabled, GlobalCap: &cap1},
	})
	assert.True(t, g.cfg().CostControl.Enabled)
	assert.InDelta(t, 10.0, g.cfg().CostControl.GlobalCap, 0.001)

	// Patch 2: raise cap to 20 (enabled stays true from patch 1)
	cap2 := 20.0
	sendPatch(config.ConfigPatch{
		CostControl: &config.CostControlPatch{GlobalCap: &cap2},
	})
	assert.True(t, g.cfg().CostControl.Enabled, "enabled should persist across patches")
	assert.InDelta(t, 20.0, g.cfg().CostControl.GlobalCap, 0.001)
}

func TestHotReload_SubscriberCalledOnAPIUpdate(t *testing.T) {
	g := newTestGateway()

	var notified bool
	g.configReloader.Subscribe(func(*config.Config) { notified = true })

	enabled := true
	body, _ := json.Marshal(config.ConfigPatch{
		CostControl: &config.CostControlPatch{Enabled: &enabled},
	})
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1"
	g.handleConfigAPI(httptest.NewRecorder(), req)

	assert.True(t, notified, "subscriber registered with configReloader should be called on PATCH")
}
