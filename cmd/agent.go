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
	"time"

	"github.com/compresr/context-gateway/internal/compresr"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/dashboard"
	"github.com/compresr/context-gateway/internal/gateway"
	"github.com/compresr/context-gateway/internal/plugins"
	"github.com/compresr/context-gateway/internal/postsession"
	"github.com/compresr/context-gateway/internal/preemptive"
	"github.com/compresr/context-gateway/internal/tui"
	"github.com/compresr/context-gateway/internal/utils"
)

// runAgentCommand is the main entry point for the agent launcher.
// It replaces start_agent.sh with native Go.
func runAgentCommand(args []string) {
	// Load .env files from standard locations (for COMPRESR_API_KEY, etc.)
	loadEnvFiles()

	// Start update check in background immediately so the network request
	// runs in parallel with flag parsing and port discovery.
	showUpdateNotification := CheckForUpdatesAsync()

	// Parse flags
	var (
		configFlag      string
		showConfigMenu  bool
		debugFlag       bool
		portFlag        string
		proxyMode       string
		listFlag        bool
		resetAPIKeyFlag bool
		agentArg        string
		passthroughArgs []string
		daemonFlag      bool
		stopFlag        bool
		sessionDirFlag  string
		sessionNameFlag string
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
		case "--stop":
			stopFlag = true
			i++
		case "--daemon":
			daemonFlag = true
			i++
		case "--session":
			if i+1 < len(args) {
				sessionDirFlag = args[i+1]
				i += 2
			} else {
				i++
			}
		case "-n", "--name":
			if i+1 < len(args) {
				sessionNameFlag = args[i+1]
				i += 2
			} else {
				fmt.Fprintln(os.Stderr, "Error: --name requires a value")
				os.Exit(1)
			}
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
		case "-a", "--agent":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				agentArg = args[i+1]
				i += 2
			} else {
				fmt.Fprintln(os.Stderr, "Error: --agent requires a value")
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
				_, _ = os.Stderr.WriteString("Error: unknown option: " + strconv.Quote(args[i]) + "\n")
				os.Exit(1)
			}
			agentArg = args[i]
			i++
		}
	}

	// Handle --stop flag - stop a running background gateway
	if stopFlag {
		pidFile := filepath.Join(os.TempDir(), "context-gateway.pid")
		portFile := filepath.Join(os.TempDir(), "context-gateway.port")
		pidBytes, err := os.ReadFile(filepath.Clean(pidFile)) // #nosec G304 -- reading pid file from temp dir
		if err != nil {
			fmt.Println("No gateway is running in background mode.")
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
		if err != nil {
			fmt.Println("Invalid PID file.")
			_ = os.Remove(pidFile)
			_ = os.Remove(portFile)
			return
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			fmt.Println("Gateway process not found.")
			_ = os.Remove(pidFile)
			_ = os.Remove(portFile)
			return
		}

		// Check if process is actually alive (FindProcess always succeeds on Unix)
		if !isProcessRunning(process) {
			fmt.Println("Gateway process not running (stale PID file).")
			_ = os.Remove(pidFile)
			_ = os.Remove(portFile)
			return
		}

		// Remove port file FIRST to signal plugins to restore config
		_ = os.Remove(portFile)

		// Wait for plugins to detect and restore configs before stopping gateway.
		// Plugin health check runs every 1s, 2s gives time to detect + restore.
		fmt.Println("Waiting for plugins to restore configs...")
		time.Sleep(2 * time.Second)

		if err := terminateProcess(process); err != nil {
			fmt.Printf("Failed to stop gateway: %v\n", err)
			// Only remove PID file if it still contains the same PID we read
			if currentBytes, readErr := os.ReadFile(filepath.Clean(pidFile)); readErr == nil {
				if currentPid, parseErr := strconv.Atoi(strings.TrimSpace(string(currentBytes))); parseErr == nil && currentPid == pid {
					_ = os.Remove(pidFile)
				}
			}
			return
		}

		// Wait for process to actually exit (up to 15s: 2s plugin wait + 10s shutdown + buffer)
		exited := false
		for i := 0; i < 150; i++ { // 150 * 100ms = 15s max
			time.Sleep(100 * time.Millisecond)
			if !isProcessRunning(process) {
				exited = true
				break
			}
		}

		if exited {
			printSuccess("Gateway stopped.")
		} else {
			printWarn("Gateway may still be shutting down.")
		}

		// Only remove PID file if it still contains the PID we stopped
		// (avoids race condition if new gateway started during shutdown)
		if currentBytes, readErr := os.ReadFile(filepath.Clean(pidFile)); readErr == nil {
			if currentPid, parseErr := strconv.Atoi(strings.TrimSpace(string(currentBytes))); parseErr == nil && currentPid == pid {
				_ = os.Remove(pidFile)
			}
		}
		return
	}

	// Handle --reset-api-key flag
	if resetAPIKeyFlag {
		printBanner()
		if !resetCompresrAPIKey() {
			os.Exit(1)
		}
		// Continue with normal flow after reset
	}

	// Find available port early so ${GATEWAY_PORT} expands correctly in agent configs
	// Port range: 18081-18090 (max 10 concurrent terminals; 18080 reserved for UI)
	basePort := config.DefaultGatewayBasePort
	maxPorts := config.MaxGatewayPorts

	var gatewayPort int

	if portFlag != "" {
		// User explicitly specified a port
		var err error
		gatewayPort, err = strconv.Atoi(portFlag)
		if err != nil || gatewayPort <= 0 || gatewayPort > 65535 {
			_, _ = os.Stderr.WriteString("Error: invalid port " + strconv.Quote(portFlag) + "\n")
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

	// Display update notification if a newer version is available.
	// showUpdateNotification waits up to 5s for the background check started
	// at the top of this function; by now it's usually already done.
	showUpdateNotification()

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
	var firstRun bool
	var gatewayStatus *compresr.GatewayStatus
	if !isCompresrAPIKeySet() {
		if !runCompresrOnboarding() {
			// User cancelled onboarding
			os.Exit(0)
		}
		firstRun = true
	} else {
		// API key is set - validate it early before showing agent menu
		var ok bool
		gatewayStatus, ok = validateCompresrAPIKeyEarly()
		if !ok {
			os.Exit(0)
		}
	}

	// Pre-start gateway so the dashboard is live while the user selects an agent.
	// Skipped when user explicitly chose a config (-c menu), is in daemon mode,
	// or when proxy is disabled.
	var previewGW *gateway.Gateway
	previewBrowserOpened := false
	if proxyMode != "skip" && !showConfigMenu && !daemonFlag {
		previewGW, previewBrowserOpened = startPreviewGateway(gatewayPort, debugFlag)
	}

	// Step 2: Show dashboard URL and account info (gateway is live above)
	showPreSessionDashboard(gatewayStatus)

	var ac *AgentConfig
	var configData []byte
	var configSource string

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

		// Install agent-specific plugins (slash commands, integrations)
		if installed, pluginErr := plugins.EnsurePluginsInstalled(ac.Agent.Name); pluginErr != nil {
			printWarn(fmt.Sprintf("Failed to install plugin: %v", pluginErr))
		} else if installed {
			printSuccess(fmt.Sprintf("Installed %s plugin", ac.Agent.Name))
		}

		// First-run experience: offer to configure or use defaults
		if firstRun && configFlag == "" && proxyMode != "skip" {
			firstRunItems := []tui.MenuItem{
				{Label: "Configure settings", Description: "customize compression, cost limits, and more", Value: "configure"},
				{Label: "Use defaults and start", Description: "recommended for most users", Value: "defaults"},
			}
			idx, selectErr := tui.SelectMenu("Getting Started", firstRunItems)
			if selectErr != nil {
				os.Exit(0)
			}
			if firstRunItems[idx].Value == "configure" {
				configFlag = runConfigCreationWizard(agentArg, ac)
				if configFlag == "__back__" || configFlag == "" {
					configFlag = ""
				}
			}
			firstRun = false
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

	// Export agent environment variables
	exportAgentEnv(ac)

	// Background mode: parent process should NOT start gateway
	// Only the daemon subprocess should start the gateway
	// This prevents duplicate logs when both parent and daemon initialize
	isBackgroundParent := ac.Agent.IsBackgroundMode() && !daemonFlag

	// Start gateway as goroutine (not background process)
	// Each agent invocation gets its own session directory for logs
	var gw *gateway.Gateway
	var sessionDir string
	var statusBar *tui.StatusBar
	if proxyMode != "skip" && configData != nil && !isBackgroundParent {

		// Parse config early to check telemetry_enabled before setting env vars
		earlyConfig, earlyErr := config.LoadFromBytes(configData)
		if earlyErr != nil {
			_, _ = os.Stderr.WriteString("Error loading config '" + configSource + "': " + earlyErr.Error() + "\n")
			os.Exit(1)
		}

		// DEBUG: Print API key resolution
		if debugFlag {
			fmt.Printf("\n[DEBUG] Config loaded from: %s\n", configSource)
			fmt.Printf("[DEBUG] GEMINI_API_KEY env: %q\n", os.Getenv("GEMINI_API_KEY"))
			for name, p := range earlyConfig.Providers {
				fmt.Printf("[DEBUG] Provider %s: model=%s, key_len=%d\n", name, p.Model, len(p.ProviderAuth))
			}
			resolved := earlyConfig.ResolvePreemptiveProvider()
			fmt.Printf("[DEBUG] Resolved summarizer: provider=%s, model=%s, key_len=%d\n",
				resolved.Summarizer.Provider, resolved.Summarizer.Model, len(resolved.Summarizer.ProviderKey))
		}

		telemetryEnabled := earlyConfig.Monitoring.TelemetryEnabled

		// Prompt for session name if not provided via flag or daemon mode
		if sessionNameFlag == "" && sessionDirFlag == "" {
			fmt.Printf("\r%sSession name%s (enter to skip): ", tui.ColorCyan, tui.ColorReset)
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				if name := strings.TrimSpace(scanner.Text()); name != "" {
					sessionNameFlag = name
				}
			}
		}

		// Prepare session path for lazy creation (directory created on first request)
		// This prevents empty session folders when gateway starts but receives no traffic
		if sessionDirFlag != "" {
			// Daemon mode: reuse session directory from parent process
			sessionDir = sessionDirFlag
		} else {
			sessionDir = prepareSessionPath("logs", agentArg, sessionNameFlag)
		}

		// Export session log paths for this agent (paths may not exist yet - lazy creation)
		// Files will be created when the session directory is initialized on first request
		_ = os.Setenv("SESSION_DIR", sessionDir)
		if telemetryEnabled {
			_ = os.Setenv("SESSION_TELEMETRY_LOG", filepath.Join(sessionDir, "telemetry.jsonl"))
			_ = os.Setenv("SESSION_COMPRESSION_LOG", filepath.Join(sessionDir, "tool_output_compression.jsonl"))
			_ = os.Setenv("SESSION_TOOL_DISCOVERY_LOG", filepath.Join(sessionDir, "tool_discovery.jsonl"))
			_ = os.Setenv("SESSION_TASK_OUTPUT_LOG", filepath.Join(sessionDir, "task_output"))
			_ = os.Setenv("SESSION_COMPACTION_LOG", filepath.Join(sessionDir, "history_compaction.jsonl"))
			_ = os.Setenv("SESSION_TRAJECTORY_LOG", filepath.Join(sessionDir, "trajectory.json"))
			_ = os.Setenv("SESSION_TOOLS_LOG", filepath.Join(sessionDir, "session_tools.json"))
			_ = os.Setenv("SESSION_STATS_LOG", filepath.Join(sessionDir, "session_stats.json"))
			_ = os.Setenv("SESSION_EXPAND_CALLS_LOG", filepath.Join(sessionDir, "expand_context_calls.jsonl"))
		}
		_ = os.Setenv("SESSION_GATEWAY_LOG", filepath.Join(sessionDir, "gateway.log"))

		// Re-apply session env overrides to the early config now that env vars are set
		earlyConfig.ApplySessionEnvOverrides()

		// Port displayed in status bar startup block below

		// Store config data for lazy session creation (config.yaml written when first request arrives)
		// Session directory is created now for gateway.log, but config.yaml is written lazily
		// This lets us see gateway startup logs while avoiding config files for sessions with no traffic
		lazyConfigData := configData

		// Create the session directory now (for gateway.log), but config.yaml is written lazily
		_ = os.MkdirAll(sessionDir, 0750) // #nosec G703 -- sessionDir is from temp + internal session name

		// Redirect ALL gateway logging to the session log file.
		// This prevents any zerolog output from polluting the agent's terminal.
		var gatewayLogFile *os.File
		gatewayLogOutput := os.DevNull
		if gwLogPath := os.Getenv("SESSION_GATEWAY_LOG"); gwLogPath != "" {
			// #nosec G304,G703 -- env-configured log path
			if f, err := os.OpenFile(gwLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
				gatewayLogFile = f
				gatewayLogOutput = gwLogPath
				defer func() { _ = f.Close() }()
			}
		}
		// If we can't open a log file, discard all gateway logs (use O_WRONLY for write access)
		if gatewayLogFile == nil {
			devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
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

		// Shutdown preview gateway now — kept alive until this point so the dashboard
		// stays accessible during agent selection and session name entry.
		// The port is freed immediately after Shutdown returns.
		if previewGW != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = previewGW.Shutdown(ctx)
			cancel()
			previewGW = nil
		}

		// Verify port is still available (preview just freed it; another process could have grabbed it)
		if isPortInUse(gatewayPort) {
			_, _ = os.Stderr.WriteString("Error: port " + strconv.Itoa(gatewayPort) + " is no longer available\n")
			os.Exit(1)
		}

		// Avoid re-opening the browser if the preview gateway already opened it
		dashboardAlreadyOpen := previewBrowserOpened || isDashboardRunning(config.DefaultDashboardPort)

		gw = gateway.New(cfg, configSource)
		gw.SetVersion(Version)

		// Configure lazy session creation (directory created on first LLM request).
		// Pass the agent name so the session is pre-registered in the dashboard
		// before any LLM requests arrive (needed for agents like Codex that wait
		// for user input before making their first request).
		if sessionDir != "" {
			gw.SetLazySession(sessionDir, lazyConfigData, ac.Agent.Name)
		}

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
				_, _ = os.Stderr.WriteString("Check logs: " + sessionDir + "\n")
			}

			fmt.Print("Continue anyway? [y/N] ")
			reader := bufio.NewReader(os.Stdin)
			resp, _ := reader.ReadString('\n')
			resp = strings.TrimSpace(strings.ToLower(resp))
			if resp != "y" && resp != "yes" {
				os.Exit(1)
			}
			printWarn("Continuing without healthy gateway...")
		}

		// Display usage status bar (if API key is configured)
		statusBar = showGatewayStatusBar(gatewayPort, filepath.Base(sessionDir), gw.CostTracker())
		if gw != nil && statusBar != nil {
			statusBar.SetSessionName(filepath.Base(sessionDir))
			statusBar.SetSavingsSource(gw.SavingsTracker())
		}

		// Register this instance in the shared dashboard registry.
		// Use the user-provided session name for the card title; fall back to agent type.
		registryName := filepath.Base(sessionDir)
		if registryName == "" || registryName == "." {
			registryName = ac.Agent.Name
		}
		dashboard.Register(gatewayPort, registryName, sessionDir)
		defer func() {
			dashboard.Deregister(gatewayPort)
			// Close the browser tab only when this was the last running gateway instance.
			if dashboard.ActiveCount() == 0 {
				closeDashboardBrowserTab(config.DefaultDashboardPort)
			}
		}()

		// Open dashboard in browser — skip in dev-frontend mode (Vite opens its own URL instead)
		if !dashboardAlreadyOpen && os.Getenv("CONTEXT_GATEWAY_DEV_FRONTEND") == "" {
			openBrowser(fmt.Sprintf("http://localhost:%d/dashboard/#/monitor", config.DefaultDashboardPort))
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

	displayName := ac.Agent.DisplayName
	if displayName == "" {
		displayName = ac.Agent.Name
	}

	// Clean up stale IDE lock files (only if truly stale)
	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		lockFiles, _ := filepath.Glob(filepath.Join(homeDir, ".claude", "ide", "*.lock"))
		for _, f := range lockFiles {
			if isLockFileStale(f) {
				_ = os.Remove(f)
			}
		}
	}

	// Run pre-run command if specified (e.g., start OpenClaw gateway)
	if len(ac.Agent.Command.PreRunCmd) > 0 {
		// Check if gateway is already running
		if checkGatewayRunning(18789) {
			printSuccess(fmt.Sprintf("%s internal gateway already running", displayName))
		} else {
			printStep(fmt.Sprintf("Starting %s internal gateway...", displayName))
			preRunCmd := exec.Command(ac.Agent.Command.PreRunCmd[0], ac.Agent.Command.PreRunCmd[1:]...) // #nosec G204 G702 -- pre_run_cmd from trusted agent config
			preRunCmd.Env = os.Environ()
			if ac.Agent.Command.PreRunBackground {
				// Start in background - don't wait for it
				preRunCmd.Stdout = nil
				preRunCmd.Stderr = nil
				if err := preRunCmd.Start(); err != nil {
					printWarn(fmt.Sprintf("Pre-run command failed: %v", err))
				} else {
					// Wait for gateway to be ready (poll with timeout)
					// OpenClaw gateway runs on port 18789 by default
					gatewayReady := waitForGateway(18789, 10*time.Second)
					if gatewayReady {
						printSuccess(fmt.Sprintf("%s gateway ready", displayName))
					} else {
						printWarn(fmt.Sprintf("%s gateway may not be ready (timeout) - TUI may show 'disconnected'", displayName))
					}
				}
			} else {
				// Run synchronously
				preRunCmd.Stdout = os.Stdout
				preRunCmd.Stderr = os.Stderr
				if err := preRunCmd.Run(); err != nil {
					printWarn(fmt.Sprintf("Pre-run command failed: %v", err))
				}
			}
		}
	}

	// Background mode: gateway runs as daemon, user launches agent separately
	if ac.Agent.IsBackgroundMode() {
		if !daemonFlag {
			// Stop any existing background gateway first (ensure only one runs at a time)
			stopExistingBackgroundGateway()

			// Not running as daemon yet - spawn daemon subprocess and exit
			fmt.Println()
			printSuccess(fmt.Sprintf("Context Gateway starting on port %d (background mode)", gatewayPort))
			fmt.Println()
			fmt.Printf("  \033[1;36mGateway will proxy traffic for %s\033[0m\n\n", displayName)

			// Build the command user should run
			agentCmd := ac.Agent.Command.Run
			if len(ac.Agent.Command.Args) > 0 {
				agentCmd += " " + strings.Join(ac.Agent.Command.Args, " ")
			}

			// For OpenClaw, show onboarding command first
			if agentArg == "openclaw" {
				fmt.Printf("  \033[1mFor first-time setup, run:\033[0m\n")
				fmt.Printf("    \033[1;32mopenclaw onboard\033[0m\n\n")
			}

			fmt.Printf("  \033[1mTo use %s with compression, run:\033[0m\n", displayName)
			fmt.Printf("    \033[1;32m%s\033[0m\n\n", agentCmd)
			fmt.Printf("  \033[1mTo stop the gateway:\033[0m\n")
			fmt.Printf("    \033[1;33mcontext-gateway --stop\033[0m\n\n")

			if sessionDir != "" {
				fmt.Printf("  \033[0;36mSession logs: %s\033[0m\n\n", filepath.Base(sessionDir))
			}

			// Spawn daemon subprocess
			exe, _ := os.Executable()
			daemonArgs := []string{"--daemon", "-a", agentArg, "-p", strconv.Itoa(gatewayPort)}
			if sessionDir != "" {
				daemonArgs = append(daemonArgs, "--session", sessionDir)
			}
			if configFlag != "" {
				daemonArgs = append(daemonArgs, "-c", configFlag)
			}
			if debugFlag {
				daemonArgs = append(daemonArgs, "-d")
			}

			daemonCmd := exec.Command(exe, daemonArgs...) // #nosec G204,G702 -- exe is our own binary path
			daemonCmd.Stdout = nil
			daemonCmd.Stderr = nil
			daemonCmd.Stdin = nil
			// Detach from parent process group (platform-specific)
			daemonCmd.SysProcAttr = getSysProcAttr()

			if err := daemonCmd.Start(); err != nil {
				printError(fmt.Sprintf("Failed to start daemon: %v", err))
				os.Exit(1)
			}

			// Save PID file immediately (so --stop works even if gateway fails)
			pidFile := filepath.Join(os.TempDir(), "context-gateway.pid")
			_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", daemonCmd.Process.Pid)), 0600) // #nosec G703 -- temp dir path
			// This prevents race condition where plugin connects before gateway is ready
			if !waitForGateway(gatewayPort, 30*time.Second) {
				printWarn("Gateway may not be ready (timeout) - first OpenClaw run might fail")
				printInfo("If OpenClaw gets stuck, Ctrl+C and try again")
			}

			// Save port file for plugins to discover (only after gateway is healthy)
			if err := writePortFile(gatewayPort); err != nil {
				printWarn("Could not write port file: " + err.Error())
			}

			fmt.Println("  \033[2mGateway running in background (PID: " + strconv.Itoa(daemonCmd.Process.Pid) + ")\033[0m\n")
			return
		}

		// Running as daemon - block and handle signals
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, getShutdownSignals()...)
		<-sigCh

		// CRITICAL: Remove port file FIRST to signal plugins to restore config.
		// This allows OpenClaw plugin to detect shutdown and restore original
		// baseUrl before we actually stop the gateway server.
		pidFile := filepath.Join(os.TempDir(), "context-gateway.pid")
		portFile := filepath.Join(os.TempDir(), "context-gateway.port")
		_ = os.Remove(portFile)

		// Wait for plugins to detect port file removal and restore configs.
		// Plugin health check runs every 1s, so 2s is enough for detection + restore.
		time.Sleep(2 * time.Second)

		// Post-session: update CLAUDE.md before shutdown (daemon mode)
		if gw != nil {
			runPostSessionUpdate(gw)
		}

		// Now safe to shutdown gateway
		if gw != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = gw.Shutdown(ctx)
		}

		// Only remove PID file if it still contains our PID (avoid race with new daemon)
		myPid := os.Getpid()
		if pidBytes, err := os.ReadFile(filepath.Clean(pidFile)); err == nil {
			if pidInFile, err := strconv.Atoi(strings.TrimSpace(string(pidBytes))); err == nil {
				if pidInFile == myPid {
					_ = os.Remove(pidFile)
				}
			}
		}
		tui.ClearTerminalTitle()
		return
	}

	// Interactive mode: launch agent as child process with env vars set for routing
	printStep(fmt.Sprintf("Launching %s...", displayName))
	fmt.Println()

	// Build agent command (all args shell-quoted for bash -c safety)
	agentCmd := ac.Agent.Command.Run
	for _, arg := range ac.Agent.Command.Args {
		agentCmd += " " + utils.ShellQuote(arg)
	}
	for _, arg := range passthroughArgs {
		agentCmd += " " + utils.ShellQuote(arg)
	}

	cmd := exec.Command("bash", "-c", agentCmd) // #nosec G204,G702 -- user-selected agent command
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	// Catch SIGINT/SIGTERM in the parent so it doesn't terminate when
	// the user presses Ctrl+C (which the agent handles internally).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, getShutdownSignals()...)

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			fmt.Printf("\n")
			printInfo(fmt.Sprintf("Agent exited with code: %d", exitErr.ExitCode()))
		}
	} else {
		fmt.Printf("\n")
		printInfo("Agent exited with code: 0")
	}

	signal.Stop(sigCh)
	signal.Reset(getShutdownSignals()...)

	// Post-session: update CLAUDE.md with session insights (before shutdown)
	if gw != nil {
		runPostSessionUpdate(gw)
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

// startPreviewGateway starts a minimal gateway with the fast_setup config so the
// dashboard is live and browsable before the user selects an agent.
// Returns the running gateway and whether the browser was opened.
// Returns nil, false if the preview could not start.
func startPreviewGateway(gatewayPort int, debugFlag bool) (*gateway.Gateway, bool) {
	data, _, err := resolveConfig("fast_setup")
	if err != nil {
		return nil, false
	}

	cfg, err := config.LoadFromBytes(data)
	if err != nil {
		return nil, false
	}

	cfg.Server.Port = gatewayPort

	// Preview gateway is ephemeral — disable ALL file-based monitoring so it
	// never creates session directories or log files on disk.
	// The dashboard is decoupled: it opens to monitor running gateways, not to
	// start a session. Session directories must only appear when an agent runs.
	cfg.Monitoring.LogToStdout = false
	cfg.Monitoring.LogOutput = os.DevNull
	cfg.Monitoring.TelemetryEnabled = false
	cfg.Monitoring.TelemetryPath = ""
	cfg.Monitoring.CompressionLogPath = ""
	cfg.Monitoring.ToolDiscoveryLogPath = ""
	cfg.Monitoring.TrajectoryEnabled = false
	cfg.Monitoring.TrajectoryPath = ""

	// Silence zerolog during preview so nothing leaks to the terminal.
	// The real startup block resets this with the session log file.
	if devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
		setupLogging(debugFlag, devNull)
		stdlog.SetOutput(devNull)
		// devNull intentionally not closed here — zerolog holds a reference;
		// setupLogging in the real startup block replaces the writer.
	}

	gw := gateway.New(cfg, "fast_setup")
	gw.SetVersion(Version)
	if dashFS, err := getDashboardFS(); err == nil {
		gw.SetDashboardFS(dashFS)
	}

	go func() { _ = gw.Start() }()

	if !waitForGateway(gatewayPort, 10*time.Second) {
		// Preview failed to start — shut down cleanly and give up
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = gw.Shutdown(ctx)
		cancel()
		return nil, false
	}

	browserOpened := false
	if os.Getenv("CONTEXT_GATEWAY_DEV_FRONTEND") == "" {
		openBrowser(fmt.Sprintf("http://localhost:%d/dashboard/#/monitor", config.DefaultDashboardPort))
		browserOpened = true
	}

	return gw, browserOpened
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

	// Show Plan, Usage, Session in green at startup
	statusBar.RenderStartup(session)
	statusBar.RenderBox()

	// Set terminal title with persistent status info
	tui.SetTerminalTitle(statusBar.FormatTitleStatus(port, session))
	return statusBar
}

// writePortFile writes the gateway port to a well-known temp file for plugin discovery.
// The path is fully controlled (no user input) to prevent path traversal.
func writePortFile(port int) error {
	const portFileName = "context-gateway.port"
	dir := os.TempDir()
	p := filepath.Join(dir, portFileName)
	clean := filepath.Clean(p)
	if !strings.HasPrefix(clean, filepath.Clean(dir)) {
		return fmt.Errorf("path escaped temp dir")
	}
	return os.WriteFile(clean, []byte(strconv.Itoa(port)), 0600) //#nosec G703 -- path is filepath.Clean'd and prefix-checked against TempDir above
}

// runPostSessionUpdate performs post-session CLAUDE.md update using the gateway's session collector.
func runPostSessionUpdate(gw *gateway.Gateway) {
	cfg := gw.PostSessionConfig()
	if !cfg.Enabled {
		return
	}

	collector := gw.SessionCollector()
	if collector == nil || !collector.HasEvents() {
		return
	}

	fmt.Println()
	printStep("Analyzing session for CLAUDE.md updates...")

	updater := postsession.NewUpdater(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	result, err := updater.Update(ctx, collector, collector.GetAuth())
	if err != nil {
		printWarn(fmt.Sprintf("Post-session update failed: %v", err))
		return
	}

	if result.Updated {
		printSuccess(fmt.Sprintf("CLAUDE.md updated: %s", result.Path))
	} else {
		printInfo(fmt.Sprintf("CLAUDE.md: %s", result.Description))
	}
}

// validateCompresrAPIKeyEarly validates the Compresr API key early in startup,
// before agent selection. This uses only the environment variable.
// Returns the gateway status (if available) and whether to continue startup.
func validateCompresrAPIKeyEarly() (*compresr.GatewayStatus, bool) {
	apiKey := os.Getenv("COMPRESR_API_KEY")
	if apiKey == "" {
		return nil, true // No key set, will be handled during onboarding
	}

	// Validate the API key by calling the status endpoint
	fmt.Printf("  %s⏳ Validating API key...%s", tui.ColorDim, tui.ColorReset)

	baseURL := os.Getenv("COMPRESR_BASE_URL")
	if baseURL == "" {
		baseURL = config.DefaultCompresrAPIBaseURL
	}

	client := compresr.NewClient(baseURL, apiKey)
	status, err := client.GetGatewayStatus()

	if err == nil {
		fmt.Printf("\r\033[2K  %s✓ API key valid%s\n", tui.ColorGreen, tui.ColorReset)
		return status, true
	}

	// Network / transient error: warn and continue — the key may be fine and
	// the Compresr API is temporarily unreachable.
	if !strings.Contains(err.Error(), "invalid API key") {
		fmt.Printf("\r\033[2K") // clear the "Validating" spinner line
		printWarn(fmt.Sprintf("Could not reach Compresr API: %v", err))
		printInfo("Compression features may be unavailable this session.")
		fmt.Println()
		return nil, true
	}

	// Invalid / expired key: run the blocking re-auth flow (OAuth or paste).
	fmt.Printf("\r\033[2K") // clear the "Validating" spinner line
	if !runCompresrReauth() {
		return nil, false
	}

	// Re-validate with whatever key is now active (may be empty if user skipped).
	newKey := os.Getenv("COMPRESR_API_KEY")
	if newKey == "" {
		return nil, true
	}
	newClient := compresr.NewClient(baseURL, newKey)
	newStatus, _ := newClient.GetGatewayStatus()
	return newStatus, true
}

// showPreSessionDashboard prints step 2: account info and the upcoming dashboard URL.
// status may be nil if the API was unreachable or the key was skipped.
func showPreSessionDashboard(status *compresr.GatewayStatus) {
	dashURL := fmt.Sprintf("http://localhost:%d/dashboard/", config.DefaultDashboardPort)
	fmt.Printf("  %s%s%s\n", tui.ColorCyan, dashURL, tui.ColorReset)

	if status != nil {
		if status.MonthlyBudgetUSD > 0 {
			fmt.Printf("  Credits:  %.2f remaining  (%.2f used this month / %.2f budget)\n",
				status.CreditsRemainingUSD, status.CreditsUsedThisMonth, status.MonthlyBudgetUSD)
		} else {
			fmt.Printf("  Credits:  %.2f remaining  (%.2f used this month)\n",
				status.CreditsRemainingUSD, status.CreditsUsedThisMonth)
		}
	}
	fmt.Println()
}
