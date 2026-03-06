package gateway

import (
	"encoding/json"
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

// handleModels serves an OpenAI-compatible model list from the pricing table.
func (g *Gateway) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		g.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	modelIDs := costcontrol.ListModels()
	now := time.Now().Unix()

	data := make([]modelObject, 0, len(modelIDs))
	for _, id := range modelIDs {
		data = append(data, modelObject{
			ID:      id,
			Object:  "model",
			Created: now,
			OwnedBy: inferOwnedBy(id),
		})
	}

	resp := modelsResponse{
		Object: "list",
		Data:   data,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
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
