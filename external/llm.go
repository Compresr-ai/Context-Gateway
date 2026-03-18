// LLM API client for compression backends.
//
// CallLLM is the single entry point for calling any supported LLM provider
// (Anthropic, OpenAI, Gemini) for content compression or summarization.
//
// USAGE:
//   - For compression/summarization: use CallLLM() (provider-agnostic)
//   - For custom API calls: use Build*Request() + manual HTTP call
//
// ADDING A NEW PROVIDER:
//  1. Add types to prompts.go (XRequest, XResponse)
//  2. Add Build*Request() and Extract*Response() to prompts.go
//  3. Add case to DetectProvider(), setAuthHeaders(), buildRequestBody(), parseResponse()
//  4. Add unit tests in tests/external/llm_test.go
//  5. Update configs/preemptive_summarization.yaml with example
package external

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/compresr/context-gateway/internal/retry"
	"github.com/rs/zerolog/log"
)

const (
	// DefaultTimeout for LLM API calls.
	DefaultTimeout = 60 * time.Second

	// maxResponseSize prevents OOM on unexpectedly large API responses (10MB).
	maxResponseSize = 10 * 1024 * 1024

	// maxErrorBodyLen limits error body in error messages to avoid log bloat.
	maxErrorBodyLen = 500

	// anthropicVersion is the Anthropic API version header value.
	anthropicVersion = "2023-06-01"
)

// defaultHTTPClient is shared across CallLLM calls to enable connection pooling.
// Timeout is managed via context, so no client-level timeout is set.
var defaultHTTPClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
	},
}

// CallLLMParams contains parameters for calling an LLM provider.
type CallLLMParams struct {
	// Provider overrides auto-detection. One of: "anthropic", "openai", "gemini", "bedrock".
	// If empty, provider is detected from the Endpoint URL.
	Provider string

	Endpoint     string
	ProviderKey  string
	BearerAuth   string // OAuth token (Authorization: Bearer). Takes precedence over ProviderKey for Anthropic.
	Model        string
	SystemPrompt string
	UserPrompt   string
	MaxTokens    int
	Timeout      time.Duration

	// ExtraHeaders are added to the request (e.g., anthropic-beta for OAuth support).
	ExtraHeaders map[string]string

	// HTTPClient overrides the default HTTP client (useful for testing and connection pooling).
	// If nil, a default client is created with context-based timeout.
	// For Bedrock, an HTTPClient with a SigV4 signing transport should be provided.
	HTTPClient *http.Client
}

// validate checks that required fields are present and sets defaults.
func (p *CallLLMParams) validate() error {
	if p.Endpoint == "" {
		return fmt.Errorf("endpoint required")
	}
	// Validate URL scheme to prevent SSRF via arbitrary protocols.
	parsedURL, err := url.Parse(p.Endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint URL: %w", err)
	}
	if parsedURL.Scheme != "https" && parsedURL.Scheme != "http" {
		return fmt.Errorf("endpoint must use http or https scheme, got %q", parsedURL.Scheme)
	}
	// Bedrock uses SigV4 signing via HTTPClient transport, not an API key.
	// OAuth uses BearerToken instead of APIKey.
	if p.ProviderKey == "" && p.BearerAuth == "" && p.Provider != "bedrock" {
		return fmt.Errorf("api key or bearer token required")
	}
	if p.Model == "" {
		return fmt.Errorf("model required")
	}
	if p.Timeout == 0 {
		p.Timeout = DefaultTimeout
	}
	return nil
}

// CallLLMResult contains the response from an LLM call.
type CallLLMResult struct {
	Content      string
	InputTokens  int
	OutputTokens int
	Provider     string
}

// CallLLM calls an LLM provider for text generation (compression/summarization).
//
// Provider detection (when params.Provider is empty):
//   - "anthropic" in URL → Anthropic Messages API
//   - "generativelanguage.googleapis.com" in URL → Gemini generateContent API
//   - otherwise → OpenAI Chat Completions API
//
// For proxy/custom endpoints where URL doesn't identify the provider,
// set params.Provider explicitly.
func CallLLM(ctx context.Context, params CallLLMParams) (*CallLLMResult, error) {
	if err := params.validate(); err != nil {
		return nil, fmt.Errorf("invalid CallLLM params: %w", err)
	}

	provider := params.Provider
	if provider == "" {
		provider = DetectProvider(params.Endpoint)
	}

	body, err := buildRequestBody(provider, params)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal %s request: %w", provider, err)
	}

	ctx, cancel := context.WithTimeout(ctx, params.Timeout)
	defer cancel()

	parsedEndpoint, err := url.Parse(params.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid %s endpoint URL: %w", provider, err)
	}
	if parsedEndpoint.Scheme != "https" && parsedEndpoint.Scheme != "http" {
		return nil, fmt.Errorf("invalid %s endpoint scheme %q: must be http or https", provider, parsedEndpoint.Scheme)
	}

	httpClient := params.HTTPClient
	if httpClient == nil {
		httpClient = defaultHTTPClient // shared client enables connection pooling
	}

	validatedEndpoint := parsedEndpoint.String()
	var lastErr error
	for attempt := 0; attempt < retry.MaxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(retry.Backoff(attempt - 1))
		}

		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, validatedEndpoint, bytes.NewReader(body)) //#nosec G704 -- scheme validated above
		if reqErr != nil {
			return nil, fmt.Errorf("failed to create %s request: %w", provider, reqErr)
		}
		req.Header.Set("Content-Type", "application/json")
		setAuthHeaders(req, provider, params.ProviderKey, params.BearerAuth)
		for k, v := range params.ExtraHeaders {
			req.Header.Set(k, v)
		}

		resp, doErr := httpClient.Do(req)
		if doErr != nil {
			if !retry.IsTransientErr(doErr) {
				return nil, fmt.Errorf("%s request failed: %w", provider, doErr)
			}
			lastErr = fmt.Errorf("%s request failed: %w", provider, doErr)
			continue
		}

		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("failed to read %s response: %w", provider, readErr)
		}

		if retry.IsTransientStatus(resp.StatusCode) {
			errBody := string(respBody)
			if len(errBody) > maxErrorBodyLen {
				errBody = errBody[:maxErrorBodyLen] + "... (truncated)"
			}
			lastErr = fmt.Errorf("%s API returned status %d: %s", provider, resp.StatusCode, errBody)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			errBody := string(respBody)
			if len(errBody) > maxErrorBodyLen {
				errBody = errBody[:maxErrorBodyLen] + "... (truncated)"
			}
			return nil, fmt.Errorf("%s API returned status %d: %s", provider, resp.StatusCode, errBody)
		}

		return parseResponse(provider, respBody)
	}

	return nil, lastErr
}

// DetectProvider infers the LLM provider from an endpoint URL.
// Exported for testing. For production use, prefer setting Provider explicitly.
func DetectProvider(endpoint string) string {
	switch {
	case strings.Contains(endpoint, "bedrock-runtime") || strings.Contains(endpoint, "bedrock"):
		return "bedrock"
	case strings.Contains(endpoint, "anthropic"):
		return "anthropic"
	case strings.Contains(endpoint, "generativelanguage.googleapis.com"):
		return "gemini"
	case strings.Contains(endpoint, ".openai.azure.com") || strings.Contains(endpoint, "azure"):
		return "azure"
	default:
		return "openai"
	}
}

func setAuthHeaders(req *http.Request, provider, apiKey, bearerToken string) {
	switch provider {
	case "anthropic":
		// Anthropic supports two auth methods:
		// 1. API key → x-api-key header
		// 2. OAuth/subscription → Authorization: Bearer header
		if apiKey != "" {
			req.Header.Set("x-api-key", apiKey)
		} else if bearerToken != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", bearerToken))
		}
		req.Header.Set("anthropic-version", anthropicVersion)
	case "bedrock":
		// Bedrock auth is handled by SigV4 signing transport in the HTTPClient.
		// No API key headers needed; the transport signs the request automatically.
	case "gemini":
		// Gemini uses x-goog-api-key header
		// apiKey parameter takes precedence (direct API key users)
		// bearerToken fallback supports subscription-like scenarios
		key := apiKey
		if key == "" {
			key = bearerToken
		}
		if key != "" {
			log.Debug().Str("provider", provider).Int("key_len", len(key)).Str("key_prefix", safePrefix(key, 10)).Msg("Setting Gemini auth header")
			req.Header.Set("x-goog-api-key", key)
		}
	case "azure":
		// Azure OpenAI uses different headers than OpenAI:
		// 1. API key → api-key header (NOT Authorization)
		// 2. OAuth (Microsoft Entra ID) → Authorization: Bearer header
		if apiKey != "" {
			req.Header.Set("api-key", apiKey)
		} else if bearerToken != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", bearerToken))
		}
	default: // openai and compatible providers
		// OpenAI uses Authorization: Bearer for all auth types
		// (both direct API key users and subscription/OAuth users)
		token := apiKey
		if token == "" {
			token = bearerToken
		}
		if token != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
		}
	}
}

// safePrefix returns the first n chars of s for debug logging.
func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Temperature strategy: 0.0 for deterministic compression output.
// Exception: OpenAI o-series models (o1, o3) reject the temperature field — omitted for OpenAI.
func buildRequestBody(provider string, params CallLLMParams) ([]byte, error) {
	switch provider {
	case "anthropic", "bedrock":
		// Bedrock with Anthropic models uses the same Messages API format.
		// The anthropic_version field uses bedrock-2023-05-31 for Bedrock.
		req := &AnthropicRequest{
			Model:       params.Model,
			MaxTokens:   params.MaxTokens,
			System:      params.SystemPrompt,
			Messages:    []AnthropicMessage{{Role: "user", Content: params.UserPrompt}},
			Temperature: 0.0,
		}
		if provider == "bedrock" {
			req.AnthropicVersion = "bedrock-2023-05-31"
		}
		return json.Marshal(req)
	case "gemini":
		return json.Marshal(&GeminiRequest{
			SystemInstruction: &GeminiContent{
				Parts: []GeminiPart{{Text: params.SystemPrompt}},
			},
			Contents: []GeminiContent{
				{Role: "user", Parts: []GeminiPart{{Text: params.UserPrompt}}},
			},
			GenerationConfig: &GeminiGenerationConfig{
				MaxOutputTokens: params.MaxTokens,
				Temperature:     0.0,
			},
		})
	default: // openai — temperature omitted (o-series models reject it)
		return json.Marshal(&OpenAIChatRequest{
			Model: params.Model,
			Messages: []OpenAIMessage{
				{Role: "system", Content: params.SystemPrompt},
				{Role: "user", Content: params.UserPrompt},
			},
			MaxCompletionTokens: params.MaxTokens,
		})
	}
}

func parseResponse(provider string, body []byte) (*CallLLMResult, error) {
	result := &CallLLMResult{Provider: provider}

	switch provider {
	case "anthropic", "bedrock":
		// Bedrock with Anthropic models returns the same response format
		var resp AnthropicResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse %s response: %w", provider, err)
		}
		content, err := ExtractAnthropicResponse(&resp)
		if err != nil {
			return nil, err
		}
		result.Content = content
		result.InputTokens = resp.Usage.InputTokens
		result.OutputTokens = resp.Usage.OutputTokens

	case "gemini":
		var resp GeminiResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse %s response: %w", provider, err)
		}
		content, err := ExtractGeminiResponse(&resp)
		if err != nil {
			return nil, err
		}
		result.Content = content
		result.InputTokens = resp.UsageMetadata.PromptTokenCount
		result.OutputTokens = resp.UsageMetadata.CandidatesTokenCount

	default: // openai
		var resp OpenAIChatResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse %s response: %w", provider, err)
		}
		content, err := ExtractOpenAIResponse(&resp)
		if err != nil {
			return nil, err
		}
		result.Content = content
		result.InputTokens = resp.Usage.PromptTokens
		result.OutputTokens = resp.Usage.CompletionTokens
	}

	return result, nil
}
