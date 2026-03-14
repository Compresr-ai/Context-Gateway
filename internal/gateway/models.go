package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/compresr/context-gateway/internal/costcontrol"
)

// modelObject represents a single model in the OpenAI-compatible /v1/models response.
type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// modelsResponse is the OpenAI-compatible response for GET /v1/models.
type modelsResponse struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}

type codexModelObject struct {
	Slug                     string                `json:"slug"`
	DisplayName              string                `json:"display_name"`
	Description              string                `json:"description"`
	DefaultReasoningLevel    string                `json:"default_reasoning_level"`
	SupportedReasoningLevels []codexReasoningLevel `json:"supported_reasoning_levels"`
	ShellType                string                `json:"shell_type"`
	Visibility               string                `json:"visibility"`
	SupportedInAPI           bool                  `json:"supported_in_api"`
	Priority                 int                   `json:"priority"`
	AvailabilityNUX          any                   `json:"availability_nux"`
	Upgrade                  any                   `json:"upgrade"`
	BaseInstructions         string                `json:"base_instructions"`
	SupportsReasoningSummary bool                  `json:"supports_reasoning_summaries"`
	SupportVerbosity         bool                  `json:"support_verbosity"`
	DefaultVerbosity         any                   `json:"default_verbosity"`
	ApplyPatchToolType       any                   `json:"apply_patch_tool_type"`
	TruncationPolicy         codexTruncationPolicy `json:"truncation_policy"`
	SupportsParallelTools    bool                  `json:"supports_parallel_tool_calls"`
	SupportsImageDetail      bool                  `json:"supports_image_detail_original"`
	ContextWindow            int                   `json:"context_window"`
	ExperimentalTools        []string              `json:"experimental_supported_tools"`
	PreferWebSockets         bool                  `json:"prefer_websockets"`
}

type codexReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

type codexTruncationPolicy struct {
	Mode  string `json:"mode"`
	Limit int    `json:"limit"`
}

type codexModelsResponse struct {
	Models []codexModelObject `json:"models"`
}

func (g *Gateway) registerModelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/models", g.handleModels)
	mux.HandleFunc("/v1/models", g.handleModels)
}

// handleModels serves an OpenAI-compatible model list from the pricing table.
func (g *Gateway) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		g.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Proxy /models upstream only when the caller explicitly targets an upstream
	// endpoint or uses provider-native API credentials. ChatGPT subscription
	// tokens do not support /backend-api/models and must keep the local synthetic list.
	if shouldProxyModelsUpstream(r) {
		resp, _, err := g.forwardPassthrough(r.Context(), r, nil)
		if err == nil && resp != nil {
			defer func() { _ = resp.Body.Close() }()
			responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize))
			copyHeaders(w, resp.Header)
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(responseBody)
			return
		}
	}

	modelIDs := costcontrol.ListModels()
	now := time.Now().Unix()
	reasoningLevels := []codexReasoningLevel{
		{Effort: "low", Description: "Fast responses with lighter reasoning"},
		{Effort: "medium", Description: "Balances speed and reasoning depth for everyday tasks"},
		{Effort: "high", Description: "Greater reasoning depth for complex problems"},
	}

	data := make([]modelObject, 0, len(modelIDs))
	codexModels := make([]codexModelObject, 0, len(modelIDs))
	for idx, id := range modelIDs {
		data = append(data, modelObject{
			ID:      id,
			Object:  "model",
			Created: now,
			OwnedBy: inferOwnedBy(id),
		})
		codexModels = append(codexModels, codexModelObject{
			Slug:                     id,
			DisplayName:              id,
			Description:              "",
			DefaultReasoningLevel:    "medium",
			SupportedReasoningLevels: reasoningLevels,
			ShellType:                "shell_command",
			Visibility:               "list",
			SupportedInAPI:           true,
			Priority:                 idx,
			AvailabilityNUX:          nil,
			Upgrade:                  nil,
			BaseInstructions:         "",
			SupportsReasoningSummary: false,
			SupportVerbosity:         false,
			DefaultVerbosity:         nil,
			ApplyPatchToolType:       nil,
			TruncationPolicy:         codexTruncationPolicy{Mode: "bytes", Limit: 10000},
			SupportsParallelTools:    true,
			SupportsImageDetail:      false,
			ContextWindow:            272000,
			ExperimentalTools:        []string{},
			PreferWebSockets:         false,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/models" {
		_ = json.NewEncoder(w).Encode(codexModelsResponse{Models: codexModels})
		return
	}

	_ = json.NewEncoder(w).Encode(modelsResponse{
		Object: "list",
		Data:   data,
	})
}

func shouldProxyModelsUpstream(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get(HeaderTargetURL)) != "" {
		return true
	}
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" && !isChatGPTSubscription(r) {
		return true
	}
	for _, header := range []string{"x-api-key", "x-goog-api-key", "api-key", "anthropic-version"} {
		if strings.TrimSpace(r.Header.Get(header)) != "" {
			return true
		}
	}
	return false
}

// inferOwnedBy returns the provider name based on model ID prefix.
func inferOwnedBy(modelID string) string {
	lower := strings.ToLower(modelID)
	switch {
	case strings.HasPrefix(lower, "claude"):
		return "anthropic"
	case strings.HasPrefix(lower, "gpt-"), strings.HasPrefix(lower, "chatgpt-"),
		strings.HasPrefix(lower, "o1"), strings.HasPrefix(lower, "o3"),
		strings.HasPrefix(lower, "o4"):
		return "openai"
	case strings.HasPrefix(lower, "gemini"):
		return "google"
	default:
		return "unknown"
	}
}
