// stats.go exposes runtime metrics via the /stats HTTP endpoint.
//
// Returns aggregated token savings, compression stats, and operational
// counters as JSON. Useful for dashboards, CLI tools, and evaluating
// the gateway's ROI (tokens saved, cache hit rates, etc.).
package gateway

import (
	"encoding/json"
	"net/http"
)

// handleStats returns aggregated gateway metrics.
func (g *Gateway) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	stats := g.metrics.FullStats()

	// Merge preemptive manager stats if available
	if g.preemptive != nil {
		pmStats := g.preemptive.Stats()
		if sessions, ok := pmStats["sessions"].(map[string]interface{}); ok {
			if total, ok := sessions["total_sessions"].(int); ok {
				stats.Preemptive.ActiveSessions = int64(total)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	json.NewEncoder(w).Encode(stats)
}
