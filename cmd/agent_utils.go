package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/compresr/context-gateway/internal/tui"

	"gopkg.in/yaml.v3"
)

// selectFromList shows an interactive menu using arrow keys and returns the selected index.
// Now uses the TUI package for arrow-key navigation.
func selectFromList(prompt string, items []string) (int, error) {
	menuItems := make([]tui.MenuItem, len(items))
	for i, item := range items {
		menuItems[i] = tui.MenuItem{
			Label: item,
			Value: item,
		}
	}
	return tui.SelectMenu(prompt, menuItems)
}

// checkGatewayRunning checks if a gateway is already running on the port.
func checkGatewayRunning(port int) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	// #nosec G107,G704 -- localhost-only health check, port from internal config
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// findAvailablePort finds the first available port in the given range.
// Returns the port number and true if found, or 0 and false if no port available.
func findAvailablePort(basePort, maxPorts int) (int, bool) {
	for i := 0; i < maxPorts; i++ {
		port := basePort + i
		if !isPortInUse(port) {
			return port, true
		}
	}
	return 0, false
}

// isPortInUse checks if a TCP port is in use.
func isPortInUse(port int) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return true
	}
	_ = listener.Close()
	return false
}

// waitForGateway polls the health endpoint until ready or timeout.
func waitForGateway(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if checkGatewayRunning(port) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// isDashboardRunning checks if the dashboard port is already listening.
// Used to avoid re-opening the browser when a dashboard is already open.
func isDashboardRunning(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// stopExistingBackgroundGateway stops any existing background gateway.
// Used to ensure only one background gateway runs at a time.
// Silently returns if no gateway is running.
func stopExistingBackgroundGateway() {
	pidFile := filepath.Join(os.TempDir(), "context-gateway.pid")
	portFile := filepath.Join(os.TempDir(), "context-gateway.port")

	// #nosec G304 -- reading pid file from temp dir (trusted path)
	pidBytes, err := os.ReadFile(filepath.Clean(pidFile))
	if err != nil {
		// No PID file - no gateway running
		return
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		// Invalid PID file - clean up
		_ = os.Remove(pidFile)
		_ = os.Remove(portFile)
		return
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		// Process not found - clean up
		_ = os.Remove(pidFile)
		_ = os.Remove(portFile)
		return
	}

	// Check if process is actually alive (FindProcess always succeeds on Unix)
	if err := process.Signal(syscall.Signal(0)); err != nil {
		// Process not running - clean up stale files
		_ = os.Remove(pidFile)
		_ = os.Remove(portFile)
		return
	}

	// Try to stop gracefully with SIGTERM
	if err := process.Signal(syscall.SIGTERM); err != nil {
		// Failed to signal - clean up
		_ = os.Remove(pidFile)
		_ = os.Remove(portFile)
		return
	}

	// Wait for daemon to fully shutdown (daemon takes 2s for plugins + 10s max for gateway)
	// We wait up to 15s to be safe
	for i := 0; i < 150; i++ { // 150 * 100ms = 15s max
		time.Sleep(100 * time.Millisecond)
		// Check if process is still running
		if err := process.Signal(syscall.Signal(0)); err != nil {
			break // Process exited
		}
	}

	// Only clean up files if they still contain the OLD pid we just stopped
	// (new daemon may have already written new files)
	if currentBytes, readErr := os.ReadFile(filepath.Clean(pidFile)); readErr == nil {
		if currentPid, parseErr := strconv.Atoi(strings.TrimSpace(string(currentBytes))); parseErr == nil && currentPid == pid {
			_ = os.Remove(pidFile)
		}
	}
	// Port file: only remove if PID file was ours (they go together)
	// Actually, the daemon removes its own port file on shutdown, so we don't need to
}

// isLockFileStale checks if a lock file is stale (process no longer running).
// Lock files typically contain the PID of the process that created them.
// If the file is empty, malformed, or the PID doesn't exist, consider it stale.
func isLockFileStale(lockPath string) bool {
	// Read lock file content
	// #nosec G304 -- lockPath is constructed internally from known directories
	content, err := os.ReadFile(lockPath)
	if err != nil {
		// Can't read file, consider it stale
		return true
	}

	// Try to parse PID from lock file
	// Claude Code lock files may contain JSON or just a PID
	pidStr := strings.TrimSpace(string(content))
	if pidStr == "" {
		// Empty file is stale
		return true
	}

	// Check if process exists by sending signal 0 (no-op signal)
	// First try to parse as simple PID
	if pid, err := strconv.Atoi(pidStr); err == nil {
		process, err := os.FindProcess(pid)
		if err != nil {
			// Process doesn't exist
			return true
		}
		// On Unix, FindProcess always succeeds, so send signal 0 to check if alive
		err = process.Signal(syscall.Signal(0))
		return err != nil // Stale if signal fails
	}

	// If not a simple PID, might be JSON - try to extract PID field
	// For now, be conservative and don't delete if we can't parse
	// (better to leave a stale lock than delete an active one)
	return false
}

// validateAgent checks if the agent binary is available and offers to install.
func validateAgent(ac *AgentConfig) error {
	displayName := ac.Agent.DisplayName
	if displayName == "" {
		displayName = ac.Agent.Name
	}

	if len(ac.Agent.Command.CheckCmd) == 0 {
		return nil
	}
	// #nosec G204,G702 -- CheckCmd comes from embedded YAML config, not user input
	checkCmd := exec.Command(ac.Agent.Command.CheckCmd[0], ac.Agent.Command.CheckCmd[1:]...)
	if err := checkCmd.Run(); err == nil {
		printSuccess(fmt.Sprintf("%s binary found", displayName))
		return nil // Agent is available
	}

	fmt.Println()
	printWarn(fmt.Sprintf("Agent '%s' is not installed", displayName))
	if ac.Agent.Command.FallbackMessage != "" {
		fmt.Printf("  \033[1;33m%s\033[0m\n", ac.Agent.Command.FallbackMessage)
	}
	fmt.Println()

	if len(ac.Agent.Command.InstallCmd) > 0 {
		fmt.Printf("Would you like to install it now? [Y/n]\n")
		fmt.Printf("  \033[2mCommand: %s\033[0m\n\n", strings.Join(ac.Agent.Command.InstallCmd, " "))

		reader := bufio.NewReader(os.Stdin)
		resp, _ := reader.ReadString('\n')
		resp = strings.TrimSpace(strings.ToLower(resp))

		if resp == "n" || resp == "no" {
			printInfo("Installation skipped.")
			return fmt.Errorf("agent not installed")
		}

		fmt.Println()
		printStep(fmt.Sprintf("Installing %s...", displayName))
		fmt.Println()
		// #nosec G204,G702 -- InstallCmd comes from embedded YAML config, not user input
		installCmd := exec.Command(ac.Agent.Command.InstallCmd[0], ac.Agent.Command.InstallCmd[1:]...)
		installCmd.Stdin = os.Stdin
		installCmd.Stdout = os.Stdout
		installCmd.Stderr = os.Stderr

		if err := installCmd.Run(); err != nil {
			fmt.Println()
			printError("Installation failed")
			fmt.Printf("  \033[1;33mYou can try manually: %s\033[0m\n", strings.Join(ac.Agent.Command.InstallCmd, " "))
			return fmt.Errorf("installation failed")
		}

		fmt.Println()
		printSuccess(fmt.Sprintf("%s installed successfully!", displayName))
		return nil
	}

	fmt.Println("No automatic installation available.")
	return fmt.Errorf("agent not installed")
}

// discoverAgents discovers agents from filesystem locations and embedded defaults.
// Filesystem agents take priority over embedded ones.
// Returns a map of agent name -> raw YAML bytes.
func discoverAgents() map[string][]byte {
	agents := make(map[string][]byte)

	homeDir, _ := os.UserHomeDir()
	searchDirs := []string{}
	if homeDir != "" {
		searchDirs = append(searchDirs, filepath.Join(homeDir, ".config", "context-gateway", "agents"))
	}
	searchDirs = append(searchDirs, "agents")

	for _, dir := range searchDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".yaml")
			if _, exists := agents[name]; exists {
				continue // first match wins (user config takes priority)
			}
			// #nosec G304 -- reading from trusted config directories
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err == nil {
				agents[name] = data
			}
		}
	}

	// Fall back to embedded agents for any not found on filesystem
	embeddedNames, err := listEmbeddedAgents()
	if err == nil {
		for _, name := range embeddedNames {
			if _, exists := agents[name]; exists {
				continue // filesystem takes priority
			}
			if data, err := getEmbeddedAgent(name); err == nil {
				agents[name] = data
			}
		}
	}

	return agents
}

// resolveConfig finds config data by name or path.
// Checks filesystem locations first, then falls back to embedded configs.
// Returns raw bytes, source description, and error.
func resolveConfig(userConfig string) ([]byte, string, error) {
	// If it looks like a file path, try reading it directly
	if strings.Contains(userConfig, "/") || strings.Contains(userConfig, "\\") {
		// #nosec G304,G703 -- userConfig path provided by CLI user (intentional)
		data, err := os.ReadFile(userConfig)
		if err != nil {
			return nil, "", fmt.Errorf("config file not found: %s", userConfig)
		}
		return data, userConfig, nil
	}

	// Normalize name (remove extension for lookup)
	name := strings.TrimSuffix(userConfig, ".yaml")

	// Check filesystem locations
	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		path := filepath.Join(homeDir, ".config", "context-gateway", "configs", name+".yaml")
		// #nosec G304,G703 -- trusted config path
		if data, err := os.ReadFile(path); err == nil {
			return data, path, nil
		}
	}

	// Check local configs directory
	path := filepath.Join("configs", name+".yaml")
	// #nosec G304,G703 -- trusted config path
	if data, err := os.ReadFile(path); err == nil {
		return data, path, nil
	}

	// Fall back to embedded config — materialize to user config dir so the
	// global config file exists on disk and dashboard changes persist across restarts.
	if data, err := getEmbeddedConfig(name); err == nil {
		if homeDir != "" {
			userConfigDir := filepath.Join(homeDir, ".config", "context-gateway", "configs")
			if mkErr := os.MkdirAll(userConfigDir, 0750); mkErr == nil {
				persistPath := filepath.Join(userConfigDir, name+".yaml")
				// #nosec G306 G703 -- config file, not secret; path constructed from validated inputs
				if wErr := os.WriteFile(persistPath, data, 0644); wErr == nil {
					return data, persistPath, nil
				}
			}
		}
		// Materialization failed — return as embedded (dashboard changes won't persist)
		return data, "(embedded) " + name + ".yaml", nil
	}

	return nil, "", fmt.Errorf("config '%s' not found", userConfig)
}

// listAvailableConfigs returns config names found in filesystem and embedded configs.
// Filesystem configs take priority over embedded ones.
func listAvailableConfigs() []string {
	seen := make(map[string]bool)
	var names []string

	// Files that are not proxy configs (should be excluded from menu)
	excludeFiles := map[string]bool{
		"external_providers": true, // LLM provider definitions for TUI, not a proxy config
	}

	homeDir, _ := os.UserHomeDir()
	dirs := []string{}
	if homeDir != "" {
		dirs = append(dirs, filepath.Join(homeDir, ".config", "context-gateway", "configs"))
	}
	dirs = append(dirs, "configs")

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".yaml")
			if excludeFiles[name] {
				continue
			}
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}

	// Include embedded configs not already found on filesystem
	embeddedNames, err := listEmbeddedConfigs()
	if err == nil {
		for _, name := range embeddedNames {
			if excludeFiles[name] {
				continue
			}
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}

	sort.Strings(names)
	return names
}

// isUserConfig checks if a config is a user-created config (in ~/.config/context-gateway/configs/).
func isUserConfig(name string) bool {
	homeDir, _ := os.UserHomeDir()
	if homeDir == "" {
		return false
	}
	path := filepath.Join(homeDir, ".config", "context-gateway", "configs", name+".yaml")
	_, err := os.Stat(path)
	return err == nil
}

// hasUserConfigs checks if there are any user-created configs.
func hasUserConfigs() bool {
	homeDir, _ := os.UserHomeDir()
	if homeDir == "" {
		return false
	}
	dir := filepath.Join(homeDir, ".config", "context-gateway", "configs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			return true
		}
	}
	return false
}

// listUserConfigs returns only user-created config names.
func listUserConfigs() []string {
	homeDir, _ := os.UserHomeDir()
	if homeDir == "" {
		return nil
	}
	dir := filepath.Join(homeDir, ".config", "context-gateway", "configs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	sort.Strings(names)
	return names
}

// extractConfigDescription reads the metadata.description from a config file.
// Returns empty string if metadata is not present or on error.
func extractConfigDescription(name string) string {
	data, _, err := resolveConfig(name)
	if err != nil {
		return ""
	}

	var meta struct {
		Metadata struct {
			Description string `yaml:"description"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return ""
	}
	return meta.Metadata.Description
}

// listAvailableConfigsPrint prints all discovered configs to stdout.
func listAvailableConfigsPrint() {
	configs := listAvailableConfigs()

	printHeader("Available Configs")

	if len(configs) == 0 {
		fmt.Println("  No configs found.")
		return
	}

	for i, name := range configs {
		source := "(built-in)"
		if isUserConfig(name) {
			source = "(user)"
		}

		desc := extractConfigDescription(name)

		fmt.Printf("  \033[0;32m[%d]\033[0m \033[1m%s\033[0m \033[2m%s\033[0m\n", i+1, name, source)
		if desc != "" {
			fmt.Printf("      %s\n", desc)
		}
		fmt.Println()
	}
}

// sanitizeName replaces characters that are not filesystem-safe with underscores.
func sanitizeName(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}

// prepareSessionPath computes a session directory path without creating it.
// The actual directory is created lazily on first LLM request via ensureSessionDir().
// This prevents empty session folders when the gateway starts but receives no traffic.
//
// Auto-generated format: <agent>_<N>_<YYYYMMDD_HHMMSS>  (e.g. claude_code_3_20240315_143022)
// If customName is non-empty it is used as-is (sanitized) without an agent prefix.
func prepareSessionPath(baseDir string, agentName string, customName string) string {
	_ = os.MkdirAll(baseDir, 0750)

	if customName != "" {
		return filepath.Join(baseDir, sanitizeName(customName))
	}

	prefix := sanitizeName(agentName)
	if prefix == "" {
		prefix = "session"
	}
	now := time.Now().Format("20060102_150405")

	// Find the highest session number already used for this agent prefix.
	sessionNum := 1
	entries, err := os.ReadDir(baseDir)
	if err == nil {
		needle := prefix + "_"
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), needle) {
				continue
			}
			// Name is "<prefix>_<N>_<date>" — parse the part right after the prefix.
			rest := e.Name()[len(needle):]
			parts := strings.SplitN(rest, "_", 2)
			if len(parts) >= 1 {
				if n, err := strconv.Atoi(parts[0]); err == nil && n >= sessionNum {
					sessionNum = n + 1
				}
			}
		}
	}

	return filepath.Join(baseDir, fmt.Sprintf("%s_%d_%s", prefix, sessionNum, now))
}

// exportAgentEnv sets environment variables defined in the agent config.
func exportAgentEnv(ac *AgentConfig) {
	// First, unset any specified variables (for OAuth-based auth)
	for _, varName := range ac.Agent.Unset {
		_ = os.Unsetenv(varName)
	}
	// Then set the specified variables
	for _, env := range ac.Agent.Environment {
		_ = os.Setenv(env.Name, env.Value)
	}
}

// listAvailableAgents prints all discovered agents.
func listAvailableAgents() {
	agents := discoverAgents()

	printHeader("Available Agents")

	names := sortedKeys(agents)
	i := 1
	for _, name := range names {
		if strings.HasPrefix(name, "template") {
			continue
		}

		ac, _ := parseAgentConfig(agents[name])
		displayName := name
		description := ""
		if ac != nil {
			if ac.Agent.DisplayName != "" {
				displayName = ac.Agent.DisplayName
			}
			description = ac.Agent.Description
		}

		fmt.Printf("  \033[0;32m[%d]\033[0m \033[1m%s\033[0m\n", i, name)
		if displayName != name {
			fmt.Printf("      \033[0;36m%s\033[0m\n", displayName)
		}
		if description != "" {
			fmt.Printf("      %s\n", description)
		}
		fmt.Println()
		i++
	}
}

// Print helper functions for consistent output formatting.
func printHeader(title string) {
	fmt.Printf("\033[1m\033[0;36m========================================\033[0m\n")
	fmt.Printf("\033[1m\033[0;36m       %s\033[0m\n", title)
	fmt.Printf("\033[1m\033[0;36m========================================\033[0m\n")
	fmt.Println()
}

func printSuccess(msg string) {
	fmt.Printf("\r\033[0;32m[OK]\033[0m %s\n", msg)
}

func printInfo(msg string) {
	fmt.Printf("  \033[2m·\033[0m %s\n", msg)
}

func printWarn(msg string) {
	fmt.Printf("\033[1;33m[WARN]\033[0m %s\n", msg)
}

func printError(msg string) {
	fmt.Printf("\033[0;31m[ERROR]\033[0m %s\n", msg)
}

func printStep(msg string) {
	fmt.Printf("\033[0;36m>>>\033[0m %s\n", msg)
}

func printAgentHelp() {
	fmt.Println("Start Agent with Gateway Proxy")
	fmt.Println()
	fmt.Println("Usage: context-gateway [OPTIONS] [AGENT] [-- AGENT_ARGS...]")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  -a, --agent AGENT    Select agent directly (claude_code, openclaw, codex, etc.)")
	fmt.Println("  -c, --config [NAME]  Config menu if NAME omitted, uses NAME directly if provided")
	fmt.Println("  --config list        List available configs")
	fmt.Println("  -p, --port PORT      Gateway port (default: 18081)")
	fmt.Println("  -d, --debug          Enable debug logging")
	fmt.Println("  --proxy MODE         auto (default), start, skip")
	fmt.Println("  --reset-api-key      Reset Compresr API key and re-run setup")
	fmt.Println("  -l, --list           List available agents")
	fmt.Println("  -h, --help           Show this help")
	fmt.Println()
	fmt.Println("Pass-through Arguments:")
	fmt.Println("  Everything after -- is forwarded directly to the agent command.")
	fmt.Println("  This is useful for passing flags that conflict with gateway options")
	fmt.Println("  (e.g., -p is used by the gateway for --port).")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  context-gateway                                  Interactive agent selection")
	fmt.Println("  context-gateway -a claude_code                   Launch Claude Code")
	fmt.Println("  context-gateway -a openclaw                      Launch OpenClaw")
	fmt.Println("  context-gateway claude_code -c                   Config management menu")
	fmt.Println("  context-gateway -a claude_code -c fast_setup     Use specific config")
	fmt.Println("  context-gateway --config list                    List configs")
	fmt.Println("  context-gateway -l                               List agents")
	fmt.Println("  context-gateway claude_code -- -p \"fix the bug\"  Pass -p to Claude Code")
}

// sortedKeys returns the sorted keys of a map.
func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
