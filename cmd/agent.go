package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	stdlog "log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
)

// runAgentCommand is the main entry point for the agent launcher.
// It replaces start_agent.sh with native Go.
func runAgentCommand(args []string) {
	// Parse flags
	var (
		configFlag string
		debugFlag  bool
		portFlag   string
		proxyMode  string
		logDir     string
		listFlag   bool
		agentArg   string
	)

	portFlag = "18080"
	proxyMode = "auto"

	i := 0
	for i < len(args) {
		switch args[i] {
		case "-h", "--help":
			printAgentHelp()
			return
		case "-l", "--list":
			listFlag = true
			i++
		case "-c", "--config":
			if i+1 < len(args) {
				configFlag = args[i+1]
				i += 2
			} else {
				fmt.Fprintln(os.Stderr, "Error: --config requires a value")
				os.Exit(1)
			}
		case "-d", "--debug":
			debugFlag = true
			i++
		case "-p", "--port":
			if i+1 < len(args) {
				portFlag = args[i+1]
				i += 2
			} else {
				fmt.Fprintln(os.Stderr, "Error: --port requires a value")
				os.Exit(1)
			}
		case "--proxy":
			if i+1 < len(args) {
				proxyMode = args[i+1]
				i += 2
			} else {
				fmt.Fprintln(os.Stderr, "Error: --proxy requires a value")
				os.Exit(1)
			}
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "Error: unknown option: %s\n", args[i])
				os.Exit(1)
			}
			agentArg = args[i]
			i++
		}
	}

	// Load .env files
	loadEnvFiles()

	// Set GATEWAY_PORT env for variable expansion in configs/agents
	os.Setenv("GATEWAY_PORT", portFlag)

	printBanner()

	// Require either ANTHROPIC_API_KEY or AWS credentials for Bedrock
	hasAnthropicKey := os.Getenv("ANTHROPIC_API_KEY") != ""
	hasAwsCredentials := os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != ""
	hasAwsProfile := os.Getenv("AWS_PROFILE") != ""
	hasAwsBearerToken := os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != ""
	hasAwsAuth := hasAwsCredentials || hasAwsProfile || hasAwsBearerToken
	if !hasAnthropicKey && !hasAwsAuth {
		fmt.Fprintf(os.Stderr, "\n  \033[1;31mError:\033[0m No API credentials configured.\n\n")
		fmt.Fprintf(os.Stderr, "  Context Gateway requires either Anthropic or AWS Bedrock credentials.\n\n")
		fmt.Fprintf(os.Stderr, "  Option 1: Anthropic API key\n")
		fmt.Fprintf(os.Stderr, "    export ANTHROPIC_API_KEY=sk-ant-...\n\n")
		fmt.Fprintf(os.Stderr, "  Option 2: AWS credentials (for Bedrock)\n")
		fmt.Fprintf(os.Stderr, "    export AWS_ACCESS_KEY_ID=AKIA...\n")
		fmt.Fprintf(os.Stderr, "    export AWS_SECRET_ACCESS_KEY=...\n")
		fmt.Fprintf(os.Stderr, "    export AWS_REGION=us-east-1\n\n")
		fmt.Fprintf(os.Stderr, "  Option 3: AWS profile\n")
		fmt.Fprintf(os.Stderr, "    export AWS_PROFILE=my-profile\n\n")
		fmt.Fprintf(os.Stderr, "  Add to .env file: ~/.config/context-gateway/.env\n\n")
		os.Exit(1)
	}

	// Log available providers
	if hasAnthropicKey {
		fmt.Fprintf(os.Stderr, "  ✓ Anthropic API key configured\n")
	}
	if hasAwsAuth {
		authMethod := "credentials"
		if hasAwsProfile {
			authMethod = "profile (" + os.Getenv("AWS_PROFILE") + ")"
		}
		fmt.Fprintf(os.Stderr, "  ✓ AWS %s configured (Bedrock)\n", authMethod)
	}

	// List mode
	if listFlag {
		listAvailableAgents()
		return
	}

	// Interactive agent selection if none specified (matches start_agent.sh)
	if agentArg == "" {
		agents := discoverAgents()
		var names []string
		for _, k := range sortedKeys(agents) {
			if !strings.HasPrefix(k, "template") {
				names = append(names, k)
			}
		}
		if len(names) == 0 {
			printError("No agents found. Place agent YAML files in agents/ or ~/.config/context-gateway/agents/")
			os.Exit(1)
		}
		idx, selectErr := selectFromList("Select an agent:", names)
		if selectErr != nil {
			os.Exit(0)
		}
		agentArg = names[idx]
	}

	// Load and validate agent
	ac, _, err := loadAgentConfig(agentArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Println()
		listAvailableAgents()
		os.Exit(1)
	}

	err = validateAgent(ac)
	if err != nil {
		os.Exit(1)
	}

	// Interactive config selection if needed and not specified
	var configData []byte
	var configSource string

	if proxyMode != "skip" && configFlag == "" {
		// Interactive config selection (matches start_agent.sh)
		configs := listAvailableConfigs()
		if len(configs) == 0 {
			printError("No config files found. Place config YAML files in configs/ or ~/.config/context-gateway/configs/")
			os.Exit(1)
		}
		idx, selectErr := selectFromList("Select a gateway configuration:", configs)
		if selectErr != nil {
			os.Exit(0)
		}
		configFlag = configs[idx]
		configData, configSource, err = resolveConfig(configFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	} else if proxyMode != "skip" && configFlag != "" {
		configData, configSource, err = resolveConfig(configFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	// Create session log directory
	var sessionDir string
	if proxyMode != "skip" {
		logsBase := logDir
		if logsBase == "" {
			logsBase = "logs"
		}
		sessionDir = createSessionDir(logsBase)

		// Export session log paths
		os.Setenv("SESSION_DIR", sessionDir)
		os.Setenv("SESSION_TELEMETRY_LOG", filepath.Join(sessionDir, "telemetry.jsonl"))
		os.Setenv("SESSION_COMPRESSION_LOG", filepath.Join(sessionDir, "compression.jsonl"))
		os.Setenv("SESSION_COMPACTION_LOG", filepath.Join(sessionDir, "compaction.jsonl"))
		os.Setenv("SESSION_TRAJECTORY_LOG", filepath.Join(sessionDir, "trajectory.json"))
		os.Setenv("SESSION_GATEWAY_LOG", filepath.Join(sessionDir, "gateway.log"))

		printSuccess("Session: " + filepath.Base(sessionDir))
	}

	// Export agent environment variables
	exportAgentEnv(ac)

	// Start gateway as goroutine (not background process)
	var gw *gateway.Gateway
	if proxyMode != "skip" && configData != nil {
		fmt.Println()
		printHeader("Step 1: Gateway Setup")

		port, _ := strconv.Atoi(portFlag)
		if checkGatewayRunning(port) {
			printSuccess(fmt.Sprintf("Reusing existing gateway on port %d", port))
		} else {
			printStep("Starting gateway in-process...")

			// Redirect ALL gateway logging to the session log file.
			// This prevents any zerolog output from polluting the agent's terminal.
			var gatewayLogFile *os.File
			gatewayLogOutput := os.DevNull
			if gwLogPath := os.Getenv("SESSION_GATEWAY_LOG"); gwLogPath != "" {
				if f, err := os.OpenFile(gwLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
					gatewayLogFile = f
					gatewayLogOutput = gwLogPath
					defer f.Close()
				}
			}
			// If we can't open a log file, discard all gateway logs
			if gatewayLogFile == nil {
				devNull, err := os.Open(os.DevNull)
				if err == nil {
					gatewayLogFile = devNull
					defer devNull.Close()
				}
			}
			setupLogging(debugFlag, gatewayLogFile)

			// Redirect Go's standard library log (used by net/http server errors)
			// to the gateway log file to prevent stderr pollution of the agent's terminal.
			if gatewayLogFile != nil {
				stdlog.SetOutput(gatewayLogFile)
			}

			cfg, err := config.LoadFromBytes(configData)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading config '%s': %v\n", configSource, err)
				os.Exit(1)
			}

			// Override monitoring config so gateway.New() -> monitoring.Global()
			// doesn't reset zerolog back to stdout.
			// Use the validated path (gatewayLogOutput) rather than re-reading
			// the env var, so if the file couldn't be opened we fall back to
			// /dev/null instead of letting monitoring.New() fall back to stdout.
			cfg.Monitoring.LogOutput = gatewayLogOutput
			cfg.Monitoring.LogToStdout = false

			gw = gateway.New(cfg)

			// Re-assert our logging setup in case monitoring.Global() overrode it
			// (e.g. if the log file couldn't be opened and it fell back to stdout)
			setupLogging(debugFlag, gatewayLogFile)

			// Start gateway in a goroutine (it blocks on ListenAndServe)
			gwErrCh := make(chan error, 1)
			go func() {
				gwErrCh <- gw.Start()
			}()

			// Wait for gateway to be healthy
			if !waitForGateway(port, 30*time.Second) {
				fmt.Fprintln(os.Stderr, "Error: gateway failed to start within 30s")
				if sessionDir != "" {
					fmt.Fprintf(os.Stderr, "Check logs: %s\n", sessionDir)
				}

				fmt.Print("Continue anyway? [y/N] ")
				reader := bufio.NewReader(os.Stdin)
				resp, _ := reader.ReadString('\n')
				resp = strings.TrimSpace(strings.ToLower(resp))
				if resp != "y" && resp != "yes" {
					os.Exit(1)
				}
				printWarn("Continuing without healthy gateway...")
			} else {
				printSuccess(fmt.Sprintf("Gateway started on port %s", portFlag))
			}
		}
	} else if proxyMode == "skip" {
		printInfo("Skipping gateway (--proxy skip)")
	}

	// OpenClaw special handling
	var openclawCmd *exec.Cmd
	if agentArg == "openclaw" {
		fmt.Println()
		printHeader("Step 2: OpenClaw Model Selection")

		selectedModel := selectModelInteractive(ac)
		port, _ := strconv.Atoi(portFlag)

		if proxyMode == "skip" {
			createOpenClawConfigDirect(selectedModel)
		} else {
			createOpenClawConfig(selectedModel, port)
		}

		openclawCmd = startOpenClawGateway()
	}

	// Start agent
	fmt.Println()
	printHeader("Step 3: Start Agent")

	displayName := ac.Agent.DisplayName
	if displayName == "" {
		displayName = ac.Agent.Name
	}
	printStep(fmt.Sprintf("Launching %s...", displayName))
	fmt.Println()
	if sessionDir != "" {
		fmt.Printf("\033[0;36mSession logs: %s\033[0m\n", filepath.Base(sessionDir))
	}
	fmt.Println()

	// Clean up stale IDE lock files
	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		lockFiles, _ := filepath.Glob(filepath.Join(homeDir, ".claude", "ide", "*.lock"))
		for _, f := range lockFiles {
			_ = os.Remove(f)
		}
	}

	// Build agent command
	agentCmd := ac.Agent.Command.Run
	if len(ac.Agent.Command.Args) > 0 {
		agentCmd += " " + strings.Join(ac.Agent.Command.Args, " ")
	}

	// Launch agent as child process
	// #nosec G204 -- agentCmd comes from validated agent YAML config
	cmd := exec.Command("bash", "-c", agentCmd)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	// Catch SIGINT/SIGTERM in the parent so it doesn't terminate when
	// the user presses Ctrl+C (which the agent handles internally).
	// Without this, Ctrl+C kills the parent and breaks the gateway proxy.
	// This matches start_agent.sh's: trap cleanup_on_exit SIGINT SIGTERM EXIT
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			fmt.Printf("\n")
			printInfo(fmt.Sprintf("Agent exited with code: %d", exitErr.ExitCode()))
		}
	} else {
		fmt.Printf("\n")
		printInfo("Agent exited with code: 0")
	}

	// Restore default signal handling after agent exits
	signal.Stop(sigCh)
	signal.Reset(syscall.SIGINT, syscall.SIGTERM)

	// Cleanup after agent exits (matches trap cleanup_on_exit in start_agent.sh)
	if openclawCmd != nil && openclawCmd.Process != nil {
		_ = openclawCmd.Process.Kill()
	}

	if gw != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = gw.Shutdown(ctx)
	}

	if sessionDir != "" {
		fmt.Printf("\n\033[0;36mSession logs: %s\033[0m\n\n", sessionDir)
	}
}

// selectFromList shows an interactive numbered menu and returns the selected index.
func selectFromList(prompt string, items []string) (int, error) {
	fmt.Println()
	fmt.Printf("\033[1m\033[0;36m%s\033[0m\n", prompt)
	fmt.Println()
	for i, item := range items {
		fmt.Printf("  \033[0;32m[%d]\033[0m %s\n", i+1, item)
	}
	fmt.Printf("  \033[1;33m[0]\033[0m Cancel\n")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("Enter number: ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "0" {
			return 0, fmt.Errorf("cancelled")
		}

		num, err := strconv.Atoi(input)
		if err == nil && num >= 1 && num <= len(items) {
			return num - 1, nil
		}
		fmt.Printf("Invalid choice. Enter 1-%d or 0 to cancel.\n", len(items))
	}
}

// checkGatewayRunning checks if a gateway is already running on the port.
func checkGatewayRunning(port int) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/health", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
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

// validateAgent checks if the agent binary is available and offers to install.
func validateAgent(ac *AgentConfig) error {
	if ac.Agent.Command.Check == "" {
		return nil
	}

	// #nosec G204 -- check command comes from agent YAML config
	checkCmd := exec.Command("bash", "-c", ac.Agent.Command.Check)
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

	if ac.Agent.Command.Install != "" {
		fmt.Printf("Would you like to install it now? [Y/n]\n")
		fmt.Printf("  \033[2mCommand: %s\033[0m\n\n", ac.Agent.Command.Install)

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

		// #nosec G204 -- install command comes from agent YAML config
		installCmd := exec.Command("bash", "-c", ac.Agent.Command.Install)
		installCmd.Stdin = os.Stdin
		installCmd.Stdout = os.Stdout
		installCmd.Stderr = os.Stderr

		if err := installCmd.Run(); err != nil {
			fmt.Println()
			printError("Installation failed")
			fmt.Printf("  \033[1;33mYou can try manually: %s\033[0m\n", ac.Agent.Command.Install)
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
		if data, err := os.ReadFile(path); err == nil {
			return data, path, nil
		}
	}

	// Check local configs directory
	path := filepath.Join("configs", name+".yaml")
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
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
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
	for _, env := range ac.Agent.Environment {
		// Values are already expanded by parseAgentConfig
		os.Setenv(env.Name, env.Value)
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
	// #nosec G204 -- hardcoded command
	_ = exec.Command("openclaw", "gateway", "stop").Run()
	time.Sleep(1 * time.Second)

	// Start fresh gateway
	printInfo("Starting OpenClaw gateway...")
	// #nosec G204 -- hardcoded command
	cmd := exec.Command("openclaw", "gateway", "--port", "18789", "--allow-unconfigured", "--token", "localdev", "--force")
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
	fmt.Println("Usage: context-gateway [AGENT] [OPTIONS]")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  -c, --config FILE    Gateway config (optional - shows menu if not specified)")
	fmt.Println("  -p, --port PORT      Gateway port (default: 18080)")
	fmt.Println("  -d, --debug          Enable debug logging")
	fmt.Println("  --proxy MODE         auto (default), start, skip")
	fmt.Println("  -l, --list           List available agents")
	fmt.Println("  -h, --help           Show this help")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  context-gateway                                  Interactive mode")
	fmt.Println("  context-gateway claude_code                      Interactive config selection")
	fmt.Println("  context-gateway claude_code -c preemptive_summarization")
	fmt.Println("  context-gateway -l                               List agents")
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
