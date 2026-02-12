package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// CredentialScope determines where credentials are persisted.
type CredentialScope int

const (
	ScopeSession CredentialScope = iota // Only for current session (env var)
	ScopeProject                        // Write to project .env
	ScopeGlobal                         // Write to ~/.config/context-gateway/.env
)

// =============================================================================
// ANTHROPIC API KEY SETUP
// =============================================================================

// setupAnthropicAPIKey interactively prompts for the Anthropic API key if not set.
// For claude_code agent, the key is optional since we can capture it from requests.
// Returns true if setup was successful or skipped, false if user cancelled.
func setupAnthropicAPIKey(agentName string) bool {
	// Check if already set
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return true
	}

	fmt.Println()
	printHeader("API Key Setup")
	fmt.Println()

	// For claude_code, API key is optional - we can capture from /login credentials
	if agentName == "claude_code" {
		fmt.Println("  You can provide an API key OR use your Claude Max/Pro subscription.")
		fmt.Println()
		fmt.Println("  Option 1: Enter an API key (from console.anthropic.com)")
		fmt.Println("  Option 2: Skip and use /login in Claude Code (Max/Pro/Teams)")
		fmt.Println()

		if !promptYesNo("Do you have an Anthropic API key to enter?", false) {
			printInfo("Skipped - will use credentials from Claude Code /login")
			fmt.Println()
			fmt.Println("  Note: Make sure you're logged into Claude Code with /login")
			fmt.Println("  The gateway will capture your auth token automatically.")
			fmt.Println()
			return true
		}
	} else {
		fmt.Println("  ANTHROPIC_API_KEY is required to use Context Gateway.")
		fmt.Println("  Get your key at: https://console.anthropic.com/settings/keys")
		fmt.Println()
	}

	// Prompt for key
	apiKey := promptOptional("Enter your Anthropic API key (sk-ant-...): ")
	if apiKey == "" {
		if agentName == "claude_code" {
			printInfo("No key entered - will use credentials from Claude Code /login")
			return true
		}
		printError("API key is required for this agent. Cannot continue without it.")
		return false
	}

	// Validate format (basic check)
	if !strings.HasPrefix(apiKey, "sk-ant-") {
		printWarn("Key doesn't start with 'sk-ant-'. Proceeding anyway...")
	}

	// Ask for scope
	scope := promptCredentialScope("Save API key for")
	persistCredential("ANTHROPIC_API_KEY", apiKey, scope)

	// Set for current session regardless
	os.Setenv("ANTHROPIC_API_KEY", apiKey)
	printSuccess("API key configured")

	return true
}

// =============================================================================
// SLACK NOTIFICATION SETUP
// =============================================================================

// SlackConfig holds Slack notification configuration.
type SlackConfig struct {
	Enabled    bool
	WebhookURL string // Webhook URL (preferred, simpler)
	BotToken   string // Bot token (legacy)
	ChannelID  string // Channel ID (only needed for bot token)
	Scope      CredentialScope
}

// setupSlackNotifications interactively prompts for Slack notification setup.
// Only called for claude_code agent. Returns the config (Enabled=false if skipped).
// nolint:unused // Will be used in future Slack wizard integration
func setupSlackNotifications() SlackConfig {
	config := SlackConfig{Enabled: false}

	// Check if already configured (webhook or bot token)
	if os.Getenv("SLACK_WEBHOOK_URL") != "" {
		if isSlackHookInstalled() {
			printInfo("Slack notifications already configured (webhook)")
			config.Enabled = true
			config.WebhookURL = os.Getenv("SLACK_WEBHOOK_URL")
			return config
		}
	} else if os.Getenv("SLACK_BOT_TOKEN") != "" && os.Getenv("SLACK_CHANNEL_ID") != "" {
		if isSlackHookInstalled() {
			printInfo("Slack notifications already configured (bot token)")
			config.Enabled = true
			config.BotToken = os.Getenv("SLACK_BOT_TOKEN")
			config.ChannelID = os.Getenv("SLACK_CHANNEL_ID")
			return config
		}
	}

	fmt.Println()
	printHeader("Slack Notifications (Optional)")
	fmt.Println()
	fmt.Println("  Get notified when Claude needs your input or finishes a task.")
	fmt.Println()

	// Ask if user wants Slack notifications
	if !promptYesNo("Enable Slack notifications?", false) {
		printInfo("Slack notifications skipped")
		return config
	}

	return promptSlackCredentials()
}

// promptSlackCredentials prompts for Slack webhook URL (simple flow).
// Called when user has already opted-in to Slack notifications.
func promptSlackCredentials() SlackConfig {
	config := SlackConfig{Enabled: false}

	fmt.Println()
	fmt.Println("  Opening Slack to create your webhook...")
	fmt.Println()
	fmt.Println("  Steps:")
	fmt.Println("    1. Click 'Create New App' → 'From scratch'")
	fmt.Println("    2. Name it 'Claude Notify', select your workspace")
	fmt.Println("    3. Click 'Incoming Webhooks' → Turn it ON")
	fmt.Println("    4. Click 'Add New Webhook to Workspace'")
	fmt.Println("    5. Select the channel (or your DM)")
	fmt.Println("    6. Copy the Webhook URL")
	fmt.Println()

	// Open browser to Slack app creation
	openBrowser("https://api.slack.com/apps?new_app=1")

	// Wait for user to paste webhook URL
	fmt.Println("  Press Enter when ready to paste the webhook URL...")
	_, _ = bufio.NewReader(os.Stdin).ReadBytes('\n')

	webhookURL := promptOptional("Paste your Webhook URL (https://hooks.slack.com/...): ")
	if webhookURL == "" {
		printInfo("Slack notifications skipped (no URL provided)")
		return config
	}

	// Validate format
	if !strings.HasPrefix(webhookURL, "https://hooks.slack.com/") {
		printWarn("URL doesn't look like a Slack webhook. Proceeding anyway...")
	}

	// Ask for scope
	scope := promptCredentialScope("Save Slack webhook for")

	// Persist credential
	persistCredential("SLACK_WEBHOOK_URL", webhookURL, scope)

	// Set for current session
	os.Setenv("SLACK_WEBHOOK_URL", webhookURL)

	config.Enabled = true
	config.WebhookURL = webhookURL
	config.Scope = scope

	printSuccess("Slack webhook configured!")
	return config
}

// openBrowser opens a URL in the default browser.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		fmt.Printf("  Please open: %s\n", url)
		return
	}
	_ = cmd.Start()
}

// installClaudeCodeHooks installs Slack notification hooks for Claude Code.
// Returns nil on success, error on failure.
func installClaudeCodeHooks() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	hooksDir := filepath.Join(homeDir, ".claude", "hooks")
	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")
	hookScript := filepath.Join(hooksDir, "slack-notify.sh")

	// 1. Create hooks directory
	err = os.MkdirAll(hooksDir, 0750)
	if err != nil {
		return fmt.Errorf("failed to create hooks directory: %w", err)
	}

	// 2. Write embedded script
	scriptData, err := getEmbeddedHook("slack-notify")
	if err != nil {
		return fmt.Errorf("failed to read embedded hook script: %w", err)
	}

	// #nosec G306 -- hook script needs to be executable
	if err := os.WriteFile(hookScript, scriptData, 0700); err != nil {
		return fmt.Errorf("failed to write hook script: %w", err)
	}

	// 3. Update settings.json
	if err := updateClaudeSettings(settingsPath, hookScript); err != nil {
		return fmt.Errorf("failed to update settings.json: %w", err)
	}

	return nil
}

// updateClaudeSettings updates ~/.claude/settings.json with hook entries.
func updateClaudeSettings(settingsPath, hookScript string) error {
	hookEntry := map[string]interface{}{
		"matcher": "",
		"hooks": []map[string]string{
			{"type": "command", "command": hookScript},
		},
	}

	var settings map[string]interface{}

	// Read existing settings or create new
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		settings = make(map[string]interface{})
	} else {
		err = json.Unmarshal(data, &settings)
		if err != nil {
			return fmt.Errorf("failed to parse settings.json: %w", err)
		}
	}

	// Ensure hooks object exists
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		hooks = make(map[string]interface{})
		settings["hooks"] = hooks
	}

	// Add Stop hook if not present
	if !hookExists(hooks, "Stop", hookScript) {
		stopHooks, _ := hooks["Stop"].([]interface{})
		hooks["Stop"] = append(stopHooks, hookEntry)
	}

	// Add Notification hook if not present
	if !hookExists(hooks, "Notification", hookScript) {
		notifHooks, _ := hooks["Notification"].([]interface{})
		hooks["Notification"] = append(notifHooks, hookEntry)
	}

	// Write back
	output, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	// Ensure .claude directory exists
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0750); err != nil {
		return err
	}

	return os.WriteFile(settingsPath, output, 0600)
}

// hookExists checks if a hook command is already registered.
func hookExists(hooks map[string]interface{}, eventType, command string) bool {
	entries, ok := hooks[eventType].([]interface{})
	if !ok {
		return false
	}

	for _, entry := range entries {
		entryMap, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		hooksList, ok := entryMap["hooks"].([]interface{})
		if !ok {
			continue
		}
		for _, h := range hooksList {
			hookMap, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			if hookMap["command"] == command {
				return true
			}
		}
	}
	return false
}

// isSlackHookInstalled checks if Slack notification hooks are already installed.
func isSlackHookInstalled() bool {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	hookScript := filepath.Join(homeDir, ".claude", "hooks", "slack-notify.sh")
	_, statErr := os.Stat(hookScript)
	if os.IsNotExist(statErr) {
		return false
	}

	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return false
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return false
	}

	return hookExists(hooks, "Stop", hookScript) && hookExists(hooks, "Notification", hookScript)
}

// =============================================================================
// CREDENTIAL PERSISTENCE
// =============================================================================

// persistCredential saves a credential based on the specified scope.
func persistCredential(key, value string, scope CredentialScope) {
	switch scope {
	case ScopeSession:
		// Do nothing - already set in environment by caller
		return
	case ScopeProject:
		appendToEnvFile(".env", key, value)
	case ScopeGlobal:
		homeDir, err := os.UserHomeDir()
		if err != nil {
			printWarn("Could not determine home directory, credential not persisted")
			return
		}
		globalEnv := filepath.Join(homeDir, ".config", "context-gateway", ".env")
		appendToEnvFile(globalEnv, key, value)
	}
}

// appendToEnvFile appends or updates a key=value pair in an .env file.
func appendToEnvFile(envPath, key, value string) {
	// Ensure directory exists
	dir := filepath.Dir(envPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		printWarn(fmt.Sprintf("Could not create directory %s: %v", dir, err))
		return
	}

	// Read existing content
	var lines []string
	found := false

	file, err := os.Open(envPath)
	if err == nil {
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, key+"=") {
				// Update existing line
				lines = append(lines, fmt.Sprintf("%s=%s", key, value))
				found = true
			} else {
				lines = append(lines, line)
			}
		}
		file.Close()
	}

	// Append if not found
	if !found {
		lines = append(lines, fmt.Sprintf("%s=%s", key, value))
	}

	// Write back
	output := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(envPath, []byte(output), 0600); err != nil {
		printWarn(fmt.Sprintf("Could not write to %s: %v", envPath, err))
	}
}

// =============================================================================
// PROMPT HELPERS
// =============================================================================

// promptOptional prompts for input that can be skipped with Enter.
func promptOptional(prompt string) string {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print(prompt)
	input, _ := reader.ReadString('\n')
	return strings.TrimSpace(input)
}

// promptYesNo prompts for a yes/no response using arrow-key selection.
func promptYesNo(question string, defaultYes bool) bool {
	options := []string{"Yes", "No"}
	idx, err := selectFromList(question, options)
	if err != nil {
		// User cancelled or error - return default
		return defaultYes
	}
	return idx == 0 // Yes is at index 0
}

// promptCredentialScope prompts user to choose where to save credentials.
func promptCredentialScope(prefix string) CredentialScope {
	options := []string{
		"This session only (not saved)",
		"This project (.env in current directory)",
		"Global (~/.config/context-gateway/.env)",
	}

	idx, err := selectFromList(fmt.Sprintf("%s:", prefix), options)
	if err != nil {
		return ScopeSession
	}

	switch idx {
	case 0:
		return ScopeSession
	case 1:
		return ScopeProject
	case 2:
		return ScopeGlobal
	default:
		return ScopeSession
	}
}
