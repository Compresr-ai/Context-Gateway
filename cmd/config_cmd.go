package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/tui"
)

// runConfigCommand handles the "context-gateway config" subcommand.
// Offers TUI or browser-based configuration with hot-reload support.
func runConfigCommand(args []string) {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	browserMode := fs.Bool("browser", false, "open settings in browser")
	_ = fs.Parse(args)

	loadEnvFiles()

	if *browserMode {
		runConfigBrowser()
		return
	}

	runConfigTUI()
}

// runConfigTUI launches the existing TUI config editor.
// After saving, pushes update to a running gateway if detected.
func runConfigTUI() {
	result := runConfigCreationWizard("claude_code", nil)
	if result == "" || result == "__back__" {
		return
	}

	fmt.Printf("%s✓%s Config saved: %s\n", tui.ColorGreen, tui.ColorReset, result)

	// Try to hot-reload if gateway is running
	if port := findRunningGatewayPort(); port > 0 {
		fmt.Printf("  Pushing update to running gateway on port %d...\n", port)
		if err := pushConfigToGateway(result, port); err != nil {
			fmt.Printf("  %s⚠%s Could not hot-reload: %v\n", tui.ColorYellow, tui.ColorReset, err)
			fmt.Printf("  Changes will take effect on next gateway restart.\n")
		} else {
			fmt.Printf("  %s✓%s Config applied to running gateway\n", tui.ColorGreen, tui.ColorReset)
		}
	}
}

// runConfigBrowser opens the Settings tab in the browser.
// If a gateway is already running (dashboard at 18080), it opens that directly.
// Otherwise, starts a lightweight standalone config server on an ephemeral port.
func runConfigBrowser() {
	// Check if the centralized dashboard is already running at 18080
	dashURL := fmt.Sprintf("http://localhost:%d/dashboard/#/settings", config.DefaultDashboardPort)
	client := &http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Get(fmt.Sprintf("http://localhost:%d/health", config.DefaultDashboardPort)); err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			fmt.Printf("%s✓%s Dashboard already running — opening Settings tab\n", tui.ColorGreen, tui.ColorReset)
			fmt.Printf("  %s\n", dashURL)
			openBrowser(dashURL)
			return
		}
	}

	// No dashboard running — start a standalone config server
	fmt.Printf("  No running gateway detected, starting standalone config server...\n")
	runStandaloneConfigServer()
}

// runStandaloneConfigServer starts a lightweight config-only HTTP server and opens
// the Settings page in the default browser. Used when no gateway is running.
func runStandaloneConfigServer() {
	// Resolve config
	configData, configSource, err := resolveServeConfig("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	cfg, err := config.LoadFromBytes(configData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config from %s: %v\n", configSource, err)
		os.Exit(1)
	}

	reloader := config.NewReloader(cfg, configSource)

	// Build HTTP mux with dashboard SPA + config API
	mux := http.NewServeMux()

	// Health endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Config API
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			resp := buildConfigResponseFromConfig(reloader.Current())
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)

		case http.MethodPatch:
			var body []byte
			var readErr error
			body, readErr = io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if readErr != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			var patch config.ConfigPatch
			if unmarshalErr := json.Unmarshal(body, &patch); unmarshalErr != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": unmarshalErr.Error()})
				return
			}
			updated, updateErr := reloader.Update(patch)
			if updateErr != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]string{"message": updateErr.Error()},
				})
				return
			}
			// Also push to running gateway if possible.
			if port := findRunningGatewayPort(); port > 0 {
				go pushPatchToRunningGateway(patch, port) //nolint:errcheck
			}
			resp := buildConfigResponseFromConfig(updated)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Stub /api/dashboard so the SPA doesn't error on its polling fetch
	mux.HandleFunc("/api/dashboard", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"sessions":       []interface{}{},
			"total_cost":     0,
			"total_requests": 0,
			"session_cap":    0,
			"global_cap":     0,
			"enabled":        false,
		})
	})

	// Dashboard SPA
	if dashFS, fsErr := getDashboardFS(); fsErr == nil {
		fsHandler := http.FileServerFS(dashFS)
		mux.HandleFunc("/dashboard/", func(w http.ResponseWriter, r *http.Request) {
			http.StripPrefix("/dashboard", fsHandler).ServeHTTP(w, r)
		})
		mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/dashboard/", http.StatusMovedPermanently)
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/dashboard/#/settings", http.StatusMovedPermanently)
			return
		}
		http.NotFound(w, r)
	})

	// Bind to ephemeral port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to bind: %v\n", err)
		os.Exit(1)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	server := &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	// Shutdown on signal
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		cancel()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		_ = server.Shutdown(shutCtx)
	}()

	dashURL := fmt.Sprintf("http://localhost:%d/dashboard/#/settings", port)
	fmt.Printf("Config server running at %s\n", dashURL)
	fmt.Printf("Press Ctrl+C to stop.\n")
	openBrowser(dashURL)

	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		}
	}()

	<-ctx.Done()
	fmt.Println("\nConfig server stopped.")
}

// configBrowserResponse mirrors the gateway's configResponse but is defined
// here to avoid importing the gateway package (which pulls in heavy deps).
type configBrowserResponse struct {
	Preemptive  configBrowserPreemptive  `json:"preemptive"`
	Pipes       configBrowserPipes       `json:"pipes"`
	CostControl configBrowserCostControl `json:"cost_control"`
	Monitoring  configBrowserMonitoring  `json:"monitoring"`
}

type configBrowserPreemptive struct {
	Enabled          bool    `json:"enabled"`
	TriggerThreshold float64 `json:"trigger_threshold"`
	Strategy         string  `json:"strategy"`
}

type configBrowserPipes struct {
	ToolOutput    configBrowserToolOutput    `json:"tool_output"`
	ToolDiscovery configBrowserToolDiscovery `json:"tool_discovery"`
}

type configBrowserToolOutput struct {
	Enabled                bool    `json:"enabled"`
	Strategy               string  `json:"strategy"`
	MinBytes               int     `json:"min_bytes"`
	TargetCompressionRatio float64 `json:"target_compression_ratio"`
}

type configBrowserToolDiscovery struct {
	Enabled        bool    `json:"enabled"`
	Strategy       string  `json:"strategy"`
	MinTools       int     `json:"min_tools"`
	MaxTools       int     `json:"max_tools"`
	TargetRatio    float64 `json:"target_ratio"`
	SearchFallback bool    `json:"search_fallback"`
}

type configBrowserCostControl struct {
	Enabled    bool    `json:"enabled"`
	SessionCap float64 `json:"session_cap"`
	GlobalCap  float64 `json:"global_cap"`
}

type configBrowserMonitoring struct {
	TelemetryEnabled bool `json:"telemetry_enabled"`
}

func buildConfigResponseFromConfig(cfg *config.Config) configBrowserResponse {
	return configBrowserResponse{
		Preemptive: configBrowserPreemptive{
			Enabled:          cfg.Preemptive.Enabled,
			TriggerThreshold: cfg.Preemptive.TriggerThreshold,
			Strategy:         cfg.Preemptive.Summarizer.Strategy,
		},
		Pipes: configBrowserPipes{
			ToolOutput: configBrowserToolOutput{
				Enabled:                cfg.Pipes.ToolOutput.Enabled,
				Strategy:               cfg.Pipes.ToolOutput.Strategy,
				MinBytes:               cfg.Pipes.ToolOutput.MinBytes,
				TargetCompressionRatio: cfg.Pipes.ToolOutput.TargetCompressionRatio,
			},
			ToolDiscovery: configBrowserToolDiscovery{
				Enabled:        cfg.Pipes.ToolDiscovery.Enabled,
				Strategy:       cfg.Pipes.ToolDiscovery.Strategy,
				MinTools:       cfg.Pipes.ToolDiscovery.MinTools,
				MaxTools:       cfg.Pipes.ToolDiscovery.MaxTools,
				TargetRatio:    cfg.Pipes.ToolDiscovery.TargetRatio,
				SearchFallback: cfg.Pipes.ToolDiscovery.EnableSearchFallback,
			},
		},
		CostControl: configBrowserCostControl{
			Enabled:    cfg.CostControl.Enabled,
			SessionCap: cfg.CostControl.SessionCap,
			GlobalCap:  cfg.CostControl.GlobalCap,
		},
		Monitoring: configBrowserMonitoring{
			TelemetryEnabled: cfg.Monitoring.TelemetryEnabled,
		},
	}
}

// findRunningGatewayPort probes gateway ports to find a running instance.
// Returns the port number or 0 if none found.
func findRunningGatewayPort() int {
	client := &http.Client{Timeout: 1 * time.Second}
	for i := 0; i < config.MaxGatewayPorts; i++ {
		port := config.DefaultGatewayBasePort + i
		resp, err := client.Get(fmt.Sprintf("http://localhost:%d/health", port))
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return port
		}
	}
	return 0
}

// pushConfigToGateway reads the saved config and pushes relevant sections
// via PATCH /api/config to a running gateway.
func pushConfigToGateway(configName string, port int) error {
	state := loadConfigToState(configName)
	if state == nil {
		return fmt.Errorf("failed to load config: %s", configName)
	}

	preemptiveEnabled := true
	costEnabled := state.CostCap > 0
	patch := config.ConfigPatch{
		Preemptive: &config.PreemptivePatch{
			Enabled:          &preemptiveEnabled,
			TriggerThreshold: &state.TriggerThreshold,
		},
		Pipes: &config.PipesPatch{
			ToolOutput: &config.ToolOutputPatch{
				Enabled:                &state.ToolOutputEnabled,
				MinBytes:               &state.ToolOutputMinBytes,
				TargetCompressionRatio: &state.ToolOutputTargetRatio,
			},
			ToolDiscovery: &config.ToolDiscoveryPatch{
				Enabled:  &state.ToolDiscoveryEnabled,
				MinTools: &state.ToolDiscoveryMinTools,
				MaxTools: &state.ToolDiscoveryMaxTools,
			},
		},
		CostControl: &config.CostControlPatch{
			Enabled:   &costEnabled,
			GlobalCap: &state.CostCap,
		},
		Monitoring: &config.MonitoringPatch{
			TelemetryEnabled: &state.TelemetryEnabled,
		},
	}

	return pushPatchToRunningGateway(patch, port)
}

// pushPatchToRunningGateway sends a config patch to a running gateway's config API.
// Accepts a typed ConfigPatch to ensure only validated data is forwarded.
func pushPatchToRunningGateway(patch config.ConfigPatch, port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("invalid gateway port: %d", port)
	}
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal patch: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	gatewayURL := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort("localhost", strconv.Itoa(port)),
		Path:   "/api/config",
	}
	req, err := http.NewRequest(http.MethodPatch, gatewayURL.String(), bytes.NewReader(patchJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
