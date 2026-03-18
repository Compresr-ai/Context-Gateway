// Config REST API endpoints for hot-reload configuration.
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

// handleConfigAPI handles GET, PATCH, and DELETE requests to /api/config.
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
	case http.MethodDelete:
		g.handleDeleteConfig(w, r)
	default:
		w.Header().Set("Allow", "GET, PATCH, DELETE")
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
	HasOverrides  bool                  `json:"has_session_overrides"`
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
	MinTokens              int     `json:"min_tokens"`
	TargetCompressionRatio float64 `json:"target_compression_ratio"`
}

type toolDiscoveryResponse struct {
	Enabled           bool                      `json:"enabled"`
	Strategy          string                    `json:"strategy"`
	TokenThreshold    int                       `json:"token_threshold"`
	SchemaCompression schemaCompressionResponse `json:"schema_compression"`
}

type schemaCompressionResponse struct {
	Enabled        bool   `json:"enabled"`
	TokenThreshold int    `json:"token_threshold"`
	Model          string `json:"model"`
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

func (g *Gateway) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if g.configReloader == nil {
		g.writeError(w, "config reloader not initialized", http.StatusInternalServerError)
		return
	}

	// ?view=overrides returns only the session overrides (so the dashboard can show what's temporary)
	if r.URL.Query().Get("view") == "overrides" {
		overrides := g.configReloader.SessionOverrides()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(overrides)
		return
	}

	cfg := g.configReloader.Current()
	resp := buildConfigResponse(cfg)
	resp.HasOverrides = !g.configReloader.SessionOverrides().IsEmpty()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Warn().Err(err).Msg("handleGetConfig: failed to encode JSON response")
	}
}

func (g *Gateway) handlePatchConfig(w http.ResponseWriter, r *http.Request) {
	if g.configReloader == nil {
		g.writeError(w, "config reloader not initialized", http.StatusInternalServerError)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB limit (DoS prevention)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if err.Error() == "http: request body too large" {
			g.writeError(w, "request body too large (max 1MB)", http.StatusRequestEntityTooLarge)
		} else {
			g.writeError(w, "failed to read request body", http.StatusBadRequest)
		}
		return
	}

	var patch config.ConfigPatch
	err = json.Unmarshal(body, &patch)
	if err != nil {
		g.writeError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// scope=session applies changes to this session only (in-memory, not persisted).
	// scope=global (default) persists to the global config file for future sessions.
	scope := r.URL.Query().Get("scope")

	var updated *config.Config
	if scope == "session" {
		updated, err = g.configReloader.UpdateSession(patch)
	} else {
		updated, err = g.configReloader.Update(patch)
	}
	if err != nil {
		log.Error().Err(err).Str("scope", scope).Msg("config patch failed")
		g.writeError(w, "config update failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	log.Info().Str("scope", scope).Msg("config updated via API")

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
	resp.HasOverrides = !g.configReloader.SessionOverrides().IsEmpty()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleDeleteConfig resets session overrides back to global defaults.
func (g *Gateway) handleDeleteConfig(w http.ResponseWriter, r *http.Request) {
	if g.configReloader == nil {
		g.writeError(w, "config reloader not initialized", http.StatusInternalServerError)
		return
	}

	scope := r.URL.Query().Get("scope")
	if scope != "session" {
		g.writeError(w, "DELETE only supports ?scope=session", http.StatusBadRequest)
		return
	}

	cfg := g.configReloader.ResetSession()
	log.Info().Msg("session overrides cleared")

	if g.monitorHub != nil {
		g.monitorHub.BroadcastEvent("config_updated", nil)
	}

	resp := buildConfigResponse(cfg)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Warn().Err(err).Msg("handlePatchConfig: failed to encode JSON response")
	}
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
				MinTokens:              cfg.Pipes.ToolOutput.MinTokens,
				TargetCompressionRatio: cfg.Pipes.ToolOutput.TargetCompressionRatio,
			},
			ToolDiscovery: toolDiscoveryResponse{
				Enabled:        cfg.Pipes.ToolDiscovery.Enabled,
				Strategy:       cfg.Pipes.ToolDiscovery.Strategy,
				TokenThreshold: cfg.Pipes.ToolDiscovery.TokenThreshold,
				SchemaCompression: schemaCompressionResponse{
					Enabled:        cfg.Pipes.ToolDiscovery.SchemaCompression.Enabled,
					TokenThreshold: cfg.Pipes.ToolDiscovery.SchemaCompression.TokenThreshold,
					Model:          cfg.Pipes.ToolDiscovery.SchemaCompression.Model,
				},
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

	// treat only file-not-found as acceptable; other errors (permission, disk full)
	// would silently discard existing content, so we abort instead.
	data, err := os.ReadFile(envPath) // #nosec G304
	if err != nil && !os.IsNotExist(err) {
		log.Warn().Err(err).Str("path", envPath).Msg("persistEnvVar: cannot read existing .env file; skipping persist to avoid data loss")
		return
	}
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
