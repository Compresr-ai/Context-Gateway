// Package config - hot-reload mechanism for gateway configuration.
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

// IsEmpty returns true if no fields are set in the patch.
func (p ConfigPatch) IsEmpty() bool {
	return p.Preemptive == nil && p.Pipes == nil && p.CostControl == nil &&
		p.Notifications == nil && p.Monitoring == nil
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
	MinTokens              *int     `json:"min_tokens,omitempty"`
	TargetCompressionRatio *float64 `json:"target_compression_ratio,omitempty"`
}

// ToolDiscoveryPatch is a partial update for tool discovery pipe config.
type ToolDiscoveryPatch struct {
	Enabled                 *bool                         `json:"enabled,omitempty"`
	Strategy                *string                       `json:"strategy,omitempty"`
	TokenThreshold          *int                          `json:"token_threshold,omitempty"`
	SearchResultCompression *SearchResultCompressionPatch `json:"search_result_compression,omitempty"` // Deprecated: use SchemaCompression
	SchemaCompression       *SchemaCompressionPatch       `json:"schema_compression,omitempty"`
}

// SearchResultCompressionPatch is a partial update for search result compression config.
// Deprecated: use SchemaCompressionPatch instead.
type SearchResultCompressionPatch struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// SchemaCompressionPatch is a partial update for per-tool schema compression config (Stage 2).
type SchemaCompressionPatch struct {
	Enabled        *bool   `json:"enabled,omitempty"`
	TokenThreshold *int    `json:"token_threshold,omitempty"`
	Parallel       *bool   `json:"parallel,omitempty"`
	MaxConcurrent  *int    `json:"max_concurrent,omitempty"`
	Model          *string `json:"model,omitempty"`
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
// Maintains two config layers: base (persisted) and session overrides (in-memory).
type Reloader struct {
	mu               sync.RWMutex
	baseConfig       *Config     // global config from file — source of truth for persistence
	sessionOverrides ConfigPatch // in-memory session deltas, never persisted
	config           *Config     // effective = base + session overrides (cached)
	filePath         string
	subscribers      []func(*Config)
}

// NewReloader creates a Reloader with the given initial config and file path.
func NewReloader(cfg *Config, filePath string) *Reloader {
	if filePath != "" {
		if abs, err := filepath.Abs(filepath.Clean(filePath)); err == nil {
			filePath = abs
		}
	}
	// Initially base and effective are identical (no session overrides yet)
	baseCopy := *cfg
	return &Reloader{
		baseConfig: &baseCopy,
		config:     cfg,
		filePath:   filePath,
	}
}

// Current returns the effective config: base + session overrides (thread-safe).
func (r *Reloader) Current() *Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config
}

// SessionOverrides returns the current session overrides (thread-safe).
func (r *Reloader) SessionOverrides() ConfigPatch {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessionOverrides
}

// Subscribe registers a callback that is called whenever the effective config changes.
func (r *Reloader) Subscribe(fn func(*Config)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subscribers = append(r.subscribers, fn)
}

// Update applies a patch to the global base config, persists to file, recomputes
// the effective config (preserving session overrides), and notifies subscribers.
func (r *Reloader) Update(patch ConfigPatch) (*Config, error) {
	r.mu.Lock()

	// Apply patch to base config
	updated := *r.baseConfig
	applyPatchToConfig(&updated, patch)
	updated.applyDefaults() // ensure defaults (e.g., always-enabled pipes) are consistent

	if err := updated.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config after patch: %w", err)
	}

	// Persist base config to file (atomic)
	if r.filePath != "" {
		if err := r.persistToFile(&updated); err != nil {
			return nil, fmt.Errorf("failed to persist config: %w", err)
		}
	}

	r.baseConfig = &updated

	// Recompute effective = base + session overrides
	effective := r.computeEffective()
	r.config = effective

	// Copy subscribers, then release lock before invoking callbacks.
	// Subscribers may call r.Current() (which acquires RLock), so holding
	// the write lock here would deadlock (RWMutex is not reentrant).
	subs := make([]func(*Config), len(r.subscribers))
	copy(subs, r.subscribers)
	r.mu.Unlock()

	for _, fn := range subs {
		fn(effective)
	}
	return effective, nil
}

// UpdateSession applies a patch as a session-only override. Changes take effect
// immediately via hot-reload but are NOT persisted — they reset on restart.
func (r *Reloader) UpdateSession(patch ConfigPatch) (*Config, error) {
	r.mu.Lock()

	// Accumulate into session overrides
	mergePatch(&r.sessionOverrides, patch)

	// Recompute effective = base + session overrides
	effective := r.computeEffective()

	if err := effective.Validate(); err != nil {
		r.mu.Unlock()
		return nil, fmt.Errorf("invalid config after session patch: %w", err)
	}

	r.config = effective

	// Copy subscribers, then release lock before invoking callbacks.
	// Subscribers may call r.Current() (which acquires RLock), so holding
	// the write lock here would deadlock (RWMutex is not reentrant).
	subs := make([]func(*Config), len(r.subscribers))
	copy(subs, r.subscribers)
	r.mu.Unlock()

	for _, fn := range subs {
		fn(effective)
	}
	return effective, nil
}

// ResetSession clears all session overrides, reverting to the global base config.
func (r *Reloader) ResetSession() *Config {
	r.mu.Lock()

	r.sessionOverrides = ConfigPatch{}

	effective := r.computeEffective()
	r.config = effective

	// Copy subscribers, then release lock before invoking callbacks.
	// Subscribers may call r.Current() (which acquires RLock), so holding
	// the write lock here would deadlock (RWMutex is not reentrant).
	subs := make([]func(*Config), len(r.subscribers))
	copy(subs, r.subscribers)
	r.mu.Unlock()

	for _, fn := range subs {
		fn(effective)
	}
	return effective
}

// computeEffective returns base + session overrides. Must be called under write lock.
func (r *Reloader) computeEffective() *Config {
	effective := *r.baseConfig
	if !r.sessionOverrides.IsEmpty() {
		applyPatchToConfig(&effective, r.sessionOverrides)
	}
	return &effective
}

// applyPatchToConfig applies a ConfigPatch to a Config in-place.
func applyPatchToConfig(cfg *Config, patch ConfigPatch) {
	if patch.Preemptive != nil {
		if patch.Preemptive.Enabled != nil {
			cfg.Preemptive.Enabled = *patch.Preemptive.Enabled
		}
		if patch.Preemptive.TriggerThreshold != nil {
			cfg.Preemptive.TriggerThreshold = *patch.Preemptive.TriggerThreshold
		}
		if patch.Preemptive.Strategy != nil {
			cfg.Preemptive.Summarizer.Strategy = *patch.Preemptive.Strategy
		}
	}

	if patch.Pipes != nil {
		if patch.Pipes.ToolOutput != nil {
			p := patch.Pipes.ToolOutput
			if p.Enabled != nil {
				cfg.Pipes.ToolOutput.Enabled = *p.Enabled
			}
			if p.Strategy != nil {
				cfg.Pipes.ToolOutput.Strategy = *p.Strategy
			}
			if p.MinTokens != nil {
				cfg.Pipes.ToolOutput.MinTokens = *p.MinTokens
			}
			if p.TargetCompressionRatio != nil {
				cfg.Pipes.ToolOutput.TargetCompressionRatio = *p.TargetCompressionRatio
			}
		}
		if patch.Pipes.ToolDiscovery != nil {
			p := patch.Pipes.ToolDiscovery
			if p.Enabled != nil {
				cfg.Pipes.ToolDiscovery.Enabled = *p.Enabled
			}
			if p.Strategy != nil {
				cfg.Pipes.ToolDiscovery.Strategy = *p.Strategy
			}
			if p.TokenThreshold != nil {
				cfg.Pipes.ToolDiscovery.TokenThreshold = *p.TokenThreshold
			}
			if p.SearchResultCompression != nil {
				src := p.SearchResultCompression
				if src.Enabled != nil {
					cfg.Pipes.ToolDiscovery.SearchResultCompression.Enabled = *src.Enabled
				}
			}
			// Stage 2: per-tool schema compression
			if p.SchemaCompression != nil {
				sc := p.SchemaCompression
				if sc.Enabled != nil {
					cfg.Pipes.ToolDiscovery.SchemaCompression.Enabled = *sc.Enabled
				}
				if sc.TokenThreshold != nil {
					cfg.Pipes.ToolDiscovery.SchemaCompression.TokenThreshold = *sc.TokenThreshold
				}
				if sc.Parallel != nil {
					cfg.Pipes.ToolDiscovery.SchemaCompression.Parallel = *sc.Parallel
				}
				if sc.MaxConcurrent != nil {
					cfg.Pipes.ToolDiscovery.SchemaCompression.MaxConcurrent = *sc.MaxConcurrent
				}
				if sc.Model != nil {
					cfg.Pipes.ToolDiscovery.SchemaCompression.Model = *sc.Model
				}
			}
		}
	}

	if patch.CostControl != nil {
		if patch.CostControl.Enabled != nil {
			cfg.CostControl.Enabled = *patch.CostControl.Enabled
		}
		if patch.CostControl.SessionCap != nil {
			cfg.CostControl.SessionCap = *patch.CostControl.SessionCap
		}
		if patch.CostControl.GlobalCap != nil {
			cfg.CostControl.GlobalCap = *patch.CostControl.GlobalCap
		}
	}

	if patch.Notifications != nil && patch.Notifications.Slack != nil {
		if patch.Notifications.Slack.Enabled != nil {
			cfg.Notifications.Slack.Enabled = *patch.Notifications.Slack.Enabled
		}
		if patch.Notifications.Slack.WebhookURL != nil {
			cfg.Notifications.Slack.WebhookURL = *patch.Notifications.Slack.WebhookURL
		}
	}

	if patch.Monitoring != nil {
		if patch.Monitoring.TelemetryEnabled != nil {
			cfg.Monitoring.TelemetryEnabled = *patch.Monitoring.TelemetryEnabled
		}
	}
}

// mergePatch accumulates src into dst. Non-nil fields in src overwrite dst.
func mergePatch(dst *ConfigPatch, src ConfigPatch) {
	if src.Preemptive != nil {
		if dst.Preemptive == nil {
			dst.Preemptive = &PreemptivePatch{}
		}
		if src.Preemptive.Enabled != nil {
			dst.Preemptive.Enabled = src.Preemptive.Enabled
		}
		if src.Preemptive.TriggerThreshold != nil {
			dst.Preemptive.TriggerThreshold = src.Preemptive.TriggerThreshold
		}
		if src.Preemptive.Strategy != nil {
			dst.Preemptive.Strategy = src.Preemptive.Strategy
		}
	}

	if src.Pipes != nil {
		if dst.Pipes == nil {
			dst.Pipes = &PipesPatch{}
		}
		if src.Pipes.ToolOutput != nil {
			if dst.Pipes.ToolOutput == nil {
				dst.Pipes.ToolOutput = &ToolOutputPatch{}
			}
			if src.Pipes.ToolOutput.Enabled != nil {
				dst.Pipes.ToolOutput.Enabled = src.Pipes.ToolOutput.Enabled
			}
			if src.Pipes.ToolOutput.Strategy != nil {
				dst.Pipes.ToolOutput.Strategy = src.Pipes.ToolOutput.Strategy
			}
			if src.Pipes.ToolOutput.MinTokens != nil {
				dst.Pipes.ToolOutput.MinTokens = src.Pipes.ToolOutput.MinTokens
			}
			if src.Pipes.ToolOutput.TargetCompressionRatio != nil {
				dst.Pipes.ToolOutput.TargetCompressionRatio = src.Pipes.ToolOutput.TargetCompressionRatio
			}
		}
		if src.Pipes.ToolDiscovery != nil {
			if dst.Pipes.ToolDiscovery == nil {
				dst.Pipes.ToolDiscovery = &ToolDiscoveryPatch{}
			}
			if src.Pipes.ToolDiscovery.Enabled != nil {
				dst.Pipes.ToolDiscovery.Enabled = src.Pipes.ToolDiscovery.Enabled
			}
			if src.Pipes.ToolDiscovery.Strategy != nil {
				dst.Pipes.ToolDiscovery.Strategy = src.Pipes.ToolDiscovery.Strategy
			}
			if src.Pipes.ToolDiscovery.TokenThreshold != nil {
				dst.Pipes.ToolDiscovery.TokenThreshold = src.Pipes.ToolDiscovery.TokenThreshold
			}
		}
	}

	if src.CostControl != nil {
		if dst.CostControl == nil {
			dst.CostControl = &CostControlPatch{}
		}
		if src.CostControl.Enabled != nil {
			dst.CostControl.Enabled = src.CostControl.Enabled
		}
		if src.CostControl.SessionCap != nil {
			dst.CostControl.SessionCap = src.CostControl.SessionCap
		}
		if src.CostControl.GlobalCap != nil {
			dst.CostControl.GlobalCap = src.CostControl.GlobalCap
		}
	}

	if src.Notifications != nil {
		if dst.Notifications == nil {
			dst.Notifications = &NotificationsPatch{}
		}
		if src.Notifications.Slack != nil {
			if dst.Notifications.Slack == nil {
				dst.Notifications.Slack = &SlackPatch{}
			}
			if src.Notifications.Slack.Enabled != nil {
				dst.Notifications.Slack.Enabled = src.Notifications.Slack.Enabled
			}
			if src.Notifications.Slack.WebhookURL != nil {
				dst.Notifications.Slack.WebhookURL = src.Notifications.Slack.WebhookURL
			}
		}
	}

	if src.Monitoring != nil {
		if dst.Monitoring == nil {
			dst.Monitoring = &MonitoringPatch{}
		}
		if src.Monitoring.TelemetryEnabled != nil {
			dst.Monitoring.TelemetryEnabled = src.Monitoring.TelemetryEnabled
		}
	}
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
		PostSession   PostSessionConfig             `yaml:"post_session"`
		Dashboard     DashboardConfig               `yaml:"dashboard"`
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
		PostSession:   cfg.PostSession,
		Dashboard:     cfg.Dashboard,
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

// reloadFromFile reads the config file, updates baseConfig, recomputes effective
// config (preserving session overrides), and notifies subscribers.
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
	r.baseConfig = newCfg
	effective := r.computeEffective()
	r.config = effective
	subs := make([]func(*Config), len(r.subscribers))
	copy(subs, r.subscribers)
	r.mu.Unlock()

	for _, fn := range subs {
		fn(effective)
	}
	return nil
}
