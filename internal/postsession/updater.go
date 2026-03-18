// Package postsession provides post-session CLAUDE.md updates.
//
// After a coding session ends, the updater analyzes session events
// and updates the project's CLAUDE.md with any structural changes.
//
// Flow:
//  1. SessionCollector gathers events during proxy operation
//  2. On session end, Updater calls an LLM to analyze the session
//  3. LLM compares session events with current CLAUDE.md
//  4. If structural changes detected, CLAUDE.md is updated
package postsession

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/external"
	authtypes "github.com/compresr/context-gateway/internal/auth/types"
)

// Config holds post-session updater configuration.
type Config struct {
	Enabled     bool          `yaml:"enabled"`
	ClaudeMDDir string        `yaml:"claude_md_dir"` // Directory containing CLAUDE.md (empty = cwd)
	Model       string        `yaml:"model"`
	Provider    string        `yaml:"provider"` // "anthropic", "openai", etc.
	Endpoint    string        `yaml:"endpoint"` // API endpoint
	APIKey      string        `yaml:"api_key"`  //nolint:gosec // G117: config field name, not a credential
	MaxTokens   int           `yaml:"max_tokens"`
	Timeout     time.Duration `yaml:"timeout"`
}

// DefaultConfig returns sensible defaults for post-session updates.
func DefaultConfig() Config {
	return Config{
		Enabled:   false,
		Model:     "claude-haiku-4-5",
		MaxTokens: 8192,
		Timeout:   60 * time.Second,
	}
}

// Updater performs post-session CLAUDE.md updates.
type Updater struct {
	config Config
}

// NewUpdater creates a new post-session updater.
func NewUpdater(cfg Config) *Updater {
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 8192
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}
	return &Updater{config: cfg}
}

// UpdateResult contains the result of a CLAUDE.md update attempt.
type UpdateResult struct {
	Updated     bool   // Whether CLAUDE.md was changed
	Path        string // Path to CLAUDE.md
	Description string // Human-readable description of what changed
}

// Update analyzes the session and updates CLAUDE.md if needed.
// Uses the provided collector's session log and auth credentials.
func (u *Updater) Update(ctx context.Context, collector *SessionCollector, auth authtypes.CapturedAuth) (*UpdateResult, error) {
	if !u.config.Enabled {
		return &UpdateResult{Updated: false, Description: "post-session updates disabled"}, nil
	}

	if collector == nil || !collector.HasEvents() {
		return &UpdateResult{Updated: false, Description: "no session events recorded"}, nil
	}

	sessionLog := collector.BuildSessionLog()
	if sessionLog == "" {
		return &UpdateResult{Updated: false, Description: "empty session log"}, nil
	}

	// Find CLAUDE.md — clean and validate to prevent path traversal
	claudeMDPath := filepath.Clean(u.findClaudeMD())
	if !strings.HasSuffix(claudeMDPath, "CLAUDE.md") {
		return nil, fmt.Errorf("invalid CLAUDE.md path: %s", claudeMDPath)
	}
	currentContent := ""
	if data, err := os.ReadFile(claudeMDPath); err == nil { //nolint:gosec // G304: path validated above
		currentContent = string(data)
	}

	// Call LLM to generate updated CLAUDE.md
	userPrompt := BuildUserPrompt(sessionLog, currentContent)

	// Resolve auth: config API key > captured auth
	endpoint := u.config.Endpoint
	provider := u.config.Provider

	if endpoint == "" {
		endpoint = auth.Endpoint
	}
	if endpoint == "" {
		endpoint = "https://api.anthropic.com/v1/messages"
	}
	if provider == "" {
		provider = external.DetectProvider(endpoint)
	}

	// Determine auth header style: config key takes precedence over captured auth
	// Uses centralized CapturedAuth pattern: IsXAPIKey → ProviderKey, else → BearerAuth
	var providerKey, bearerAuth string
	var extraHeaders map[string]string
	if u.config.APIKey != "" {
		providerKey = u.config.APIKey
	} else if auth.IsXAPIKey {
		providerKey = auth.Token
	} else {
		bearerAuth = auth.Token
		// OAuth tokens (Claude Code Max/Pro) require anthropic-beta header
		if auth.BetaHeader != "" {
			extraHeaders = map[string]string{"anthropic-beta": auth.BetaHeader}
		}
	}

	log.Info().
		Str("model", u.config.Model).
		Str("provider", provider).
		Str("claude_md", claudeMDPath).
		Int("session_log_len", len(sessionLog)).
		Msg("post-session: calling LLM for CLAUDE.md analysis")

	result, err := external.CallLLM(ctx, external.CallLLMParams{
		Provider:     provider,
		Endpoint:     endpoint,
		ProviderKey:  providerKey,
		BearerAuth:   bearerAuth,
		ExtraHeaders: extraHeaders,
		Model:        u.config.Model,
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
		MaxTokens:    u.config.MaxTokens,
		Timeout:      u.config.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("post-session LLM call failed: %w", err)
	}

	content := strings.TrimSpace(result.Content)

	// Check if LLM determined no changes needed
	if content == "NO_CHANGES_NEEDED" || content == "" {
		log.Info().Msg("post-session: no structural changes detected")
		return &UpdateResult{
			Updated:     false,
			Path:        claudeMDPath,
			Description: "no structural changes detected",
		}, nil
	}

	// Write updated CLAUDE.md
	if err := os.MkdirAll(filepath.Dir(claudeMDPath), 0750); err != nil {
		return nil, fmt.Errorf("failed to create directory for CLAUDE.md: %w", err)
	}
	if err := os.WriteFile(claudeMDPath, []byte(content+"\n"), 0600); err != nil {
		return nil, fmt.Errorf("failed to write CLAUDE.md: %w", err)
	}

	log.Info().
		Str("path", claudeMDPath).
		Int("input_tokens", result.InputTokens).
		Int("output_tokens", result.OutputTokens).
		Msg("post-session: CLAUDE.md updated")

	return &UpdateResult{
		Updated:     true,
		Path:        claudeMDPath,
		Description: "CLAUDE.md updated with session insights",
	}, nil
}

// findClaudeMD resolves the CLAUDE.md file path.
func (u *Updater) findClaudeMD() string {
	if u.config.ClaudeMDDir != "" {
		return filepath.Join(u.config.ClaudeMDDir, "CLAUDE.md")
	}
	// Default: current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return "CLAUDE.md"
	}
	return filepath.Join(cwd, "CLAUDE.md")
}
