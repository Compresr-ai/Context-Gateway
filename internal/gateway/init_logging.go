package gateway

import (
	"sort"
	"strings"
	"time"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/monitoring"
)

func buildInitEvent(cfg *config.Config) *monitoring.InitEvent {
	ev := &monitoring.InitEvent{
		Timestamp:             time.Now(),
		Event:                 "gateway_init",
		ServerPort:            cfg.Server.Port,
		ServerReadTimeoutMs:   cfg.Server.ReadTimeout.Milliseconds(),
		ServerWriteTimeoutMs:  cfg.Server.WriteTimeout.Milliseconds(),
		ToolOutputEnabled:     cfg.Pipes.ToolOutput.Enabled,
		ToolOutputStrategy:    cfg.Pipes.ToolOutput.Strategy,
		ToolDiscoveryEnabled:  cfg.Pipes.ToolDiscovery.Enabled,
		ToolDiscoveryStrategy: cfg.Pipes.ToolDiscovery.Strategy,
		PreemptiveEnabled:     cfg.Preemptive.Enabled,
		PreemptiveTrigger:     cfg.Preemptive.TriggerThreshold,
		TelemetryPath:         cfg.Monitoring.TelemetryPath,
		CompressionLogPath:    cfg.Monitoring.CompressionLogPath,
		ToolDiscoveryLogPath:  cfg.Monitoring.ToolDiscoveryLogPath,
		TrajectoryEnabled:     cfg.Monitoring.TrajectoryEnabled,
	}

	if cfg.AgentFlags != nil {
		ev.AgentName = cfg.AgentFlags.AgentName
		ev.AgentFlags = append([]string(nil), cfg.AgentFlags.Raw...)
		ev.AutoApproveMode = cfg.AgentFlags.IsAutoApproveMode()
	}

	providerNames := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)

	for _, name := range providerNames {
		p := cfg.Providers[name]
		auth := strings.TrimSpace(p.Auth)
		if auth == "" {
			auth = "api_key"
		}
		ev.Providers = append(ev.Providers, monitoring.InitProvider{
			Name:          name,
			Auth:          auth,
			Model:         p.Model,
			Endpoint:      p.GetEndpoint(name),
			HasAPIKey:     strings.TrimSpace(p.APIKey) != "",
			APIKeyEnvLike: strings.Contains(p.APIKey, "${"),
		})
	}

	return ev
}
