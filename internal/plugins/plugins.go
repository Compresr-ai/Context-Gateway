// Package plugins handles installation of agent-specific plugins
// for OpenClaw, Claude Code, and other supported agents.
package plugins

import (
	"context"
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed openclaw/*
var openclawFiles embed.FS

//go:embed claudecode/*
var claudecodeFiles embed.FS

// EnsurePluginsInstalled checks if plugins for the given agent are installed,
// and installs them if not. Returns true if plugins were installed (first run).
func EnsurePluginsInstalled(agentName string) (bool, error) {
	switch agentName {
	case "openclaw":
		return ensureOpenClawPlugin()
	case "claude_code":
		return ensureClaudeCodePlugin()
	default:
		return false, nil
	}
}

// ensureOpenClawPlugin installs the Context Gateway plugin for OpenClaw
// Uses `openclaw plugins install` for proper provenance tracking
func ensureOpenClawPlugin() (bool, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false, fmt.Errorf("failed to get home directory: %w", err)
	}

	// Folder name matches manifest id "context-gateway"
	pluginDir := filepath.Join(homeDir, ".openclaw", "extensions", "context-gateway")
	manifestPath := filepath.Join(pluginDir, "openclaw.plugin.json")

	// Check if plugin already installed and up-to-date
	if _, statErr := os.Stat(manifestPath); statErr == nil {
		// Check version - reinstall if embedded version is newer
		installed, _ := os.ReadFile(manifestPath) // #nosec G304 -- manifestPath is constructed from known safe paths
		embedded, _ := openclawFiles.ReadFile("openclaw/openclaw.plugin.json")
		if len(installed) > 0 && len(embedded) > 0 && string(installed) == string(embedded) {
			return false, nil // Already installed and up-to-date
		}
		// Version mismatch - remove old plugin to trigger reinstall
		_ = os.RemoveAll(pluginDir)
	}

	// Create temp directory for plugin files
	tmpDir, err := os.MkdirTemp("", "context-gateway-plugin-*")
	if err != nil {
		return false, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Copy embedded files to temp dir
	files := []string{"package.json", "openclaw.plugin.json", "index.ts"}
	for _, f := range files {
		content, err := openclawFiles.ReadFile("openclaw/" + f)
		if err != nil {
			return false, fmt.Errorf("failed to read embedded %s: %w", f, err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, f), content, 0600); err != nil {
			return false, fmt.Errorf("failed to write %s: %w", f, err)
		}
	}

	// Install using openclaw plugins install (creates proper provenance)
	// Use a timeout since openclaw may hang if just installed and not yet initialized
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "openclaw", "plugins", "install", tmpDir) // #nosec G204 -- tmpDir is our temp directory
	cmd.Stdin = strings.NewReader("y\n")                                      // Auto-confirm prompts
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		// Fallback: manual install if openclaw command fails
		if err := os.MkdirAll(pluginDir, 0750); err != nil {
			return false, fmt.Errorf("failed to create plugin directory: %w", err)
		}
		for _, f := range files {
			content, _ := openclawFiles.ReadFile("openclaw/" + f)
			if err := os.WriteFile(filepath.Join(pluginDir, f), content, 0600); err != nil {
				return false, fmt.Errorf("failed to write %s: %w", f, err)
			}
		}
	}

	return true, nil
}

// ensureClaudeCodePlugin installs the /savings command for Claude Code
func ensureClaudeCodePlugin() (bool, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false, fmt.Errorf("failed to get home directory: %w", err)
	}

	commandsDir := filepath.Join(homeDir, ".claude", "commands")
	savingsPath := filepath.Join(commandsDir, "savings.md")

	// Check if command already installed
	if _, statErr := os.Stat(savingsPath); statErr == nil {
		return false, nil // Already installed
	}

	// Create commands directory
	if mkdirErr := os.MkdirAll(commandsDir, 0750); mkdirErr != nil {
		return false, fmt.Errorf("failed to create commands directory: %w", mkdirErr)
	}

	// Read embedded savings.md
	content, err := claudecodeFiles.ReadFile("claudecode/savings.md")
	if err != nil {
		return false, fmt.Errorf("failed to read embedded savings.md: %w", err)
	}

	// Write savings command
	if err := os.WriteFile(savingsPath, content, 0600); err != nil {
		return false, fmt.Errorf("failed to write savings command: %w", err)
	}

	return true, nil
}
