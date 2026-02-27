package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/compresr/context-gateway/internal/compresr"
	"github.com/compresr/context-gateway/internal/tui"
	"github.com/compresr/context-gateway/internal/utils"
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
	ToolOutputStrategy    string           // external_provider, api
	ToolOutputProvider    tui.ProviderInfo // Provider for compression (external_provider strategy)
	ToolOutputModel       string           // Model for compression
	ToolOutputAPIKey      string           //nolint:gosec // config template placeholder, not a secret
	ToolOutputMinBytes    int              // Minimum bytes to trigger compression
	ToolOutputTargetRatio float64          // Target compression ratio
	// Compresr API settings (shared by tool_discovery and tool_output when using api strategy)
	CompresrAPIKey string //nolint:gosec // config template placeholder, not a secret
	// Logging settings
	TelemetryEnabled     bool                       // Enable JSONL telemetry logs
	ToolOutputPricing    *compresr.ModelPricingData // Cached pricing for tool output models
	ToolDiscoveryPricing *compresr.ModelPricingData // Cached pricing for tool discovery models
}

// promptCompresrAPIKeyAndFetchPricing prompts for Compresr API key and fetches pricing for a model group.
// modelGroup should be "tool-output" or "tool-discovery".
// Returns the pricing data if successful, nil if cancelled or failed.
func promptCompresrAPIKeyAndFetchPricing(state *ConfigState, modelGroup string) *compresr.ModelPricingData {
	envVar := tui.CompresrModels.EnvVar
	existingKey := os.Getenv(envVar)

	var apiKey string

	if existingKey != "" {
		// API key exists - ask if user wants to use it or enter a new one
		items := []tui.MenuItem{
			{Label: "Use existing key", Description: utils.MaskKeyShort(existingKey), Value: "use_existing"},
			{Label: "Enter new key", Value: "new_key"},
			{Label: "← Back", Value: "back"},
		}
		idx, err := tui.SelectMenu("Compresr API Key", items)
		if err != nil || items[idx].Value == "back" {
			return nil
		}

		switch items[idx].Value {
		case "use_existing":
			apiKey = existingKey
		case "new_key":
			apiKey = promptNewCompresrAPIKey()
			if apiKey == "" {
				return nil
			}
		}
	} else {
		// No existing key - prompt for new one
		apiKey = promptNewCompresrAPIKey()
		if apiKey == "" {
			return nil
		}
	}

	// Fetch pricing with the API key
	pricing := fetchModelPricing(apiKey, modelGroup)
	if pricing == nil {
		// Fetch failed - offer retry
		items := []tui.MenuItem{
			{Label: "Try again", Value: "retry"},
			{Label: "← Back", Value: "back"},
		}
		idx, err := tui.SelectMenu("Failed to fetch pricing", items)
		if err != nil || items[idx].Value == "back" {
			return nil
		}
		return promptCompresrAPIKeyAndFetchPricing(state, modelGroup)
	}

	// Success - save the key for session
	_ = os.Setenv(envVar, apiKey)
	state.CompresrAPIKey = "${" + envVar + ":-}"

	// If this was a new key, save it permanently
	if apiKey != existingKey {
		saveCompresrAPIKey(apiKey)
	}

	return pricing
}

// promptNewCompresrAPIKey prompts for a new Compresr API key.
// Returns the key if entered, empty string if cancelled.
func promptNewCompresrAPIKey() string {
	envVar := tui.CompresrModels.EnvVar

	fmt.Printf("\n  Get your API key at: %shttps://compresr.ai/dashboard%s\n\n", tui.ColorCyan, tui.ColorReset)
	key := tui.PromptInput(fmt.Sprintf("Enter your %s: ", envVar))
	return key
}

// saveCompresrAPIKey saves the API key to the global config.
func saveCompresrAPIKey(apiKey string) {
	envVar := tui.CompresrModels.EnvVar

	items := []tui.MenuItem{
		{Label: "Yes, save to config", Value: "yes"},
		{Label: "No, session only", Value: "no"},
	}
	idx, _ := tui.SelectMenu("Save API key?", items)
	if idx == 0 {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			envPath := filepath.Join(homeDir, ".config", "context-gateway", ".env")
			appendToEnvFile(envPath, envVar, apiKey)
			fmt.Printf("%s✓%s Saved %s\n", tui.ColorGreen, tui.ColorReset, envVar)
		}
	}
}

// fetchModelPricing fetches pricing for a model group using the provided API key.
// Returns nil if the request failed.
func fetchModelPricing(apiKey string, modelGroup string) *compresr.ModelPricingData {
	client := compresr.NewClient("", apiKey)

	fmt.Printf("  %sFetching pricing...%s", tui.ColorDim, tui.ColorReset)

	pricing, err := client.GetModelsPricing(modelGroup)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "invalid API key") || strings.Contains(errStr, "401") {
			fmt.Printf("\r\033[2K  %s✗ Invalid API key%s\n", tui.ColorRed, tui.ColorReset)
		} else {
			fmt.Printf("\r\033[2K  %s✗ Failed to fetch pricing: %s%s\n", tui.ColorRed, err.Error(), tui.ColorReset)
		}
		return nil
	}

	fmt.Printf("\r\033[2K  %s✓%s %s%s%s tier (%.2f credits remaining)\n",
		tui.ColorGreen, tui.ColorReset,
		tui.ColorBold, pricing.UserTierDisplay, tui.ColorReset,
		pricing.CreditsRemaining)

	return pricing
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
	state.ToolDiscoveryEnabled = false
	state.ToolDiscoveryStrategy = "relevance"
	state.ToolDiscoveryMinTools = 5
	state.ToolDiscoveryMaxTools = 25
	state.ToolDiscoveryTargetRatio = 0.8
	state.ToolDiscoverySearchFallback = true
	state.ToolDiscoveryModel = tui.CompresrModels.ToolDiscovery.DefaultModel
	// Tool Output Compression defaults
	state.ToolOutputEnabled = false
	state.ToolOutputStrategy = "api"
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
		authType := "subscription"
		if !state.UseSubscription {
			authType = "API key"
		}
		compactDesc := fmt.Sprintf("%s / %s / %s / %.0f%%", state.Provider.DisplayName, state.Model, authType, state.TriggerThreshold)

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
	if state.ToolOutputStrategy == "api" {
		return fmt.Sprintf("● api / %s", state.ToolOutputModel)
	}
	return fmt.Sprintf("● %s / %s", state.ToolOutputProvider.DisplayName, state.ToolOutputModel)
}

func toolDiscoverySummary(state *ConfigState) string {
	if !state.ToolDiscoveryEnabled {
		return "○ Disabled"
	}
	if state.ToolDiscoveryStrategy == "api" {
		return fmt.Sprintf("● %s / %s", state.ToolDiscoveryStrategy, state.ToolDiscoveryModel)
	}
	return fmt.Sprintf("● %s (min=%d max=%d)", state.ToolDiscoveryStrategy, state.ToolDiscoveryMinTools, state.ToolDiscoveryMaxTools)
}

// editToolDiscovery opens tool discovery settings submenu.
func editToolDiscovery(state *ConfigState) {
	for {
		enabledDesc := "○ Disabled"
		if state.ToolDiscoveryEnabled {
			enabledDesc = "● Enabled"
		}

		items := []tui.MenuItem{
			{Label: "Enabled", Description: enabledDesc, Value: "toggle_enabled"},
		}

		if state.ToolDiscoveryEnabled {
			items = append(items,
				tui.MenuItem{Label: "Strategy", Description: state.ToolDiscoveryStrategy, Value: "strategy"},
			)

			// API strategy: show model and API key
			if state.ToolDiscoveryStrategy == "api" {
				items = append(items,
					tui.MenuItem{Label: "Model", Description: state.ToolDiscoveryModel, Value: "model"},
				)
				// API key status
				keyStatus := "not set"
				if os.Getenv(tui.CompresrModels.EnvVar) != "" {
					keyStatus = utils.MaskKeyShort(os.Getenv(tui.CompresrModels.EnvVar))
				}
				items = append(items, tui.MenuItem{
					Label:       "API Key",
					Description: keyStatus,
					Value:       "apikey",
				})
				// Advanced settings for API strategy
				advancedDesc := fmt.Sprintf("min: %d, max: %d, ratio: %.2f", state.ToolDiscoveryMinTools, state.ToolDiscoveryMaxTools, state.ToolDiscoveryTargetRatio)
				items = append(items, tui.MenuItem{Label: "Advanced Settings", Description: advancedDesc, Value: "advanced"})
			}

			// tool-search strategy: show search fallback toggle
			if state.ToolDiscoveryStrategy == "tool-search" {
				searchFallbackDesc := "● Enabled (required)"
				items = append(items, tui.MenuItem{
					Label:       "Search Fallback",
					Description: searchFallbackDesc,
					Value:       "__info__",
				})
			}

			// relevance strategy: show filtering params
			if state.ToolDiscoveryStrategy == "relevance" {
				searchFallbackDesc := "○ Disabled"
				if state.ToolDiscoverySearchFallback {
					searchFallbackDesc = "● Enabled"
				}
				items = append(items,
					tui.MenuItem{Label: "Min Tools", Description: strconv.Itoa(state.ToolDiscoveryMinTools), Value: "min_tools", Editable: true},
					tui.MenuItem{Label: "Max Tools", Description: strconv.Itoa(state.ToolDiscoveryMaxTools), Value: "max_tools", Editable: true},
					tui.MenuItem{Label: "Target Ratio", Description: fmt.Sprintf("%.2f", state.ToolDiscoveryTargetRatio), Value: "target_ratio", Editable: true},
					tui.MenuItem{Label: "Search Fallback", Description: searchFallbackDesc, Value: "toggle_search_fallback"},
				)
			}
		}

		items = append(items, tui.MenuItem{Label: "← Back", Value: "back"})

		idx, err := tui.SelectMenu("Tool Discovery Settings", items)

		// Process editable fields BEFORE checking for back (user may have edited inline)
		minToolsInvalid := false
		maxToolsInvalid := false
		targetRatioInvalid := false
		for _, item := range items {
			switch item.Value {
			case "min_tools":
				expected := strconv.Itoa(state.ToolDiscoveryMinTools)
				if item.Editable && item.Description != expected {
					if v, parseErr := strconv.Atoi(strings.TrimSpace(item.Description)); parseErr == nil && v >= 1 {
						state.ToolDiscoveryMinTools = v
					} else {
						minToolsInvalid = true
					}
				}
			case "max_tools":
				expected := strconv.Itoa(state.ToolDiscoveryMaxTools)
				if item.Editable && item.Description != expected {
					if v, parseErr := strconv.Atoi(strings.TrimSpace(item.Description)); parseErr == nil && v >= 1 {
						state.ToolDiscoveryMaxTools = v
					} else {
						maxToolsInvalid = true
					}
				}
			case "target_ratio":
				expected := fmt.Sprintf("%.2f", state.ToolDiscoveryTargetRatio)
				if item.Editable && item.Description != expected {
					if v, parseErr := strconv.ParseFloat(strings.TrimSpace(item.Description), 64); parseErr == nil && v > 0 && v <= 1 {
						state.ToolDiscoveryTargetRatio = v
					} else {
						targetRatioInvalid = true
					}
				}
			}
		}

		if err != nil || items[idx].Value == "back" {
			return
		}

		if minToolsInvalid {
			fmt.Printf("%s⚠%s Min Tools must be a whole number >= 1.\n", tui.ColorYellow, tui.ColorReset)
			continue
		}
		if maxToolsInvalid {
			fmt.Printf("%s⚠%s Max Tools must be a whole number >= 1.\n", tui.ColorYellow, tui.ColorReset)
			continue
		}
		if targetRatioInvalid {
			fmt.Printf("%s⚠%s Target Ratio must be a number between 0 and 1.\n", tui.ColorYellow, tui.ColorReset)
			continue
		}
		if state.ToolDiscoveryMaxTools < state.ToolDiscoveryMinTools {
			fmt.Printf("%s⚠%s Max Tools must be >= Min Tools.\n", tui.ColorYellow, tui.ColorReset)
			continue
		}

		switch items[idx].Value {
		case "toggle_enabled":
			state.ToolDiscoveryEnabled = !state.ToolDiscoveryEnabled
		case "strategy":
			selectToolDiscoveryStrategy(state)
		case "model":
			selectToolDiscoveryModel(state)
		case "apikey":
			promptAndSetCompresrAPIKey(state, "tool-discovery")
		case "toggle_search_fallback":
			state.ToolDiscoverySearchFallback = !state.ToolDiscoverySearchFallback
		case "advanced":
			editToolDiscoveryAdvanced(state)
		}
	}
}

// editToolDiscoveryAdvanced opens the advanced settings submenu for tool discovery
func editToolDiscoveryAdvanced(state *ConfigState) {
	for {
		searchFallbackDesc := "○ Disabled"
		if state.ToolDiscoverySearchFallback {
			searchFallbackDesc = "● Enabled"
		}

		items := []tui.MenuItem{
			{Label: "Min Tools", Description: strconv.Itoa(state.ToolDiscoveryMinTools), Value: "min_tools", Editable: true},
			{Label: "Max Tools", Description: strconv.Itoa(state.ToolDiscoveryMaxTools), Value: "max_tools", Editable: true},
			{Label: "Target Ratio", Description: fmt.Sprintf("%.2f", state.ToolDiscoveryTargetRatio), Value: "target_ratio", Editable: true},
			{Label: "Search Fallback", Description: searchFallbackDesc, Value: "toggle_search_fallback"},
			{Label: "← Back", Value: "back"},
		}

		idx, err := tui.SelectMenu("Advanced Tool Discovery Settings", items)

		// Process editable fields BEFORE checking for back (user may have edited inline)
		minToolsInvalid := false
		maxToolsInvalid := false
		targetRatioInvalid := false
		for _, item := range items {
			switch item.Value {
			case "min_tools":
				expected := strconv.Itoa(state.ToolDiscoveryMinTools)
				if item.Editable && item.Description != expected {
					if v, parseErr := strconv.Atoi(strings.TrimSpace(item.Description)); parseErr == nil && v >= 1 {
						state.ToolDiscoveryMinTools = v
					} else {
						minToolsInvalid = true
					}
				}
			case "max_tools":
				expected := strconv.Itoa(state.ToolDiscoveryMaxTools)
				if item.Editable && item.Description != expected {
					if v, parseErr := strconv.Atoi(strings.TrimSpace(item.Description)); parseErr == nil && v >= 1 {
						state.ToolDiscoveryMaxTools = v
					} else {
						maxToolsInvalid = true
					}
				}
			case "target_ratio":
				expected := fmt.Sprintf("%.2f", state.ToolDiscoveryTargetRatio)
				if item.Editable && item.Description != expected {
					if v, parseErr := strconv.ParseFloat(strings.TrimSpace(item.Description), 64); parseErr == nil && v > 0 && v <= 1 {
						state.ToolDiscoveryTargetRatio = v
					} else {
						targetRatioInvalid = true
					}
				}
			}
		}

		if err != nil || items[idx].Value == "back" {
			return
		}

		if minToolsInvalid {
			fmt.Printf("%s⚠%s Min Tools must be a whole number >= 1.\n", tui.ColorYellow, tui.ColorReset)
			continue
		}
		if maxToolsInvalid {
			fmt.Printf("%s⚠%s Max Tools must be a whole number >= 1.\n", tui.ColorYellow, tui.ColorReset)
			continue
		}
		if targetRatioInvalid {
			fmt.Printf("%s⚠%s Target Ratio must be a number between 0 and 1.\n", tui.ColorYellow, tui.ColorReset)
			continue
		}
		if state.ToolDiscoveryMaxTools < state.ToolDiscoveryMinTools {
			fmt.Printf("%s⚠%s Max Tools must be >= Min Tools.\n", tui.ColorYellow, tui.ColorReset)
			continue
		}

		switch items[idx].Value {
		case "toggle_search_fallback":
			state.ToolDiscoverySearchFallback = !state.ToolDiscoverySearchFallback
		}
	}
}

func selectToolDiscoveryStrategy(state *ConfigState) {
	items := []tui.MenuItem{
		{Label: "api", Description: "Compresr API selects relevant tools", Value: "api"},
		{Label: "tool-search", Description: "LLM searches via regex pattern", Value: "tool-search"},
		{Label: "relevance", Description: "local keyword scoring", Value: "relevance"},
		{Label: "passthrough", Description: "no filtering", Value: "passthrough"},
		{Label: "← Back", Value: "back"},
	}

	idx, err := tui.SelectMenu("Tool Discovery Strategy", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	selectedStrategy := items[idx].Value

	// If API strategy selected, prompt for API key and fetch pricing
	if selectedStrategy == "api" {
		pricing := promptCompresrAPIKeyAndFetchPricing(state, "tool-discovery")
		if pricing == nil {
			// User cancelled or fetch failed - don't change strategy
			return
		}
		state.ToolDiscoveryPricing = pricing
		// Auto-select first available model
		for _, m := range pricing.Models {
			if !m.Locked {
				state.ToolDiscoveryModel = m.Name
				break
			}
		}
	}

	state.ToolDiscoveryStrategy = selectedStrategy
}

// selectToolDiscoveryModel shows model selection for tool discovery API strategy
func selectToolDiscoveryModel(state *ConfigState) {
	var items []tui.MenuItem

	// If we have pricing data from the API, use it
	if state.ToolDiscoveryPricing != nil && len(state.ToolDiscoveryPricing.Models) > 0 {
		items = make([]tui.MenuItem, len(state.ToolDiscoveryPricing.Models)+1)
		for i, m := range state.ToolDiscoveryPricing.Models {
			desc := fmt.Sprintf("$%.2f/1M tokens", m.InputPricePer1M)
			if m.Locked {
				desc = fmt.Sprintf("🔒 %s tier required", m.MinSubscription)
			}
			items[i] = tui.MenuItem{
				Label:        m.DisplayName,
				Description:  desc,
				Value:        m.Name,
				Locked:       m.Locked,
				LockedReason: fmt.Sprintf("Requires %s tier", m.MinSubscription),
			}
		}
	} else {
		// Fall back to hardcoded models from YAML
		models := tui.CompresrModels.ToolDiscovery.Models
		items = make([]tui.MenuItem, len(models)+1)
		for i, m := range models {
			desc := m.Description
			if m.Recommended {
				desc += " (recommended)"
			}
			items[i] = tui.MenuItem{
				Label:       m.Name,
				Description: desc,
				Value:       m.Name,
				Locked:      false,
			}
		}
	}
	items[len(items)-1] = tui.MenuItem{Label: "← Back", Value: "back"}

	idx, err := tui.SelectMenu("Select Tool Discovery Model", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	state.ToolDiscoveryModel = items[idx].Value
}

// promptAndSetCompresrAPIKey prompts for Compresr API key and fetches pricing.
// modelGroup should be "tool-discovery" or "tool-output".
func promptAndSetCompresrAPIKey(state *ConfigState, modelGroup string) {
	// Using the shared function - it handles key prompt, validation, and pricing fetch
	pricing := promptCompresrAPIKeyAndFetchPricing(state, modelGroup)
	if pricing == nil {
		return // User cancelled or fetch failed
	}

	// Store the pricing for the appropriate feature
	switch modelGroup {
	case "tool-discovery":
		state.ToolDiscoveryPricing = pricing
	case "tool-output":
		state.ToolOutputPricing = pricing
	}
}

// editCompact opens the compact (preemptive summarization) settings submenu
func editCompact(state *ConfigState, agentName string) {
	for {
		// Build auth description based on context
		authDesc := ""
		// Claude Code + Anthropic: show subscription/api-key
		if agentName == "claude_code" && state.Provider.Name == "anthropic" {
			if state.UseSubscription {
				authDesc = "subscription"
			} else {
				authDesc = "api-key"
			}
			// Codex + OpenAI: show subscription/api-key
		} else if agentName == "codex" && state.Provider.Name == "openai" {
			if state.UseSubscription {
				authDesc = "subscription"
			} else {
				authDesc = "api-key"
			}
		} else {
			keyStatus := "not set"
			if os.Getenv(state.Provider.EnvVar) != "" {
				keyStatus = utils.MaskKeyShort(os.Getenv(state.Provider.EnvVar))
			}
			authDesc = keyStatus
		}

		items := []tui.MenuItem{
			{Label: "Model", Description: state.Model, Value: "model"},
			{Label: "Auth", Description: authDesc, Value: "auth"},
			{
				Label:       "Trigger %",
				Description: fmt.Sprintf("%.0f", state.TriggerThreshold),
				Value:       "edit_trigger",
				Editable:    true,
			},
			{Label: "← Back", Value: "back"},
		}

		idx, err := tui.SelectMenu("Compact Settings", items)

		// Process editable fields BEFORE checking for back (user may have edited inline)
		for _, item := range items {
			if item.Value == "edit_trigger" && item.Editable {
				desc := strings.TrimSpace(item.Description)
				if desc == "" {
					continue
				}
				if item.Description != fmt.Sprintf("%.0f", state.TriggerThreshold) {
					if v, parseErr := strconv.ParseFloat(desc, 64); parseErr == nil {
						if v >= 1 && v <= 99 {
							state.TriggerThreshold = v
						} else {
							fmt.Printf("%s⚠%s Trigger value must be between 1 and 99.\n", tui.ColorYellow, tui.ColorReset)
						}
					} else {
						fmt.Printf("%s⚠%s Invalid trigger value.\n", tui.ColorYellow, tui.ColorReset)
					}
				}
			}
		}

		if err != nil || items[idx].Value == "back" {
			return
		}

		switch items[idx].Value {
		case "model":
			selectCompactModel(state)

		case "auth":
			selectCompactAuth(state, agentName)

		case "edit_trigger":
			continue
		}
	}
}

// findProviderByModel finds the provider that contains the given model.
// Returns the provider and true if found, or empty provider and false if not found.
func findProviderByModel(modelName string) (tui.ProviderInfo, bool) {
	for _, p := range tui.SupportedProviders {
		for _, m := range p.Models {
			if m == modelName {
				return p, true
			}
		}
	}
	return tui.ProviderInfo{}, false
}

// selectCompactModel shows a flat list of all models from all providers
func selectCompactModel(state *ConfigState) {
	var items []tui.MenuItem

	// Build flat list with provider as description
	for _, p := range tui.SupportedProviders {
		for _, m := range p.Models {
			desc := p.DisplayName
			if m == p.DefaultModel {
				desc += " (recommended)"
			}
			items = append(items, tui.MenuItem{
				Label:       m,
				Description: desc,
				Value:       m,
			})
		}
	}
	items = append(items, tui.MenuItem{Label: "← Back", Value: "back"})

	idx, err := tui.SelectMenu("Select Model", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	selectedModel := items[idx].Value

	// Auto-detect provider from model
	provider, found := findProviderByModel(selectedModel)
	if !found {
		fmt.Printf("%s⚠%s Could not find provider for model\n", tui.ColorYellow, tui.ColorReset)
		return
	}

	// Update state
	state.Model = selectedModel
	state.Provider = provider
	state.APIKey = "${" + provider.EnvVar + ":-}"
}

// selectCompactAuth handles authentication selection for compact settings
func selectCompactAuth(state *ConfigState, agentName string) {
	// For Claude Code + Anthropic provider: offer subscription/api-key choice
	if agentName == "claude_code" && state.Provider.Name == "anthropic" {
		items := []tui.MenuItem{
			{Label: "Subscription", Description: "Use claude code --login", Value: "subscription"},
			{Label: "API Key", Description: "Use your own ANTHROPIC_API_KEY", Value: "api_key"},
			{Label: "← Back", Value: "back"},
		}

		idx, err := tui.SelectMenu("Authentication", items)
		if err != nil || items[idx].Value == "back" {
			return
		}

		if items[idx].Value == "subscription" {
			state.UseSubscription = true
			state.APIKey = "${ANTHROPIC_API_KEY:-}"
			return
		}

		// User chose API key - prompt for it
		state.UseSubscription = false
	}

	// For Codex + OpenAI provider: offer subscription/api-key choice
	if agentName == "codex" && state.Provider.Name == "openai" {
		items := []tui.MenuItem{
			{Label: "Subscription", Description: "ChatGPT Plus/Team (codex --login)", Value: "subscription"},
			{Label: "API Key", Description: "Use your own OPENAI_API_KEY", Value: "api_key"},
			{Label: "← Back", Value: "back"},
		}

		idx, err := tui.SelectMenu("Authentication", items)
		if err != nil || items[idx].Value == "back" {
			return
		}

		if items[idx].Value == "subscription" {
			state.UseSubscription = true
			state.APIKey = "${OPENAI_API_KEY:-}"
			// Set provider URL for ChatGPT subscription endpoint
			_ = os.Setenv("OPENAI_PROVIDER_URL", "https://chatgpt.com/backend-api")
			return
		}

		// User chose API key - set standard OpenAI API endpoint
		state.UseSubscription = false
		_ = os.Setenv("OPENAI_PROVIDER_URL", "https://api.openai.com/v1")
	}

	// For all other cases (or if user chose api_key above): prompt for API key
	promptAndSetAPIKey(state)
}

// editToolOutputCompression opens the tool output compression settings submenu
func editToolOutputCompression(state *ConfigState) {
	for {
		enabledDesc := "○ Disabled"
		if state.ToolOutputEnabled {
			enabledDesc = "● Enabled"
		}

		items := []tui.MenuItem{
			{Label: "Enabled", Description: enabledDesc, Value: "toggle_enabled"},
		}

		if state.ToolOutputEnabled {
			items = append(items,
				tui.MenuItem{Label: "Strategy", Description: state.ToolOutputStrategy, Value: "strategy"},
			)

			// Show different options based on strategy
			if state.ToolOutputStrategy == "api" {
				// API strategy: show Compresr model and API key
				items = append(items,
					tui.MenuItem{Label: "Model", Description: state.ToolOutputModel, Value: "compresr_model"},
				)
				// Compresr API key status
				keyStatus := "not set"
				if os.Getenv(tui.CompresrModels.EnvVar) != "" {
					keyStatus = utils.MaskKeyShort(os.Getenv(tui.CompresrModels.EnvVar))
				}
				items = append(items, tui.MenuItem{
					Label:       "API Key",
					Description: keyStatus,
					Value:       "compresr_apikey",
				})
			} else {
				// external_provider strategy: show LLM provider, model, and API key
				items = append(items,
					tui.MenuItem{Label: "Provider", Description: state.ToolOutputProvider.DisplayName, Value: "provider"},
					tui.MenuItem{Label: "Model", Description: state.ToolOutputModel, Value: "model"},
				)
				// Provider API key status
				keyStatus := "not set"
				if os.Getenv(state.ToolOutputProvider.EnvVar) != "" {
					keyStatus = utils.MaskKeyShort(os.Getenv(state.ToolOutputProvider.EnvVar))
				}
				items = append(items, tui.MenuItem{
					Label:       "API Key",
					Description: keyStatus,
					Value:       "apikey",
				})
			}

			// Advanced settings (shown for all strategies when enabled)
			advancedDesc := fmt.Sprintf("min_bytes: %d", state.ToolOutputMinBytes)
			items = append(items, tui.MenuItem{Label: "Advanced Settings", Description: advancedDesc, Value: "advanced"})
		}

		items = append(items, tui.MenuItem{Label: "← Back", Value: "back"})

		idx, err := tui.SelectMenu("Compression Settings", items)
		if err != nil || items[idx].Value == "back" {
			return
		}

		switch items[idx].Value {
		case "toggle_enabled":
			state.ToolOutputEnabled = !state.ToolOutputEnabled
		case "strategy":
			selectToolOutputStrategy(state)
			// Reset model when strategy changes
			if state.ToolOutputStrategy == "api" {
				state.ToolOutputModel = tui.CompresrModels.ToolOutput.DefaultModel
			} else {
				state.ToolOutputModel = state.ToolOutputProvider.DefaultModel
			}
		case "provider":
			selectToolOutputProvider(state)
		case "model":
			selectToolOutputModel(state)
		case "compresr_model":
			selectToolOutputCompresrModel(state)
		case "apikey":
			promptAndSetToolOutputAPIKey(state)
		case "compresr_apikey":
			promptAndSetCompresrAPIKey(state, "tool-output")
		case "advanced":
			editToolOutputAdvanced(state)
		}
	}
}

// editToolOutputAdvanced opens the advanced settings submenu for tool output compression
func editToolOutputAdvanced(state *ConfigState) {
	for {
		items := []tui.MenuItem{
			{Label: "Min Bytes", Description: strconv.Itoa(state.ToolOutputMinBytes), Value: "min_bytes", Editable: true},
			{Label: "← Back", Value: "back"},
		}

		idx, err := tui.SelectMenu("Advanced Compression Settings", items)

		// Process editable fields BEFORE checking for back (user may have edited inline)
		minBytesInvalid := false
		for _, item := range items {
			switch item.Value {
			case "min_bytes":
				expected := strconv.Itoa(state.ToolOutputMinBytes)
				if item.Editable && item.Description != expected {
					if v, parseErr := strconv.Atoi(strings.TrimSpace(item.Description)); parseErr == nil && v >= 0 {
						state.ToolOutputMinBytes = v
					} else {
						minBytesInvalid = true
					}
				}
			}
		}

		if err != nil || items[idx].Value == "back" {
			return
		}

		if minBytesInvalid {
			fmt.Printf("%s⚠%s Min Bytes must be a whole number >= 0.\n", tui.ColorYellow, tui.ColorReset)
			continue
		}
	}
}

// selectToolOutputStrategy shows strategy selection for tool output compression
func selectToolOutputStrategy(state *ConfigState) {
	items := []tui.MenuItem{
		{Label: "api", Description: "Compresr API compresses tool outputs", Value: "api"},
		{Label: "external_provider", Description: "Use LLM provider to compress", Value: "external_provider"},
		{Label: "← Back", Value: "back"},
	}

	idx, err := tui.SelectMenu("Compression Strategy", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	selectedStrategy := items[idx].Value

	// If API strategy selected, prompt for API key and fetch pricing
	if selectedStrategy == "api" {
		pricing := promptCompresrAPIKeyAndFetchPricing(state, "tool-output")
		if pricing == nil {
			// User cancelled or fetch failed - don't change strategy
			return
		}
		state.ToolOutputPricing = pricing
		// Auto-select first available model
		for _, m := range pricing.Models {
			if !m.Locked {
				state.ToolOutputModel = m.Name
				break
			}
		}
	}

	state.ToolOutputStrategy = selectedStrategy
}

// selectToolOutputCompresrModel shows model selection for tool output API strategy
func selectToolOutputCompresrModel(state *ConfigState) {
	var items []tui.MenuItem

	// If we have pricing data from the API, use it
	if state.ToolOutputPricing != nil && len(state.ToolOutputPricing.Models) > 0 {
		items = make([]tui.MenuItem, len(state.ToolOutputPricing.Models)+1)
		for i, m := range state.ToolOutputPricing.Models {
			desc := fmt.Sprintf("$%.2f/1M tokens", m.InputPricePer1M)
			if m.Locked {
				desc = fmt.Sprintf("🔒 %s tier required", m.MinSubscription)
			}
			items[i] = tui.MenuItem{
				Label:        m.DisplayName,
				Description:  desc,
				Value:        m.Name,
				Locked:       m.Locked,
				LockedReason: fmt.Sprintf("Requires %s tier", m.MinSubscription),
			}
		}
	} else {
		// Fall back to hardcoded models from YAML
		models := tui.CompresrModels.ToolOutput.Models
		items = make([]tui.MenuItem, len(models)+1)
		for i, m := range models {
			desc := m.Description
			if m.Recommended {
				desc += " (recommended)"
			}
			items[i] = tui.MenuItem{
				Label:       m.Name,
				Description: desc,
				Value:       m.Name,
				Locked:      false,
			}
		}
	}
	items[len(items)-1] = tui.MenuItem{Label: "← Back", Value: "back"}

	idx, err := tui.SelectMenu("Select Compression Model", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	state.ToolOutputModel = items[idx].Value
}

// selectToolOutputProvider shows provider selection for tool output compression
func selectToolOutputProvider(state *ConfigState) {
	items := make([]tui.MenuItem, len(tui.SupportedProviders)+1)
	for i, p := range tui.SupportedProviders {
		items[i] = tui.MenuItem{
			Label:       p.DisplayName,
			Description: p.EnvVar,
			Value:       p.Name,
		}
	}
	items[len(tui.SupportedProviders)] = tui.MenuItem{Label: "← Back", Value: "back"}

	idx, err := tui.SelectMenu("Select Compression Provider", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	state.ToolOutputProvider = tui.SupportedProviders[idx]
	state.ToolOutputModel = state.ToolOutputProvider.DefaultModel
	state.ToolOutputAPIKey = "${" + state.ToolOutputProvider.EnvVar + ":-}"
}

// selectToolOutputModel shows model selection for tool output compression (external_provider strategy)
func selectToolOutputModel(state *ConfigState) {
	// Use models from provider config
	items := make([]tui.MenuItem, len(state.ToolOutputProvider.Models)+1)
	for i, m := range state.ToolOutputProvider.Models {
		desc := ""
		if m == state.ToolOutputProvider.DefaultModel {
			desc = "recommended"
		}
		items[i] = tui.MenuItem{Label: m, Description: desc, Value: m}
	}
	items[len(state.ToolOutputProvider.Models)] = tui.MenuItem{Label: "← Back", Value: "back"}

	idx, err := tui.SelectMenu("Select Compression Model", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	state.ToolOutputModel = items[idx].Value
}

// promptAndSetToolOutputAPIKey prompts for API key for tool output compression
func promptAndSetToolOutputAPIKey(state *ConfigState) {
	existingKey := os.Getenv(state.ToolOutputProvider.EnvVar)
	if existingKey != "" {
		items := []tui.MenuItem{
			{Label: "Use existing", Description: utils.MaskKeyShort(existingKey), Value: "yes"},
			{Label: "Enter new", Value: "no"},
			{Label: "← Back", Value: "back"},
		}
		idx, err := tui.SelectMenu(state.ToolOutputProvider.EnvVar, items)
		if err != nil || items[idx].Value == "back" {
			return
		}
		if items[idx].Value == "yes" {
			state.ToolOutputAPIKey = "${" + state.ToolOutputProvider.EnvVar + ":-}"
			return
		}
	}

	fmt.Printf("\n  Get key at: %s\n", getProviderKeyURL(state.ToolOutputProvider.Name))
	enteredKey := tui.PromptInput(fmt.Sprintf("Enter %s: ", state.ToolOutputProvider.EnvVar))
	if enteredKey == "" {
		return
	}

	if !validateAPIKeyFormat(state.ToolOutputProvider.Name, enteredKey) {
		fmt.Printf("%s⚠%s Key format looks unusual\n", tui.ColorYellow, tui.ColorReset)
	}

	_ = os.Setenv(state.ToolOutputProvider.EnvVar, enteredKey)

	items := []tui.MenuItem{
		{Label: "Yes, save permanently", Value: "yes"},
		{Label: "No, session only", Value: "no"},
	}
	idx, _ := tui.SelectMenu("Save API key?", items)
	if idx == 0 {
		persistCredential(state.ToolOutputProvider.EnvVar, enteredKey, ScopeGlobal)
		fmt.Printf("%s✓%s Saved\n", tui.ColorGreen, tui.ColorReset)
	}
	state.ToolOutputAPIKey = "${" + state.ToolOutputProvider.EnvVar + ":-}"
}

// promptAndSetAPIKey prompts for API key
func promptAndSetAPIKey(state *ConfigState) {
	existingKey := os.Getenv(state.Provider.EnvVar)
	if existingKey != "" {
		items := []tui.MenuItem{
			{Label: "Use existing", Description: utils.MaskKeyShort(existingKey), Value: "yes"},
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
	enteredKey := tui.PromptInput(fmt.Sprintf("Enter %s: ", state.Provider.EnvVar))
	if enteredKey == "" {
		return
	}

	if !validateAPIKeyFormat(state.Provider.Name, enteredKey) {
		fmt.Printf("%s⚠%s Key format looks unusual\n", tui.ColorYellow, tui.ColorReset)
	}

	_ = os.Setenv(state.Provider.EnvVar, enteredKey)

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
			// Extract model from api section
			if api, ok := toolOutput["api"].(map[string]interface{}); ok {
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
			// Extract model from api section
			if api, ok := toolDiscovery["api"].(map[string]interface{}); ok {
				if model, ok := api["model"].(string); ok {
					state.ToolDiscoveryModel = model
				}
			}
		}
	}
	// Set tool_output defaults if not found
	if state.ToolOutputStrategy == "" {
		state.ToolOutputStrategy = "api"
	}
	// Set model based on strategy
	if state.ToolOutputModel == "" {
		if state.ToolOutputStrategy == "api" {
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
		state.ToolDiscoveryStrategy = "relevance"
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
