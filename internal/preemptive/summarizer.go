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
	"github.com/compresr/context-gateway/internal/compresr"
)

// Summarizer generates conversation summaries.
type Summarizer struct {
	config           SummarizerConfig
	capturedAuth     string // Auth token captured from incoming requests
	authHeaderIsXAPI bool   // true if captured from x-api-key header (use x-api-key), false = use Authorization: Bearer
	capturedEndpoint string // Upstream endpoint captured from incoming requests
	authMutex        sync.RWMutex
}

// NewSummarizer creates a new summarizer.
func NewSummarizer(cfg SummarizerConfig) *Summarizer {
	return &Summarizer{
		config: cfg,
	}
}

// SetAuthToken stores an auth token captured from incoming requests.
// Used when no API key is configured (e.g., Max/Pro subscription users).
// isFromXAPIKeyHeader indicates if token came from x-api-key header (vs Authorization: Bearer).
func (s *Summarizer) SetAuthToken(token string, isFromXAPIKeyHeader bool) {
	if token == "" {
		return
	}
	s.authMutex.Lock()
	defer s.authMutex.Unlock()
	s.capturedAuth = token
	s.authHeaderIsXAPI = isFromXAPIKeyHeader
}

// SetEndpoint stores the upstream endpoint URL captured from incoming requests.
func (s *Summarizer) SetEndpoint(endpoint string) {
	if endpoint == "" {
		return
	}
	s.authMutex.Lock()
	defer s.authMutex.Unlock()
	s.capturedEndpoint = endpoint
}

// getAuthToken returns the best available auth token and whether to use x-api-key header.
func (s *Summarizer) getAuthToken() (string, bool) {
	// Priority 1: Configured API key (always use x-api-key)
	if s.config.APISecret != "" {
		return s.config.APISecret, true
	}
	// Priority 2: Captured from request - mirror original header format
	s.authMutex.RLock()
	defer s.authMutex.RUnlock()
	return s.capturedAuth, s.authHeaderIsXAPI
}

// getEndpoint returns the endpoint URL to use for API calls.
func (s *Summarizer) getEndpoint() string {
	// When using captured auth (no configured API key), prefer captured endpoint
	// because OAuth tokens are only valid for the endpoint they were issued for
	if s.config.APISecret == "" {
		s.authMutex.RLock()
		captured := s.capturedEndpoint
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
	if s.capturedEndpoint != "" {
		return s.capturedEndpoint
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
	AuthToken     string // Auth token for this specific job
	AuthIsXAPIKey bool   // true = use x-api-key header
	AuthEndpoint  string // Endpoint for this specific job
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

// Summarize generates a summary using either LLM or Compresr API based on strategy.
func (s *Summarizer) Summarize(ctx context.Context, input SummarizeInput) (*SummarizeOutput, error) {
	// Route to appropriate implementation based on strategy
	if s.config.Strategy == "api" {
		return s.summarizeViaAPI(ctx, input)
	}
	return s.summarizeViaLLM(ctx, input)
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

	tokens := len(summary) / 4
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

	if s.config.API == nil {
		return nil, fmt.Errorf("API config is nil (required for strategy: api)")
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
		// Use interface{} for Content to handle both string and array (Anthropic content blocks)
		var parsedMsg struct {
			Role    string      `json:"role"`
			Content interface{} `json:"content"`
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

	// Call Compresr API
	client := compresr.NewClient(s.config.API.Endpoint, s.config.API.APIKey)
	response, err := client.CompressHistory(compresr.CompressHistoryParams{
		Messages:   historyMessages,
		KeepRecent: keepRecent,
		ModelName:  s.config.API.Model,
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

	// Estimate tokens per message (bytes / 4 is a rough approximation)
	ratio := s.config.TokenEstimateRatio
	if ratio <= 0 {
		ratio = 4
	}

	// Walk backwards, accumulating tokens
	accumulatedTokens := 0
	cutoffIndex := -1

	for i := total - 1; i >= 0; i-- {
		msgTokens := len(messages[i]) / ratio
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

	// Prefer per-job auth over global captured auth for session isolation
	endpoint := input.AuthEndpoint
	if endpoint == "" {
		endpoint = s.getEndpoint()
	}

	// Determine auth token: configured API key > per-job > global captured
	// Configured API key takes precedence because it's provider-specific (e.g., Gemini key
	// for Gemini summarizer). Per-job auth from request headers may be for a different
	// provider (e.g., Anthropic key) and must not override the configured key.
	apiKey := ""
	keySource := ""
	if s.config.APISecret != "" {
		apiKey = s.config.APISecret
		keySource = "config.APISecret"
	} else if input.AuthToken != "" {
		apiKey = input.AuthToken
		keySource = "input.AuthToken"
	} else {
		apiKey, _ = s.getAuthToken()
		keySource = "captured auth"
	}

	log.Debug().
		Str("provider", s.config.Provider).
		Str("key_source", keySource).
		Int("key_len", len(apiKey)).
		Str("endpoint", endpoint).
		Msg("Summarizer API key resolved")

	params := external.CallLLMParams{
		Provider:     s.config.Provider,
		Endpoint:     endpoint,
		APISecret:    apiKey,
		Model:        s.config.Model,
		SystemPrompt: systemPrompt,
		UserPrompt:   userContent,
		MaxTokens:    s.config.MaxTokens,
		Timeout:      s.config.Timeout,
	}

	// For Bedrock, use a signing HTTP client
	if s.config.Provider == "bedrock" {
		client, err := s.getBedrockHTTPClient()
		if err != nil {
			return nil, fmt.Errorf("failed to create Bedrock HTTP client: %w", err)
		}
		params.HTTPClient = client
	}

	return external.CallLLM(ctx, params)
}

// getBedrockHTTPClient returns an HTTP client with SigV4 signing for Bedrock.
func (s *Summarizer) getBedrockHTTPClient() (*http.Client, error) {
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
