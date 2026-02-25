// Package config loads and validates the gateway configuration.
//
// DESIGN: All configuration MUST come from YAML files. No defaults.
// This ensures explicit, auditable configuration for production deployments.
//
// FILES:
//   - config.go:     Root Config struct, Load(), Validate()
//   - pipes.go:      Pipe configs, compression thresholds, strategies
//   - monitoring.go: Logging and telemetry settings
package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration for the Context Gateway.
// All fields are required - no defaults are applied.
type Config struct {
	Server      ServerConfig      `yaml:"server"`       // HTTP server settings
	URLs        URLsConfig        `yaml:"urls"`         // Upstream URLs
	Providers   ProvidersConfig   `yaml:"providers"`    // LLM provider configurations
	Pipes       PipesConfig       `yaml:"pipes"`        // Compression pipelines
	Store       StoreConfig       `yaml:"store"`        // Shadow context store
	Monitoring  MonitoringConfig  `yaml:"monitoring"`   // Telemetry and logging
	Preemptive  PreemptiveConfig  `yaml:"preemptive"`   // Preemptive summarization settings
	Bedrock     BedrockConfig     `yaml:"bedrock"`      // AWS Bedrock support (opt-in)
	CostControl CostControlConfig `yaml:"cost_control"` // Cost control (session/global budget enforcement)

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
	Gateway  string `yaml:"gateway"`  // Gateway's own URL (for external access)
	Compresr string `yaml:"compresr"` // Compresr platform URL - not used in current release
}

// StoreConfig contains shadow context store settings.
type StoreConfig struct {
	Type string        `yaml:"type"` // Store type: "memory"
	TTL  time.Duration `yaml:"ttl"`  // Time-to-live for entries
}

// expandEnvWithDefaults expands environment variables with support for default values.
// Supports both ${VAR} and ${VAR:-default} syntax.
func expandEnvWithDefaults(s string) string {
	// Pattern matches ${VAR:-default} or ${VAR}
	re := regexp.MustCompile(`\$\{([^}:]+)(?::-([^}]*))?\}`)

	return re.ReplaceAllStringFunc(s, func(match string) string {
		// Extract variable name and default value
		parts := re.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}

		varName := parts[1]
		defaultValue := ""
		if len(parts) > 2 {
			defaultValue = parts[2]
		}

		// Get environment variable value
		if value := os.Getenv(varName); value != "" {
			return value
		}

		// Return default if provided, otherwise empty string
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
	cfg.applyEnvOverrides()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

// ExpandEnvWithDefaults expands environment variables with support for default values.
// Exported for use by agent config parsing.
func ExpandEnvWithDefaults(s string) string {
	return expandEnvWithDefaults(s)
}

// applyEnvOverrides applies environment variable overrides to the config.
// This allows external systems (Harbor, Daytona) to redirect log paths
// without modifying the base config files.
func (c *Config) applyEnvOverrides() {
	c.ApplySessionEnvOverrides()
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
	if c.Server.ReadTimeout == 0 {
		return fmt.Errorf("server.read_timeout is required")
	}
	if c.Server.WriteTimeout == 0 {
		return fmt.Errorf("server.write_timeout is required")
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
