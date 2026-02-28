// Package compresr provides types for the Compresr API.
package compresr

// =============================================================================
// API Response Types
// =============================================================================

// APIResponse is the generic wrapper for all Compresr API responses.
type APIResponse[T any] struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Data    T      `json:"data"`
}

// =============================================================================
// Subscription & Pricing Types
// =============================================================================

// SubscriptionData contains subscription information from /pricing/subscription.
type SubscriptionData struct {
	Tier              string   `json:"tier"`                // "free", "pro", "enterprise"
	DisplayName       string   `json:"display_name"`        // "Free", "Pro", "Enterprise"
	MonthlyCreditsUSD float64  `json:"monthly_credits_usd"` // Monthly credit allowance
	CreditsRemaining  float64  `json:"credits_remaining"`   // Remaining credits
	Features          []string `json:"features"`            // Enabled features
}

// AvailableModel represents a model from /pricing/available-models.
type AvailableModel struct {
	Name             string  `json:"name"`                // Model ID (e.g., "agnostic_compressor_1")
	DisplayName      string  `json:"display_name"`        // Display name
	InputPricePer1M  float64 `json:"input_price_per_1m"`  // Price per 1M input tokens
	OutputPricePer1M float64 `json:"output_price_per_1m"` // Price per 1M output tokens
}

// ModelsData contains model list from /compress/tool-output/models or tool-discovery/models.
type ModelsData struct {
	SampleTexts       []string    `json:"sample_texts,omitempty"`
	CompressionModels []ModelInfo `json:"compression_models,omitempty"` // For tool-output
	DiscoveryModels   []ModelInfo `json:"discovery_models,omitempty"`   // For tool-discovery
}

// ModelInfo describes a single model.
type ModelInfo struct {
	Value       string `json:"value"`       // Model ID (e.g., "history_compressor_1")
	Label       string `json:"label"`       // Display name (e.g., "History Compressor v1")
	Description string `json:"description"` // Model description
}

// ModelPricingData contains the response from /api/pricing/models/{model_group}.
type ModelPricingData struct {
	UserTier         string             `json:"user_tier"`         // "free", "pro", "enterprise"
	UserTierDisplay  string             `json:"user_tier_display"` // "Free", "Pro", "Enterprise"
	CreditsRemaining float64            `json:"credits_remaining"` // Remaining credits
	ModelGroup       string             `json:"model_group"`       // "tool-output" or "tool-discovery"
	Models           []ModelPricingInfo `json:"models"`            // Available models with pricing
}

// ModelPricingInfo describes a model with pricing and availability info.
type ModelPricingInfo struct {
	Name             string  `json:"name"`                // Model ID
	DisplayName      string  `json:"display_name"`        // Display name
	InputPricePer1M  float64 `json:"input_price_per_1m"`  // Price per 1M input tokens
	OutputPricePer1M float64 `json:"output_price_per_1m"` // Price per 1M output tokens
	Locked           bool    `json:"locked"`              // True if model is not available for user's tier
	MinSubscription  string  `json:"min_subscription"`    // Minimum subscription tier required
}

// =============================================================================
// Tool Output Compression Types
// =============================================================================

// CompressToolOutputParams contains parameters for tool output compression.
type CompressToolOutputParams struct {
	ToolOutput string // The tool output content to compress (required, non-empty)
	UserQuery  string // The user query for context
	ToolName   string // Name of the tool that produced the output (required)
	ModelName  string // Compression model (default: "toc_latte_v1")
	Source     string // Source identifier (e.g., "gateway:anthropic", "sdk:python")
}

// CompressToolOutputResponse contains the compressed output.
type CompressToolOutputResponse struct {
	CompressedOutput string `json:"compressed_output"`
}

// =============================================================================
// Tool Discovery Types
// =============================================================================

// ToolDefinition represents a tool for discovery API requests.
// NOTE: Backend expects 'parameters' field, not 'definition'.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"` // JSON schema for tool parameters
}

// FilterToolsParams contains parameters for tool discovery filtering.
type FilterToolsParams struct {
	Query      string           // The user query for relevance matching
	AlwaysKeep []string         // Tool names to always include
	Tools      []ToolDefinition // Tools to filter
	MaxTools   int              // Maximum number of tools to return (default: 10)
	ModelName  string           // Compression model (default: "tdc_coldbrew_v1")
	Source     string           // Source identifier (e.g., "gateway:anthropic", "sdk:python")
}

// FilterToolsResponse contains the filtered tools.
type FilterToolsResponse struct {
	RelevantTools []string `json:"relevant_tools"`
}

// =============================================================================
// History Compression Types
// =============================================================================

// HistoryMessage represents a message in conversation history.
type HistoryMessage struct {
	Role    string `json:"role"`    // Message role: 'user', 'assistant', 'system', or 'tool'
	Content string `json:"content"` // Message content
}

// CompressHistoryParams contains parameters for history compression.
type CompressHistoryParams struct {
	Messages   []HistoryMessage // Conversation history to compress (required)
	KeepRecent int              // Number of recent messages to keep uncompressed (default: 3)
	ModelName  string           // Compression model (default: "hcc_espresso_v1")
	Source     string           // Source identifier (e.g., "gateway:anthropic", "sdk:python")
}

// CompressHistoryResponse contains the compressed history summary.
type CompressHistoryResponse struct {
	Summary            string  `json:"summary"`
	OriginalTokens     int     `json:"original_tokens"`
	CompressedTokens   int     `json:"compressed_tokens"`
	MessagesCompressed int     `json:"messages_compressed"`
	MessagesKept       int     `json:"messages_kept"`
	CompressionRatio   float64 `json:"compression_ratio"`
	DurationMS         int     `json:"duration_ms"`
}

// =============================================================================
// Model With Availability (for UI display)
// =============================================================================

// ModelWithAvailability wraps ModelInfo with subscription availability.
type ModelWithAvailability struct {
	ModelInfo
	Available bool // True if model is available for current subscription
}

// =============================================================================
// Gateway Status Types (for CLI status bar)
// =============================================================================

// GatewayStatus contains usage status for display in the gateway CLI.
// Returned by GET /api/gateway/status.
type GatewayStatus struct {
	Tier                 string  `json:"tier"`                    // Subscription tier: "free", "pro", "business"
	CreditsRemainingUSD  float64 `json:"credits_remaining_usd"`   // Total remaining credits (subscription + wallet)
	CreditsUsedThisMonth float64 `json:"credits_used_this_month"` // Credits used this billing month
	MonthlyBudgetUSD     float64 `json:"monthly_budget_usd"`      // Monthly budget/allocation (0 = unlimited)
	UsagePercent         float64 `json:"usage_percent"`           // Percentage of monthly budget used (0-100)
	RequestsToday        int     `json:"requests_today"`          // Compression requests today
	RequestsThisMonth    int     `json:"requests_this_month"`     // Compression requests this month
	DailyRequestLimit    *int    `json:"daily_request_limit"`     // Daily request limit (nil = unlimited)
	IsAdmin              bool    `json:"is_admin"`                // Whether user has admin/unlimited access
}
