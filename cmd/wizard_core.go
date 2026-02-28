package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/compresr/context-gateway/internal/compresr"
	"github.com/compresr/context-gateway/internal/pipes"
	"github.com/compresr/context-gateway/internal/preemptive"
	"github.com/compresr/context-gateway/internal/tui"
	"gopkg.in/yaml.v3"
)

// ConfigState holds the configuration state during wizard editing.
type ConfigState struct {
	Name                        string
	Provider                    tui.ProviderInfo
	Model                       string
	APIKey                      string //nolint:gosec // config template placeholder, not a secret
	UseSubscription             bool
	SlackEnabled                bool
	SlackConfigured             bool    // True if Slack credentials exist
	TriggerThreshold            float64 // Context usage % to trigger summarization (1-99)
	CostCap                     float64 // USD aggregate spend cap. 0 = unlimited (disabled).
	ToolDiscoveryEnabled        bool
	ToolDiscoveryStrategy       string
	ToolDiscoveryMinTools       int
	ToolDiscoveryMaxTools       int
	ToolDiscoveryTargetRatio    float64
	ToolDiscoverySearchFallback bool
	ToolDiscoveryModel          string // Model for API strategy
	// Tool Output Compression settings
	ToolOutputEnabled     bool
	ToolOutputStrategy    string           // external_provider, compresr
	ToolOutputProvider    tui.ProviderInfo // Provider for compression (external_provider strategy)
	ToolOutputModel       string           // Model for compression
	ToolOutputAPIKey      string           //nolint:gosec // config template placeholder, not a secret
	ToolOutputMinBytes    int              // Minimum bytes to trigger compression
	ToolOutputTargetRatio float64          // Target compression ratio
	// Compact (preemptive summarization) strategy settings
	CompactStrategy      string // "compresr" or "external_provider" (LLM)
	CompactCompresrModel string // HCC model when using compresr strategy
	// Compresr API settings (shared by tool_discovery, tool_output, and compact when using compresr strategy)
	CompresrAPIKey string //nolint:gosec // config template placeholder, not a secret
	// Logging settings
	TelemetryEnabled     bool                       // Enable JSONL telemetry logs
	ToolOutputPricing    *compresr.ModelPricingData // Cached pricing for tool output models
	ToolDiscoveryPricing *compresr.ModelPricingData // Cached pricing for tool discovery models
	CompactPricing       *compresr.ModelPricingData // Cached pricing for HCC models
}

// runConfigCreationWizard runs the config creation with summary editor.
// Returns the config name or empty string if cancelled.
func runConfigCreationWizard(agentName string, ac *AgentConfig) string {
	state := &ConfigState{}

	// Set defaults based on agent type
	if agentName == "codex" {
		// Codex: OpenAI with subscription (ChatGPT Plus/Team)
		for _, p := range tui.SupportedProviders {
			if p.Name == "openai" {
				state.Provider = p
				break
			}
		}
		if state.Provider.Name == "" {
			state.Provider = tui.SupportedProviders[0] // fallback
		}
		state.Model = state.Provider.DefaultModel
		state.UseSubscription = true
		state.APIKey = "${OPENAI_API_KEY:-}"
		// Set ChatGPT subscription endpoint
		_ = os.Setenv("OPENAI_PROVIDER_URL", "https://chatgpt.com/backend-api")
	} else {
		// Claude Code and others: Anthropic with subscription
		state.Provider = tui.SupportedProviders[0] // anthropic
		state.Model = state.Provider.DefaultModel  // default to haiku
		state.UseSubscription = true
		state.APIKey = "${ANTHROPIC_API_KEY:-}"
	}
	state.TriggerThreshold = 85.0 // Trigger at 85% context usage
	state.CompactStrategy = preemptive.StrategyCompresr
	state.CompactCompresrModel = tui.CompresrModels.History.DefaultModel
	state.ToolDiscoveryEnabled = true
	state.ToolDiscoveryStrategy = pipes.StrategyCompresr
	state.ToolDiscoveryMinTools = 5
	state.ToolDiscoveryMaxTools = 25
	state.ToolDiscoveryTargetRatio = 0.8
	state.ToolDiscoverySearchFallback = true
	state.ToolDiscoveryModel = tui.CompresrModels.ToolDiscovery.DefaultModel
	// Tool Output Compression defaults
	state.ToolOutputEnabled = true
	state.ToolOutputStrategy = pipes.StrategyCompresr
	state.ToolOutputModel = tui.CompresrModels.ToolOutput.DefaultModel
	state.ToolOutputMinBytes = 2048
	state.ToolOutputTargetRatio = 0.15
	// Fallback external provider settings (used if user switches to external_provider)
	state.ToolOutputProvider = tui.SupportedProviders[1] // gemini
	state.ToolOutputAPIKey = "${" + state.ToolOutputProvider.EnvVar + ":-}"
	// Compresr API defaults
	state.CompresrAPIKey = "${COMPRESR_API_KEY:-}"
	// Logging defaults
	state.TelemetryEnabled = false

	// Check if Slack is already configured (webhook URL or legacy bot token)
	slackWebhook := os.Getenv("SLACK_WEBHOOK_URL") != ""
	slackBotToken := os.Getenv("SLACK_BOT_TOKEN") != "" && os.Getenv("SLACK_CHANNEL_ID") != ""
	state.SlackConfigured = (slackWebhook || slackBotToken) && isSlackHookInstalled()
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
		var compactDesc string
		if state.CompactStrategy == preemptive.StrategyCompresr {
			compactDesc = fmt.Sprintf("compresr / %s / %.0f%%", state.CompactCompresrModel, state.TriggerThreshold)
		} else {
			authType := "subscription"
			if !state.UseSubscription {
				authType = "API key"
			}
			compactDesc = fmt.Sprintf("%s / %s / %s / %.0f%%", state.Provider.DisplayName, state.Model, authType, state.TriggerThreshold)
		}

		costCapDesc := "unlimited"
		if state.CostCap > 0 {
			costCapDesc = fmt.Sprintf("$%.2f", state.CostCap)
		}

		items := []tui.MenuItem{
			{Label: "Compact", Description: compactDesc, Value: "edit_compact"},
			{Label: "Tool Compression", Description: toolOutputSummary(state), Value: "edit_compression"},
			{Label: "Tool Discovery", Description: toolDiscoverySummary(state), Value: "edit_tool_discovery"},
			{Label: "Cost Cap $", Description: costCapDesc, Value: "edit_cost_cap", Editable: true},
		}

		// Telemetry toggle
		telemetryStatus := "○ Disabled"
		if state.TelemetryEnabled {
			telemetryStatus = "● Enabled"
		}
		items = append(items, tui.MenuItem{
			Label:       "Logging",
			Description: telemetryStatus,
			Value:       "toggle_telemetry",
		})

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
		for _, item := range items {
			if item.Value == "edit_name" && item.Editable && item.Description != state.Name {
				newName := item.Description
				state.Name = strings.ReplaceAll(newName, " ", "_")
				state.Name = strings.ReplaceAll(state.Name, "/", "_")
				// don't break; allow processing other editable fields too
			}
			if item.Value == "edit_cost_cap" && item.Editable {
				desc := strings.TrimSpace(item.Description)
				if desc == "" || desc == "unlimited" || desc == "0" {
					state.CostCap = 0
				} else {
					// Strip leading $ if present
					desc = strings.TrimPrefix(desc, "$")
					if v, err := strconv.ParseFloat(desc, 64); err == nil && v >= 0 {
						state.CostCap = v
					}
				}
			}
		}

		switch items[idx].Value {
		case "edit_name":
			// Name already updated above, just re-render
			continue

		case "edit_compact":
			editCompact(state, agentName)

		case "edit_compression":
			editToolOutputCompression(state)

		case "edit_tool_discovery":
			editToolDiscovery(state)

		case "toggle_telemetry":
			state.TelemetryEnabled = !state.TelemetryEnabled

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
			return saveConfig(state)

		case "back":
			return "__back__"
		}
	}
}

func toolOutputSummary(state *ConfigState) string {
	if !state.ToolOutputEnabled {
		return "○ Disabled"
	}
	if state.ToolOutputStrategy == pipes.StrategyCompresr {
		return fmt.Sprintf("● compresr / %s", state.ToolOutputModel)
	}
	return fmt.Sprintf("● %s / %s", state.ToolOutputProvider.DisplayName, state.ToolOutputModel)
}

func toolDiscoverySummary(state *ConfigState) string {
	if !state.ToolDiscoveryEnabled {
		return "○ Disabled"
	}
	if state.ToolDiscoveryStrategy == pipes.StrategyCompresr {
		return fmt.Sprintf("● %s / %s", state.ToolDiscoveryStrategy, state.ToolDiscoveryModel)
	}
	return fmt.Sprintf("● %s (min=%d max=%d)", state.ToolDiscoveryStrategy, state.ToolDiscoveryMinTools, state.ToolDiscoveryMaxTools)
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
		data, err = os.ReadFile(path) // #nosec G304 -- trusted config path
	}

	// Then try local configs dir
	if err != nil {
		path := filepath.Join("configs", configName+".yaml")
		data, err = os.ReadFile(path) // #nosec G304 -- trusted config path
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

	// Extract trigger threshold and compact strategy from preemptive section
	if preemptive, ok := cfg["preemptive"].(map[string]interface{}); ok {
		if threshold, ok := preemptive["trigger_threshold"].(float64); ok {
			state.TriggerThreshold = threshold
		}
		if summarizer, ok := preemptive["summarizer"].(map[string]interface{}); ok {
			if strategy, ok := summarizer["strategy"].(string); ok {
				state.CompactStrategy = strategy
			}
			// Load compresr model from compresr section (or legacy api section)
			if api, ok := summarizer["compresr"].(map[string]interface{}); ok {
				if model, ok := api["model"].(string); ok {
					state.CompactCompresrModel = model
				}
			}
		}
	}
	// Default to 85% if not found
	if state.TriggerThreshold == 0 {
		state.TriggerThreshold = 85.0
	}
	if state.CompactStrategy == "" {
		state.CompactStrategy = preemptive.StrategyExternalProvider
	}
	if state.CompactCompresrModel == "" {
		state.CompactCompresrModel = tui.CompresrModels.History.DefaultModel
	}

	// Extract tool_output compression settings from pipes section
	if pipes, ok := cfg["pipes"].(map[string]interface{}); ok {
		if toolOutput, ok := pipes["tool_output"].(map[string]interface{}); ok {
			if enabled, ok := toolOutput["enabled"].(bool); ok {
				state.ToolOutputEnabled = enabled
			}
			if strategy, ok := toolOutput["strategy"].(string); ok {
				state.ToolOutputStrategy = strategy
			}
			if providerName, ok := toolOutput["provider"].(string); ok {
				// Find matching provider
				for _, p := range tui.SupportedProviders {
					if p.Name == providerName {
						state.ToolOutputProvider = p
						break
					}
				}
			}
			// Extract model from compresr section (or legacy api section)
			if compresrCfg, ok := toolOutput["compresr"].(map[string]interface{}); ok {
				if model, ok := compresrCfg["model"].(string); ok {
					state.ToolOutputModel = model
				}
				if apiKey, ok := compresrCfg["api_key"].(string); ok {
					state.ToolOutputAPIKey = apiKey
				}
			} else if api, ok := toolOutput["api"].(map[string]interface{}); ok {
				if model, ok := api["model"].(string); ok {
					state.ToolOutputModel = model
				}
				if apiKey, ok := api["api_key"].(string); ok {
					state.ToolOutputAPIKey = apiKey
				}
			}
		}

		// Extract tool_discovery settings
		if toolDiscovery, ok := pipes["tool_discovery"].(map[string]interface{}); ok {
			if enabled, ok := toolDiscovery["enabled"].(bool); ok {
				state.ToolDiscoveryEnabled = enabled
			}
			if strategy, ok := toolDiscovery["strategy"].(string); ok {
				state.ToolDiscoveryStrategy = strategy
			}
			// Handle both int and float64 (YAML parsing quirks)
			if minTools, ok := toolDiscovery["min_tools"].(int); ok {
				state.ToolDiscoveryMinTools = minTools
			} else if minToolsF, ok := toolDiscovery["min_tools"].(float64); ok {
				state.ToolDiscoveryMinTools = int(minToolsF)
			}
			if maxTools, ok := toolDiscovery["max_tools"].(int); ok {
				state.ToolDiscoveryMaxTools = maxTools
			} else if maxToolsF, ok := toolDiscovery["max_tools"].(float64); ok {
				state.ToolDiscoveryMaxTools = int(maxToolsF)
			}
			if targetRatio, ok := toolDiscovery["target_ratio"].(float64); ok {
				state.ToolDiscoveryTargetRatio = targetRatio
			}
			if searchFallback, ok := toolDiscovery["enable_search_fallback"].(bool); ok {
				state.ToolDiscoverySearchFallback = searchFallback
			}
			// Extract model from compresr section (or legacy api section)
			if compresrCfg, ok := toolDiscovery["compresr"].(map[string]interface{}); ok {
				if model, ok := compresrCfg["model"].(string); ok {
					state.ToolDiscoveryModel = model
				}
			} else if api, ok := toolDiscovery["api"].(map[string]interface{}); ok {
				if model, ok := api["model"].(string); ok {
					state.ToolDiscoveryModel = model
				}
			}
		}
	}
	// Set tool_output defaults if not found
	if state.ToolOutputStrategy == "" {
		state.ToolOutputStrategy = pipes.StrategyCompresr
	}
	// Set model based on strategy
	if state.ToolOutputModel == "" {
		if state.ToolOutputStrategy == pipes.StrategyCompresr {
			state.ToolOutputModel = tui.CompresrModels.ToolOutput.DefaultModel
		} else if state.ToolOutputProvider.Name != "" {
			state.ToolOutputModel = state.ToolOutputProvider.DefaultModel
		}
	}
	// Set provider defaults for external_provider strategy
	if state.ToolOutputProvider.Name == "" && len(tui.SupportedProviders) > 1 {
		state.ToolOutputProvider = tui.SupportedProviders[1] // gemini
	}
	if state.ToolOutputAPIKey == "" && state.ToolOutputProvider.Name != "" {
		state.ToolOutputAPIKey = "${" + state.ToolOutputProvider.EnvVar + ":-}"
	}

	// Set tool_discovery defaults if not found
	if state.ToolDiscoveryStrategy == "" {
		state.ToolDiscoveryStrategy = pipes.StrategyRelevance
	}
	if state.ToolDiscoveryMinTools == 0 {
		state.ToolDiscoveryMinTools = 5
	}
	if state.ToolDiscoveryMaxTools == 0 {
		state.ToolDiscoveryMaxTools = 25
	}
	if state.ToolDiscoveryTargetRatio == 0 {
		state.ToolDiscoveryTargetRatio = 0.8
	}
	if state.ToolDiscoveryModel == "" {
		state.ToolDiscoveryModel = tui.CompresrModels.ToolDiscovery.DefaultModel
	}

	// Extract telemetry settings from monitoring section
	if monitoring, ok := cfg["monitoring"].(map[string]interface{}); ok {
		if enabled, ok := monitoring["telemetry_enabled"].(bool); ok {
			state.TelemetryEnabled = enabled
		}
	}

	return state
}
