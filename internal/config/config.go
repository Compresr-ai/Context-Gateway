// Package config loads and validates the gateway configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/compresr/context-gateway/internal/costcontrol"
	"github.com/compresr/context-gateway/internal/postsession"
)

// PostSessionConfig is an alias for postsession.Config.
type PostSessionConfig = postsession.Config

// CostControlConfig is an alias for costcontrol.CostControlConfig.
type CostControlConfig = costcontrol.CostControlConfig

// Config is the root configuration for the Context Gateway.
// All fields are required - no defaults are applied.
type Config struct {
	Server        ServerConfig        `yaml:"server"`        // HTTP server settings
	URLs          URLsConfig          `yaml:"urls"`          // Upstream URLs
	Providers     ProvidersConfig     `yaml:"providers"`     // LLM provider configurations
	Pipes         PipesConfig         `yaml:"pipes"`         // Compression pipelines
	Store         StoreConfig         `yaml:"store"`         // Shadow context store
	Monitoring    MonitoringConfig    `yaml:"monitoring"`    // Telemetry and logging
	Preemptive    PreemptiveConfig    `yaml:"preemptive"`    // Preemptive summarization settings
	Bedrock       BedrockConfig       `yaml:"bedrock"`       // AWS Bedrock support (opt-in)
	CostControl   CostControlConfig   `yaml:"cost_control"`  // Cost control (session/global budget enforcement)
	Notifications NotificationsConfig `yaml:"notifications"` // Notification integrations (Slack, etc.)
	PostSession   PostSessionConfig   `yaml:"post_session"`  // Post-session CLAUDE.md updates
	Dashboard     DashboardConfig     `yaml:"dashboard"`     // Dashboard UI settings
	CompresrCreds CompresrCredsConfig `yaml:"compresr"`      // Centralized Compresr credentials (inherited by all pipes)

	// Runtime-only fields (not loaded from YAML)
	AgentFlags *AgentFlags `yaml:"-"` // Agent CLI flags, set at runtime by cmd/agent.go
}

// AgentFlags stores passthrough args from the gateway CLI.
// These are flags intended for the agent (Claude Code, Codex, etc.) that the
// gateway also needs to be aware of for behavior adjustments.
type AgentFlags struct {
	AgentName string   // Which agent these flags are for (e.g., "claude_code")
	Raw       []string // Original passthrough args (e.g., ["--dangerously-skip-permissions"])
}

// NewAgentFlags creates AgentFlags from passthrough args.
func NewAgentFlags(agentName string, args []string) *AgentFlags {
	if len(args) == 0 {
		return nil
	}
	return &AgentFlags{
		AgentName: agentName,
		Raw:       args,
	}
}

// HasFlag checks if a specific flag is present in the args.
func (f *AgentFlags) HasFlag(name string) bool {
	if f == nil {
		return false
	}
	for _, arg := range f.Raw {
		if arg == name {
			return true
		}
	}
	return false
}

// GetFlagValue returns the value of a flag (e.g., --model claude-3-opus → "claude-3-opus").
func (f *AgentFlags) GetFlagValue(name string) string {
	if f == nil {
		return ""
	}
	for i, arg := range f.Raw {
		// --flag value
		if arg == name && i+1 < len(f.Raw) {
			return f.Raw[i+1]
		}
		// --flag=value
		if strings.HasPrefix(arg, name+"=") {
			return arg[len(name)+1:]
		}
	}
	return ""
}

// IsAutoApproveMode returns true if the agent is running in auto-approve/skip-permissions mode.
// This is provider-agnostic: maps different agent flags to the same semantic behavior.
func (f *AgentFlags) IsAutoApproveMode() bool {
	if f == nil {
		return false
	}
	switch f.AgentName {
	case "claude_code":
		return f.HasFlag("--dangerously-skip-permissions") || f.HasFlag("-y")
	case "codex":
		// Codex uses --full-auto for autonomous mode
		return f.HasFlag("--full-auto")
	default:
		// Generic detection for common flags
		return f.HasFlag("--auto-approve") || f.HasFlag("-y")
	}
}

// BedrockConfig controls AWS Bedrock provider support.
// Bedrock support is disabled by default and must be explicitly enabled.
type BedrockConfig struct {
	Enabled bool `yaml:"enabled"` // Must be true to enable Bedrock provider detection and SigV4 signing
}

// ServerConfig contains HTTP server settings.
type ServerConfig struct {
	Port         int           `yaml:"port"`          // Port to listen on
	ReadTimeout  time.Duration `yaml:"read_timeout"`  // Max time to read request
	WriteTimeout time.Duration `yaml:"write_timeout"` // Max time to write response
}

// URLsConfig contains upstream URL configuration.
type URLsConfig struct {
	Compresr string `yaml:"compresr"` // Compresr platform URL (e.g., "https://api.compresr.ai")
}

// NotificationsConfig controls notification integrations.
type NotificationsConfig struct {
	Slack SlackConfig `yaml:"slack"` // Slack notification settings
}

// SlackConfig controls Slack notifications via Claude Code hooks.
type SlackConfig struct {
	Enabled    bool   `yaml:"enabled"`               // Whether Slack notifications are enabled
	WebhookURL string `yaml:"webhook_url,omitempty"` // Slack incoming webhook URL
}

// DashboardConfig controls the embedded dashboard UI.
type DashboardConfig struct {
	HiddenTabs         []string      `yaml:"hidden_tabs"`          // Tabs to hide from the dashboard UI (e.g., ["savings"])
	SessionIdleTimeout time.Duration `yaml:"session_idle_timeout"` // Inactivity window before heartbeat liveness check fires (default: 10m)
}

// StoreConfig contains shadow context store settings.
type StoreConfig struct {
	Type string        `yaml:"type"` // Store type: "memory"
	TTL  time.Duration `yaml:"ttl"`  // Time-to-live for entries
}

// envVarRe matches ${VAR:-default} and ${VAR} syntax.
// Compiled once at package level — this function is called on every config load and hot-reload.
var envVarRe = regexp.MustCompile(`\$\{([^}:]+)(?::-([^}]*))?\}`)

// expandEnvWithDefaults expands environment variables with support for default values.
// Supports both ${VAR} and ${VAR:-default} syntax.
func expandEnvWithDefaults(s string) string {
	return envVarRe.ReplaceAllStringFunc(s, func(match string) string {
		parts := envVarRe.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		varName := parts[1]
		defaultValue := ""
		if len(parts) > 2 {
			defaultValue = parts[2]
		}
		if value := os.Getenv(varName); value != "" {
			return value
		}
		return defaultValue
	})
}

// Load reads configuration from a YAML file.
// Returns an error if the file doesn't exist or is invalid.
func Load(path string) (*Config, error) {
	if path == "" {
		return nil, fmt.Errorf("config file path is required")
	}

	data, err := os.ReadFile(path) // #nosec G304 -- user-specified config path
	if err != nil {
		return nil, fmt.Errorf("failed to read config file '%s': %w", path, err)
	}

	return LoadFromBytes(data)
}

// LoadFromBytes parses configuration from raw YAML bytes.
// Supports ${VAR:-default} env var expansion, env overrides, and validation.
func LoadFromBytes(data []byte) (*Config, error) {
	// Expand environment variables (supports ${VAR:-default} syntax)
	expanded := expandEnvWithDefaults(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Apply environment variable overrides for telemetry paths
	// This allows Harbor/Daytona to redirect logs without modifying config files
	cfg.ApplySessionEnvOverrides()

	// Apply defaults for optional fields not present in YAML
	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

// applyDefaults sets sensible defaults for optional numeric fields that default
// to zero when absent from YAML but need a non-zero value to function correctly.
//
// PIPE PHILOSOPHY: All pipes are always enabled with passthrough as the default
// strategy. A config section is not required to activate a pipe — omitting it
// means "run with passthrough". Explicit config overrides the default.
func (c *Config) applyDefaults() {
	// TargetCompressionRatio: 0 means "unset" — apply the default.
	// This ensures consistent behaviour when the field is absent from older configs.
	if c.Pipes.ToolOutput.TargetCompressionRatio == 0 {
		c.Pipes.ToolOutput.TargetCompressionRatio = DefaultTargetCompressionRatio
	}

	// All pipes default to enabled=true with passthrough strategy.
	// - Strategy defaults to passthrough when absent from config (empty string).
	// - Enabled defaults to true when the pipe has no explicit config at all
	//   (i.e., Strategy was empty before defaulting — meaning the entire section
	//   was omitted). If a strategy was explicitly set, Enabled defaults to true
	//   as well (a strategy without enabled=true is almost certainly a mistake).
	// Explicit config can override with enabled: false to disable a pipe entirely.
	if c.Pipes.ToolOutput.Strategy == "" {
		c.Pipes.ToolOutput.Strategy = StrategyPassthrough
	}
	if !c.Pipes.ToolOutput.Enabled {
		c.Pipes.ToolOutput.Enabled = true
	}

	if c.Pipes.ToolDiscovery.Strategy == "" {
		c.Pipes.ToolDiscovery.Strategy = StrategyPassthrough
	}
	if !c.Pipes.ToolDiscovery.Enabled {
		c.Pipes.ToolDiscovery.Enabled = true
	}

	if c.Pipes.TaskOutput.Strategy == "" {
		c.Pipes.TaskOutput.Strategy = StrategyPassthrough
	}
	if !c.Pipes.TaskOutput.Enabled {
		c.Pipes.TaskOutput.Enabled = true
	}

	// Propagate top-level compresr credentials to per-pipe sections.
	c.applyCompresrFallbacks()
}

// applyCompresrFallbacks propagates the top-level CompresrCreds to all per-pipe
// compresr sections when the per-pipe api_key is absent.
//
// This allows configs to define COMPRESR_API_KEY once at the root instead of
// repeating it in tool_output, tool_discovery, and preemptive.summarizer sections.
// Per-pipe api_key values (when explicitly set) always take priority.
func (c *Config) applyCompresrFallbacks() {
	key := c.CompresrCreds.APIKey
	if key == "" {
		return
	}

	// Propagate to tool output pipe
	if c.Pipes.ToolOutput.Compresr.APIKey == "" {
		c.Pipes.ToolOutput.Compresr.APIKey = key
	}
	// Propagate to tool discovery pipe
	if c.Pipes.ToolDiscovery.Compresr.APIKey == "" {
		c.Pipes.ToolDiscovery.Compresr.APIKey = key
	}
	// Propagate to preemptive summarizer (only when compresr section exists)
	if c.Preemptive.Summarizer.Compresr != nil && c.Preemptive.Summarizer.Compresr.APIKey == "" {
		c.Preemptive.Summarizer.Compresr.APIKey = key
	}
}

// ExpandEnvWithDefaults expands environment variables with support for default values.
// Exported for use by agent config parsing.
func ExpandEnvWithDefaults(s string) string {
	return expandEnvWithDefaults(s)
}

// ApplySessionEnvOverrides applies SESSION_* environment variable overrides.
// Exported so agent.go can call it after setting session env vars.
func (c *Config) ApplySessionEnvOverrides() {
	// SESSION_TELEMETRY_LOG overrides the telemetry log path
	if envPath := os.Getenv("SESSION_TELEMETRY_LOG"); envPath != "" {
		c.Monitoring.TelemetryPath = envPath
	}

	// SESSION_COMPRESSION_LOG overrides the compression log path
	if envPath := os.Getenv("SESSION_COMPRESSION_LOG"); envPath != "" {
		c.Monitoring.CompressionLogPath = envPath
	}

	// SESSION_TOOL_DISCOVERY_LOG overrides the tool discovery log path
	if envPath := os.Getenv("SESSION_TOOL_DISCOVERY_LOG"); envPath != "" {
		c.Monitoring.ToolDiscoveryLogPath = envPath
	}

	// SESSION_TASK_OUTPUT_LOG overrides the task output log path
	if envPath := os.Getenv("SESSION_TASK_OUTPUT_LOG"); envPath != "" {
		c.Monitoring.TaskOutputLogPath = envPath
	}

	// SESSION_TRAJECTORY_LOG overrides the trajectory log path
	if envPath := os.Getenv("SESSION_TRAJECTORY_LOG"); envPath != "" {
		c.Monitoring.TrajectoryPath = envPath
		// Auto-enable trajectory logging if path is provided
		c.Monitoring.TrajectoryEnabled = true
	}

	// SESSION_COMPACTION_LOG overrides the preemptive compaction log path
	if envPath := os.Getenv("SESSION_COMPACTION_LOG"); envPath != "" {
		c.Preemptive.CompactionLogPath = envPath
	}

	// SESSION_TOOLS_LOG overrides the session tools catalog path
	if envPath := os.Getenv("SESSION_TOOLS_LOG"); envPath != "" {
		c.Monitoring.SessionToolsPath = envPath
	}

	// SESSION_STATS_LOG overrides the live session stats snapshot path
	if envPath := os.Getenv("SESSION_STATS_LOG"); envPath != "" {
		c.Monitoring.SessionStatsPath = envPath
	}

	// SESSION_EXPAND_CALLS_LOG overrides the expand_context_calls.jsonl path
	if envPath := os.Getenv("SESSION_EXPAND_CALLS_LOG"); envPath != "" {
		c.Monitoring.ExpandContextCallsPath = envPath
	}

	// Auto-derive ExpandContextCallsPath from CompressionLogPath when missing.
	// Handles stale configs that predate expand_context_calls_path.
	if c.Monitoring.ExpandContextCallsPath == "" && c.Monitoring.CompressionLogPath != "" {
		dir := filepath.Dir(c.Monitoring.CompressionLogPath)
		c.Monitoring.ExpandContextCallsPath = filepath.Join(dir, "expand_context_calls.jsonl")
	}

	// Auto-derive TaskOutputLogPath from CompressionLogPath when missing.
	// This handles stale configs generated before task_output_log_path was added,
	// so task output events are logged without requiring a config migration.
	// Example: "logs/session1/tool_output_compression.jsonl" → "logs/session1/task_output"
	if c.Monitoring.TaskOutputLogPath == "" && c.Monitoring.CompressionLogPath != "" {
		dir := filepath.Dir(c.Monitoring.CompressionLogPath)
		c.Monitoring.TaskOutputLogPath = filepath.Join(dir, "task_output")
	}

	// Apply monitoring.TaskOutputLogPath to pipes.task_output.log_file if not explicitly set.
	// This centralizes log path configuration in the monitoring section.
	if c.Pipes.TaskOutput.LogFile == "" && c.Monitoring.TaskOutputLogPath != "" {
		c.Pipes.TaskOutput.LogFile = c.Monitoring.TaskOutputLogPath
	}
}

// Validate checks if the configuration is valid.
func (c *Config) Validate() error {
	// Server validation
	if c.Server.Port == 0 {
		return fmt.Errorf("server.port is required")
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid server.port: %d (must be 1-65535)", c.Server.Port)
	}
	if c.Server.ReadTimeout <= 0 {
		return fmt.Errorf("server.read_timeout must be positive")
	}
	if c.Server.WriteTimeout <= 0 {
		return fmt.Errorf("server.write_timeout must be positive")
	}

	// Store validation
	if c.Store.Type == "" {
		return fmt.Errorf("store.type is required")
	}
	if c.Store.TTL == 0 {
		return fmt.Errorf("store.ttl is required")
	}

	// Providers validation (if defined)
	if c.Providers != nil {
		if err := c.Providers.Validate(); err != nil {
			return err
		}
	}

	// Pipe validations
	if err := c.Pipes.Validate(); err != nil {
		return err
	}

	// Preemptive summarization validation
	if err := c.Preemptive.Validate(); err != nil {
		return err
	}

	// Cost control validation
	if err := c.CostControl.Validate(); err != nil {
		return err
	}

	// Validate provider references
	if err := c.ValidateUsedProviders(); err != nil {
		return err
	}

	return nil
}
