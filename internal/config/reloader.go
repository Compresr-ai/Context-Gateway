// Package config - hot-reload mechanism for gateway configuration.
//
// DESIGN: The Reloader provides thread-safe config updates with partial patches.
// Uses pointer fields (nil = unchanged) for surgical updates without requiring
// the full config. Persists changes via atomic write (temp file + rename).
package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/costcontrol"
	"github.com/compresr/context-gateway/internal/pipes"
	"gopkg.in/yaml.v3"
)

// ConfigPatch represents a partial configuration update.
// Nil fields are left unchanged.
type ConfigPatch struct {
	Preemptive    *PreemptivePatch    `json:"preemptive,omitempty"`
	Pipes         *PipesPatch         `json:"pipes,omitempty"`
	CostControl   *CostControlPatch   `json:"cost_control,omitempty"`
	Notifications *NotificationsPatch `json:"notifications,omitempty"`
	Monitoring    *MonitoringPatch    `json:"monitoring,omitempty"`
}

// PreemptivePatch is a partial update for preemptive summarization config.
type PreemptivePatch struct {
	Enabled          *bool    `json:"enabled,omitempty"`
	TriggerThreshold *float64 `json:"trigger_threshold,omitempty"`
	Strategy         *string  `json:"strategy,omitempty"`
}

// PipesPatch is a partial update for pipe configs.
type PipesPatch struct {
	ToolOutput    *ToolOutputPatch    `json:"tool_output,omitempty"`
	ToolDiscovery *ToolDiscoveryPatch `json:"tool_discovery,omitempty"`
}

// ToolOutputPatch is a partial update for tool output pipe config.
type ToolOutputPatch struct {
	Enabled                *bool    `json:"enabled,omitempty"`
	Strategy               *string  `json:"strategy,omitempty"`
	MinBytes               *int     `json:"min_bytes,omitempty"`
	TargetCompressionRatio *float64 `json:"target_compression_ratio,omitempty"`
}

// ToolDiscoveryPatch is a partial update for tool discovery pipe config.
type ToolDiscoveryPatch struct {
	Enabled        *bool    `json:"enabled,omitempty"`
	Strategy       *string  `json:"strategy,omitempty"`
	MinTools       *int     `json:"min_tools,omitempty"`
	MaxTools       *int     `json:"max_tools,omitempty"`
	TargetRatio    *float64 `json:"target_ratio,omitempty"`
	SearchFallback *bool    `json:"search_fallback,omitempty"`
}

// CostControlPatch is a partial update for cost control config.
type CostControlPatch struct {
	Enabled    *bool    `json:"enabled,omitempty"`
	SessionCap *float64 `json:"session_cap,omitempty"`
	GlobalCap  *float64 `json:"global_cap,omitempty"`
}

// NotificationsPatch is a partial update for notifications config.
type NotificationsPatch struct {
	Slack *SlackPatch `json:"slack,omitempty"`
}

// SlackPatch is a partial update for Slack notification config.
type SlackPatch struct {
	Enabled    *bool   `json:"enabled,omitempty"`
	WebhookURL *string `json:"webhook_url,omitempty"`
}

// MonitoringPatch is a partial update for monitoring config.
type MonitoringPatch struct {
	TelemetryEnabled *bool `json:"telemetry_enabled,omitempty"`
}

// Reloader provides thread-safe config reading and hot-reload updates.
type Reloader struct {
	mu          sync.RWMutex
	config      *Config
	filePath    string
	subscribers []func(*Config)
}

// NewReloader creates a Reloader with the given initial config and file path.
func NewReloader(cfg *Config, filePath string) *Reloader {
	if filePath != "" {
		if abs, err := filepath.Abs(filepath.Clean(filePath)); err == nil {
			filePath = abs
		}
	}
	return &Reloader{
		config:   cfg,
		filePath: filePath,
	}
}

// Current returns the current config (thread-safe).
func (r *Reloader) Current() *Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config
}

// Subscribe registers a callback that is called whenever config is updated.
func (r *Reloader) Subscribe(fn func(*Config)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subscribers = append(r.subscribers, fn)
}

// Update applies a partial patch to the current config, validates, persists, and notifies subscribers.
func (r *Reloader) Update(patch ConfigPatch) (*Config, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Deep copy current config to apply patch
	updated := *r.config

	// Apply preemptive patch
	if patch.Preemptive != nil {
		if patch.Preemptive.Enabled != nil {
			updated.Preemptive.Enabled = *patch.Preemptive.Enabled
		}
		if patch.Preemptive.TriggerThreshold != nil {
			updated.Preemptive.TriggerThreshold = *patch.Preemptive.TriggerThreshold
		}
		if patch.Preemptive.Strategy != nil {
			updated.Preemptive.Summarizer.Strategy = *patch.Preemptive.Strategy
		}
	}

	// Apply pipes patch
	if patch.Pipes != nil {
		if patch.Pipes.ToolOutput != nil {
			p := patch.Pipes.ToolOutput
			if p.Enabled != nil {
				updated.Pipes.ToolOutput.Enabled = *p.Enabled
			}
			if p.Strategy != nil {
				updated.Pipes.ToolOutput.Strategy = *p.Strategy
			}
			if p.MinBytes != nil {
				updated.Pipes.ToolOutput.MinBytes = *p.MinBytes
			}
			if p.TargetCompressionRatio != nil {
				updated.Pipes.ToolOutput.TargetCompressionRatio = *p.TargetCompressionRatio
			}
		}
		if patch.Pipes.ToolDiscovery != nil {
			p := patch.Pipes.ToolDiscovery
			if p.Enabled != nil {
				updated.Pipes.ToolDiscovery.Enabled = *p.Enabled
			}
			if p.Strategy != nil {
				updated.Pipes.ToolDiscovery.Strategy = *p.Strategy
			}
			if p.MinTools != nil {
				updated.Pipes.ToolDiscovery.MinTools = *p.MinTools
			}
			if p.MaxTools != nil {
				updated.Pipes.ToolDiscovery.MaxTools = *p.MaxTools
			}
			if p.TargetRatio != nil {
				updated.Pipes.ToolDiscovery.TargetRatio = *p.TargetRatio
			}
			if p.SearchFallback != nil {
				updated.Pipes.ToolDiscovery.EnableSearchFallback = *p.SearchFallback
			}
		}
	}

	// Apply cost control patch
	if patch.CostControl != nil {
		if patch.CostControl.Enabled != nil {
			updated.CostControl.Enabled = *patch.CostControl.Enabled
		}
		if patch.CostControl.SessionCap != nil {
			updated.CostControl.SessionCap = *patch.CostControl.SessionCap
		}
		if patch.CostControl.GlobalCap != nil {
			updated.CostControl.GlobalCap = *patch.CostControl.GlobalCap
		}
	}

	// Apply notifications patch
	if patch.Notifications != nil && patch.Notifications.Slack != nil {
		if patch.Notifications.Slack.Enabled != nil {
			updated.Notifications.Slack.Enabled = *patch.Notifications.Slack.Enabled
		}
		if patch.Notifications.Slack.WebhookURL != nil {
			updated.Notifications.Slack.WebhookURL = *patch.Notifications.Slack.WebhookURL
		}
	}

	// Apply monitoring patch
	if patch.Monitoring != nil {
		if patch.Monitoring.TelemetryEnabled != nil {
			updated.Monitoring.TelemetryEnabled = *patch.Monitoring.TelemetryEnabled
		}
	}

	// Validate
	if err := updated.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config after patch: %w", err)
	}

	// Persist to file (atomic: write temp + rename)
	if r.filePath != "" {
		if err := r.persistToFile(&updated); err != nil {
			return nil, fmt.Errorf("failed to persist config: %w", err)
		}
	}

	r.config = &updated

	// Notify subscribers (outside lock would be ideal but simpler this way for safety)
	for _, fn := range r.subscribers {
		fn(&updated)
	}

	return &updated, nil
}

// persistToFile writes config to YAML using atomic write (temp file + rename).
func (r *Reloader) persistToFile(cfg *Config) error {
	data, err := ToYAML(cfg)
	if err != nil {
		return err
	}

	// Resolve the canonical target path by evaluating the parent directory's real path.
	// Using filepath.EvalSymlinks on the directory (not the file, which may not exist yet)
	// ensures the destination path is fully resolved and free of symlink traversal.
	cleanFilePath := filepath.Clean(r.filePath)
	realDir, err := filepath.EvalSymlinks(filepath.Dir(cleanFilePath))
	if err != nil {
		return fmt.Errorf("invalid config directory: %w", err)
	}
	target := filepath.Join(realDir, filepath.Base(cleanFilePath))

	// Create temp file in os.TempDir() so tmpPath is not derived from user-supplied filePath.
	// This ensures the path used for cleanup (os.Remove) is from a trusted source.
	tmp, err := os.CreateTemp("", ".config-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// ToYAML serializes the config to YAML bytes.
func ToYAML(cfg *Config) ([]byte, error) {
	// Create a serializable copy that excludes runtime-only fields
	type yamlConfig struct {
		Server        ServerConfig                  `yaml:"server"`
		URLs          URLsConfig                    `yaml:"urls"`
		Providers     ProvidersConfig               `yaml:"providers"`
		Pipes         pipes.Config                  `yaml:"pipes"`
		Store         StoreConfig                   `yaml:"store"`
		Monitoring    MonitoringConfig              `yaml:"monitoring"`
		Preemptive    PreemptiveConfig              `yaml:"preemptive"`
		Bedrock       BedrockConfig                 `yaml:"bedrock"`
		CostControl   costcontrol.CostControlConfig `yaml:"cost_control"`
		Notifications NotificationsConfig           `yaml:"notifications"`
	}

	out := yamlConfig{
		Server:        cfg.Server,
		URLs:          cfg.URLs,
		Providers:     cfg.Providers,
		Pipes:         cfg.Pipes,
		Store:         cfg.Store,
		Monitoring:    cfg.Monitoring,
		Preemptive:    cfg.Preemptive,
		Bedrock:       cfg.Bedrock,
		CostControl:   cfg.CostControl,
		Notifications: cfg.Notifications,
	}

	data, err := yaml.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config to YAML: %w", err)
	}
	return data, nil
}

// WatchFile polls the config file for modifications and reloads when changed.
// Blocks until ctx is cancelled — call in a goroutine.
// interval is the polling frequency; 0 defaults to 3 seconds.
// No-ops immediately if filePath was not set on the Reloader.
func (r *Reloader) WatchFile(ctx context.Context, interval time.Duration) {
	if r.filePath == "" {
		return
	}
	if interval <= 0 {
		interval = 3 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	lastMod := r.fileMod()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			mod := r.fileMod()
			if mod.IsZero() || mod.Equal(lastMod) {
				continue
			}
			lastMod = mod
			if err := r.reloadFromFile(); err != nil {
				log.Warn().Err(err).Str("path", r.filePath).Msg("config watch: reload failed")
			} else {
				log.Info().Str("path", r.filePath).Msg("config reloaded from file")
			}
		}
	}
}

// fileMod returns the modification time of the config file, or zero on error.
func (r *Reloader) fileMod() time.Time {
	info, err := os.Stat(r.filePath)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// reloadFromFile reads the config file, parses it, and notifies subscribers.
// Called when WatchFile detects a file modification.
func (r *Reloader) reloadFromFile() error {
	data, err := os.ReadFile(r.filePath) //#nosec G304 -- filePath is set at startup from a trusted CLI arg, cleaned via filepath.Abs in NewReloader
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	newCfg, err := LoadFromBytes(data)
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.config = newCfg
	for _, fn := range r.subscribers {
		fn(newCfg)
	}
	return nil
}
