// HTTP handlers for the session monitoring dashboard.
//
// Endpoints:
//
//	GET  /monitor/                — Dashboard SPA
//	GET  /monitor/api/all         — Aggregated terminal data from ALL running gateways
//	GET  /monitor/api/sessions    — Local sessions (this instance only)
//	GET  /monitor/api/instances   — List of running gateway instances
//	GET  /monitor/ws              — WebSocket for real-time updates (local)
package dashboard

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Terminal represents one gateway instance in the aggregated dashboard view.
// One terminal = one card on the dashboard.
type Terminal struct {
	Port      int    `json:"port"`
	AgentName string `json:"agent_name"`
	Status    string `json:"status"` // active, idle, waiting_for_human, finished
	Model     string `json:"model"`
	StartedAt string `json:"started_at"`

	// Activity
	LastActivity  string `json:"last_activity"`
	LastUserQuery string `json:"last_user_query"`
	LastTool      string `json:"last_tool"`

	// Metrics
	Requests           int     `json:"requests"`
	CompressedRequests int     `json:"compressed_requests"`
	TotalTokens        int     `json:"total_tokens"`
	TokensSaved        int     `json:"tokens_saved"`
	TokensSavedPct     float64 `json:"tokens_saved_pct"`
	CostUSD            float64 `json:"cost_usd"`
	CostSavedUSD       float64 `json:"cost_saved_usd"`
	OriginalCostUSD    float64 `json:"original_cost_usd"`
}

// Handlers holds HTTP handlers for the monitoring dashboard.
type Handlers struct {
	store    *SessionStore
	hub      *Hub
	selfPort int
}

// NewHandlers creates handlers wired to the given store and hub.
func NewHandlers(store *SessionStore, hub *Hub) *Handlers {
	return &Handlers{store: store, hub: hub}
}

// SetPort sets this instance's port for session tagging.
func (h *Handlers) SetPort(port int) {
	h.selfPort = port
}

// RegisterRoutes adds dashboard routes to the given mux.
func (h *Handlers) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/monitor", h.handleRedirect)
	mux.HandleFunc("/monitor/", h.handleDashboard)
	mux.HandleFunc("/monitor/api/all", h.handleAll)
	mux.HandleFunc("/monitor/api/sessions", h.handleSessions)
	mux.HandleFunc("/monitor/api/instances", h.handleInstances)
	mux.HandleFunc("/monitor/api/focus", h.handleFocus)
	mux.HandleFunc("/monitor/ws", h.handleWS)
}

func (h *Handlers) handleRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/monitor/", http.StatusMovedPermanently)
}

func (h *Handlers) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(dashboardHTML))
}

// handleAll aggregates data from ALL running gateway instances.
// Returns one Terminal per instance with savings, tokens, status.
func (h *Handlers) handleAll(w http.ResponseWriter, r *http.Request) {
	instances := DiscoverInstances()

	var terminals []Terminal
	var mu sync.Mutex
	var wg sync.WaitGroup

	client := &http.Client{Timeout: 3 * time.Second}

	for _, inst := range instances {
		wg.Add(1)
		go func(i Instance) {
			defer wg.Done()
			t := fetchTerminalData(client, i)
			mu.Lock()
			terminals = append(terminals, t)
			mu.Unlock()
		}(inst)
	}
	wg.Wait()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"terminals": terminals,
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

// fetchTerminalData gathers dashboard + session data from a single gateway instance.
func fetchTerminalData(client *http.Client, inst Instance) Terminal {
	t := Terminal{
		Port:      inst.Port,
		AgentName: inst.AgentName,
		Status:    "active",
		StartedAt: inst.StartedAt.Format(time.RFC3339),
	}

	// Fetch savings/cost data from /api/dashboard
	dashURL := fmt.Sprintf("http://localhost:%d/api/dashboard", inst.Port)
	if resp, err := client.Get(dashURL); err == nil { // #nosec G107 -- localhost only
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

		var dash struct {
			TotalCost     float64 `json:"total_cost"`
			TotalRequests int     `json:"total_requests"`
			Savings       *struct {
				TotalRequests      int     `json:"total_requests"`
				CompressedRequests int     `json:"compressed_requests"`
				TokensSaved        int     `json:"tokens_saved"`
				TokenSavedPct      float64 `json:"token_saved_pct"`
				BilledSpendUSD     float64 `json:"billed_spend_usd"`
				CostSavedUSD       float64 `json:"cost_saved_usd"`
				OriginalCostUSD    float64 `json:"original_cost_usd"`
				CompressedCostUSD  float64 `json:"compressed_cost_usd"`
			} `json:"savings"`
			Gateway *struct {
				Uptime        string `json:"uptime"`
				TotalRequests int64  `json:"total_requests"`
			} `json:"gateway"`
		}
		if json.Unmarshal(body, &dash) == nil {
			t.Requests = dash.TotalRequests
			t.CostUSD = dash.TotalCost
			if dash.Savings != nil {
				t.CompressedRequests = dash.Savings.CompressedRequests
				t.TokensSaved = dash.Savings.TokensSaved
				t.TokensSavedPct = dash.Savings.TokenSavedPct
				t.CostSavedUSD = dash.Savings.CostSavedUSD
				t.OriginalCostUSD = dash.Savings.OriginalCostUSD
				// Prefer BilledSpendUSD when available (authoritative from savings);
				// fall back to TotalCost (from cost tracker) otherwise.
				if dash.Savings.BilledSpendUSD > 0 {
					t.CostUSD = dash.Savings.BilledSpendUSD
				}
			}
		}
	}

	// Fetch session activity from /monitor/api/sessions
	sessURL := fmt.Sprintf("http://localhost:%d/monitor/api/sessions", inst.Port)
	if resp, err := client.Get(sessURL); err == nil { // #nosec G107 -- localhost only
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

		var sr struct {
			Sessions []Session `json:"sessions"`
		}
		if json.Unmarshal(body, &sr) == nil && len(sr.Sessions) > 0 {
			// Sum metrics across ALL sessions for this instance
			var totalIn, totalOut int
			var latestActivity time.Time
			var bestSession *Session
			var primarySession *Session // session with most requests (main conversation)
			for i := range sr.Sessions {
				s := &sr.Sessions[i]
				totalIn += s.TokensIn
				totalOut += s.TokensOut
				if s.LastActivityAt.After(latestActivity) {
					latestActivity = s.LastActivityAt
					bestSession = s
				}
				// The primary (main) session is the one with the most requests.
				// Subagent sessions are short-lived with few requests.
				if primarySession == nil || s.RequestCount > primarySession.RequestCount {
					primarySession = s
				}
			}
			t.TotalTokens = totalIn + totalOut
			// Use the most recently active session for status/activity
			if bestSession != nil {
				t.Status = string(bestSession.Status)
				t.LastTool = bestSession.LastToolUsed
				t.LastActivity = bestSession.LastActivityAt.Format(time.RFC3339)
			}
			// Use the primary (most active) session for Model and UserQuery.
			// This avoids subagent sessions (Haiku, code reviewers) overwriting
			// the main conversation's model and user query on the dashboard.
			if primarySession != nil {
				if primarySession.Model != "" {
					t.Model = primarySession.Model
				} else if bestSession != nil {
					t.Model = bestSession.Model
				}
				if primarySession.LastUserQuery != "" {
					t.LastUserQuery = primarySession.LastUserQuery
				} else if bestSession != nil {
					t.LastUserQuery = bestSession.LastUserQuery
				}
			}
		}
	}

	return t
}

// handleSessions returns local sessions for this instance only.
func (h *Handlers) handleSessions(w http.ResponseWriter, r *http.Request) {
	sessions := h.store.All()
	for i := range sessions {
		sessions[i].GatewayPort = h.selfPort
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sessions":  sessions,
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

// handleInstances returns all discovered gateway instances.
func (h *Handlers) handleInstances(w http.ResponseWriter, r *http.Request) {
	instances := DiscoverInstances()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(instances)
}

// handleFocus brings the terminal window for a gateway instance to the foreground.
// POST /monitor/api/focus?port=18080
func (h *Handlers) handleFocus(w http.ResponseWriter, r *http.Request) {
	portStr := r.URL.Query().Get("port")
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid port"})
		return
	}

	// Find the instance from registry
	instances := DiscoverInstances()
	var target *Instance
	for _, inst := range instances {
		if inst.Port == port {
			target = &inst
			break
		}
	}
	if target == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "instance not found"})
		return
	}

	// Focus the terminal window
	if err := FocusTerminal(*target); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "focused", "port": portStr})
}

// handleWS upgrades to WebSocket for real-time local updates.
func (h *Handlers) handleWS(w http.ResponseWriter, r *http.Request) {
	h.hub.HandleWS(w, r)
}
