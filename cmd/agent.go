package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	stdlog "log"
	"net"
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
	"github.com/compresr/context-gateway/internal/preemptive"
	"github.com/compresr/context-gateway/internal/tui"
	"gopkg.in/yaml.v3"
)

// runAgentCommand is the main entry point for the agent launcher.
// It replaces start_agent.sh with native Go.
func runAgentCommand(args []string) {
	// Parse flags
	var (
		configFlag      string
		debugFlag       bool
		portFlag        string
		proxyMode       string
		logDir          string
		listFlag        bool
		agentArg        string
		passthroughArgs []string
	)

	portFlag = "" // Empty = auto-find available port
	proxyMode = "auto"

	i := 0
parseLoop:
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
		case "--":
			passthroughArgs = args[i+1:]
			break parseLoop
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

	// Find available port early so ${GATEWAY_PORT} expands correctly in agent configs
	// Port range: 18080-18089 (max 10 concurrent terminals)
	basePort := 18080
	maxPorts := 10
	var gatewayPort int

	if portFlag != "" {
		// User explicitly specified a port
		var err error
		gatewayPort, err = strconv.Atoi(portFlag)
		if err != nil || gatewayPort <= 0 || gatewayPort > 65535 {
			fmt.Fprintf(os.Stderr, "Error: invalid port '%s'\n", portFlag)
			os.Exit(1)
		}
	} else {
		// Find first available port
		port, found := findAvailablePort(basePort, maxPorts)
		if !found {
			fmt.Fprintf(os.Stderr, "Error: no available ports in range %d-%d\n", basePort, basePort+maxPorts-1)
			fmt.Fprintln(os.Stderr, "Close some terminal sessions to free up ports.")
			os.Exit(1)
		}
		gatewayPort = port
	}

	// Set GATEWAY_PORT env for variable expansion in configs/agents
	os.Setenv("GATEWAY_PORT", strconv.Itoa(gatewayPort))

	printBanner()

	// List mode (doesn't require API key)
	if listFlag {
		listAvailableAgents()
		return
	}

	// =============================================================================
	// STEP 1: AGENT SELECTION
	// =============================================================================

	var ac *AgentConfig
	var configData []byte
	var configSource string
	var createdNewConfig bool

mainSelectionLoop:
	for {
		if agentArg == "" {
			agents := discoverAgents()
			var agentNames []string
			var agentMenuItems []tui.MenuItem
			for _, k := range sortedKeys(agents) {
				if !strings.HasPrefix(k, "template") {
					agentNames = append(agentNames, k)
					agentCfg, _, loadErr := loadAgentConfig(k)
					displayName := k
					description := ""
					if loadErr == nil && agentCfg != nil {
						if agentCfg.Agent.DisplayName != "" {
							displayName = agentCfg.Agent.DisplayName
						}
						if agentCfg.Agent.Description != "" {
							description = agentCfg.Agent.Description
						}
					}
					agentMenuItems = append(agentMenuItems, tui.MenuItem{
						Label:       displayName,
						Description: description,
						Value:       k,
					})
				}
			}
			if len(agentNames) == 0 {
				printError("No agents found. Place agent YAML files in agents/ or ~/.config/context-gateway/agents/")
				os.Exit(1)
			}

			// Add exit option
			agentMenuItems = append(agentMenuItems, tui.MenuItem{
				Label: "✗ Exit",
				Value: "__exit__",
			})

			idx, selectErr := tui.SelectMenu("Step 1: Select Agent", agentMenuItems)
			if selectErr != nil {
				os.Exit(0)
			}

			if agentMenuItems[idx].Value == "__exit__" {
				os.Exit(0)
			}

			agentArg = agentNames[idx]
		}

		// Load agent config to determine provider
		var err error
		ac, _, err = loadAgentConfig(agentArg)
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

		// =============================================================================
		// STEP 2: CONFIG SELECTION
		// =============================================================================

	configSelectionLoop:
		for proxyMode != "skip" && configFlag == "" {
			configs := listAvailableConfigs()

			// Build menu: existing configs + create new + delete + back
			configMenuItems := []tui.MenuItem{}
			for _, c := range configs {
				desc := ""
				if isUserConfig(c) {
					desc = "custom"
				} else {
					desc = "predefined"
				}
				configMenuItems = append(configMenuItems, tui.MenuItem{Label: c, Description: desc, Value: c})
			}
			configMenuItems = append(configMenuItems, tui.MenuItem{
				Label:       "[+] Create new configuration",
				Description: "custom compression settings",
				Value:       "__create_new__",
			})
			// Edit available for all configs
			configMenuItems = append(configMenuItems, tui.MenuItem{
				Label:       "[✎] Edit configuration",
				Description: "modify any config",
				Value:       "__edit__",
			})
			// Delete only for custom configs
			if hasUserConfigs() {
				configMenuItems = append(configMenuItems, tui.MenuItem{
					Label:       "[-] Delete configuration",
					Description: "remove custom config",
					Value:       "__delete__",
				})
			}
			configMenuItems = append(configMenuItems, tui.MenuItem{
				Label: "← Back",
				Value: "__back__",
			})

			idx, selectErr := tui.SelectMenu("Step 2: Select Configuration", configMenuItems)
			if selectErr != nil {
				os.Exit(0)
			}

			selectedValue := configMenuItems[idx].Value

			if selectedValue == "__back__" {
				// Go back to Step 1
				agentArg = ""                 // Reset agent selection
				fmt.Print("\033[1A\033[2K\r") // Clear confirmation line
				continue mainSelectionLoop
			}

			if selectedValue == "__delete__" {
				// Show delete menu
				deleteConfig()
				fmt.Print("\033[1A\033[2K\r") // Clear confirmation line
				continue configSelectionLoop
			}

			if selectedValue == "__edit__" {
				// Show edit menu
				editConfig(agentArg)
				fmt.Print("\033[1A\033[2K\r") // Clear confirmation line
				continue configSelectionLoop
			}

			if selectedValue == "__create_new__" {
				// User chose to create new config - go to Step 3
				configFlag = runConfigCreationWizard(agentArg, ac)
				if configFlag == "__back__" {
					configFlag = "" // Reset and loop back to config selection
					// Clear the "← Back" confirmation line before re-showing menu
					fmt.Print("\033[1A\033[2K\r")
					continue configSelectionLoop
				}
				if configFlag == "" {
					os.Exit(0) // User cancelled
				}
				createdNewConfig = true // Config wizard already handled API key/auth setup
			} else {
				configFlag = configs[idx]
			}
			break configSelectionLoop
		}
		break mainSelectionLoop
	}

	if proxyMode != "skip" && configFlag != "" {
		var configErr error
		configData, configSource, configErr = resolveConfig(configFlag)
		if configErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", configErr)
			os.Exit(1)
		}
	}

	// =============================================================================
	// STEP 4: API KEY SETUP (if needed, skip if wizard handled it)
	// =============================================================================

	if !createdNewConfig {
		if !setupAnthropicAPIKey(agentArg) {
			os.Exit(1)
		}
	}

	// =============================================================================
	// STEP 5: START GATEWAY
	// =============================================================================

	// Export agent environment variables
	exportAgentEnv(ac)

	// Start gateway as goroutine (not background process)
	// Each agent invocation gets its own session directory for logs
	var gw *gateway.Gateway
	var sessionDir string
	if proxyMode != "skip" && configData != nil {
		fmt.Println()
		printHeader("Starting Gateway")

		// gatewayPort was already found early (before agent config loading)
		// Verify it's still available (unlikely to change but be safe)
		if isPortInUse(gatewayPort) {
			fmt.Fprintf(os.Stderr, "Error: port %d is no longer available\n", gatewayPort)
			os.Exit(1)
		}

		// Create session directory for this agent invocation
		logsBase := logDir
		if logsBase == "" {
			logsBase = "logs"
		}
		sessionDir = createSessionDir(logsBase)

		// Export session log paths for this agent
		os.Setenv("SESSION_DIR", sessionDir)
		os.Setenv("SESSION_TELEMETRY_LOG", filepath.Join(sessionDir, "telemetry.jsonl"))
		os.Setenv("SESSION_COMPRESSION_LOG", filepath.Join(sessionDir, "compression.jsonl"))
		os.Setenv("SESSION_COMPACTION_LOG", filepath.Join(sessionDir, "compaction.jsonl"))
		os.Setenv("SESSION_TRAJECTORY_LOG", filepath.Join(sessionDir, "trajectory.json"))
		os.Setenv("SESSION_GATEWAY_LOG", filepath.Join(sessionDir, "gateway.log"))

		printSuccess("Agent Session: " + filepath.Base(sessionDir))
		printInfo(fmt.Sprintf("Gateway port: %d", gatewayPort))

		// Save a copy of the config used for this session (do this regardless of gateway reuse)
		if sessionDir != "" && len(configData) > 0 {
			configCopy := filepath.Join(sessionDir, "config.yaml")
			if err := os.WriteFile(configCopy, configData, 0600); err == nil {
				printInfo("Config saved to: " + filepath.Base(sessionDir) + "/config.yaml")
			}
		}

		// Always start a new gateway for this terminal
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

		// Override port with the dynamically allocated port for this terminal
		cfg.Server.Port = gatewayPort

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
		if !waitForGateway(gatewayPort, 30*time.Second) {
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
			printSuccess(fmt.Sprintf("Gateway ready on port %d", gatewayPort))
		}

		// Log the config used for this session
		preemptive.LogSessionConfig(
			configFlag,
			configSource,
			cfg.Preemptive.Summarizer.Provider,
			cfg.Preemptive.Summarizer.Model,
		)
	} else if proxyMode == "skip" {
		printInfo("Skipping gateway (--proxy skip)")
	}

	// OpenClaw special handling
	var openclawCmd *exec.Cmd
	if agentArg == "openclaw" {
		fmt.Println()
		printHeader("Step 2: OpenClaw Model Selection")

		selectedModel := selectModelInteractive(ac)

		if proxyMode == "skip" {
			createOpenClawConfigDirect(selectedModel)
		} else {
			createOpenClawConfig(selectedModel, gatewayPort)
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

	// Clean up stale IDE lock files (only if truly stale)
	// Don't remove active lock files from running sessions
	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		lockFiles, _ := filepath.Glob(filepath.Join(homeDir, ".claude", "ide", "*.lock"))
		for _, f := range lockFiles {
			// Check if lock file is stale by verifying process exists
			if isLockFileStale(f) {
				_ = os.Remove(f)
			}
		}
	}

	// Build agent command (all args shell-quoted for bash -c safety)
	agentCmd := ac.Agent.Command.Run
	for _, arg := range ac.Agent.Command.Args {
		agentCmd += " " + shellQuote(arg)
	}
	for _, arg := range passthroughArgs {
		agentCmd += " " + shellQuote(arg)
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

// =============================================================================
// CONFIG CREATION WIZARD (Step 3)
// =============================================================================

// ConfigState holds the current config being edited
type ConfigState struct {
	Name             string
	Provider         tui.ProviderInfo
	Model            string
	APIKey           string
	UseSubscription  bool
	SlackEnabled     bool
	SlackConfigured  bool    // True if Slack credentials exist
	TriggerThreshold float64 // Context usage % to trigger summarization (1-99)
}

// runConfigCreationWizard runs the config creation with summary editor.
// Returns the config name or empty string if cancelled.
func runConfigCreationWizard(agentName string, ac *AgentConfig) string {
	state := &ConfigState{}

	// Set defaults: Claude Haiku with subscription
	state.Provider = tui.SupportedProviders[0] // anthropic
	state.Model = state.Provider.DefaultModel  // default to haiku
	state.UseSubscription = true
	state.APIKey = "${ANTHROPIC_API_KEY:-}"
	state.TriggerThreshold = 85.0 // Trigger at 85% context usage

	// Check if Slack is already configured
	state.SlackConfigured = os.Getenv("SLACK_BOT_TOKEN") != "" && os.Getenv("SLACK_CHANNEL_ID") != "" && isSlackHookInstalled()
	state.SlackEnabled = state.SlackConfigured

	// Generate default name
	timestamp := time.Now().Format("20060102")
	state.Name = fmt.Sprintf("custom_%s_%s", state.Provider.Name, timestamp)

	// Go straight to config editor with defaults
	return runConfigEditor(state, agentName)
}

// runConfigEditor shows config summary with editable sections
func runConfigEditor(state *ConfigState, agentName string) string {
	for {
		// Build menu
		authType := "subscription"
		if !state.UseSubscription {
			authType = "API key"
		}
		summarizerDesc := fmt.Sprintf("%s / %s / %s", state.Provider.DisplayName, state.Model, authType)
		triggerDesc := fmt.Sprintf("%.0f", state.TriggerThreshold)

		items := []tui.MenuItem{
			{Label: "Summarizer", Description: summarizerDesc, Value: "edit_summarizer"},
			{Label: "Trigger %", Description: triggerDesc, Value: "edit_trigger", Editable: true},
		}

		// Slack toggle (only for claude_code)
		if agentName == "claude_code" {
			slackStatus := "○ Disabled"
			if state.SlackEnabled {
				slackStatus = "● Enabled"
			}
			items = append(items, tui.MenuItem{
				Label:       "Slack Notifications",
				Description: slackStatus,
				Value:       "toggle_slack",
			})
		}

		// Config name (editable inline)
		configNameItem := tui.MenuItem{
			Label:       "Config Name",
			Description: state.Name,
			Value:       "edit_name",
			Editable:    true,
		}
		items = append(items, configNameItem)

		// Actions
		items = append(items,
			tui.MenuItem{Label: "✓ Save", Value: "save"},
			tui.MenuItem{Label: "← Back", Value: "back"},
		)

		idx, err := tui.SelectMenu("Create Configuration", items)
		if err != nil {
			return "__back__" // q/Esc goes back to config selection
		}

		// Check if editable items were changed (could happen even if user selects Save afterward)
		triggerEditedEmpty := false
		triggerInvalid := false
		for _, item := range items {
			if item.Value == "edit_name" && item.Editable && item.Description != state.Name {
				newName := item.Description
				state.Name = strings.ReplaceAll(newName, " ", "_")
				state.Name = strings.ReplaceAll(state.Name, "/", "_")
				// don't break; allow processing other editable fields too
			}
			if item.Value == "edit_trigger" && item.Editable {
				// empty description means user cleared the field
				if strings.TrimSpace(item.Description) == "" {
					triggerEditedEmpty = true
					continue
				}
				// menu shows the number without %; parse and validate
				if item.Description != fmt.Sprintf("%.0f", state.TriggerThreshold) {
					if v, err := strconv.ParseFloat(item.Description, 64); err == nil {
						if v >= 1 && v <= 99 {
							state.TriggerThreshold = v
						} else {
							triggerInvalid = true
						}
					} else {
						triggerInvalid = true
					}
				}
			}
		}

		switch items[idx].Value {
		case "edit_name":
			// Name already updated above, just re-render
			continue

		case "edit_summarizer":
			editSummarizer(state, agentName)

		case "toggle_slack":
			if !state.SlackEnabled {
				if state.SlackConfigured {
					state.SlackEnabled = true
				} else {
					slackConfig := promptSlackCredentials()
					if slackConfig.Enabled {
						if err := installClaudeCodeHooks(); err != nil {
							fmt.Printf("%s⚠%s Failed to install hooks: %v\n", tui.ColorYellow, tui.ColorReset, err)
						} else {
							state.SlackEnabled = true
							state.SlackConfigured = true
						}
					}
				}
			} else {
				state.SlackEnabled = false
			}

		case "save":
			if triggerEditedEmpty {
				fmt.Printf("%s⚠%s Trigger value cannot be empty. Please enter a number between 1 and 99.\n", tui.ColorYellow, tui.ColorReset)
				continue
			}
			if triggerInvalid {
				fmt.Printf("%s⚠%s Invalid trigger value. Please enter a number between 1 and 99.\n", tui.ColorYellow, tui.ColorReset)
				continue
			}
			return saveConfig(state)

		case "back":
			return "__back__"
		}
	}
}

// editTriggerThreshold prompts user for trigger threshold (1-99%)
// editTriggerThreshold removed — inline editing now supported in the menu

// editSummarizer opens the summarizer settings submenu
func editSummarizer(state *ConfigState, agentName string) {
	for {
		items := []tui.MenuItem{
			{Label: "Provider", Description: state.Provider.DisplayName, Value: "provider"},
			{Label: "Model", Description: state.Model, Value: "model"},
		}

		// Claude Code + Anthropic: auth handled by Claude Code CLI (no options needed)
		// All other cases: need API key
		if agentName == "claude_code" && state.Provider.Name == "anthropic" {
			items = append(items, tui.MenuItem{
				Label:       "Auth",
				Description: "handled by Claude Code",
				Value:       "__info__", // Not selectable
			})
		} else {
			// Need API key for all other combinations
			keyStatus := "not set"
			if os.Getenv(state.Provider.EnvVar) != "" {
				keyStatus = maskKey(os.Getenv(state.Provider.EnvVar))
			}
			items = append(items, tui.MenuItem{
				Label:       "API Key",
				Description: keyStatus,
				Value:       "apikey",
			})
		}

		items = append(items, tui.MenuItem{Label: "← Back", Value: "back"})

		idx, err := tui.SelectMenu("Summarizer Settings", items)
		if err != nil || items[idx].Value == "back" {
			return
		}

		switch items[idx].Value {
		case "provider":
			selectProvider(state, agentName)

		case "model":
			selectModel(state)

		case "auth":
			selectAuth(state)

		case "apikey":
			promptAndSetAPIKey(state)
		}
	}
}

// selectProvider shows provider selection menu
func selectProvider(state *ConfigState, agentName string) {
	items := make([]tui.MenuItem, len(tui.SupportedProviders)+1)
	for i, p := range tui.SupportedProviders {
		items[i] = tui.MenuItem{
			Label:       p.DisplayName,
			Description: p.EnvVar,
			Value:       p.Name,
		}
	}
	items[len(tui.SupportedProviders)] = tui.MenuItem{Label: "← Back", Value: "back"}

	idx, err := tui.SelectMenu("Select Provider", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	state.Provider = tui.SupportedProviders[idx]
	state.Model = state.Provider.DefaultModel
	// Reset API key when provider changes
	state.APIKey = "${" + state.Provider.EnvVar + "}"
	// Only Claude Code agent + Anthropic can use subscription auth
	if agentName == "claude_code" && state.Provider.Name == "anthropic" {
		state.UseSubscription = true
	} else {
		state.UseSubscription = false // All other combinations need API key
	}
}

// selectModel shows model selection menu
func selectModel(state *ConfigState) {
	items := make([]tui.MenuItem, len(state.Provider.Models)+1)
	for i, m := range state.Provider.Models {
		desc := ""
		if m == state.Provider.DefaultModel {
			desc = "recommended"
		}
		items[i] = tui.MenuItem{Label: m, Description: desc, Value: m}
	}
	items[len(state.Provider.Models)] = tui.MenuItem{Label: "← Back", Value: "back"}

	idx, err := tui.SelectMenu("Select Model", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	state.Model = items[idx].Value
}

// selectAuth shows auth method selection
func selectAuth(state *ConfigState) {
	items := []tui.MenuItem{
		{Label: "Subscription", Description: "claude code --login", Value: "subscription"},
		{Label: "API Key", Description: "your own key", Value: "api_key"},
		{Label: "← Back", Value: "back"},
	}

	idx, err := tui.SelectMenu("Authentication", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	state.UseSubscription = (items[idx].Value == "subscription")
	if state.UseSubscription {
		state.APIKey = "${ANTHROPIC_API_KEY:-}"
	}
}

// promptAndSetAPIKey prompts for API key
func promptAndSetAPIKey(state *ConfigState) {
	existingKey := os.Getenv(state.Provider.EnvVar)
	if existingKey != "" {
		items := []tui.MenuItem{
			{Label: "Use existing", Description: maskKey(existingKey), Value: "yes"},
			{Label: "Enter new", Value: "no"},
			{Label: "← Back", Value: "back"},
		}
		idx, err := tui.SelectMenu(state.Provider.EnvVar, items)
		if err != nil || items[idx].Value == "back" {
			return
		}
		if items[idx].Value == "yes" {
			state.APIKey = "${" + state.Provider.EnvVar + "}"
			return
		}
	}

	fmt.Printf("\n  Get key at: %s\n", getProviderKeyURL(state.Provider.Name))
	enteredKey := tui.PromptPassword(fmt.Sprintf("Enter %s: ", state.Provider.EnvVar))
	if enteredKey == "" {
		return
	}

	if !validateAPIKeyFormat(state.Provider.Name, enteredKey) {
		fmt.Printf("%s⚠%s Key format looks unusual\n", tui.ColorYellow, tui.ColorReset)
	}

	os.Setenv(state.Provider.EnvVar, enteredKey)

	items := []tui.MenuItem{
		{Label: "Yes, save permanently", Value: "yes"},
		{Label: "No, session only", Value: "no"},
	}
	idx, _ := tui.SelectMenu("Save API key?", items)
	if idx == 0 {
		persistCredential(state.Provider.EnvVar, enteredKey, ScopeGlobal)
		fmt.Printf("%s✓%s Saved\n", tui.ColorGreen, tui.ColorReset)
	}
	state.APIKey = "${" + state.Provider.EnvVar + "}"
}

// saveConfig saves the config to disk and returns its name
func saveConfig(state *ConfigState) string {
	configContent := generateCustomConfigYAML(state.Name, state.Provider.Name, state.Model, state.APIKey, state.SlackEnabled, state.TriggerThreshold)

	configDir := filepath.Join(os.Getenv("HOME"), ".config", "context-gateway", "configs")
	if err := os.MkdirAll(configDir, 0750); err != nil {
		printError(fmt.Sprintf("Failed to create config directory: %v", err))
		return ""
	}

	configPath := filepath.Join(configDir, state.Name+".yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		printError(fmt.Sprintf("Failed to write config: %v", err))
		return ""
	}

	fmt.Printf("\n%s✓%s Config saved: %s\n", tui.ColorGreen, tui.ColorReset, configPath)
	return state.Name
}

// generateCustomConfigYAML generates a gateway config YAML.
func generateCustomConfigYAML(name, provider, model, apiKey string, enableSlack bool, triggerThreshold float64) string {
	slackEnabled := "false"
	if enableSlack {
		slackEnabled = "true"
	}

	// Get provider endpoint
	var endpoint string
	switch provider {
	case "anthropic":
		endpoint = "https://api.anthropic.com/v1/messages"
	case "gemini":
		endpoint = "https://generativelanguage.googleapis.com/v1beta/models/" + model + ":generateContent"
	case "openai":
		endpoint = "https://api.openai.com/v1/chat/completions"
	}

	return fmt.Sprintf(`# =============================================================================
# Context Gateway - Custom Configuration
# =============================================================================
# Generated by Context Gateway wizard
# Name: %s
# =============================================================================

metadata:
  name: "%s"
  description: "Custom compression settings"
  strategy: "passthrough"

server:
  port: ${GATEWAY_PORT:-18080}
  read_timeout: 30s
  write_timeout: 1000s

urls:
  gateway: "http://localhost:${GATEWAY_PORT:-18080}"

providers:
  %s:
    api_key: "%s"
    model: "%s"

preemptive:
  enabled: true
  trigger_threshold: %.1f
  add_response_headers: true
  log_dir: "${SESSION_DIR:-logs}"
  compaction_log_path: "${SESSION_COMPACTION_LOG:-logs/compaction.jsonl}"
  
  summarizer:
    provider: "%s"
    endpoint: "%s"
    max_tokens: 4096
    timeout: 60s
    token_estimate_ratio: 4
  
  session:
    summary_ttl: 3h
    hash_message_count: 3

pipes:
  tool_output:
    enabled: false
  tool_discovery:
    enabled: false

store:
  type: "memory"
  ttl: 1h

notifications:
  slack:
    enabled: %s

monitoring:
  log_level: "info"
  log_format: "console"
  log_output: "stdout"
  telemetry_enabled: true
  telemetry_path: "${SESSION_TELEMETRY_LOG:-logs/telemetry.jsonl}"
  compression_log_path: "${SESSION_COMPRESSION_LOG:-logs/compression.jsonl}"
`, name, name, provider, apiKey, model, triggerThreshold, provider, endpoint, slackEnabled)
}

// Helper functions for config wizard
func getProviderKeyURL(provider string) string {
	switch provider {
	case "anthropic":
		return "https://console.anthropic.com/settings/keys"
	case "gemini":
		return "https://aistudio.google.com/apikey"
	case "openai":
		return "https://platform.openai.com/api-keys"
	default:
		return ""
	}
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

func validateAPIKeyFormat(provider, key string) bool {
	switch provider {
	case "anthropic":
		return strings.HasPrefix(key, "sk-ant-")
	case "openai":
		return strings.HasPrefix(key, "sk-")
	case "gemini":
		return len(key) > 20 // Gemini keys are long random strings
	default:
		return true
	}
}

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
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/health", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// findAvailablePort finds the first available port in the range [basePort, basePort+maxPorts).
// Returns the port number and true if found, or 0 and false if all ports are in use.
func findAvailablePort(basePort, maxPorts int) (int, bool) {
	for i := 0; i < maxPorts; i++ {
		port := basePort + i
		if !checkGatewayRunning(port) && !isPortInUse(port) {
			return port, true
		}
	}
	return 0, false
}

// isPortInUse checks if a port is in use (by any process, not just our gateway).
func isPortInUse(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 500*time.Millisecond)
	if err != nil {
		return false // Port is free
	}
	conn.Close()
	return true // Port is in use
}

// stopGateway stops a running gateway by finding and killing the process.
// nolint:unused // Reserved for future terminal cleanup functionality
func stopGateway(port int) bool {
	// Find process using the port and kill it
	// #nosec G204 -- port is an integer, not user input
	cmd := exec.Command("lsof", "-ti", fmt.Sprintf(":%d", port))
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	pids := strings.Fields(strings.TrimSpace(string(output)))
	for _, pid := range pids {
		// #nosec G204 -- pid comes from lsof output
		killCmd := exec.Command("kill", "-9", pid)
		_ = killCmd.Run()
	}

	// Wait for port to be free
	for i := 0; i < 10; i++ {
		if !checkGatewayRunning(port) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return !checkGatewayRunning(port)
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

// deleteConfig shows a menu to select and delete a user config
func deleteConfig() {
	userConfigs := listUserConfigs()
	if len(userConfigs) == 0 {
		fmt.Printf("%s[INFO]%s No custom configurations to delete\n", tui.ColorYellow, tui.ColorReset)
		return
	}

	// Build menu
	items := []tui.MenuItem{}
	for _, c := range userConfigs {
		items = append(items, tui.MenuItem{Label: c, Value: c})
	}
	items = append(items, tui.MenuItem{Label: "← Cancel", Value: "__cancel__"})

	idx, err := tui.SelectMenu("Delete Configuration", items)
	if err != nil || items[idx].Value == "__cancel__" {
		return
	}

	configName := items[idx].Value

	// Confirm deletion
	confirmItems := []tui.MenuItem{
		{Label: "Yes, delete " + configName, Value: "yes"},
		{Label: "No, cancel", Value: "no"},
	}
	confirmIdx, confirmErr := tui.SelectMenu("Are you sure?", confirmItems)
	if confirmErr != nil || confirmItems[confirmIdx].Value == "no" {
		return
	}

	// Delete the config
	homeDir, _ := os.UserHomeDir()
	path := filepath.Join(homeDir, ".config", "context-gateway", "configs", configName+".yaml")
	if err := os.Remove(path); err != nil {
		fmt.Printf("%s[ERROR]%s Failed to delete: %v\n", tui.ColorRed, tui.ColorReset, err)
	} else {
		fmt.Printf("%s✓%s Deleted: %s\n", tui.ColorGreen, tui.ColorReset, configName)
	}
}

// editConfig shows a menu to select and edit a user config
func editConfig(agentName string) {
	configs := listAvailableConfigs()
	if len(configs) == 0 {
		fmt.Printf("%s[INFO]%s No configurations to edit\n", tui.ColorYellow, tui.ColorReset)
		return
	}

	// Build menu - show all configs with predefined/custom label
	items := []tui.MenuItem{}
	for _, c := range configs {
		desc := ""
		if isUserConfig(c) {
			desc = "custom"
		} else {
			desc = "predefined"
		}
		items = append(items, tui.MenuItem{Label: c, Description: desc, Value: c})
	}
	items = append(items, tui.MenuItem{Label: "← Cancel", Value: "__cancel__"})

	idx, err := tui.SelectMenu("Edit Configuration", items)
	if err != nil || items[idx].Value == "__cancel__" {
		return
	}

	configName := items[idx].Value
	isPredefined := !isUserConfig(configName)

	// Load the config and convert to state
	state := loadConfigToState(configName)
	if state == nil {
		fmt.Printf("%s[ERROR]%s Failed to load config: %s\n", tui.ColorRed, tui.ColorReset, configName)
		return
	}

	// If editing predefined config, save as a new custom config with different name
	if isPredefined {
		timestamp := time.Now().Format("20060102")
		state.Name = fmt.Sprintf("%s_custom_%s", configName, timestamp)
		fmt.Printf("%s[INFO]%s Editing predefined config - will save as: %s\n", tui.ColorYellow, tui.ColorReset, state.Name)
	}

	// Run config editor
	result := runConfigEditor(state, agentName)
	if result != "" && result != "__back__" {
		fmt.Printf("%s✓%s Config saved: %s\n", tui.ColorGreen, tui.ColorReset, result)
	}
}

// loadConfigToState loads a config file and converts it to ConfigState for editing
func loadConfigToState(configName string) *ConfigState {
	// Try to load from multiple locations
	var data []byte
	var err error

	// First try user config dir
	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		path := filepath.Join(homeDir, ".config", "context-gateway", "configs", configName+".yaml")
		data, err = os.ReadFile(path)
	}

	// Then try local configs dir
	if err != nil {
		path := filepath.Join("configs", configName+".yaml")
		data, err = os.ReadFile(path)
	}

	// Finally try embedded configs
	if err != nil {
		data, err = configsFS.ReadFile("configs/" + configName + ".yaml")
	}

	if err != nil {
		return nil
	}

	// Parse YAML to extract values
	var cfg map[string]interface{}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil
	}

	state := &ConfigState{
		Name: configName,
	}

	// Extract provider info
	if providers, ok := cfg["providers"].(map[string]interface{}); ok {
		for providerName, providerData := range providers {
			if pd, ok := providerData.(map[string]interface{}); ok {
				// Find matching provider
				for _, p := range tui.SupportedProviders {
					if p.Name == providerName {
						state.Provider = p
						break
					}
				}
				if model, ok := pd["model"].(string); ok {
					state.Model = model
				}
				if apiKey, ok := pd["api_key"].(string); ok {
					state.APIKey = apiKey
					// Check if using subscription (env var) or explicit key
					state.UseSubscription = strings.Contains(apiKey, "${") || apiKey == ""
				}
			}
			break // Only process first provider
		}
	}

	// Extract slack settings
	if notifications, ok := cfg["notifications"].(map[string]interface{}); ok {
		if slack, ok := notifications["slack"].(map[string]interface{}); ok {
			if enabled, ok := slack["enabled"].(bool); ok {
				state.SlackEnabled = enabled
				state.SlackConfigured = enabled
			}
		}
	}

	// Set defaults if not found
	if state.Provider.Name == "" {
		state.Provider = tui.SupportedProviders[0] // anthropic
	}
	if state.Model == "" {
		state.Model = state.Provider.DefaultModel
	}

	// Extract trigger threshold from preemptive section
	if preemptive, ok := cfg["preemptive"].(map[string]interface{}); ok {
		if threshold, ok := preemptive["trigger_threshold"].(float64); ok {
			state.TriggerThreshold = threshold
		}
	}
	// Default to 85% if not found
	if state.TriggerThreshold == 0 {
		state.TriggerThreshold = 85.0
	}

	return state
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

// shellQuote returns a shell-safe single-quoted version of arg.
// Used to safely embed user-provided pass-through arguments into a bash -c command string.
func shellQuote(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
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
