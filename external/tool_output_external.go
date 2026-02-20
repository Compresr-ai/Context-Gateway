// Tool output compression via external providers.
//
// STATUS: Not used in current release. Tool output compression is disabled.
// This file is retained for future integration with external compression services.
//
// This file implements external compression for tool outputs by calling
// an external API. It works for BOTH OpenAI and Anthropic formatted requests.
package external

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog/log"
)

// ExternalProvider implements Provider for external compression APIs.
// Works with both OpenAI and Anthropic formatted requests.
type ExternalProvider struct {
	config     Config
	httpClient *http.Client
	apiKey     string
}

// NewExternalProvider creates a new external compression provider.
func NewExternalProvider(cfg Config) (*ExternalProvider, error) {
	// Apply defaults
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultConfig().Endpoint
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultConfig().Timeout
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = DefaultConfig().MaxRetries
	}

	// Get API key from environment
	apiKey := ""
	if cfg.APIKeyEnv != "" {
		apiKey = os.Getenv(cfg.APIKeyEnv)
		if apiKey == "" {
			log.Warn().
				Str("env_var", cfg.APIKeyEnv).
				Msg("external: API key environment variable not set")
		}
	}

	return &ExternalProvider{
		config: cfg,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
		apiKey: apiKey,
	}, nil
}

// Name returns the provider name.
func (p *ExternalProvider) Name() string {
	if p.config.Provider != "" {
		return p.config.Provider
	}
	return "external"
}

// externalAPIRequest is the request body sent to the compression API.
// Works for both OpenAI and Anthropic - SourceProvider indicates format.
type externalAPIRequest struct {
	Content        string `json:"content"`
	ToolName       string `json:"tool_name"`
	UserQuery      string `json:"user_query,omitempty"`
	SourceProvider string `json:"source_provider"` // "openai" or "anthropic"
	Model          string `json:"model,omitempty"`
	MaxTokens      int    `json:"max_tokens,omitempty"`
}

// externalAPIResponse is the response body from the compression API.
type externalAPIResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Data    struct {
		Content          string  `json:"content"`
		OriginalSize     int     `json:"original_size"`
		CompressedSize   int     `json:"compressed_size"`
		CompressionRatio float64 `json:"compression_ratio"`
		Model            string  `json:"model,omitempty"`
		CacheHit         bool    `json:"cache_hit"`
		ProcessingTimeMs int64   `json:"processing_time_ms,omitempty"`
	} `json:"data,omitempty"`
}

// Compress sends a compression request to the external API.
// Works for both OpenAI and Anthropic based on req.SourceProvider.
func (p *ExternalProvider) Compress(req *CompressRequest) (*CompressResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("compress request is nil")
	}

	// Build API request
	apiReq := externalAPIRequest{
		Content:        req.Content,
		ToolName:       req.ToolName,
		UserQuery:      req.UserQuery,
		SourceProvider: req.SourceProvider, // "openai" or "anthropic"
		Model:          req.Model,
		MaxTokens:      req.MaxTokens,
	}

	// Use config model if not specified in request
	if apiReq.Model == "" {
		apiReq.Model = p.config.Model
	}

	// Default to openai if not specified
	if apiReq.SourceProvider == "" {
		apiReq.SourceProvider = "openai"
	}

	// Marshal request
	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Build URL
	url := fmt.Sprintf("%s%s", p.config.BaseURL, p.config.Endpoint)

	// Retry loop
	var lastErr error
	for attempt := 0; attempt <= p.config.MaxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff
			time.Sleep(time.Duration(attempt*100) * time.Millisecond)
			log.Debug().
				Int("attempt", attempt+1).
				Str("provider", p.Name()).
				Str("source", apiReq.SourceProvider).
				Msg("external: retrying compression request")
		}

		resp, err := p.doRequest(url, body, apiReq.SourceProvider)
		if err != nil {
			lastErr = err
			continue
		}
		return resp, nil
	}

	return nil, fmt.Errorf("compression failed after %d attempts: %w", p.config.MaxRetries+1, lastErr)
}

// doRequest performs a single HTTP request to the compression API.
func (p *ExternalProvider) doRequest(url string, body []byte, sourceProvider string) (*CompressResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.config.Timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Source-Provider", sourceProvider) // Tell API the format
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.apiKey))
	}

	// Make request
	startTime := time.Now()
	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Check HTTP status
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse response
	var apiResp externalAPIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Check for API-level error
	if !apiResp.Success {
		return nil, fmt.Errorf("API error: %s", apiResp.Error)
	}

	// Build response
	return &CompressResponse{
		Content:          apiResp.Data.Content,
		OriginalSize:     apiResp.Data.OriginalSize,
		CompressedSize:   apiResp.Data.CompressedSize,
		CompressionRatio: apiResp.Data.CompressionRatio,
		Model:            apiResp.Data.Model,
		CacheHit:         apiResp.Data.CacheHit,
		ProcessingTime:   time.Since(startTime),
	}, nil
}

// HealthCheck verifies the external API is reachable.
func (p *ExternalProvider) HealthCheck() error {
	url := fmt.Sprintf("%s/health", p.config.BaseURL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create health check request: %w", err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned status %d", resp.StatusCode)
	}

	return nil
}

// NewProvider creates a Provider based on the config.
// Factory function for creating external providers.
func NewProvider(cfg Config) (Provider, error) {
	switch cfg.Provider {
	case "external", "compresr", "":
		return NewExternalProvider(cfg)
	default:
		return nil, fmt.Errorf("unknown provider: %s", cfg.Provider)
	}
}

// Convenience aliases for backward compatibility
var (
	NewCompresrProvider = NewExternalProvider
)
