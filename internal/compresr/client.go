// Package compresr provides a client for the Compresr API.
package compresr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/compresr/context-gateway/internal/retry"
)

// Default model names for each service.
const (
	DefaultToolOutputModel    = "toc_latte_v1"
	DefaultToolDiscoveryModel = "tdc_coldbrew_v1"
	DefaultHistoryModel       = "hcc_espresso_v1"
)

// DefaultCompresrAPIBaseURL is the production Compresr API base URL.
// The canonical definition lives here. config.DefaultCompresrAPIBaseURL re-exports it.
const DefaultCompresrAPIBaseURL = "https://api.compresr.ai"

// Client is the Compresr API client.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client

	// Cached gateway status to avoid slow external calls on every dashboard refresh
	statusMu    sync.RWMutex
	statusCache *GatewayStatus
	statusTime  time.Time

	// Background refresh goroutine control
	refreshStopCh chan struct{}
	refreshOnce   sync.Once
	stopOnce      sync.Once // ensures StopBackgroundRefresh is safe to call concurrently
}

// ClientOption configures the Client.
type ClientOption func(*Client)

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(c *http.Client) ClientOption {
	return func(client *Client) {
		client.httpClient = c
	}
}

// WithTimeout sets the HTTP client timeout.
func WithTimeout(timeout time.Duration) ClientOption {
	return func(client *Client) {
		client.httpClient.Timeout = timeout
	}
}

// NewClient creates a new Compresr API client.
// It reads COMPRESR_BASE_URL and COMPRESR_API_KEY from environment if not provided.
func NewClient(baseURL, apiKey string, opts ...ClientOption) *Client {
	if baseURL == "" {
		baseURL = os.Getenv("COMPRESR_BASE_URL")
	}
	if baseURL == "" {
		baseURL = DefaultCompresrAPIBaseURL
	}

	if apiKey == "" {
		apiKey = os.Getenv("COMPRESR_API_KEY")
	}

	c := &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// HasAPIKey returns true if an API key is configured.
func (c *Client) HasAPIKey() bool {
	return c.apiKey != ""
}

// SetAPIKey updates the API key.
func (c *Client) SetAPIKey(key string) {
	c.apiKey = key
}

// StartBackgroundRefresh starts a goroutine that refreshes gateway status periodically.
// This ensures /savings and /costs endpoints return instantly without blocking on API calls.
// Safe to call multiple times - only starts once.
func (c *Client) StartBackgroundRefresh(interval time.Duration) {
	if c.apiKey == "" {
		return // No point refreshing without an API key
	}

	c.refreshOnce.Do(func() {
		c.refreshStopCh = make(chan struct{})
		go c.backgroundRefreshLoop(interval)
	})
}

// StopBackgroundRefresh stops the background refresh goroutine.
// Safe to call multiple times and from concurrent goroutines.
func (c *Client) StopBackgroundRefresh() {
	// sync.Once guarantees exactly one close even under concurrent calls.
	c.stopOnce.Do(func() {
		if c.refreshStopCh != nil {
			close(c.refreshStopCh)
		}
	})
}

// backgroundRefreshLoop runs in a goroutine and refreshes status at the given interval.
func (c *Client) backgroundRefreshLoop(interval time.Duration) {
	// Do an initial refresh immediately
	_, _ = c.getGatewayStatus(true)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.refreshStopCh:
			return
		case <-ticker.C:
			_, _ = c.getGatewayStatus(true) // Bypass cache to get fresh data
		}
	}
}

// GetCachedStatus returns the cached gateway status without making an API call.
// Returns nil if no cached status is available. Use this for instant /savings responses.
func (c *Client) GetCachedStatus() *GatewayStatus {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	return c.statusCache
}

// GetSubscription fetches the subscription status for the current API key.
func (c *Client) GetSubscription() (*SubscriptionData, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("no API key configured")
	}

	var resp APIResponse[SubscriptionData]
	if err := c.get("/api/pricing/subscription", &resp); err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("API error: %s", resp.Message)
	}

	return &resp.Data, nil
}

// GetGatewayStatus fetches the user's current usage status for display in the CLI.
// This is a lightweight endpoint designed for frequent polling.
// Uses a 30s cache to avoid excessive API calls.
func (c *Client) GetGatewayStatus() (*GatewayStatus, error) {
	return c.getGatewayStatus(false)
}

// GetGatewayStatusFresh fetches fresh status, bypassing the cache.
// Use this for explicit refresh calls (e.g., exit summary) where stale data is unacceptable.
func (c *Client) GetGatewayStatusFresh() (*GatewayStatus, error) {
	return c.getGatewayStatus(true)
}

func (c *Client) getGatewayStatus(bypassCache bool) (*GatewayStatus, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("no API key configured")
	}

	// Return cached result if fresh (30s TTL) and not bypassing
	if !bypassCache {
		c.statusMu.RLock()
		if c.statusCache != nil && time.Since(c.statusTime) < 30*time.Second {
			cached := c.statusCache
			c.statusMu.RUnlock()
			return cached, nil
		}
		c.statusMu.RUnlock()
	}

	var resp APIResponse[GatewayStatus]
	if err := c.get("/api/gateway/status", &resp); err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("API error: %s", resp.Message)
	}

	// Cache the result
	c.statusMu.Lock()
	c.statusCache = &resp.Data
	c.statusTime = time.Now()
	c.statusMu.Unlock()

	return &resp.Data, nil
}

// GetAvailableModels fetches which models are available for the current subscription.
func (c *Client) GetAvailableModels() ([]AvailableModel, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("no API key configured")
	}

	var resp APIResponse[[]AvailableModel]
	if err := c.get("/api/pricing/available-models", &resp); err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("API error: %s", resp.Message)
	}

	return resp.Data, nil
}

// GetToolOutputModels fetches all tool output compression models.
func (c *Client) GetToolOutputModels() ([]ModelInfo, error) {
	var resp APIResponse[ModelsData]
	if err := c.get("/api/compress/tool-output/models", &resp); err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("API error: %s", resp.Message)
	}

	return resp.Data.CompressionModels, nil
}

// GetToolDiscoveryModels fetches all tool discovery models.
func (c *Client) GetToolDiscoveryModels() ([]ModelInfo, error) {
	var resp APIResponse[ModelsData]
	if err := c.get("/api/compress/tool-discovery/models", &resp); err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("API error: %s", resp.Message)
	}

	return resp.Data.DiscoveryModels, nil
}

// GetModelsPricing fetches models with pricing and availability for a model group.
// modelGroup should be "tool-output" or "tool-discovery".
func (c *Client) GetModelsPricing(modelGroup string) (*ModelPricingData, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("no API key configured")
	}

	var resp APIResponse[ModelPricingData]
	if err := c.get("/api/pricing/models/"+modelGroup, &resp); err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("API error: %s", resp.Message)
	}

	return &resp.Data, nil
}

// ValidateAPIKey checks if the API key is valid by making a subscription request.
// Returns the subscription tier if valid, or an error if invalid.
func (c *Client) ValidateAPIKey() (string, error) {
	sub, err := c.GetSubscription()
	if err != nil {
		return "", err
	}
	return sub.Tier, nil
}

// CompressHistory calls the Compresr API to compress conversation history.
func (c *Client) CompressHistory(params CompressHistoryParams) (*CompressHistoryResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("no API key configured")
	}

	// Default model if not specified
	modelName := params.ModelName
	if modelName == "" {
		modelName = DefaultHistoryModel
	}

	// Validate required fields
	if len(params.Messages) == 0 {
		return nil, fmt.Errorf("messages are required")
	}

	// Default keep_recent
	keepRecent := params.KeepRecent
	if keepRecent == 0 {
		keepRecent = 3
	}

	payload := struct {
		Messages             []HistoryMessage `json:"messages"`
		KeepRecent           int              `json:"keep_recent"`
		CompressionModelName string           `json:"compression_model_name"`
		Source               string           `json:"source,omitempty"`
	}{
		Messages:             params.Messages,
		KeepRecent:           keepRecent,
		CompressionModelName: modelName,
		Source:               params.Source,
	}

	var resp APIResponse[CompressHistoryResponse]
	if err := c.post("/api/compress/history/", payload, &resp); err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("API error: %s", resp.Message)
	}

	return &resp.Data, nil
}

// CompressToolOutput calls the Compresr API to compress tool output.
func (c *Client) CompressToolOutput(params CompressToolOutputParams) (*CompressToolOutputResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("no API key configured")
	}

	// Default model if not specified
	modelName := params.ModelName
	if modelName == "" {
		modelName = DefaultToolOutputModel
	}

	// Validate required fields
	if params.ToolOutput == "" {
		return nil, fmt.Errorf("tool_output is required")
	}
	if params.ToolName == "" {
		return nil, fmt.Errorf("tool_name is required")
	}

	payload := struct {
		ToolOutput             string  `json:"tool_output"`
		Query                  string  `json:"query,omitempty"`
		ToolName               string  `json:"tool_name"`
		ModelName              string  `json:"compression_model_name"`
		Source                 string  `json:"source,omitempty"`
		TargetCompressionRatio float64 `json:"target_compression_ratio,omitempty"`
	}{
		ToolOutput:             params.ToolOutput,
		Query:                  params.UserQuery,
		ToolName:               params.ToolName,
		ModelName:              modelName,
		Source:                 params.Source,
		TargetCompressionRatio: params.TargetCompressionRatio,
	}

	var resp APIResponse[CompressToolOutputResponse]
	if err := c.post("/api/compress/tool-output/", payload, &resp); err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("API error: %s", resp.Message)
	}

	if resp.Data.CompressedOutput == "" {
		return nil, fmt.Errorf("API returned empty compressed_output")
	}

	return &resp.Data, nil
}

// FilterTools calls the Compresr API to select relevant tools.
func (c *Client) FilterTools(params FilterToolsParams) (*FilterToolsResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("no API key configured")
	}

	// Validate required fields
	if params.Query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if len(params.Tools) == 0 {
		return nil, fmt.Errorf("tools list is required and must not be empty")
	}

	// Use default model if not specified
	modelName := params.ModelName
	if modelName == "" {
		modelName = DefaultToolDiscoveryModel
	}

	// Use default max_tools if not specified (backend default is 5)
	maxTools := params.MaxTools
	if maxTools <= 0 {
		maxTools = 5 // Backend default is 5
	}

	payload := struct {
		Query                string           `json:"query"`
		AlwaysKeep           []string         `json:"always_keep,omitempty"`
		Tools                []ToolDefinition `json:"tools"`
		MaxTools             int              `json:"max_tools"`
		CompressionModelName string           `json:"compression_model_name"`
		Source               string           `json:"source,omitempty"`
	}{
		Query:                params.Query,
		AlwaysKeep:           params.AlwaysKeep,
		Tools:                params.Tools,
		MaxTools:             maxTools,
		CompressionModelName: modelName,
		Source:               params.Source,
	}

	var resp APIResponse[FilterToolsResponse]
	if err := c.post("/api/compress/tool-discovery/", payload, &resp); err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("API error: %s", resp.Message)
	}

	return &resp.Data, nil
}

func (c *Client) get(path string, result any) error {
	reqURL := c.baseURL + path

	parsedURL, err := url.Parse(reqURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsedURL.Scheme != "https" && parsedURL.Scheme != "http" {
		return fmt.Errorf("URL must use http or https scheme, got %q", parsedURL.Scheme)
	}

	validatedURL := parsedURL.String()
	var lastErr error
	for attempt := 0; attempt < retry.MaxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(retry.Backoff(attempt - 1))
		}

		req, reqErr := http.NewRequest(http.MethodGet, validatedURL, nil) //#nosec G704 -- scheme validated above
		if reqErr != nil {
			return fmt.Errorf("creating request: %w", reqErr)
		}
		if c.apiKey != "" {
			req.Header.Set("X-API-Key", c.apiKey)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "compresr-gateway/1.0")

		resp, doErr := c.httpClient.Do(req)
		if doErr != nil {
			if !retry.IsTransientErr(doErr) {
				return fmt.Errorf("request failed: %w", doErr)
			}
			lastErr = fmt.Errorf("request failed: %w", doErr)
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("reading response: %w", readErr)
		}

		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("invalid API key")
		}
		if retry.IsTransientStatus(resp.StatusCode) {
			lastErr = fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
		}

		if unmarshalErr := json.Unmarshal(body, result); unmarshalErr != nil {
			return fmt.Errorf("parsing response: %w", unmarshalErr)
		}
		return nil
	}

	return lastErr
}

func (c *Client) post(path string, payload any, result any) error {
	reqURL := c.baseURL + path

	parsedURL, err := url.Parse(reqURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsedURL.Scheme != "https" && parsedURL.Scheme != "http" {
		return fmt.Errorf("URL must use http or https scheme, got %q", parsedURL.Scheme)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling payload: %w", err)
	}

	validatedURL := parsedURL.String()
	var lastErr error
	for attempt := 0; attempt < retry.MaxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(retry.Backoff(attempt - 1))
		}

		req, reqErr := http.NewRequest(http.MethodPost, validatedURL, bytes.NewReader(body)) //#nosec G704 -- scheme validated above
		if reqErr != nil {
			return fmt.Errorf("creating request: %w", reqErr)
		}
		req.Header.Set("Content-Type", "application/json")
		if c.apiKey != "" {
			req.Header.Set("X-API-Key", c.apiKey)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "compresr-gateway/1.0")

		resp, doErr := c.httpClient.Do(req)
		if doErr != nil {
			if !retry.IsTransientErr(doErr) {
				return fmt.Errorf("request failed: %w", doErr)
			}
			lastErr = fmt.Errorf("request failed: %w", doErr)
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("reading response: %w", readErr)
		}

		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("invalid API key")
		}
		if retry.IsTransientStatus(resp.StatusCode) {
			lastErr = fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
		}

		if unmarshalErr := json.Unmarshal(respBody, result); unmarshalErr != nil {
			return fmt.Errorf("parsing response: %w", unmarshalErr)
		}
		return nil
	}

	return lastErr
}
