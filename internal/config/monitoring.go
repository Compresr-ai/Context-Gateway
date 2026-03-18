// Monitoring configuration - telemetry and logging settings.
package config

// MonitoringConfig contains all monitoring settings.
type MonitoringConfig struct {
	// Logging settings
	LogLevel  string `yaml:"log_level"`  // debug, info, warn, error
	LogFormat string `yaml:"log_format"` // json, console
	LogOutput string `yaml:"log_output"` // stdout, stderr, or file path

	// Telemetry settings
	TelemetryEnabled bool   `yaml:"telemetry_enabled"` // Enable telemetry tracking
	TelemetryPath    string `yaml:"telemetry_path"`    // Path to telemetry JSONL file
	LogToStdout      bool   `yaml:"log_to_stdout"`     // Also log telemetry to stdout
	VerbosePayloads  bool   `yaml:"verbose_payloads"`  // Log full request/response payloads

	// Additional log files
	CompressionLogPath     string `yaml:"compression_log_path"`      // Log original vs compressed
	ToolDiscoveryLogPath   string `yaml:"tool_discovery_log_path"`   // Log tool discovery filtering details
	TaskOutputLogPath      string `yaml:"task_output_log_path"`      // Base path for task/subagent output logs (per-provider)
	SessionToolsPath       string `yaml:"session_tools_path"`        // Human-readable JSON catalog of all tools seen in the session
	SessionStatsPath       string `yaml:"session_stats_path"`        // Live session_stats.json snapshot (rewritten every ~3s)
	ExpandContextCallsPath string `yaml:"expand_context_calls_path"` // JSONL log of expand_context calls (original + compressed content)

	// Trajectory logging (ATIF format)
	TrajectoryEnabled bool   `yaml:"trajectory_enabled"` // Enable trajectory logging
	TrajectoryPath    string `yaml:"trajectory_path"`    // Path to trajectory.json file
	AgentName         string `yaml:"agent_name"`         // Agent name for trajectory metadata
}
