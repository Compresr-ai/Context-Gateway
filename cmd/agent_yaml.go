package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
	"github.com/compresr/context-gateway/internal/preemptive"
	"github.com/compresr/context-gateway/internal/tui"
)

// saveConfig saves the config to disk and returns its name
func saveConfig(state *ConfigState) string {
	configContent := generateCustomConfigYAML(
		state.Name,
		state.Provider.Name,
		state.Model,
		state.APIKey,
		state.SlackEnabled,
		state.TriggerThreshold,
		state.CostCap,
		state.CompactStrategy,
		state.CompactCompresrModel,
		state.ToolDiscoveryEnabled,
		state.ToolDiscoveryStrategy,
		state.ToolDiscoveryTokenThreshold,
		state.ToolDiscoveryModel,
		state.ToolOutputEnabled,
		state.ToolOutputStrategy,
		state.ToolOutputProvider.Name,
		state.ToolOutputModel,
		state.ToolOutputAPIKey,
		state.ToolOutputMinTokens,
		state.ToolOutputTargetRatio,
		state.TelemetryEnabled,
	)

	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		printError("Failed to resolve user home directory")
		return ""
	}
	configDir := filepath.Join(homeDir, ".config", "context-gateway", "configs")
	// #nosec G301 -- config directory permissions
	if err := os.MkdirAll(configDir, 0750); err != nil {
		printError(fmt.Sprintf("Failed to create config directory: %v", err))
		return ""
	}

	configPath := filepath.Join(configDir, state.Name+".yaml")
	// #nosec G306 -- config file permissions
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		printError(fmt.Sprintf("Failed to write config: %v", err))
		return ""
	}

	fmt.Printf("\n%s✓%s Config saved: %s\n", tui.ColorGreen, tui.ColorReset, configPath)
	if state.CostCap > 0 {
		fmt.Printf("  %sDashboard will be available at http://localhost:%d/dashboard/%s\n", tui.ColorCyan, config.DefaultDashboardPort, tui.ColorReset)
	}
	return state.Name
}

// generateCustomConfigYAML generates a gateway config YAML.
func generateCustomConfigYAML(
	name, provider, model, apiKey string,
	enableSlack bool,
	triggerThreshold float64,
	costCap float64,
	compactStrategy string,
	compactCompresrModel string,
	toolDiscoveryEnabled bool,
	toolDiscoveryStrategy string,
	toolDiscoveryTokenThreshold int,
	toolDiscoveryModel string,
	toolOutputEnabled bool,
	toolOutputStrategy string,
	toolOutputProvider string,
	toolOutputModel string,
	toolOutputAPIKey string,
	toolOutputMinTokens int,
	toolOutputTargetRatio float64,
	telemetryEnabled bool,
) string {
	slackEnabled := "false"
	if enableSlack {
		slackEnabled = "true"
	}

	costCapEnabled := "false"
	if costCap > 0 {
		costCapEnabled = "true"
	}

	// Get provider endpoint for summarizer
	var endpoint string
	switch provider {
	case "anthropic":
		endpoint = "https://api.anthropic.com/v1/messages"
	case "gemini":
		endpoint = "https://generativelanguage.googleapis.com/v1beta/models/" + model + ":generateContent"
	case "openai":
		endpoint = "https://api.openai.com/v1/chat/completions"
	}

	// Get tool output compression endpoint (for external_provider strategy)
	var toolOutputEndpoint string
	// Determine which provider to use for tool_output
	// Only use specified toolOutputProvider if enabled and using external_provider strategy
	effectiveToolOutputProvider := provider // Default to main provider
	if toolOutputEnabled && toolOutputStrategy == pipes.StrategyExternalProvider && toolOutputProvider != "" {
		effectiveToolOutputProvider = toolOutputProvider
	}

	if toolOutputStrategy == pipes.StrategyCompresr {
		toolOutputEndpoint = fmt.Sprintf("${COMPRESR_BASE_URL:-%s}/v1/tool-output/compress", config.DefaultCompresrAPIBaseURL)
		// api_key is inherited from the top-level compresr section — no inline key needed
	} else {
		switch effectiveToolOutputProvider {
		case "anthropic":
			toolOutputEndpoint = "https://api.anthropic.com/v1/messages"
		case "gemini":
			toolOutputEndpoint = "https://generativelanguage.googleapis.com/v1beta/models/" + toolOutputModel + ":generateContent"
		case "openai":
			toolOutputEndpoint = "https://api.openai.com/v1/chat/completions"
		}
	}

	// Build providers section - include tool_output provider if different and enabled
	providersSection := fmt.Sprintf(`providers:
  %s:
    api_key: "%s"
    model: "%s"`, provider, apiKey, model)

	if toolOutputEnabled && toolOutputStrategy == pipes.StrategyExternalProvider && effectiveToolOutputProvider != "" && effectiveToolOutputProvider != provider {
		providersSection += fmt.Sprintf(`
  %s:
    api_key: "%s"
    model: "%s"`, effectiveToolOutputProvider, toolOutputAPIKey, toolOutputModel)
	}

	// Build preemptive summarizer section based on compact strategy
	var summarizerSection string
	if compactStrategy == preemptive.StrategyCompresr {
		summarizerSection = fmt.Sprintf(`  summarizer:
    strategy: "%s"
    max_tokens: 4096
    timeout: 60s
    token_estimate_ratio: 4
    compresr:
      endpoint: "/api/compress/history/"
      model: "%s"
      timeout: 60s`, preemptive.StrategyCompresr, compactCompresrModel)
	} else {
		summarizerSection = fmt.Sprintf(`  summarizer:
    strategy: "%s"
    provider: "%s"
    endpoint: "%s"
    max_tokens: 4096
    timeout: 60s
    token_estimate_ratio: 4`, preemptive.StrategyExternalProvider, provider, endpoint)
	}

	// Build tool_output section based on strategy
	var toolOutputSection string
	if toolOutputStrategy == pipes.StrategyCompresr {
		// Compresr strategy: use Compresr API endpoint, no provider field.
		// api_key is inherited from the top-level compresr section.
		toolOutputSection = fmt.Sprintf(`  tool_output:
    enabled: %t
    strategy: "%s"
    enable_expand_context: true
    include_expand_hint: true
    compresr:
      endpoint: "%s"
      model: "%s"
      timeout: 30s
    min_tokens: %d
    target_compression_ratio: %.2f  # 0.1 = least aggressive (remove 10%%), 0.9 = most aggressive (remove 90%%)`,
			toolOutputEnabled, toolOutputStrategy, toolOutputEndpoint, toolOutputModel,
			toolOutputMinTokens, toolOutputTargetRatio)
	} else {
		// External provider strategy: reference provider from providers section, no api field
		toolOutputSection = fmt.Sprintf(`  tool_output:
    enabled: %t
    strategy: "%s"
    provider: "%s"
    enable_expand_context: true
    include_expand_hint: true
    min_tokens: %d
    target_compression_ratio: %.2f  # 0.1 = least aggressive (remove 10%%), 0.9 = most aggressive (remove 90%%)`,
			toolOutputEnabled, toolOutputStrategy, effectiveToolOutputProvider,
			toolOutputMinTokens, toolOutputTargetRatio)
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
  port: ${GATEWAY_PORT:-18081}
  read_timeout: 30s
  write_timeout: 1000s

urls:
  compresr: "${COMPRESR_BASE_URL:-%s}"

# Compresr credentials — defined once here, inherited by all pipes.
compresr:
  api_key: "${COMPRESR_API_KEY:-}"

%s

preemptive:
  enabled: true
  trigger_threshold: %.1f
  add_response_headers: true
  log_dir: "${SESSION_DIR:-logs}"
  compaction_log_path: "${SESSION_COMPACTION_LOG:-logs/history_compaction.jsonl}"

%s

  session:
    summary_ttl: 3h
    hash_message_count: 3

cost_control:
  enabled: %s
  session_cap: 0
  global_cap: %.2f

pipes:
%s
  tool_discovery:
    enabled: %t
    strategy: "%s"
    compresr:
      endpoint: "${COMPRESR_BASE_URL:-%s}/api/compress/tool-discovery/"
      model: "%s"
      timeout: 10s
    token_threshold: %d

store:
  type: "memory"
  ttl: 1h

notifications:
  slack:
    enabled: %s

monitoring:
  # Set to "info" or "debug" to see gateway logs. Off disables gateway.log.
  log_level: "off"
  log_format: "console"
  log_output: "stdout"
  # Telemetry controls JSONL telemetry logs (telemetry.jsonl, tool_output_compression.jsonl, etc.)
  telemetry_enabled: %t
  # Verbose payloads: set to true to capture request/response bodies and sanitized headers
  verbose_payloads: false
  telemetry_path: "${SESSION_TELEMETRY_LOG:-logs/telemetry.jsonl}"
  compression_log_path: "${SESSION_COMPRESSION_LOG:-logs/tool_output_compression.jsonl}"
  tool_discovery_log_path: "${SESSION_TOOL_DISCOVERY_LOG:-logs/tool_discovery.jsonl}"
  task_output_log_path: "${SESSION_TASK_OUTPUT_LOG:-logs/task_output}"
`, name, name, config.DefaultCompresrAPIBaseURL,
		providersSection, triggerThreshold, summarizerSection, costCapEnabled, costCap,
		toolOutputSection,
		toolDiscoveryEnabled, toolDiscoveryStrategy, config.DefaultCompresrAPIBaseURL, toolDiscoveryModel,
		toolDiscoveryTokenThreshold, slackEnabled, telemetryEnabled)
}

// getProviderKeyURL returns the URL where users can get API keys for a provider.
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

// validateAPIKeyFormat validates the format of an API key for a provider.
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
