package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/compresr/context-gateway/internal/config"
	"gopkg.in/yaml.v3"
)

// AgentConfig is the top-level agent YAML structure.
type AgentConfig struct {
	Agent AgentSpec `yaml:"agent"`
}

// AgentSpec defines an agent's properties.
type AgentSpec struct {
	Name         string        `yaml:"name"`
	DisplayName  string        `yaml:"display_name"`
	Description  string        `yaml:"description"`
	Models       []AgentModel  `yaml:"models"`
	DefaultModel string        `yaml:"default_model"`
	Environment  []AgentEnvVar `yaml:"environment"`
	Command      AgentCommand  `yaml:"command"`
}

// AgentModel defines a selectable model for agents like OpenClaw.
type AgentModel struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	Provider string `yaml:"provider"`
}

// AgentEnvVar defines an environment variable to export.
type AgentEnvVar struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

// AgentCommand defines how to check, run, and install the agent.
type AgentCommand struct {
	Check           string   `yaml:"check"`       // legacy: shell-style string
	CheckCmd        []string `yaml:"check_cmd"`   // preferred: executable + args
	Run             string   `yaml:"run"`         // executable name/path
	Args            []string `yaml:"args"`        // executable args
	Install         string   `yaml:"install"`     // legacy: shell-style string
	InstallCmd      []string `yaml:"install_cmd"` // preferred: executable + args
	FallbackMessage string   `yaml:"fallback_message"`
}

var shellMetaPattern = regexp.MustCompile(`[|&;<>()$` + "`" + `\n\r]`)

func parseLegacyCommand(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if shellMetaPattern.MatchString(trimmed) {
		return nil, fmt.Errorf("legacy shell command contains unsupported shell operators: %q", raw)
	}
	parts := strings.Fields(trimmed)
	if len(parts) == 0 {
		return nil, nil
	}
	return parts, nil
}

func normalizeCommandSpec(cmd AgentCommand) (AgentCommand, error) {
	if len(cmd.CheckCmd) == 0 && cmd.Check != "" {
		checkCmd, err := parseLegacyCommand(cmd.Check)
		if err != nil {
			return cmd, fmt.Errorf("invalid command.check: %w; use command.check_cmd instead", err)
		}
		cmd.CheckCmd = checkCmd
	}

	if len(cmd.InstallCmd) == 0 && cmd.Install != "" {
		installCmd, err := parseLegacyCommand(cmd.Install)
		if err != nil {
			return cmd, fmt.Errorf("invalid command.install: %w; use command.install_cmd instead", err)
		}
		cmd.InstallCmd = installCmd
	}

	if strings.TrimSpace(cmd.Run) == "" {
		return cmd, fmt.Errorf("agent.command.run is required")
	}
	if shellMetaPattern.MatchString(cmd.Run) {
		return cmd, fmt.Errorf("agent.command.run must be an executable, not a shell expression")
	}
	if strings.ContainsAny(cmd.Run, " \t") {
		return cmd, fmt.Errorf("agent.command.run must not include spaces; use command.args for arguments")
	}

	return cmd, nil
}

// parseAgentConfig parses agent YAML bytes into an AgentConfig.
// Environment variable references in values are expanded.
func parseAgentConfig(data []byte) (*AgentConfig, error) {
	// Expand env vars in the YAML before parsing
	expanded := config.ExpandEnvWithDefaults(string(data))

	var ac AgentConfig
	if err := yaml.Unmarshal([]byte(expanded), &ac); err != nil {
		return nil, fmt.Errorf("failed to parse agent config: %w", err)
	}

	if ac.Agent.Name == "" {
		return nil, fmt.Errorf("agent.name is required")
	}
	normalizedCmd, err := normalizeCommandSpec(ac.Agent.Command)
	if err != nil {
		return nil, err
	}
	ac.Agent.Command = normalizedCmd

	return &ac, nil
}

// loadAgentConfig loads an agent config by name.
// It checks filesystem locations in order of priority, then falls back to embedded.
func loadAgentConfig(name string) (*AgentConfig, []byte, error) {
	// Ensure no extension in name for lookup
	name = strings.TrimSuffix(name, ".yaml")

	// Check filesystem override locations
	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		overridePath := filepath.Join(homeDir, ".config", "context-gateway", "agents", name+".yaml")
		// #nosec G304,G703 -- path is constructed from internal agent override directory and normalized name
		if data, err := os.ReadFile(overridePath); err == nil {
			ac, err := parseAgentConfig(data)
			return ac, data, err
		}
	}

	// Check local agents directory
	localPath := filepath.Join("agents", name+".yaml")
	// #nosec G304,G703 -- path is constructed from local agents directory and normalized name
	if data, err := os.ReadFile(localPath); err == nil {
		ac, err := parseAgentConfig(data)
		return ac, data, err
	}

	// Fall back to embedded agent
	if data, err := getEmbeddedAgent(name); err == nil {
		ac, err := parseAgentConfig(data)
		return ac, data, err
	}

	return nil, nil, fmt.Errorf("agent '%s' not found", name)
}
