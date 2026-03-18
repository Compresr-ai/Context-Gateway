package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/compresr/context-gateway/internal/config"
)

// minimalConfig returns a Config that passes Validate().
func minimalConfig() *config.Config {
	cfg, err := config.LoadFromBytes([]byte(`
server:
  port: 18081
  read_timeout: 30s
  write_timeout: 1000s
urls:
  compresr: "https://api.compresr.ai"
store:
  type: memory
  ttl: 1h
providers:
  anthropic:
    api_key: "test-key"
    model: "claude-haiku-4-5"
pipes:
  tool_output:
    enabled: true
    strategy: compresr
    min_tokens: 512
    target_compression_ratio: 0.5
    compresr:
      endpoint: "https://api.compresr.ai/api/compress/tool-output/"
      api_key: "test"
      model: "hcc_espresso_v1"
      timeout: 30s
  tool_discovery:
    enabled: true
    strategy: relevance
preemptive:
  enabled: true
  trigger_threshold: 85.0
  summarizer:
    strategy: compresr
    model: "claude-haiku-4-5"
    max_tokens: 4096
    timeout: 60s
    compresr:
      endpoint: "https://api.compresr.ai/api/compress/history/"
      api_key: "test"
      model: "hcc_espresso_v1"
      timeout: 60s
  session:
    summary_ttl: 3h
    hash_message_count: 3
cost_control:
  enabled: true
  session_cap: 0
  global_cap: 10.0
monitoring:
  telemetry_enabled: false
`))
	if err != nil {
		panic("minimalConfig failed: " + err.Error())
	}
	return cfg
}

func TestReloaderCurrent(t *testing.T) {
	cfg := minimalConfig()
	r := config.NewReloader(cfg, "")

	got := r.Current()
	if got != cfg {
		t.Fatal("Current() should return the initial config")
	}
}

func TestReloaderUpdatePatchesCostControl(t *testing.T) {
	cfg := minimalConfig()
	r := config.NewReloader(cfg, "")

	newCap := 25.0
	updated, err := r.Update(config.ConfigPatch{
		CostControl: &config.CostControlPatch{
			GlobalCap: &newCap,
		},
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if updated.CostControl.GlobalCap != 25.0 {
		t.Fatalf("expected global_cap=25.0, got %f", updated.CostControl.GlobalCap)
	}
	// Original preemptive should be unchanged
	if updated.Preemptive.TriggerThreshold != 85.0 {
		t.Fatalf("expected trigger_threshold=85.0, got %f", updated.Preemptive.TriggerThreshold)
	}
}

func TestReloaderUpdatePatchesPreemptive(t *testing.T) {
	cfg := minimalConfig()
	r := config.NewReloader(cfg, "")

	threshold := 70.0
	disabled := false
	updated, err := r.Update(config.ConfigPatch{
		Preemptive: &config.PreemptivePatch{
			Enabled:          &disabled,
			TriggerThreshold: &threshold,
		},
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if updated.Preemptive.Enabled {
		t.Fatal("expected preemptive disabled")
	}
	if updated.Preemptive.TriggerThreshold != 70.0 {
		t.Fatalf("expected 70.0, got %f", updated.Preemptive.TriggerThreshold)
	}
}

func TestReloaderUpdatePatchesPreemptiveStrategy(t *testing.T) {
	cfg := minimalConfig()
	r := config.NewReloader(cfg, "")

	strategy := "external_provider"
	updated, err := r.Update(config.ConfigPatch{
		Preemptive: &config.PreemptivePatch{
			Strategy: &strategy,
		},
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if updated.Preemptive.Summarizer.Strategy != "external_provider" {
		t.Fatalf("expected strategy=external_provider, got %s", updated.Preemptive.Summarizer.Strategy)
	}
	// Other fields unchanged
	if !updated.Preemptive.Enabled {
		t.Fatal("expected preemptive still enabled")
	}
}

func TestReloaderUpdatePatchesPipes(t *testing.T) {
	cfg := minimalConfig()
	r := config.NewReloader(cfg, "")

	// Pipes are always enabled by design (applyDefaults forces Enabled=true).
	// Patching Enabled=false is overridden back to true on the next applyDefaults call.
	disabled := false
	updated, err := r.Update(config.ConfigPatch{
		Pipes: &config.PipesPatch{
			ToolOutput: &config.ToolOutputPatch{
				Enabled: &disabled,
			},
		},
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if !updated.Pipes.ToolOutput.Enabled {
		t.Fatal("expected tool_output enabled (pipes are always-on by design)")
	}
}

func TestReloaderSubscriberNotified(t *testing.T) {
	cfg := minimalConfig()
	r := config.NewReloader(cfg, "")

	notified := false
	r.Subscribe(func(c *config.Config) {
		notified = true
	})

	cap := 5.0
	_, err := r.Update(config.ConfigPatch{
		CostControl: &config.CostControlPatch{GlobalCap: &cap},
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if !notified {
		t.Fatal("subscriber was not notified")
	}
}

func TestReloaderPersistsToFile(t *testing.T) {
	cfg := minimalConfig()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "config.yaml")

	// Write initial file so reloader has a valid path
	initial, _ := config.ToYAML(cfg)
	if err := os.WriteFile(filePath, initial, 0600); err != nil {
		t.Fatal(err)
	}

	r := config.NewReloader(cfg, filePath)

	cap := 42.0
	_, err := r.Update(config.ConfigPatch{
		CostControl: &config.CostControlPatch{GlobalCap: &cap},
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Read back file and verify
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read persisted file: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("persisted file is empty")
	}

	// Load the persisted config and verify the patch was applied
	reloaded, err := config.LoadFromBytes(data)
	if err != nil {
		t.Fatalf("failed to reload persisted config: %v", err)
	}
	if reloaded.CostControl.GlobalCap != 42.0 {
		t.Fatalf("expected global_cap=42.0 in persisted file, got %f", reloaded.CostControl.GlobalCap)
	}
}

func TestReloaderNilPatchIsNoOp(t *testing.T) {
	cfg := minimalConfig()
	r := config.NewReloader(cfg, "")

	updated, err := r.Update(config.ConfigPatch{})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Everything should be unchanged
	if updated.CostControl.GlobalCap != cfg.CostControl.GlobalCap {
		t.Fatal("empty patch should not change config")
	}
}

func TestToYAML(t *testing.T) {
	cfg := minimalConfig()
	data, err := config.ToYAML(cfg)
	if err != nil {
		t.Fatalf("ToYAML failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("ToYAML returned empty data")
	}

	// Should be loadable
	reloaded, err := config.LoadFromBytes(data)
	if err != nil {
		t.Fatalf("failed to reload ToYAML output: %v", err)
	}
	if reloaded.Server.Port != cfg.Server.Port {
		t.Fatalf("port mismatch: %d vs %d", reloaded.Server.Port, cfg.Server.Port)
	}
}
