package costcontrol

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// HandleDashboard serves the cost dashboard HTML page.
func (t *Tracker) HandleDashboard(w http.ResponseWriter, r *http.Request) {
	sessions := t.AllSessions()
	cfg := t.Config()

	// Sort by last updated (most recent first)
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastUpdated.After(sessions[j].LastUpdated)
	})

	// Calculate total spend
	var totalCost float64
	var totalRequests int
	for _, s := range sessions {
		totalCost += s.Cost
		totalRequests += s.RequestCount
	}

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="5">
<title>Context Gateway - Cost Dashboard</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { font-family: 'SF Mono', 'Fira Code', 'Cascadia Code', monospace; background: #0d1117; color: #c9d1d9; padding: 24px; }
  h1 { color: #58a6ff; font-size: 18px; margin-bottom: 16px; }
	  .summary { display: flex; gap: 24px; margin-bottom: 24px; padding: 16px; background: #161b22; border: 1px solid #30363d; border-radius: 6px; }
	  .stat { }
	  .stat-label { font-size: 11px; color: #8b949e; text-transform: uppercase; letter-spacing: 1px; }
	  .stat-value { font-size: 24px; font-weight: bold; color: #f0f6fc; }
	  .stat-value.cost { color: #ffa657; }
	  table { width: 100%; border-collapse: collapse; background: #161b22; border: 1px solid #30363d; border-radius: 6px; overflow: hidden; }
	  th { text-align: left; padding: 10px 14px; font-size: 11px; color: #8b949e; text-transform: uppercase; letter-spacing: 1px; background: #0d1117; border-bottom: 1px solid #30363d; }
	  td { padding: 10px 14px; font-size: 13px; border-bottom: 1px solid #21262d; }
	  tr:last-child td { border-bottom: none; }
  .session-id { color: #58a6ff; }
  .model { color: #d2a8ff; }
  .cost { color: #ffa657; font-weight: bold; }
  .bar-container { width: 100px; height: 8px; background: #21262d; border-radius: 4px; overflow: hidden; display: inline-block; vertical-align: middle; margin-right: 8px; }
	  .bar { height: 100%; border-radius: 4px; }
	  .bar-ok { background: #3fb950; }
	  .bar-warn { background: #d29922; }
	  .bar-danger { background: #f85149; }
	  .empty { text-align: center; padding: 40px; color: #8b949e; }
	  .footer { margin-top: 16px; font-size: 11px; color: #484f58; }
	</style>
</head>
<body>
<h1>Context Gateway - Cost Dashboard</h1>
<div class="summary">
  <div class="stat">
    <div class="stat-label">Total Spend</div>
    <div class="stat-value cost">`)
	fmt.Fprintf(&b, "$%.4f", totalCost)
	b.WriteString(`</div>
  </div>
  <div class="stat">
    <div class="stat-label">Sessions</div>
    <div class="stat-value">`)
	fmt.Fprintf(&b, "%d", len(sessions))
	b.WriteString(`</div>
  </div>
  <div class="stat">
    <div class="stat-label">Total Requests</div>
    <div class="stat-value">`)
	fmt.Fprintf(&b, "%d", totalRequests)
	b.WriteString(`</div>
  </div>
  <div class="stat">
    <div class="stat-label">Budget Cap</div>
    <div class="stat-value">`)
	if cfg.Enabled && (cfg.SessionCap > 0 || cfg.GlobalCap > 0) {
		var parts []string
		if cfg.SessionCap > 0 {
			parts = append(parts, fmt.Sprintf("$%s/session", formatCost(cfg.SessionCap)))
		}
		if cfg.GlobalCap > 0 {
			parts = append(parts, fmt.Sprintf("$%s global", formatCost(cfg.GlobalCap)))
		}
		b.WriteString(strings.Join(parts, ", "))
	} else {
		b.WriteString("Unlimited")
	}
	b.WriteString(`</div>
  </div>
	</div>
	`)

	if len(sessions) == 0 {
		b.WriteString(`<div class="empty">No sessions yet. Requests will appear here as they are processed.</div>`)
	} else {
		b.WriteString(`<table>
<tr>
  <th>Session</th>
  <th>Model</th>
  <th>Requests</th>
	  <th>Cost</th>`)
		if cfg.Enabled && cfg.SessionCap > 0 {
			b.WriteString(`
	  <th>Budget</th>`)
		}
		b.WriteString(`
	  <th>Last Activity</th>
</tr>
`)
		for _, s := range sessions {
			sessionDisplay := s.ID
			if len(sessionDisplay) > 12 {
				sessionDisplay = sessionDisplay[:12] + "..."
			}

			fmt.Fprintf(&b, `<tr>
  <td class="session-id">%s</td>
  <td class="model">%s</td>
  <td>%d</td>
  <td class="cost">$%.4f</td>`, sessionDisplay, s.Model, s.RequestCount, s.Cost)

			if cfg.Enabled && cfg.SessionCap > 0 {
				pct := s.Cost / cfg.SessionCap * 100
				if pct > 100 {
					pct = 100
				}

				barClass := "bar-ok"
				if pct > 80 {
					barClass = "bar-danger"
				} else if pct > 50 {
					barClass = "bar-warn"
				}

				fmt.Fprintf(&b, `
	  <td><div class="bar-container"><div class="bar %s" style="width:%.0f%%"></div></div>%.0f%%</td>`, barClass, pct, pct)
			}

			ago := time.Since(s.LastUpdated)
			var agoStr string
			switch {
			case ago < time.Minute:
				agoStr = fmt.Sprintf("%ds ago", int(ago.Seconds()))
			case ago < time.Hour:
				agoStr = fmt.Sprintf("%dm ago", int(ago.Minutes()))
			default:
				agoStr = fmt.Sprintf("%dh ago", int(ago.Hours()))
			}

			fmt.Fprintf(&b, `
  <td>%s</td>
</tr>
`, agoStr)
		}
		b.WriteString(`</table>`)
	}

	b.WriteString(`
<div class="footer">Auto-refreshes every 5 seconds</div>
</body>
</html>`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

// formatCost formats a dollar amount, using more decimal places for small values.
func formatCost(v float64) string {
	if v >= 1.0 {
		return fmt.Sprintf("%.2f", v)
	}
	return fmt.Sprintf("%.4f", v)
}
