package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/compresr/context-gateway/internal/auth"
	"github.com/compresr/context-gateway/internal/compresr"
	"github.com/compresr/context-gateway/internal/config"
	"github.com/compresr/context-gateway/internal/tui"
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
		printInfo("Using credentials from Claude Code /login (no API key prompt)")
		fmt.Println()
		fmt.Println("  Note: Make sure you're logged into Claude Code with /login")
		fmt.Println("  The gateway will capture your auth token automatically.")
		fmt.Println()
		return true
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

	_ = os.Setenv("ANTHROPIC_API_KEY", apiKey)
	printSuccess("API key configured")

	return true
}

// =============================================================================
// COMPRESR API KEY ONBOARDING
// =============================================================================

const (
	compresrAPIKeyEnvVar = "COMPRESR_API_KEY" // #nosec G101 -- env var name, not a secret

	// Frontend URLs - derived from config.DefaultCompresrFrontendBaseURL
	compresrFrontendBaseURL = config.DefaultCompresrFrontendBaseURL
	compresrTokenURL        = compresrFrontendBaseURL + "/dashboard/tokens" // #nosec G101 -- URL, not credentials

	// Backend base URL - WebSocket auth endpoint lives here
	compresrBackendBaseURL = "http://localhost:8000"
)

// isCompresrAPIKeySet checks if the Compresr API key is configured.
func isCompresrAPIKeySet() bool {
	return os.Getenv(compresrAPIKeyEnvVar) != ""
}

// runCompresrOnboarding runs the initial Compresr API key setup flow.
// This is shown to first-time users before any other configuration.
// Tries OAuth flow first, falls back to manual copy-paste if OAuth fails.
// Returns true if successful, false if user cancelled.
func runCompresrOnboarding() bool {
	fmt.Println()
	printHeader("Welcome to Context Gateway")
	fmt.Println()
	fmt.Println("  Context Gateway optimizes your AI coding experience by:")
	fmt.Println()
	fmt.Printf("    %s•%s Preemptive summarization - keeps context fresh\n", tui.ColorGreen, tui.ColorReset)
	fmt.Printf("    %s•%s Tool output compression - reduces token usage\n", tui.ColorGreen, tui.ColorReset)
	fmt.Printf("    %s•%s Tool discovery optimization - faster responses\n", tui.ColorGreen, tui.ColorReset)
	fmt.Println()
	fmt.Printf("  %sTo get started, you need a Compresr API key.%s\n", tui.ColorBold, tui.ColorReset)
	fmt.Println()

	// Try OAuth flow first
	apiKey := tryOAuthFlow()

	// If OAuth failed or timed out, fall back to manual flow
	if apiKey == "" {
		printInfo("Switching to manual setup...")
		fmt.Println()
		apiKey = manualAPIKeyFlow()
		if apiKey == "" {
			return false
		}
	}

	// Validate the API key
	fmt.Println()
	printStep("Validating API key...")
	client := compresr.NewClient("", apiKey)
	tier, err := client.ValidateAPIKey()
	if err != nil {
		printError(fmt.Sprintf("Invalid API key: %v", err))
		fmt.Println()
		printWarn("Please run with --reset-api-key to try again")
		return false
	}

	// Save to global config
	persistCredential(compresrAPIKeyEnvVar, apiKey, ScopeGlobal)

	// Set for current session
	_ = os.Setenv(compresrAPIKeyEnvVar, apiKey)

	fmt.Println()
	printSuccess(fmt.Sprintf("API key validated! (tier: %s)", tier))
	fmt.Println()

	return true
}

// tryOAuthFlow attempts to authorize via WebSocket auth client.
// Connects outbound to the backend which pushes the token when OAuth completes.
// Returns the API key if successful, empty string if failed or timed out.
func tryOAuthFlow() string {
	fmt.Printf("  %sAttempting secure authorization...%s\n", tui.ColorCyan, tui.ColorReset)
	fmt.Println()

	// Create auth client (WebSocket-based, works from VMs and remote machines)
	client, err := auth.NewAuthClient(compresrBackendBaseURL)
	if err != nil {
		printWarn(fmt.Sprintf("Could not create auth client: %v", err))
		return ""
	}
	defer func() { _ = client.Close() }()

	// Connect to backend and get the authorize URL
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	authorizeURL, err := client.Connect(ctx)
	cancel()
	if err != nil {
		printWarn(fmt.Sprintf("Could not connect to auth server: %v", err))
		return ""
	}

	fmt.Printf("  Opening browser in %s2 seconds%s...\n", tui.ColorCyan, tui.ColorReset)
	time.Sleep(2 * time.Second)

	// Open browser to authorization page
	openBrowser(authorizeURL)

	fmt.Println()
	fmt.Printf("  %sBrowser opened:%s %s\n", tui.ColorDim, tui.ColorReset, authorizeURL)
	fmt.Println()
	fmt.Println("  1. Sign in or create an account")
	fmt.Println("  2. Click 'Authorize' to grant access")
	fmt.Println()
	fmt.Printf("  %sWaiting for authorization...%s (5 minute timeout)\n", tui.ColorCyan, tui.ColorReset)
	fmt.Println()

	// Wait for token via WebSocket (5 minute timeout)
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	token, err := client.WaitForToken(ctx)
	if err != nil {
		printWarn(fmt.Sprintf("Authorization timed out or failed: %v", err))
		return ""
	}

	printSuccess("Authorization successful!")
	return token
}

// manualAPIKeyFlow prompts the user to manually copy-paste an API key.
// Returns the API key if entered, empty string if cancelled.
func manualAPIKeyFlow() string {
	fmt.Printf("  Opening browser in %s2 seconds%s...\n", tui.ColorCyan, tui.ColorReset)
	time.Sleep(2 * time.Second)

	// Open browser to token generation page
	openBrowser(compresrTokenURL)

	fmt.Println()
	fmt.Printf("  %sBrowser opened:%s %s\n", tui.ColorDim, tui.ColorReset, compresrTokenURL)
	fmt.Println()
	fmt.Println("  1. Sign in or create an account")
	fmt.Println("  2. Generate a new API token")
	fmt.Println("  3. Copy the token and paste it below")
	fmt.Println()

	// Prompt for API key
	apiKey := tui.PromptInput("  Enter your Compresr API key: ")
	if apiKey == "" {
		printWarn("No API key entered. You can set it later with --reset-api-key")
		return ""
	}

	return apiKey
}

// resetCompresrAPIKey removes the existing API key and re-runs onboarding.
func resetCompresrAPIKey() bool {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		printError("Could not determine home directory")
		return false
	}

	envPath := filepath.Join(homeDir, ".config", "context-gateway", ".env")

	// Remove the key from global .env
	removeCredentialFromEnvFile(envPath, compresrAPIKeyEnvVar)

	// Clear from environment
	_ = os.Unsetenv(compresrAPIKeyEnvVar)

	printInfo("Compresr API key removed")
	fmt.Println()

	// Re-run onboarding
	return runCompresrOnboarding()
}

// removeCredentialFromEnvFile removes a key from an .env file.
func removeCredentialFromEnvFile(envPath, key string) {
	// #nosec G304 -- env file constructed from known paths
	file, err := os.Open(envPath)
	if err != nil {
		return // File doesn't exist, nothing to remove
	}
	defer func() { _ = file.Close() }()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, key+"=") {
			lines = append(lines, line)
		}
	}

	// Write back without the removed key
	output := strings.Join(lines, "\n")
	if len(lines) > 0 {
		output += "\n"
	}
	_ = os.WriteFile(envPath, []byte(output), 0600)
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

	_ = os.Setenv("SLACK_WEBHOOK_URL", webhookURL)

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

	// #nosec G306 -- hook script must be executable (0700)
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
	data, err := os.ReadFile(settingsPath) // #nosec G304 -- known settings path under ~/.claude
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
	data, err := os.ReadFile(settingsPath) // #nosec G304 -- known settings path
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

	// #nosec G304 -- env file constructed from known paths
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
		_ = file.Close()
	}

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
