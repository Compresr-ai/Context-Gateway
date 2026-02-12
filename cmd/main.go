// Package main is the entry point for the Context Gateway.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/gateway"
)

// ANSI color codes
const (
	compresrGreen = "\033[38;2;23;128;68m" // #178044
	bold          = "\033[1m"
	reset         = "\033[0m"
)

// ASCII banner for startup
const banner = `
  ██████╗ ██████╗ ███╗  ██╗████████╗███████╗██╗ ██╗████████╗  ██████╗  █████╗ ████████╗███████╗██╗    ██╗ █████╗ ██╗   ██╗
 ██╔════╝██╔═══██╗████╗ ██║╚══██╔══╝██╔════╝╚██╗██╔╝╚══██╔══╝ ██╔════╝ ██╔══██╗╚══██╔══╝██╔════╝██║    ██║██╔══██╗╚██╗ ██╔╝
 ██║     ██║   ██║██╔██╗██║   ██║   █████╗   ╚███╔╝    ██║    ██║  ███╗███████║   ██║   █████╗  ██║ █╗ ██║███████║ ╚████╔╝
 ██║     ██║   ██║██║╚████║   ██║   ██╔══╝   ██╔██╗    ██║    ██║   ██║██╔══██║   ██║   ██╔══╝  ██║███╗██║██╔══██║  ╚██╔╝
 ╚██████╗╚██████╔╝██║ ╚███║   ██║   ███████╗██╔╝ ██╗   ██║    ╚██████╔╝██║  ██║   ██║   ███████╗╚███╔███╔╝██║  ██║   ██║
  ╚═════╝ ╚═════╝ ╚═╝  ╚══╝   ╚═╝   ╚══════╝╚═╝  ╚═╝   ╚═╝     ╚═════╝ ╚═╝  ╚═╝   ╚═╝   ╚══════╝ ╚══╝╚══╝ ╚═╝  ╚═╝   ╚═╝
`

func printBanner() {
	fmt.Print(compresrGreen + bold + banner + reset + "\n")
}

// loadEnvFiles loads .env from standard locations
func loadEnvFiles() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		_ = godotenv.Load()
		return
	}

	// Try loading from ~/.config/context-gateway/.env first
	configEnv := filepath.Join(homeDir, ".config", "context-gateway", ".env")
	if _, err := os.Stat(configEnv); err == nil {
		_ = godotenv.Load(configEnv)
	}

	// Also load local .env (can override)
	_ = godotenv.Load()
}

func main() {
	// Handle subcommands first (before flags)
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "agent":
			// Launch agent with interactive selection
			runAgentCommand(os.Args[2:])
			return
		case "serve", "start":
			// Start the gateway server only (no agent)
			runGatewayServer(os.Args[2:])
			return
		case "update":
			printBanner()
			if err := DoUpdate(); err != nil {
				fmt.Fprintf(os.Stderr, "Update failed: %v\n", err)
				os.Exit(1)
			}
			return
		case "uninstall", "remove":
			printBanner()
			if err := DoUninstall(); err != nil {
				fmt.Fprintf(os.Stderr, "Uninstall failed: %v\n", err)
				os.Exit(1)
			}
			return
		case "version", "-v", "--version":
			PrintVersion()
			return
		case "help", "-h", "--help":
			printHelp()
			return
		}
	}

	// Default: launch agent with interactive selection (gateway + claude code)
	runAgentCommand(os.Args[1:])
}

// resolveServeConfig resolves the config for the serve command.
// Checks: user flag -> filesystem locations -> embedded configs.
// Returns raw bytes and source description.
func resolveServeConfig(userConfig string) ([]byte, string, error) {
	// If user specified a config path, read it directly
	if userConfig != "" {
		data, err := os.ReadFile(userConfig)
		if err != nil {
			return nil, "", fmt.Errorf("config file not found: %s", userConfig)
		}
		return data, userConfig, nil
	}

	homeDir, _ := os.UserHomeDir()

	// Search filesystem in order of preference
	searchPaths := []string{}
	if homeDir != "" {
		searchPaths = append(searchPaths,
			filepath.Join(homeDir, ".config", "context-gateway", "configs", "preemptive_summarization.yaml"),
			filepath.Join(homeDir, ".config", "context-gateway", "configs", "config.yaml"),
		)
	}
	searchPaths = append(searchPaths,
		"configs/preemptive_summarization.yaml",
		"configs/config.yaml",
	)

	for _, path := range searchPaths {
		if data, err := os.ReadFile(path); err == nil {
			return data, path, nil
		}
	}

	// Fall back to embedded config
	if data, err := getEmbeddedConfig("preemptive_summarization"); err == nil {
		return data, "(embedded) preemptive_summarization.yaml", nil
	}

	return nil, "", fmt.Errorf("no config file found. Specify --config path")
}

// runGatewayServer starts the gateway proxy server
func runGatewayServer(args []string) {
	// Load .env files from standard locations
	loadEnvFiles()

	// Parse flags
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file")
	debug := fs.Bool("debug", false, "enable debug logging")
	noBanner := fs.Bool("no-banner", false, "suppress startup banner")
	_ = fs.Parse(args) // ExitOnError handles errors

	// Print banner unless suppressed
	if !*noBanner {
		printBanner()
	}



	// Check for updates (non-blocking notification)
	CheckForUpdates()

	// Setup logging
	setupLogging(*debug)

	// Resolve config from filesystem
	configData, configSource, err := resolveServeConfig(*configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("No config file found. Specify --config path")
	}

	log.Info().
		Str("version", Version).
		Str("config", configSource).
		Msg("Context Gateway starting")

	// Load configuration from bytes
	cfg, err := config.LoadFromBytes(configData)
	if err != nil {
		log.Fatal().Err(err).Str("config", configSource).Msg("failed to load configuration")
	}

	log.Info().
		Int("port", cfg.Server.Port).
		Bool("tool_output_pipe", cfg.Pipes.ToolOutput.Enabled).
		Bool("tool_discovery_pipe", cfg.Pipes.ToolDiscovery.Enabled).
		Msg("configuration loaded")

	// Create gateway
	gw := gateway.New(cfg)

	// Handle graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		log.Info().Msg("shutdown signal received")

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := gw.Shutdown(ctx); err != nil {
			log.Error().Err(err).Msg("gateway shutdown error")
		}
	}()

	// Start gateway
	if err := gw.Start(); err != nil {
		if err.Error() != "http: Server closed" {
			log.Fatal().Err(err).Msg("gateway error")
		}
	}

	log.Info().Msg("Context Gateway stopped")
}

// setupLogging configures zerolog.
// If logFile is non-nil, logs are written there instead of stdout.
func setupLogging(debug bool, logFile ...*os.File) {
	var out *os.File
	if len(logFile) > 0 && logFile[0] != nil {
		out = logFile[0]
	} else {
		out = os.Stdout
	}

	// Pretty console output
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        out,
		TimeFormat: time.RFC3339,
	})

	// Set log level
	if debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
}

// printHelp prints usage information
func printHelp() {
	printBanner()
	fmt.Println("Context Gateway - LLM prompt compression proxy")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  context-gateway [options]")
	fmt.Println("  context-gateway [command]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  (none)       Launch Claude Code with gateway proxy (default)")
	fmt.Println("  serve        Start the gateway proxy server only")
	fmt.Println("  update       Update to the latest version")
	fmt.Println("  uninstall    Remove context-gateway")
	fmt.Println("  version      Print version information")
	fmt.Println("  help         Show this help message")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  -c, --config FILE    Gateway config (shows menu if not specified)")
	fmt.Println("  -p, --port PORT      Gateway port (default: 18080)")
	fmt.Println("  -d, --debug          Enable debug logging")
	fmt.Println("  --proxy MODE         auto (default), start, skip")
	fmt.Println("  -l, --list           List available agents")
	fmt.Println()
	fmt.Println("Server Options:")
	fmt.Println("  context-gateway serve [--config FILE] [--debug] [--no-banner]")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  context-gateway                    Launch Claude Code (default)")
	fmt.Println("  context-gateway -d                 Launch with debug logging")
	fmt.Println("  context-gateway serve              Start gateway server only")
	fmt.Println("  context-gateway update             Update to latest version")
	fmt.Println()
	fmt.Println("Documentation: https://docs.compresr.ai/gateway")
}
