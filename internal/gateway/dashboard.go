// Package gateway - dashboard.go serves the React cost dashboard SPA at /costs.
//
// DESIGN: The React SPA (web/dashboard) is built to cmd/dashboard_dist and embedded.
// It fetches data from /api/dashboard (handleDashboardAPI in handler.go).
// This handler serves the SPA, with a minimal fallback if embedding isn't available.
package gateway

import (
	"net/http"
)

// handleCostDashboard serves the React cost dashboard SPA.
// Restricted to localhost to prevent external access to cost data.
// Falls back to a minimal page if the SPA isn't embedded.
func (g *Gateway) handleCostDashboard(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(r.RemoteAddr) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// Redirect /costs to /costs/ so relative asset paths work
	if r.URL.Path == "/costs" {
		http.Redirect(w, r, "/costs/", http.StatusMovedPermanently)
		return
	}

	// Serve embedded React SPA if available
	if g.dashboardFS != nil {
		http.StripPrefix("/costs", g.dashboardFS).ServeHTTP(w, r)
		return
	}

	// Fallback: show minimal HTML that links to the JSON API
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>Context Gateway Dashboard</title>
<style>
  body { font-family: system-ui, sans-serif; background: #0a0a0a; color: #fff; display: flex; align-items: center; justify-content: center; min-height: 100vh; margin: 0; }
  .container { text-align: center; padding: 48px; }
  h1 { font-size: 24px; margin-bottom: 16px; }
  p { color: #9ca3af; margin-bottom: 24px; }
  a { color: #22c55e; text-decoration: none; font-family: monospace; }
  a:hover { text-decoration: underline; }
</style>
</head>
<body>
<div class="container">
  <h1>Context Gateway</h1>
  <p>Dashboard SPA not embedded. View raw data:</p>
  <a href="/api/dashboard">/api/dashboard</a> (JSON) &nbsp;|&nbsp;
  <a href="/stats">/stats</a> (metrics)
</div>
</body>
</html>`))
}
