package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/compresr/context-gateway/internal/compresr"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/pipes"
	"github.com/compresr/context-gateway/internal/tui"
	"github.com/compresr/context-gateway/internal/utils"
)

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

	fmt.Printf("\n  Get your API key at: %s%s%s\n\n", tui.ColorCyan, config.DefaultCompresrDashboardURL, tui.ColorReset)
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
			if state.ToolDiscoveryStrategy == pipes.StrategyCompresr {
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
			if state.ToolDiscoveryStrategy == pipes.StrategyToolSearch {
				searchFallbackDesc := "● Enabled (required)"
				items = append(items, tui.MenuItem{
					Label:       "Search Fallback",
					Description: searchFallbackDesc,
					Value:       "__info__",
				})
			}

			// relevance strategy: show filtering params
			if state.ToolDiscoveryStrategy == pipes.StrategyRelevance {
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
		{Label: "compresr", Description: "Compresr API selects relevant tools", Value: pipes.StrategyCompresr},
		{Label: "tool-search", Description: "LLM searches via regex pattern", Value: pipes.StrategyToolSearch},
		{Label: "relevance", Description: "local keyword scoring", Value: pipes.StrategyRelevance},
		{Label: "passthrough", Description: "no filtering", Value: pipes.StrategyPassthrough},
		{Label: "← Back", Value: "back"},
	}

	idx, err := tui.SelectMenu("Tool Discovery Strategy", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	selectedStrategy := items[idx].Value

	// If compresr strategy selected, fetch pricing (skip API key prompt if key exists in env)
	if selectedStrategy == pipes.StrategyCompresr {
		envKey := os.Getenv(tui.CompresrModels.EnvVar)
		if envKey != "" {
			// Key exists in env — use it directly, skip prompt
			pricing := fetchModelPricing(envKey, "tool-discovery")
			if pricing != nil {
				state.ToolDiscoveryPricing = pricing
				for _, m := range pricing.Models {
					if !m.Locked {
						state.ToolDiscoveryModel = m.Name
						break
					}
				}
			}
			// Set strategy even if pricing fetch fails — user can configure model later
		} else {
			// No env key — prompt as before
			pricing := promptCompresrAPIKeyAndFetchPricing(state, "tool-discovery")
			if pricing == nil {
				return
			}
			state.ToolDiscoveryPricing = pricing
			for _, m := range pricing.Models {
				if !m.Locked {
					state.ToolDiscoveryModel = m.Name
					break
				}
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
			desc := m.DisplayName
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
// modelGroup should be "tool-discovery", "tool-output", or "history".
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
	case "history":
		state.CompactPricing = pricing
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
			)

			// Show different options based on strategy
			if state.ToolOutputStrategy == pipes.StrategyCompresr {
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
			if state.ToolOutputStrategy == pipes.StrategyCompresr {
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
		{Label: "compresr", Description: "Compresr API compresses tool outputs", Value: pipes.StrategyCompresr},
		{Label: "external_provider", Description: "Use LLM provider to compress", Value: pipes.StrategyExternalProvider},
		{Label: "← Back", Value: "back"},
	}

	idx, err := tui.SelectMenu("Compression Strategy", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	selectedStrategy := items[idx].Value

	// If compresr strategy selected, fetch pricing (skip API key prompt if key exists in env)
	if selectedStrategy == pipes.StrategyCompresr {
		envKey := os.Getenv(tui.CompresrModels.EnvVar)
		if envKey != "" {
			// Key exists in env — use it directly, skip prompt
			pricing := fetchModelPricing(envKey, "tool-output")
			if pricing != nil {
				state.ToolOutputPricing = pricing
				for _, m := range pricing.Models {
					if !m.Locked {
						state.ToolOutputModel = m.Name
						break
					}
				}
			}
			// Set strategy even if pricing fetch fails — user can configure model later
		} else {
			// No env key — prompt as before
			pricing := promptCompresrAPIKeyAndFetchPricing(state, "tool-output")
			if pricing == nil {
				return
			}
			state.ToolOutputPricing = pricing
			for _, m := range pricing.Models {
				if !m.Locked {
					state.ToolOutputModel = m.Name
					break
				}
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
			desc := m.DisplayName
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
