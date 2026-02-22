package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	// Tool Output Compression settings
	ToolOutputEnabled  bool
	ToolOutputStrategy string           // external_provider
	ToolOutputProvider tui.ProviderInfo // Provider for compression
	ToolOutputModel    string           // Model for compression
	ToolOutputAPIKey   string           //nolint:gosec // config template placeholder, not a secret
}

// runConfigCreationWizard runs the config creation with summary editor.
// Returns the config name or empty string if cancelled.
func runConfigCreationWizard(agentName string, ac *AgentConfig) string {
	state := &ConfigState{}

	// Set defaults: Claude Haiku with subscription
	state.Provider = tui.SupportedProviders[0] // anthropic
	state.Model = state.Provider.DefaultModel  // default to haiku
	state.UseSubscription = true
	state.APIKey = "${ANTHROPIC_API_KEY:-}"
	state.TriggerThreshold = 85.0 // Trigger at 85% context usage
	state.ToolDiscoveryEnabled = false
	state.ToolDiscoveryStrategy = "relevance"
	state.ToolDiscoveryMinTools = 5
	state.ToolDiscoveryMaxTools = 25
	state.ToolDiscoveryTargetRatio = 0.8
	state.ToolDiscoverySearchFallback = true
	// Tool Output Compression defaults
	state.ToolOutputEnabled = false
	state.ToolOutputStrategy = "external_provider"
	state.ToolOutputProvider = tui.SupportedProviders[1] // gemini
	state.ToolOutputModel = state.ToolOutputProvider.DefaultModel
	state.ToolOutputAPIKey = "${" + state.ToolOutputProvider.EnvVar + ":-}"

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
			{Label: "Compression", Description: toolOutputSummary(state), Value: "edit_compression"},
			{Label: "Tool Discovery", Description: toolDiscoverySummary(state), Value: "edit_tool_discovery"},
			{Label: "Cost Cap $", Description: costCapDesc, Value: "edit_cost_cap", Editable: true},
		}

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
	return fmt.Sprintf("● %s / %s", state.ToolOutputProvider.DisplayName, state.ToolOutputModel)
}

func toolDiscoverySummary(state *ConfigState) string {
	if !state.ToolDiscoveryEnabled {
		return "○ Disabled"
	}
	return fmt.Sprintf("● %s (min=%d max=%d ratio=%.2f)", state.ToolDiscoveryStrategy, state.ToolDiscoveryMinTools, state.ToolDiscoveryMaxTools, state.ToolDiscoveryTargetRatio)
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
			searchFallbackDesc := "○ Disabled"
			if state.ToolDiscoverySearchFallback {
				searchFallbackDesc = "● Enabled"
			}

			items = append(items,
				tui.MenuItem{Label: "Strategy", Description: state.ToolDiscoveryStrategy, Value: "strategy"},
				tui.MenuItem{Label: "Min Tools", Description: strconv.Itoa(state.ToolDiscoveryMinTools), Value: "min_tools", Editable: true},
				tui.MenuItem{Label: "Max Tools", Description: strconv.Itoa(state.ToolDiscoveryMaxTools), Value: "max_tools", Editable: true},
				tui.MenuItem{Label: "Target Ratio", Description: fmt.Sprintf("%.2f", state.ToolDiscoveryTargetRatio), Value: "target_ratio", Editable: true},
				tui.MenuItem{Label: "Search Fallback", Description: searchFallbackDesc, Value: "toggle_search_fallback"},
			)
		}

		items = append(items, tui.MenuItem{Label: "← Back", Value: "back"})

		idx, err := tui.SelectMenu("Tool Discovery Settings", items)
		if err != nil || items[idx].Value == "back" {
			return
		}

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
		case "toggle_search_fallback":
			state.ToolDiscoverySearchFallback = !state.ToolDiscoverySearchFallback
		}
	}
}

func selectToolDiscoveryStrategy(state *ConfigState) {
	items := []tui.MenuItem{
		{Label: "api", Description: "gateway search + API selector", Value: "api"},
		{Label: "relevance", Description: "local filtering", Value: "relevance"},
		{Label: "passthrough", Description: "no filtering", Value: "passthrough"},
		{Label: "← Back", Value: "back"},
	}

	idx, err := tui.SelectMenu("Tool Discovery Strategy", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	state.ToolDiscoveryStrategy = items[idx].Value
}

// editCompact opens the compact (preemptive summarization) settings submenu
func editCompact(state *ConfigState, agentName string) {
	for {
		items := []tui.MenuItem{
			{Label: "Provider", Description: state.Provider.DisplayName, Value: "provider"},
			{Label: "Model", Description: state.Model, Value: "model"},
		}

		// Claude Code + Anthropic: auth handled by Claude Code CLI (no options needed)
		// All other cases: need API key
		if agentName == "claude_code" && state.Provider.Name == "anthropic" {
			items = append(items, tui.MenuItem{
				Label:       "Auth",
				Description: "handled by Claude Code",
				Value:       "__info__", // Not selectable
			})
		} else {
			// Need API key for all other combinations
			keyStatus := "not set"
			if os.Getenv(state.Provider.EnvVar) != "" {
				keyStatus = utils.MaskKeyShort(os.Getenv(state.Provider.EnvVar))
			}
			items = append(items, tui.MenuItem{
				Label:       "API Key",
				Description: keyStatus,
				Value:       "apikey",
			})
		}

		// Trigger % as editable field
		items = append(items, tui.MenuItem{
			Label:       "Trigger %",
			Description: fmt.Sprintf("%.0f", state.TriggerThreshold),
			Value:       "edit_trigger",
			Editable:    true,
		})

		items = append(items, tui.MenuItem{Label: "← Back", Value: "back"})

		idx, err := tui.SelectMenu("Compact Settings", items)
		if err != nil || items[idx].Value == "back" {
			return
		}

		// Process editable fields
		for _, item := range items {
			if item.Value == "edit_trigger" && item.Editable {
				desc := strings.TrimSpace(item.Description)
				if desc == "" {
					fmt.Printf("%s⚠%s Trigger value cannot be empty. Please enter a number between 1 and 99.\n", tui.ColorYellow, tui.ColorReset)
					continue
				}
				if item.Description != fmt.Sprintf("%.0f", state.TriggerThreshold) {
					if v, err := strconv.ParseFloat(desc, 64); err == nil {
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

		switch items[idx].Value {
		case "provider":
			selectProvider(state, agentName)

		case "model":
			selectModel(state)

		case "auth":
			selectAuth(state)

		case "apikey":
			promptAndSetAPIKey(state)

		case "edit_trigger":
			// Already handled above via editable field
			continue
		}
	}
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
				tui.MenuItem{Label: "Provider", Description: state.ToolOutputProvider.DisplayName, Value: "provider"},
				tui.MenuItem{Label: "Model", Description: state.ToolOutputModel, Value: "model"},
			)

			// API key status for compression provider
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
		case "provider":
			selectToolOutputProvider(state)
		case "model":
			selectToolOutputModel(state)
		case "apikey":
			promptAndSetToolOutputAPIKey(state)
		}
	}
}

// selectToolOutputStrategy shows strategy selection for tool output compression
func selectToolOutputStrategy(state *ConfigState) {
	items := []tui.MenuItem{
		{Label: "external_provider", Description: "Use LLM provider to compress tool outputs", Value: "external_provider"},
		{Label: "← Back", Value: "back"},
	}

	idx, err := tui.SelectMenu("Compression Strategy", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	state.ToolOutputStrategy = items[idx].Value
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

// selectToolOutputModel shows model selection for tool output compression
func selectToolOutputModel(state *ConfigState) {
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
	enteredKey := tui.PromptPassword(fmt.Sprintf("Enter %s: ", state.ToolOutputProvider.EnvVar))
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

// selectProvider shows provider selection menu
func selectProvider(state *ConfigState, agentName string) {
	items := make([]tui.MenuItem, len(tui.SupportedProviders)+1)
	for i, p := range tui.SupportedProviders {
		items[i] = tui.MenuItem{
			Label:       p.DisplayName,
			Description: p.EnvVar,
			Value:       p.Name,
		}
	}
	items[len(tui.SupportedProviders)] = tui.MenuItem{Label: "← Back", Value: "back"}

	idx, err := tui.SelectMenu("Select Provider", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	state.Provider = tui.SupportedProviders[idx]
	state.Model = state.Provider.DefaultModel
	// Reset API key when provider changes
	state.APIKey = "${" + state.Provider.EnvVar + "}"
	// Only Claude Code agent + Anthropic can use subscription auth
	if agentName == "claude_code" && state.Provider.Name == "anthropic" {
		state.UseSubscription = true
	} else {
		state.UseSubscription = false // All other combinations need API key
	}
}

// selectModel shows model selection menu
func selectModel(state *ConfigState) {
	items := make([]tui.MenuItem, len(state.Provider.Models)+1)
	for i, m := range state.Provider.Models {
		desc := ""
		if m == state.Provider.DefaultModel {
			desc = "recommended"
		}
		items[i] = tui.MenuItem{Label: m, Description: desc, Value: m}
	}
	items[len(state.Provider.Models)] = tui.MenuItem{Label: "← Back", Value: "back"}

	idx, err := tui.SelectMenu("Select Model", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	state.Model = items[idx].Value
}

// selectAuth shows auth method selection
func selectAuth(state *ConfigState) {
	items := []tui.MenuItem{
		{Label: "Subscription", Description: "claude code --login", Value: "subscription"},
		{Label: "API Key", Description: "your own key", Value: "api_key"},
		{Label: "← Back", Value: "back"},
	}

	idx, err := tui.SelectMenu("Authentication", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	state.UseSubscription = (items[idx].Value == "subscription")
	if state.UseSubscription {
		state.APIKey = "${ANTHROPIC_API_KEY:-}"
	}
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
	enteredKey := tui.PromptPassword(fmt.Sprintf("Enter %s: ", state.Provider.EnvVar))
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
		data, err = os.ReadFile(path)
	}

	// Then try local configs dir
	if err != nil {
		path := filepath.Join("configs", configName+".yaml")
		data, err = os.ReadFile(path)
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
	}
	// Set tool_output defaults if not found
	if state.ToolOutputStrategy == "" {
		state.ToolOutputStrategy = "external_provider"
	}
	if state.ToolOutputProvider.Name == "" && len(tui.SupportedProviders) > 1 {
		state.ToolOutputProvider = tui.SupportedProviders[1] // gemini
	}
	if state.ToolOutputModel == "" && state.ToolOutputProvider.Name != "" {
		state.ToolOutputModel = state.ToolOutputProvider.DefaultModel
	}
	if state.ToolOutputAPIKey == "" && state.ToolOutputProvider.Name != "" {
		state.ToolOutputAPIKey = "${" + state.ToolOutputProvider.EnvVar + ":-}"
	}

	return state
}
