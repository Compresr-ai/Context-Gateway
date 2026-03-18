// Summarization service for preemptive summarization.
package preemptive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/compresr/context-gateway/external"
	authtypes "github.com/compresr/context-gateway/internal/auth/types"
	"github.com/compresr/context-gateway/internal/compresr"
	"github.com/compresr/context-gateway/internal/tokenizer"
)

// Summarizer generates conversation summaries.
type Summarizer struct {
	config       SummarizerConfig
	capturedAuth authtypes.CapturedAuth // Auth + endpoint captured from incoming requests
	authMutex    sync.RWMutex

	// pre-created client for StrategyCompresr to avoid per-call allocation and
	// connection pool churn. Nil for other strategies.
	compresrClient *compresr.Client

	// bedrockClient is the cached HTTP client with SigV4 signing for Bedrock.
	// Initialized once in NewSummarizer to avoid per-call transport creation.
	bedrockClient *http.Client
}

// NewSummarizer creates a new summarizer.
func NewSummarizer(cfg SummarizerConfig) *Summarizer {
	s := &Summarizer{config: cfg}
	// Pre-create the Compresr client once so all summarizeViaAPI calls share the same
	// connection pool (Go's http.Transport is designed to be reused across requests).
	if cfg.Strategy == StrategyCompresr && cfg.Compresr != nil {
		s.compresrClient = compresr.NewClient(cfg.CompresrBaseURL, cfg.Compresr.APIKey, compresr.WithTimeout(cfg.Compresr.Timeout))
	}
	if cfg.Provider == "bedrock" {
		if client, err := s.buildBedrockHTTPClient(); err == nil {
			s.bedrockClient = client
		}
		// If construction fails here, callAPI will retry and surface the error.
	}
	return s
}

// SetAuth stores auth captured from an incoming request.
// Used when no API key is configured (e.g., Max/Pro subscription users).
func (s *Summarizer) SetAuth(auth authtypes.CapturedAuth) {
	if !auth.HasAuth() {
		return
	}
	s.authMutex.Lock()
	defer s.authMutex.Unlock()
	s.capturedAuth = auth
}

// getAuthValue returns the best available CapturedAuth.
func (s *Summarizer) getAuthValue() authtypes.CapturedAuth {
	// Priority 1: Configured API key (always use x-api-key)
	if s.config.ProviderKey != "" {
		return authtypes.CapturedAuth{Token: s.config.ProviderKey, IsXAPIKey: true}
	}
	// Priority 2: Captured from request - mirror original header format
	s.authMutex.RLock()
	defer s.authMutex.RUnlock()
	return s.capturedAuth
}

// getEndpoint returns the endpoint URL to use for API calls.
func (s *Summarizer) getEndpoint() string {
	// When using captured auth (no configured API key), prefer captured endpoint
	// because OAuth tokens are only valid for the endpoint they were issued for
	if s.config.ProviderKey == "" {
		s.authMutex.RLock()
		captured := s.capturedAuth.Endpoint
		s.authMutex.RUnlock()
		if captured != "" {
			return captured
		}
	}
	// Configured endpoint (for API key users)
	if s.config.Endpoint != "" {
		return s.config.Endpoint
	}
	// Captured from request (fallback for API key users too)
	s.authMutex.RLock()
	defer s.authMutex.RUnlock()
	if s.capturedAuth.Endpoint != "" {
		return s.capturedAuth.Endpoint
	}
	// Fallback
	return "https://api.anthropic.com/v1/messages"
}

// SummarizeInput contains input for summarization.
type SummarizeInput struct {
	Messages         []json.RawMessage
	TriggerThreshold float64 // e.g., 80% → keep 20% of context as recent
	KeepRecentTokens int     // Fixed token count (override)
	KeepRecentCount  int     // Message-based (legacy fallback)
	Model            string  // Used to look up context window
	ContextWindow    int     // Override context window (for testing)

	// Per-job auth credentials for session isolation
	// When set, these override global captured auth to prevent cross-session leakage
	Auth authtypes.CapturedAuth
}

// SummarizeOutput contains the result.
type SummarizeOutput struct {
	Summary             string
	SummaryTokens       int
	LastSummarizedIndex int
	Duration            time.Duration
	InputTokens         int
	OutputTokens        int
}

// Summarize generates a summary based on the configured strategy.
func (s *Summarizer) Summarize(ctx context.Context, input SummarizeInput) (*SummarizeOutput, error) {
	switch s.config.Strategy {
	case StrategyCompresr:
		return s.summarizeViaAPI(ctx, input)
	default:
		return s.summarizeViaLLM(ctx, input)
	}
}

// summarizeViaLLM uses LLM provider for summarization (original behavior).
func (s *Summarizer) summarizeViaLLM(ctx context.Context, input SummarizeInput) (*SummarizeOutput, error) {
	startTime := time.Now()
	total := len(input.Messages)
	if total == 0 {
		return nil, fmt.Errorf("no messages to summarize")
	}

	// Determine cutoff point using token-based or message-based approach
	lastIndex, err := s.findSummarizationCutoff(input)
	if err != nil {
		return nil, err
	}

	toSummarize := input.Messages[:lastIndex+1]

	// Build request
	prompt := s.config.SystemPrompt
	if prompt == "" {
		prompt = DefaultClaudeSystemPrompt
	}

	formatted := FormatMessages(toSummarize)
	result, err := s.callAPI(ctx, prompt, fmt.Sprintf("Please summarize the following conversation:\n\n%s", formatted), input)
	if err != nil {
		return nil, fmt.Errorf("API call failed: %w", err)
	}

	summary := result.Content
	if summary == "" {
		return nil, fmt.Errorf("empty summary returned")
	}

	tokens := tokenizer.CountTokens(summary)
	if result.OutputTokens > 0 {
		tokens = result.OutputTokens
	}

	return &SummarizeOutput{
		Summary:             summary,
		SummaryTokens:       tokens,
		LastSummarizedIndex: lastIndex,
		Duration:            time.Since(startTime),
		InputTokens:         result.InputTokens,
		OutputTokens:        result.OutputTokens,
	}, nil
}

// summarizeViaAPI uses Compresr API for history compression.
func (s *Summarizer) summarizeViaAPI(ctx context.Context, input SummarizeInput) (*SummarizeOutput, error) {
	startTime := time.Now()
	total := len(input.Messages)
	if total == 0 {
		return nil, fmt.Errorf("no messages to summarize")
	}

	if s.config.Compresr == nil {
		return nil, fmt.Errorf("compresr config is nil (required for strategy: compresr)")
	}

	// Determine keep_recent count
	keepRecent := input.KeepRecentCount
	if keepRecent <= 0 {
		keepRecent = s.config.KeepRecentCount
	}
	if keepRecent <= 0 {
		keepRecent = 3 // default
	}

	// Convert messages to Compresr format
	historyMessages := make([]compresr.HistoryMessage, 0, total)
	for _, msg := range input.Messages {
		// Use any for Content to handle both string and array (Anthropic content blocks)
		var parsedMsg struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		}
		if err := json.Unmarshal(msg, &parsedMsg); err != nil {
			return nil, fmt.Errorf("failed to parse message: %w", err)
		}
		// Extract text content from string or content blocks array
		contentStr := ExtractContentString(parsedMsg.Content)
		historyMessages = append(historyMessages, compresr.HistoryMessage{
			Role:    parsedMsg.Role,
			Content: contentStr,
		})
	}

	// Reuse the pre-created client (created in NewSummarizer) to share the connection pool.
	// Fall back to creating a new one only if the pre-created client is unexpectedly nil.
	client := s.compresrClient
	if client == nil {
		client = compresr.NewClient(s.config.CompresrBaseURL, s.config.Compresr.APIKey, compresr.WithTimeout(s.config.Compresr.Timeout))
	}
	response, err := client.CompressHistory(compresr.CompressHistoryParams{
		Messages:   historyMessages,
		KeepRecent: keepRecent,
		ModelName:  s.config.Compresr.Model,
		Source:     "gateway",
	})
	if err != nil {
		return nil, fmt.Errorf("compresr API call failed: %w", err)
	}

	// Calculate last summarized index (all messages except the kept ones)
	lastIndex := total - response.MessagesKept - 1
	if lastIndex < 0 {
		lastIndex = 0
	}

	return &SummarizeOutput{
		Summary:             response.Summary,
		SummaryTokens:       response.CompressedTokens,
		LastSummarizedIndex: lastIndex,
		Duration:            time.Since(startTime),
		InputTokens:         response.OriginalTokens,
		OutputTokens:        response.CompressedTokens,
	}, nil
}

func (s *Summarizer) findSummarizationCutoff(input SummarizeInput) (int, error) {
	total := len(input.Messages)

	// Priority 1: Fixed token override (explicit config takes precedence)
	keepTokens := input.KeepRecentTokens
	if keepTokens <= 0 {
		keepTokens = s.config.KeepRecentTokens
	}
	if keepTokens > 0 {
		return s.findCutoffByTokens(input.Messages, keepTokens)
	}

	// Priority 2: Derive from trigger_threshold
	// If trigger is 80%, we keep 20% of context as recent messages
	triggerThreshold := input.TriggerThreshold
	if triggerThreshold <= 0 {
		triggerThreshold = 80.0 // default
	}

	if triggerThreshold > 0 && triggerThreshold < 100 {
		// Get context window size
		contextWindow := input.ContextWindow
		if contextWindow <= 0 && input.Model != "" {
			modelCtx := GetModelContextWindow(input.Model)
			contextWindow = modelCtx.EffectiveMax
		}
		if contextWindow <= 0 {
			contextWindow = 100000 // fallback: 100K
		}

		// keep_percent = 100 - trigger_threshold
		// If trigger at 80%, keep 20% of context window
		keepPercent := 100.0 - triggerThreshold
		keepTokensCalc := int(float64(contextWindow) * keepPercent / 100.0)
		return s.findCutoffByTokens(input.Messages, keepTokensCalc)
	}

	// Priority 3: Message-based (legacy fallback)
	keepCount := input.KeepRecentCount
	if keepCount <= 0 {
		keepCount = s.config.KeepRecentCount
	}
	if keepCount <= 0 {
		keepCount = 2 // absolute fallback
	}

	if total <= keepCount {
		return -1, fmt.Errorf("not enough messages: have %d, keeping %d", total, keepCount)
	}

	return total - keepCount - 1, nil
}

// findCutoffByTokens walks backwards through messages, accumulating tokens.
// Returns the last index to summarize (everything after is kept).
func (s *Summarizer) findCutoffByTokens(messages []json.RawMessage, keepTokens int) (int, error) {
	total := len(messages)
	if total == 0 {
		return -1, fmt.Errorf("no messages")
	}

	// Count tokens per message using tiktoken
	// Walk backwards, accumulating tokens
	accumulatedTokens := 0
	cutoffIndex := -1

	for i := total - 1; i >= 0; i-- {
		msgTokens := tokenizer.CountBytes(messages[i])
		accumulatedTokens += msgTokens

		// Once we've accumulated enough "recent" tokens, everything before is summarizable
		if accumulatedTokens >= keepTokens && i > 0 {
			cutoffIndex = i - 1
			break
		}
	}

	// If we went through all messages without hitting threshold,
	// check if we have at least 2 messages (need something to summarize + something to keep)
	if cutoffIndex < 0 {
		if total >= 2 {
			// Summarize all but the last message
			cutoffIndex = total - 2
		} else {
			return -1, fmt.Errorf("not enough content to summarize: %d tokens in %d messages", accumulatedTokens, total)
		}
	}

	return cutoffIndex, nil
}

func (s *Summarizer) callAPI(ctx context.Context, systemPrompt, userContent string, input SummarizeInput) (*external.CallLLMResult, error) {
	log.Debug().Str("model", s.config.Model).Str("provider", s.config.Provider).Int("max_tokens", s.config.MaxTokens).Msg("Calling summarization API")

	// Prefer per-job auth endpoint over global captured endpoint for session isolation
	endpoint := input.Auth.Endpoint
	if endpoint == "" {
		endpoint = s.getEndpoint()
	}

	// Determine auth: configured API key > per-job > global captured.
	// Configured API key takes precedence because it's provider-specific (e.g., Gemini key
	// for Gemini summarizer). Per-job auth from request headers may be for a different
	// provider (e.g., Anthropic key) and must not override the configured key.
	var auth authtypes.CapturedAuth
	keySource := ""
	if s.config.ProviderKey != "" {
		auth = authtypes.CapturedAuth{Token: s.config.ProviderKey, IsXAPIKey: true}
		keySource = "config.ProviderKey"
	} else if input.Auth.HasAuth() {
		auth = input.Auth
		keySource = "input.Auth"
	} else {
		auth = s.getAuthValue()
		keySource = "captured auth"
	}

	// Route the token to the correct field:
	//   ProviderKey → x-api-key header (API users)
	//   BearerAuth  → Authorization: Bearer header (subscription users)
	var providerKey, bearerAuth string
	if auth.IsXAPIKey {
		providerKey = auth.Token
	} else {
		bearerAuth = auth.Token
	}

	log.Debug().
		Str("provider", s.config.Provider).
		Str("key_source", keySource).
		Int("provider_key_len", len(providerKey)).
		Int("bearer_auth_len", len(bearerAuth)).
		Str("endpoint", endpoint).
		Msg("Summarizer API key resolved")

	params := external.CallLLMParams{
		Provider:     s.config.Provider,
		Endpoint:     endpoint,
		ProviderKey:  providerKey,
		BearerAuth:   bearerAuth,
		Model:        s.config.Model,
		SystemPrompt: systemPrompt,
		UserPrompt:   userContent,
		MaxTokens:    s.config.MaxTokens,
		Timeout:      s.config.Timeout,
	}

	// OAuth tokens (Claude Code Max/Pro) require the anthropic-beta header to work
	if bearerAuth != "" && auth.BetaHeader != "" {
		params.ExtraHeaders = map[string]string{"anthropic-beta": auth.BetaHeader}
	}

	// For Bedrock, use the cached signing HTTP client.
	if s.config.Provider == "bedrock" {
		if s.bedrockClient != nil {
			params.HTTPClient = s.bedrockClient
		} else {
			client, err := s.buildBedrockHTTPClient()
			if err != nil {
				return nil, fmt.Errorf("failed to create Bedrock HTTP client: %w", err)
			}
			params.HTTPClient = client
		}
	}

	return external.CallLLM(ctx, params)
}

// buildBedrockHTTPClient constructs an HTTP client with SigV4 signing for Bedrock.
// Called once at construction time; the result is cached in s.bedrockClient.
func (s *Summarizer) buildBedrockHTTPClient() (*http.Client, error) {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		region = "us-east-1"
	}

	transport, err := external.NewBedrockSigningTransport(region, nil)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: transport}, nil
}
