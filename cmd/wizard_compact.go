package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/compresr/context-gateway/internal/preemptive"
	"github.com/compresr/context-gateway/internal/tui"
	"github.com/compresr/context-gateway/internal/utils"
)

// editCompact opens the compact (preemptive summarization) settings submenu
func editCompact(state *ConfigState, agentName string) {
	for {
		items := []tui.MenuItem{
			{Label: "Strategy", Description: state.CompactStrategy, Value: "strategy"},
		}

		if state.CompactStrategy == preemptive.StrategyCompresr {
			// Compresr API strategy: show HCC model and API key
			items = append(items,
				tui.MenuItem{Label: "Model", Description: state.CompactCompresrModel, Value: "compresr_model"},
			)
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
			// LLM provider strategy: show model and auth
			items = append(items,
				tui.MenuItem{Label: "Model", Description: state.Model, Value: "model"},
			)

			// Build auth description
			authDesc := ""
			if agentName == "claude_code" && state.Provider.Name == "anthropic" {
				if state.UseSubscription {
					authDesc = "subscription"
				} else {
					authDesc = "api-key"
				}
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
			items = append(items,
				tui.MenuItem{Label: "Auth", Description: authDesc, Value: "auth"},
			)
		}

		items = append(items, tui.MenuItem{
			Label:       "Trigger %",
			Description: fmt.Sprintf("%.0f", state.TriggerThreshold),
			Value:       "edit_trigger",
			Editable:    true,
		})
		items = append(items, tui.MenuItem{Label: "← Back", Value: "back"})

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
		case "strategy":
			selectCompactStrategy(state)

		case "model":
			selectCompactModel(state)

		case "compresr_model":
			selectCompactCompresrModel(state)

		case "compresr_apikey":
			promptAndSetCompresrAPIKey(state, "history")

		case "auth":
			selectCompactAuth(state, agentName)

		case "edit_trigger":
			continue
		}
	}
}

// selectCompactStrategy shows strategy selection for compact (preemptive summarization)
func selectCompactStrategy(state *ConfigState) {
	items := []tui.MenuItem{
		{Label: "compresr", Description: "Compresr API (HCC models)", Value: preemptive.StrategyCompresr},
		{Label: "external_provider", Description: "LLM provider (Claude, Gemini, GPT)", Value: preemptive.StrategyExternalProvider},
		{Label: "← Back", Value: "back"},
	}

	idx, err := tui.SelectMenu("Compact Strategy", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	selected := items[idx].Value

	// If compresr selected, fetch pricing (skip API key prompt if key exists in env)
	if selected == preemptive.StrategyCompresr {
		envKey := os.Getenv(tui.CompresrModels.EnvVar)
		if envKey != "" {
			// Key exists in env — use it directly, skip prompt
			pricing := fetchModelPricing(envKey, "history")
			if pricing != nil {
				state.CompactPricing = pricing
				for _, m := range pricing.Models {
					if !m.Locked {
						state.CompactCompresrModel = m.Name
						break
					}
				}
			}
			// Set strategy even if pricing fetch fails — user can configure model later
		} else {
			// No env key — prompt as before
			pricing := promptCompresrAPIKeyAndFetchPricing(state, "history")
			if pricing == nil {
				return
			}
			state.CompactPricing = pricing
			for _, m := range pricing.Models {
				if !m.Locked {
					state.CompactCompresrModel = m.Name
					break
				}
			}
		}
	}

	state.CompactStrategy = selected
}

// selectCompactCompresrModel shows model selection for compact compresr strategy
func selectCompactCompresrModel(state *ConfigState) {
	var items []tui.MenuItem

	// If we have pricing data from the API, use it
	if state.CompactPricing != nil && len(state.CompactPricing.Models) > 0 {
		items = make([]tui.MenuItem, len(state.CompactPricing.Models)+1)
		for i, m := range state.CompactPricing.Models {
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
		models := tui.CompresrModels.History.Models
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

	idx, err := tui.SelectMenu("Select HCC Model", items)
	if err != nil || items[idx].Value == "back" {
		return
	}

	state.CompactCompresrModel = items[idx].Value
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
