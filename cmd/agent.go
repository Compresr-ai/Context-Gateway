package main

import (
	"bufio"
	"context"
	"fmt"
	stdlog "log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/compresr/context-gateway/internal/compresr"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/compresr/context-gateway/internal/preemptive"
	"github.com/compresr/context-gateway/internal/tui"
	"github.com/compresr/context-gateway/internal/utils"
)

// runAgentCommand is the main entry point for the agent launcher.
// It replaces start_agent.sh with native Go.
func runAgentCommand(args []string) {
	// Parse flags
	var (
		configFlag      string
		showConfigMenu  bool
		debugFlag       bool
		portFlag        string
		proxyMode       string
		logDir          string
		listFlag        bool
		resetAPIKeyFlag bool
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
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				configFlag = args[i+1]
				i += 2
			} else {
				// -c without value → show config management menu
				showConfigMenu = true
				i++
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
		case "--reset-api-key":
			resetAPIKeyFlag = true
			i++
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

	// Handle --reset-api-key flag
	if resetAPIKeyFlag {
		printBanner()
		if !resetCompresrAPIKey() {
			os.Exit(1)
		}
		// Continue with normal flow after reset
	}

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
	_ = os.Setenv("GATEWAY_PORT", strconv.Itoa(gatewayPort))

	printBanner()

	// List mode (doesn't require API key)
	if listFlag {
		listAvailableAgents()
		return
	}

	// Handle --config list (doesn't require API key)
	if configFlag == "list" {
		listAvailableConfigsPrint()
		return
	}

	// Check if this is first run (no Compresr API key set)
	if !isCompresrAPIKeySet() {
		if !runCompresrOnboarding() {
			// User cancelled onboarding
			os.Exit(0)
		}
	}

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

			idx, selectErr := tui.SelectMenu("Select Agent", agentMenuItems)
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

		if proxyMode != "skip" && showConfigMenu && configFlag == "" {
		configSelectionLoop:
			for {
				configs := listAvailableConfigs()

				// Build menu: existing configs + create new + edit + delete + back
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
				configMenuItems = append(configMenuItems, tui.MenuItem{
					Label:       "[\u270e] Edit configuration",
					Description: "modify any config",
					Value:       "__edit__",
				})
				if hasUserConfigs() {
					configMenuItems = append(configMenuItems, tui.MenuItem{
						Label:       "[-] Delete configuration",
						Description: "remove custom config",
						Value:       "__delete__",
					})
				}
				configMenuItems = append(configMenuItems, tui.MenuItem{
					Label: "\u2190 Back",
					Value: "__back__",
				})

				idx, selectErr := tui.SelectMenu("Select Configuration", configMenuItems)
				if selectErr != nil {
					os.Exit(0)
				}

				selectedValue := configMenuItems[idx].Value

				if selectedValue == "__back__" {
					agentArg = ""
					fmt.Print("\033[1A\033[2K\r")
					continue mainSelectionLoop
				}

				if selectedValue == "__delete__" {
					deleteConfig()
					fmt.Print("\033[1A\033[2K\r")
					continue configSelectionLoop
				}

				if selectedValue == "__edit__" {
					editConfig(agentArg)
					fmt.Print("\033[1A\033[2K\r")
					continue configSelectionLoop
				}

				if selectedValue == "__create_new__" {
					configFlag = runConfigCreationWizard(agentArg, ac)
					if configFlag == "__back__" {
						configFlag = ""
						fmt.Print("\033[1A\033[2K\r")
						continue configSelectionLoop
					}
					if configFlag == "" {
						os.Exit(0)
					}
					createdNewConfig = true
				} else {
					configFlag = configs[idx]
				}
				break configSelectionLoop
			}
		}

		// Default path: no -c flag → auto-use fast_setup
		if proxyMode != "skip" && configFlag == "" {
			configFlag = "fast_setup"
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

	if !createdNewConfig {
		if !setupAnthropicAPIKey(agentArg) {
			os.Exit(1)
		}
	}

	// Export agent environment variables
	exportAgentEnv(ac)

	// Start gateway as goroutine (not background process)
	// Each agent invocation gets its own session directory for logs
	var gw *gateway.Gateway
	var sessionDir string
	var statusBar *tui.StatusBar
	if proxyMode != "skip" && configData != nil {
		// gatewayPort was already found early (before agent config loading)
		// Verify it's still available (unlikely to change but be safe)
		if isPortInUse(gatewayPort) {
			fmt.Fprintf(os.Stderr, "Error: port %d is no longer available\n", gatewayPort)
			os.Exit(1)
		}

		// Parse config early to check telemetry_enabled before setting env vars
		earlyConfig, earlyErr := config.LoadFromBytes(configData)
		if earlyErr != nil {
			fmt.Fprintf(os.Stderr, "Error loading config '%s': %v\n", configSource, earlyErr)
			os.Exit(1)
		}

		// DEBUG: Print API key resolution
		if debugFlag {
			fmt.Printf("\n[DEBUG] Config loaded from: %s\n", configSource)
			fmt.Printf("[DEBUG] GEMINI_API_KEY env: %q\n", os.Getenv("GEMINI_API_KEY"))
			for name, p := range earlyConfig.Providers {
				fmt.Printf("[DEBUG] Provider %s: model=%s, key_len=%d\n", name, p.Model, len(p.APIKey))
			}
			resolved := earlyConfig.ResolvePreemptiveProvider()
			fmt.Printf("[DEBUG] Resolved summarizer: provider=%s, model=%s, key_len=%d\n",
				resolved.Summarizer.Provider, resolved.Summarizer.Model, len(resolved.Summarizer.APISecret))
		}

		telemetryEnabled := earlyConfig.Monitoring.TelemetryEnabled

		// Create session directory for this agent invocation
		logsBase := logDir
		if logsBase == "" {
			logsBase = "logs"
		}
		sessionDir = createSessionDir(logsBase)

		// Export session log paths for this agent (only if telemetry is enabled)
		_ = os.Setenv("SESSION_DIR", sessionDir)
		if telemetryEnabled {
			_ = os.Setenv("SESSION_TELEMETRY_LOG", filepath.Join(sessionDir, "telemetry.jsonl"))
			_ = os.Setenv("SESSION_COMPRESSION_LOG", filepath.Join(sessionDir, "tool_output_compression.jsonl"))
			_ = os.Setenv("SESSION_TOOL_DISCOVERY_LOG", filepath.Join(sessionDir, "tool_discovery.jsonl"))
			_ = os.Setenv("SESSION_COMPACTION_LOG", filepath.Join(sessionDir, "history_compaction.jsonl"))
			_ = os.Setenv("SESSION_TRAJECTORY_LOG", filepath.Join(sessionDir, "trajectory.json"))
		}
		_ = os.Setenv("SESSION_GATEWAY_LOG", filepath.Join(sessionDir, "gateway.log"))

		// Re-apply session env overrides to the early config now that env vars are set
		earlyConfig.ApplySessionEnvOverrides()

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
			// #nosec G304 -- env-configured log path
			if f, err := os.OpenFile(gwLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
				gatewayLogFile = f
				gatewayLogOutput = gwLogPath
				defer func() { _ = f.Close() }()
			}
		}
		// If we can't open a log file, discard all gateway logs
		if gatewayLogFile == nil {
			devNull, err := os.Open(os.DevNull)
			if err == nil {
				gatewayLogFile = devNull
				defer func() { _ = devNull.Close() }()
			}
		}
		setupLogging(debugFlag, gatewayLogFile)

		// Redirect Go's standard library log (used by net/http server errors)
		// to the gateway log file to prevent stderr pollution of the agent's terminal.
		if gatewayLogFile != nil {
			stdlog.SetOutput(gatewayLogFile)
		}

		// Reuse the config we already parsed earlier
		cfg := earlyConfig

		// Propagate agent flags to gateway config
		// This allows the proxy to know about flags like --dangerously-skip-permissions
		cfg.AgentFlags = config.NewAgentFlags(ac.Agent.Name, passthroughArgs)

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

		// Attach embedded React dashboard SPA
		if dashFS, err := getDashboardFS(); err == nil {
			gw.SetDashboardFS(dashFS)
		}

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

		// Display usage status bar (if API key is configured)
		statusBar = showGatewayStatusBar(gatewayPort, filepath.Base(sessionDir), gw.CostTracker())
		if gw != nil && statusBar != nil {
			gw.SetStatusReporter(statusBar)
			statusBar.SetSavingsSource(gw.SavingsTracker())
		}

		// Log the config used for this session (use resolved config to get inherited model)
		resolvedPreemptive := cfg.ResolvePreemptiveProvider()
		summModel, summProvider := resolvedPreemptive.Summarizer.EffectiveModelAndProvider()
		preemptive.LogSessionConfig(
			configFlag,
			configSource,
			summProvider,
			summModel,
		)
	} else if proxyMode == "skip" {
		printInfo("Skipping gateway (--proxy skip)")
	}

	// OpenClaw special handling
	// Model selection is delegated to OpenClaw's own TUI — we just use the default
	// model for the proxy config. Calling selectModelInteractive here caused a freeze
	// because the status bar was already running and competing for terminal control.
	var openclawCmd *exec.Cmd
	if agentArg == "openclaw" {
		selectedModel := ac.Agent.DefaultModel

		if proxyMode == "skip" {
			createOpenClawConfigDirect(selectedModel)
		} else {
			createOpenClawConfig(selectedModel, gatewayPort)
		}

		openclawCmd = startOpenClawGateway()
	}

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
		agentCmd += " " + utils.ShellQuote(arg)
	}
	for _, arg := range passthroughArgs {
		agentCmd += " " + utils.ShellQuote(arg)
	}

	// Launch agent as child process

	cmd := exec.Command("bash", "-c", agentCmd) // #nosec G204 -- user-selected agent command
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

	// Reset terminal title
	tui.ClearTerminalTitle()

	// Show session summary with actual spend
	fmt.Println()
	if statusBar != nil {
		statusBar.StopAutoRefresh()
	}
	printHeader("Session Summary")
	if statusBar != nil {
		_ = statusBar.Refresh() // Get latest credits
		statusBar.RenderSummary()
	} else if gw != nil {
		cost := gw.CostTracker().GetGlobalCost()
		printInfo(fmt.Sprintf("Session spend: $%.4f", cost))
	}

	if sessionDir != "" {
		fmt.Printf("\033[0;36mSession logs: %s\033[0m\n\n", sessionDir)
	}
}

// showGatewayStatusBar displays the usage/balance status bar at startup
// and sets the terminal title with persistent status info.
func showGatewayStatusBar(port int, session string, costSource tui.CostSource) *tui.StatusBar {
	apiKey := os.Getenv("COMPRESR_API_KEY")
	if apiKey == "" {
		// No API key configured, set basic title only
		tui.SetTerminalTitle(fmt.Sprintf("Context Gateway | :%d", port))
		return nil
	}

	baseURL := os.Getenv("COMPRESR_BASE_URL")
	if baseURL == "" {
		baseURL = config.DefaultCompresrAPIBaseURL
	}

	client := compresr.NewClient(baseURL, apiKey)
	statusBar := tui.NewStatusBar(client)
	statusBar.SetDashboardPort(port)
	statusBar.EnableFooter(true)

	// Wire cost source before rendering so the box includes local spend
	if costSource != nil {
		statusBar.SetCostSource(costSource)
	}

	// Fetch and display status (non-blocking on error)
	if err := statusBar.Refresh(); err != nil {
		// API failed — still render cost box if available
		statusBar.RenderBox()
		tui.SetTerminalTitle(fmt.Sprintf("Context Gateway | :%d", port))
		return statusBar
	}

	statusBar.RenderBox()

	// Set terminal title with persistent status info
	tui.SetTerminalTitle(statusBar.FormatTitleStatus(port, session))
	statusBar.StartAutoRefresh(tui.AutoRefreshInterval)
	return statusBar
}
