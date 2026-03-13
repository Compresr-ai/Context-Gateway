// Config REST API endpoints for hot-reload configuration.
//
// DESIGN: Localhost-only endpoints for reading and patching gateway config.
// GET /api/config returns current config (with API keys masked).
// PATCH /api/config accepts a ConfigPatch and applies it via the reloader.
package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/utils"
)

// handleConfigAPI handles GET and PATCH requests to /api/config.
func (g *Gateway) handleConfigAPI(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		g.writeError(w, "forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		g.handleGetConfig(w, r)
	case http.MethodPatch:
		g.handlePatchConfig(w, r)
	default:
		w.Header().Set("Allow", "GET, PATCH")
		g.writeError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// configResponse is the JSON representation of the config for the API.
// API keys are masked for security.
type configResponse struct {
	Preemptive    preemptiveResponse    `json:"preemptive"`
	Pipes         pipesResponse         `json:"pipes"`
	CostControl   costControlResponse   `json:"cost_control"`
	Notifications notificationsResponse `json:"notifications"`
	Monitoring    monitoringResponse    `json:"monitoring"`
}

type preemptiveResponse struct {
	Enabled          bool    `json:"enabled"`
	TriggerThreshold float64 `json:"trigger_threshold"`
	Strategy         string  `json:"strategy"`
}

type pipesResponse struct {
	ToolOutput    toolOutputResponse    `json:"tool_output"`
	ToolDiscovery toolDiscoveryResponse `json:"tool_discovery"`
}

type toolOutputResponse struct {
	Enabled                bool    `json:"enabled"`
	Strategy               string  `json:"strategy"`
	MinBytes               int     `json:"min_bytes"`
	TargetCompressionRatio float64 `json:"target_compression_ratio"`
}

type toolDiscoveryResponse struct {
	Enabled        bool    `json:"enabled"`
	Strategy       string  `json:"strategy"`
	MinTools       int     `json:"min_tools"`
	MaxTools       int     `json:"max_tools"`
	TargetRatio    float64 `json:"target_ratio"`
	SearchFallback bool    `json:"search_fallback"`
}

type costControlResponse struct {
	Enabled    bool    `json:"enabled"`
	SessionCap float64 `json:"session_cap"`
	GlobalCap  float64 `json:"global_cap"`
}

type notificationsResponse struct {
	Slack slackResponse `json:"slack"`
}

type slackResponse struct {
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`  // true if webhook URL is set (config or env)
	WebhookURL string `json:"webhook_url"` // masked for security
}

type monitoringResponse struct {
	TelemetryEnabled bool `json:"telemetry_enabled"`
}

func (g *Gateway) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	if g.configReloader == nil {
		g.writeError(w, "config reloader not initialized", http.StatusInternalServerError)
		return
	}

	cfg := g.configReloader.Current()
	resp := buildConfigResponse(cfg)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (g *Gateway) handlePatchConfig(w http.ResponseWriter, r *http.Request) {
	if g.configReloader == nil {
		g.writeError(w, "config reloader not initialized", http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		g.writeError(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var patch config.ConfigPatch
	err = json.Unmarshal(body, &patch)
	if err != nil {
		g.writeError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	updated, err := g.configReloader.Update(patch)
	if err != nil {
		log.Error().Err(err).Msg("config patch failed")
		g.writeError(w, "config update failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	log.Info().Msg("config updated via API")

	// If webhook URL was set, also persist to global .env so the hook script can read it
	if patch.Notifications != nil && patch.Notifications.Slack != nil && patch.Notifications.Slack.WebhookURL != nil {
		webhookVal := *patch.Notifications.Slack.WebhookURL
		if webhookVal != "" {
			_ = os.Setenv("SLACK_WEBHOOK_URL", webhookVal)
			if home, err := os.UserHomeDir(); err == nil {
				envPath := filepath.Join(home, ".config", "context-gateway", ".env")
				persistEnvVar(envPath, "SLACK_WEBHOOK_URL", webhookVal)
			}
		}
	}

	// Broadcast config_updated event to WebSocket clients
	if g.monitorHub != nil {
		g.monitorHub.BroadcastEvent("config_updated", nil)
	}

	resp := buildConfigResponse(updated)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func buildConfigResponse(cfg *config.Config) configResponse {
	// Determine effective webhook URL from config or env
	webhookURL := cfg.Notifications.Slack.WebhookURL
	if webhookURL == "" {
		webhookURL = os.Getenv("SLACK_WEBHOOK_URL")
	}
	slackConfigured := webhookURL != ""
	maskedWebhook := ""
	if slackConfigured {
		maskedWebhook = utils.MaskKeyShort(webhookURL)
	}

	return configResponse{
		Preemptive: preemptiveResponse{
			Enabled:          cfg.Preemptive.Enabled,
			TriggerThreshold: cfg.Preemptive.TriggerThreshold,
			Strategy:         cfg.Preemptive.Summarizer.Strategy,
		},
		Pipes: pipesResponse{
			ToolOutput: toolOutputResponse{
				Enabled:                cfg.Pipes.ToolOutput.Enabled,
				Strategy:               cfg.Pipes.ToolOutput.Strategy,
				MinBytes:               cfg.Pipes.ToolOutput.MinBytes,
				TargetCompressionRatio: cfg.Pipes.ToolOutput.TargetCompressionRatio,
			},
			ToolDiscovery: toolDiscoveryResponse{
				Enabled:        cfg.Pipes.ToolDiscovery.Enabled,
				Strategy:       cfg.Pipes.ToolDiscovery.Strategy,
				MinTools:       cfg.Pipes.ToolDiscovery.MinTools,
				MaxTools:       cfg.Pipes.ToolDiscovery.MaxTools,
				TargetRatio:    cfg.Pipes.ToolDiscovery.TargetRatio,
				SearchFallback: cfg.Pipes.ToolDiscovery.EnableSearchFallback,
			},
		},
		CostControl: costControlResponse{
			Enabled:    cfg.CostControl.Enabled,
			SessionCap: cfg.CostControl.SessionCap,
			GlobalCap:  cfg.CostControl.GlobalCap,
		},
		Notifications: notificationsResponse{
			Slack: slackResponse{
				Enabled:    cfg.Notifications.Slack.Enabled,
				Configured: slackConfigured,
				WebhookURL: maskedWebhook,
			},
		},
		Monitoring: monitoringResponse{
			TelemetryEnabled: cfg.Monitoring.TelemetryEnabled,
		},
	}
}

// persistEnvVar appends or updates a KEY=VALUE pair in an .env file.
func persistEnvVar(envPath, key, value string) {
	dir := filepath.Dir(envPath)
	_ = os.MkdirAll(dir, 0750) // #nosec G301

	data, _ := os.ReadFile(envPath) // #nosec G304
	lines := strings.Split(string(data), "\n")

	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+"=") {
			lines[i] = key + "=" + value
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, key+"="+value)
	}

	// Remove trailing empty lines then add exactly one newline
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	_ = os.WriteFile(envPath, []byte(strings.Join(lines, "\n")+"\n"), 0600) // #nosec G306 G703 -- envPath is constructed from os.UserHomeDir() + a fixed suffix
}
