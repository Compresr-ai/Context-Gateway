package compresr

import (
	"fmt"
	"strings"
	"time"
)

// SubscriptionCache holds cached subscription data for the session.
// Use FetchAll() to populate, then query methods to check availability.
type SubscriptionCache struct {
	Subscription        *SubscriptionData
	AvailableModels     []AvailableModel
	ToolOutputModels    []ModelInfo
	ToolDiscoveryModels []ModelInfo
	FetchedAt           time.Time
}

// IsStale returns true if the cache is older than the given duration.
func (sc *SubscriptionCache) IsStale(maxAge time.Duration) bool {
	return time.Since(sc.FetchedAt) > maxAge
}

// IsModelAvailable checks if a model is available for the subscriber.
func (sc *SubscriptionCache) IsModelAvailable(service, modelID string) bool {
	if sc.AvailableModels == nil {
		return false
	}

	for _, m := range sc.AvailableModels {
		if m.Name == modelID {
			return true
		}
	}
	return false
}

// GetAvailableToolOutputModels returns tool output models from AvailableModels.
// Models containing "tool_discovery" in name are excluded.
func (sc *SubscriptionCache) GetAvailableToolOutputModels() []AvailableModel {
	var result []AvailableModel
	for _, m := range sc.AvailableModels {
		if !strings.Contains(m.Name, "tool_discovery") {
			result = append(result, m)
		}
	}
	return result
}

// GetAvailableToolDiscoveryModels returns tool discovery models from AvailableModels.
// Only models containing "tool_discovery" in name are included.
func (sc *SubscriptionCache) GetAvailableToolDiscoveryModels() []AvailableModel {
	var result []AvailableModel
	for _, m := range sc.AvailableModels {
		if strings.Contains(m.Name, "tool_discovery") {
			result = append(result, m)
		}
	}
	return result
}

// IsFreeSubscription returns true if the subscription is free tier.
func (sc *SubscriptionCache) IsFreeSubscription() bool {
	if sc.Subscription == nil {
		return true
	}
	return sc.Subscription.Tier == "free"
}

// FetchAll populates the cache by fetching all data from the API.
func (sc *SubscriptionCache) FetchAll(client *Client) error {
	sub, err := client.GetSubscription()
	if err != nil {
		return fmt.Errorf("fetching subscription: %w", err)
	}
	sc.Subscription = sub

	available, err := client.GetAvailableModels()
	if err != nil {
		return fmt.Errorf("fetching available models: %w", err)
	}
	sc.AvailableModels = available

	toolOutputModels, err := client.GetToolOutputModels()
	if err != nil {
		// Non-fatal - continue with empty list
		toolOutputModels = nil
	}
	sc.ToolOutputModels = toolOutputModels

	toolDiscoveryModels, err := client.GetToolDiscoveryModels()
	if err != nil {
		// Non-fatal - continue with empty list
		toolDiscoveryModels = nil
	}
	sc.ToolDiscoveryModels = toolDiscoveryModels

	sc.FetchedAt = time.Now()
	return nil
}

// GetToolOutputModelsWithAvailability returns models with availability flag set.
func (sc *SubscriptionCache) GetToolOutputModelsWithAvailability() []ModelWithAvailability {
	result := make([]ModelWithAvailability, len(sc.ToolOutputModels))
	for i, m := range sc.ToolOutputModels {
		result[i] = ModelWithAvailability{
			ModelInfo: m,
			Available: sc.IsModelAvailable("tool_output", m.Value),
		}
	}
	return result
}

// GetToolDiscoveryModelsWithAvailability returns models with availability flag set.
func (sc *SubscriptionCache) GetToolDiscoveryModelsWithAvailability() []ModelWithAvailability {
	result := make([]ModelWithAvailability, len(sc.ToolDiscoveryModels))
	for i, m := range sc.ToolDiscoveryModels {
		result[i] = ModelWithAvailability{
			ModelInfo: m,
			Available: sc.IsModelAvailable("tool_discovery", m.Value),
		}
	}
	return result
}
