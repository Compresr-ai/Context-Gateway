package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	// #nosec G107 -- localhost-only health check, port from internal config
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
	printStep("Waiting for gateway to be ready...")

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if checkGatewayRunning(port) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
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
	if len(ac.Agent.Command.CheckCmd) == 0 {
		return nil
	}
	// #nosec G204 -- CheckCmd comes from embedded YAML config, not user input
	checkCmd := exec.Command(ac.Agent.Command.CheckCmd[0], ac.Agent.Command.CheckCmd[1:]...)
	if err := checkCmd.Run(); err == nil {
		return nil // Agent is available
	}

	displayName := ac.Agent.DisplayName
	if displayName == "" {
		displayName = ac.Agent.Name
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
		// #nosec G204 -- InstallCmd comes from embedded YAML config, not user input
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
		// #nosec G304 -- userConfig path provided by CLI user (intentional)
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
		// #nosec G304 -- trusted config path
		if data, err := os.ReadFile(path); err == nil {
			return data, path, nil
		}
	}

	// Check local configs directory
	path := filepath.Join("configs", name+".yaml")
	// #nosec G304 -- trusted config path
	if data, err := os.ReadFile(path); err == nil {
		return data, path, nil
	}

	// Fall back to embedded config
	if data, err := getEmbeddedConfig(name); err == nil {
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

// isUserConfig checks if a config is a user-created config (in ~/.config/context-gateway/configs/)
func isUserConfig(name string) bool {
	homeDir, _ := os.UserHomeDir()
	if homeDir == "" {
		return false
	}
	path := filepath.Join(homeDir, ".config", "context-gateway", "configs", name+".yaml")
	_, err := os.Stat(path)
	return err == nil
}

// hasUserConfigs checks if there are any user-created configs
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

// listUserConfigs returns only user-created configs
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

// createSessionDir creates a timestamped session directory.
func createSessionDir(baseDir string) string {
	_ = os.MkdirAll(baseDir, 0750)

	now := time.Now().Format("20060102_150405")

	// Find next session number
	sessionNum := 1
	entries, err := os.ReadDir(baseDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), "session_") {
				parts := strings.SplitN(e.Name(), "_", 3)
				if len(parts) >= 2 {
					if n, err := strconv.Atoi(parts[1]); err == nil && n >= sessionNum {
						sessionNum = n + 1
					}
				}
			}
		}
	}

	dir := filepath.Join(baseDir, fmt.Sprintf("session_%d_%s", sessionNum, now))
	_ = os.MkdirAll(dir, 0750)
	return dir
}

// exportAgentEnv sets environment variables defined in the agent config.
func exportAgentEnv(ac *AgentConfig) {
	// First, unset any specified variables (for OAuth-based auth)
	for _, varName := range ac.Agent.Unset {
		_ = os.Unsetenv(varName)
		printInfo(fmt.Sprintf("Unset: %s (agent will use OAuth)", varName))
	}
	// Then set the specified variables
	for _, env := range ac.Agent.Environment {
		_ = os.Setenv(env.Name, env.Value)
		printInfo(fmt.Sprintf("Exported: %s", env.Name))
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

// selectModelInteractive shows a model selection menu for agents like OpenClaw.
// Returns the selected model ID.
func selectModelInteractive(ac *AgentConfig) string {
	if len(ac.Agent.Models) == 0 {
		return ac.Agent.DefaultModel
	}

	labels := make([]string, len(ac.Agent.Models))
	for i, m := range ac.Agent.Models {
		label := m.Name
		if m.ID == ac.Agent.DefaultModel {
			label += " (default)"
		}
		labels[i] = label
	}

	idx, err := selectFromList("Choose which model to use:", labels)
	if err != nil {
		return ac.Agent.DefaultModel
	}

	selected := ac.Agent.Models[idx]
	printSuccess(fmt.Sprintf("Selected: %s (%s)", selected.Name, selected.ID))
	return selected.ID
}

// createOpenClawConfig writes the OpenClaw config with proxy routing.
func createOpenClawConfig(model string, gatewayPort int) {
	homeDir, _ := os.UserHomeDir()
	if homeDir == "" {
		return
	}

	configDir := filepath.Join(homeDir, ".openclaw")
	_ = os.MkdirAll(configDir, 0750)

	cfg := map[string]interface{}{
		"agents": map[string]interface{}{
			"defaults": map[string]interface{}{
				"model": map[string]interface{}{
					"primary": model,
				},
			},
		},
		"models": map[string]interface{}{
			"providers": map[string]interface{}{
				"anthropic": map[string]interface{}{
					"baseUrl": fmt.Sprintf("http://localhost:%d", gatewayPort),
					"models":  []interface{}{},
				},
				"openai": map[string]interface{}{
					"baseUrl": fmt.Sprintf("http://localhost:%d/v1", gatewayPort),
					"models":  []interface{}{},
				},
			},
		},
	}

	data, _ := json.MarshalIndent(cfg, "", "  ")
	configFile := filepath.Join(configDir, "openclaw.json")
	_ = os.WriteFile(configFile, data, 0600)

	printSuccess(fmt.Sprintf("Created OpenClaw config with model: %s", model))
	printInfo(fmt.Sprintf("API calls routed through Context Gateway on port %d", gatewayPort))
}

// createOpenClawConfigDirect writes OpenClaw config without proxy.
func createOpenClawConfigDirect(model string) {
	homeDir, _ := os.UserHomeDir()
	if homeDir == "" {
		return
	}

	configDir := filepath.Join(homeDir, ".openclaw")
	_ = os.MkdirAll(configDir, 0750)

	cfg := map[string]interface{}{
		"agents": map[string]interface{}{
			"defaults": map[string]interface{}{
				"model": map[string]interface{}{
					"primary": model,
				},
			},
		},
	}

	data, _ := json.MarshalIndent(cfg, "", "  ")
	configFile := filepath.Join(configDir, "openclaw.json")
	_ = os.WriteFile(configFile, data, 0600)

	printSuccess(fmt.Sprintf("Created OpenClaw config with model: %s", model))
	printInfo("API calls go directly to providers (no proxy)")
}

// startOpenClawGateway starts the OpenClaw TUI gateway subprocess.
func startOpenClawGateway() *exec.Cmd {
	// Stop any existing gateway
	_ = exec.Command("openclaw", "gateway", "stop").Run()
	time.Sleep(1 * time.Second)

	// Start fresh gateway
	printInfo("Starting OpenClaw gateway...")

	// Generate random token for security
	tokenBytes := make([]byte, 16)
	_, _ = rand.Read(tokenBytes)
	randomToken := hex.EncodeToString(tokenBytes)

	cmd := exec.Command("openclaw", "gateway", "--port", "18789", "--allow-unconfigured", "--token", randomToken, "--force") // #nosec G204 -- controlled command with known args
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Start()
	time.Sleep(2 * time.Second)

	printSuccess("OpenClaw gateway started on port 18789")
	return cmd
}

// Print helper functions for consistent output formatting.
func printHeader(title string) {
	fmt.Printf("\033[1m\033[0;36m========================================\033[0m\n")
	fmt.Printf("\033[1m\033[0;36m       %s\033[0m\n", title)
	fmt.Printf("\033[1m\033[0;36m========================================\033[0m\n")
	fmt.Println()
}

func printSuccess(msg string) {
	fmt.Printf("\033[0;32m[OK]\033[0m %s\n", msg)
}

func printInfo(msg string) {
	fmt.Printf("\033[0;34m[INFO]\033[0m %s\n", msg)
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
	fmt.Println("Usage: context-gateway [AGENT] [OPTIONS] [-- AGENT_ARGS...]")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  -c, --config FILE    Gateway config (optional - shows menu if not specified)")
	fmt.Println("  -p, --port PORT      Gateway port (default: 18080)")
	fmt.Println("  -d, --debug          Enable debug logging")
	fmt.Println("  --proxy MODE         auto (default), start, skip")
	fmt.Println("  -l, --list           List available agents")
	fmt.Println("  -h, --help           Show this help")
	fmt.Println()
	fmt.Println("Pass-through Arguments:")
	fmt.Println("  Everything after -- is forwarded directly to the agent command.")
	fmt.Println("  This is useful for passing flags that conflict with gateway options")
	fmt.Println("  (e.g., -p is used by the gateway for --port).")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  context-gateway                                  Interactive mode")
	fmt.Println("  context-gateway claude_code                      Interactive config selection")
	fmt.Println("  context-gateway claude_code -c preemptive_summarization")
	fmt.Println("  context-gateway -l                               List agents")
	fmt.Println("  context-gateway claude_code -- -p \"fix the bug\"  Pass -p to Claude Code")
	fmt.Println("  context-gateway claude_code -d -- --verbose      Debug gateway, --verbose to agent")
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
