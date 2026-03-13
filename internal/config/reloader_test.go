package config_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/compresr/context-gateway/internal/config"
)

// minimalYAML is a valid config YAML with all pipes and optional features disabled.
// Used as the base fixture for hot-reload tests.
const minimalYAML = `
server:
  port: 19999
  read_timeout: 30s
  write_timeout: 30s

urls:
  gateway: "http://localhost:19999"
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
`

// minimalYAMLWithThreshold is the same config but with a different trigger_threshold
// so tests can detect that the config changed.
const minimalYAMLUpdated = `
server:
  port: 19999
  read_timeout: 30s
  write_timeout: 30s

urls:
  gateway: "http://localhost:19999"
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
  enabled: true
  session_cap: 5.0
  global_cap: 10.0

monitoring:
  log_level: "off"
  log_format: "console"
  log_output: "stdout"
  telemetry_enabled: false
`

func loadTestConfig(t *testing.T, yaml string) *config.Config {
	t.Helper()
	cfg, err := config.LoadFromBytes([]byte(yaml))
	require.NoError(t, err)
	return cfg
}

// ---------------------------------------------------------------------------
// Reloader.Current
// ---------------------------------------------------------------------------

func TestReloader_Current_ReturnsInitialConfig(t *testing.T) {
	cfg := loadTestConfig(t, minimalYAML)
	r := config.NewReloader(cfg, "")
	assert.Equal(t, cfg, r.Current())
}

// ---------------------------------------------------------------------------
// Reloader.Update (API patch path)
// ---------------------------------------------------------------------------

func TestReloader_Update_AppliesPatch(t *testing.T) {
	cfg := loadTestConfig(t, minimalYAML)
	r := config.NewReloader(cfg, "")

	enabled := true
	cap := 42.0
	updated, err := r.Update(config.ConfigPatch{
		CostControl: &config.CostControlPatch{
			Enabled:   &enabled,
			GlobalCap: &cap,
		},
	})
	require.NoError(t, err)
	assert.True(t, updated.CostControl.Enabled)
	assert.InDelta(t, 42.0, updated.CostControl.GlobalCap, 0.001)
	// Current() reflects the change
	assert.True(t, r.Current().CostControl.Enabled)
}

func TestReloader_Update_NotifiesSubscribers(t *testing.T) {
	cfg := loadTestConfig(t, minimalYAML)
	r := config.NewReloader(cfg, "")

	var got *config.Config
	r.Subscribe(func(c *config.Config) { got = c })

	enabled := true
	_, err := r.Update(config.ConfigPatch{
		CostControl: &config.CostControlPatch{Enabled: &enabled},
	})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.CostControl.Enabled)
}

func TestReloader_Update_DoesNotPersistWhenNoFilePath(t *testing.T) {
	// No file path → should succeed without trying to write to disk.
	cfg := loadTestConfig(t, minimalYAML)
	r := config.NewReloader(cfg, "")
	enabled := true
	_, err := r.Update(config.ConfigPatch{
		CostControl: &config.CostControlPatch{Enabled: &enabled},
	})
	require.NoError(t, err)
}

func TestReloader_Update_PersistsToFile(t *testing.T) {
	f := writeTempConfig(t, minimalYAML)
	cfg := loadTestConfig(t, minimalYAML)
	r := config.NewReloader(cfg, f)

	enabled := true
	cap := 7.5
	_, err := r.Update(config.ConfigPatch{
		CostControl: &config.CostControlPatch{Enabled: &enabled, GlobalCap: &cap},
	})
	require.NoError(t, err)

	// File should now contain the updated config — reload from disk and check.
	raw, err := os.ReadFile(f)
	require.NoError(t, err)
	fromDisk, err := config.LoadFromBytes(raw)
	require.NoError(t, err)
	assert.True(t, fromDisk.CostControl.Enabled)
	assert.InDelta(t, 7.5, fromDisk.CostControl.GlobalCap, 0.001)
}

// ---------------------------------------------------------------------------
// Reloader.WatchFile (file-watch path)
// ---------------------------------------------------------------------------

func TestReloader_WatchFile_NoOpsWithoutFilePath(t *testing.T) {
	cfg := loadTestConfig(t, minimalYAML)
	r := config.NewReloader(cfg, "")

	// Should return immediately without blocking when filePath is empty.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		r.WatchFile(ctx, 10*time.Millisecond)
		close(done)
	}()
	// WatchFile returns immediately (no-op) since filePath is "".
	select {
	case <-done:
	case <-time.After(200*time.Millisecond):
		t.Fatal("WatchFile should return immediately with empty filePath")
	}
}

func TestReloader_WatchFile_StopsOnContextCancel(t *testing.T) {
	f := writeTempConfig(t, minimalYAML)
	cfg := loadTestConfig(t, minimalYAML)
	r := config.NewReloader(cfg, f)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		r.WatchFile(ctx, 20*time.Millisecond)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(500*time.Millisecond):
		t.Fatal("WatchFile should stop when context is cancelled")
	}
}

func TestReloader_WatchFile_DetectsFileChange(t *testing.T) {
	f := writeTempConfig(t, minimalYAML)
	cfg := loadTestConfig(t, minimalYAML)
	r := config.NewReloader(cfg, f)

	// Track subscriber calls.
	var mu sync.Mutex
	var received []*config.Config
	r.Subscribe(func(c *config.Config) {
		mu.Lock()
		received = append(received, c)
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.WatchFile(ctx, 20*time.Millisecond)

	// Wait a tick so WatchFile records the initial mtime.
	time.Sleep(40 * time.Millisecond)

	// Overwrite the file with updated config (different cost_control).
	// Use a small sleep to ensure the mtime changes (filesystem granularity).
	time.Sleep(20 * time.Millisecond)
	err := os.WriteFile(f, []byte(minimalYAMLUpdated), 0o600)
	require.NoError(t, err)

	// Wait for the watcher to pick it up (up to 500ms).
	assert.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) > 0
	}, 500*time.Millisecond, 10*time.Millisecond, "subscriber should be called after file change")

	mu.Lock()
	latest := received[len(received)-1]
	mu.Unlock()

	assert.True(t, latest.CostControl.Enabled, "updated config should have cost_control.enabled=true")
	assert.InDelta(t, 10.0, latest.CostControl.GlobalCap, 0.001)

	// Current() should also reflect the new config.
	assert.True(t, r.Current().CostControl.Enabled)
}

func TestReloader_WatchFile_IgnoresInvalidYAML(t *testing.T) {
	f := writeTempConfig(t, minimalYAML)
	cfg := loadTestConfig(t, minimalYAML)
	r := config.NewReloader(cfg, f)

	var callCount int
	r.Subscribe(func(*config.Config) { callCount++ })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.WatchFile(ctx, 20*time.Millisecond)

	time.Sleep(40 * time.Millisecond)

	// Write invalid YAML — watcher should log a warning but NOT update config.
	time.Sleep(20 * time.Millisecond)
	err := os.WriteFile(f, []byte("not: valid: yaml: :::"), 0o600)
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)

	assert.Equal(t, 0, callCount, "subscriber should not be called on invalid YAML")
	// Config should remain unchanged.
	assert.False(t, r.Current().CostControl.Enabled)
}

func TestReloader_WatchFile_DoesNotFireOnUnchangedFile(t *testing.T) {
	f := writeTempConfig(t, minimalYAML)
	cfg := loadTestConfig(t, minimalYAML)
	r := config.NewReloader(cfg, f)

	var callCount int
	r.Subscribe(func(*config.Config) { callCount++ })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.WatchFile(ctx, 20*time.Millisecond)

	// Wait several poll cycles without touching the file.
	time.Sleep(150 * time.Millisecond)

	assert.Equal(t, 0, callCount, "subscriber should not fire when file has not changed")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}
