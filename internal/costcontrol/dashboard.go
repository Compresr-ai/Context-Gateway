package costcontrol

import (
	"fmt"
	"html"
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
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="5">
<title>Context Gateway - Cost Dashboard</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700;800&family=JetBrains+Mono:wght@400;500;700&display=swap" rel="stylesheet">
<style>
  *, *::before, *::after { margin: 0; padding: 0; box-sizing: border-box; }

  :root {
    --bg: #0a0a0a;
    --surface: rgba(26,26,26,0.8);
    --surface-solid: #1a1a1a;
    --surface-highlight: #2a2a2a;
    --primary: #16a34a;
    --primary-light: #22c55e;
    --blue: #3b82f6;
    --purple: #a78bfa;
    --amber: #f59e0b;
    --text-primary: #ffffff;
    --text-secondary: #9ca3af;
    --text-muted: #6b7280;
    --border: rgba(255,255,255,0.1);
    --border-subtle: rgba(255,255,255,0.06);
    --success: #22c55e;
    --warning: #eab308;
    --error: #ef4444;
    --radius: 16px;
    --radius-sm: 12px;
    --shadow-card: 0 25px 50px -12px rgba(0,0,0,0.5);
    --shadow-glow: 0 0 60px rgba(22,163,74,0.12);
  }

  body {
    font-family: 'Inter', system-ui, -apple-system, sans-serif;
    background: var(--bg);
    color: var(--text-primary);
    min-height: 100vh;
    background-image:
      linear-gradient(to right, rgba(128,128,128,0.03) 1px, transparent 1px),
      linear-gradient(to bottom, rgba(128,128,128,0.03) 1px, transparent 1px);
    background-size: 32px 32px;
  }

  .container {
    max-width: 1000px;
    margin: 0 auto;
    padding: 48px 32px;
  }

  /* ── Header ── */
  .header {
    display: flex;
    align-items: center;
    gap: 16px;
    margin-bottom: 40px;
  }
  .header-icon {
    width: 44px; height: 44px;
    background: linear-gradient(135deg, var(--primary), var(--primary-light));
    border-radius: 14px;
    display: flex; align-items: center; justify-content: center;
    box-shadow: var(--shadow-glow);
    flex-shrink: 0;
  }
  .header-icon svg { width: 22px; height: 22px; color: #fff; }
  .header-text h1 {
    font-size: 22px;
    font-weight: 800;
    letter-spacing: -0.02em;
    background: linear-gradient(135deg, #fff 0%, var(--text-secondary) 100%);
    -webkit-background-clip: text;
    -webkit-text-fill-color: transparent;
    background-clip: text;
  }
  .header-text p {
    font-size: 13px;
    color: var(--text-muted);
    margin-top: 2px;
  }
  .header-badge {
    margin-left: auto;
    font-family: 'JetBrains Mono', monospace;
    font-size: 11px;
    color: var(--text-muted);
    background: var(--surface-solid);
    border: 1px solid var(--border);
    padding: 6px 14px;
    border-radius: 20px;
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .dot {
    width: 7px; height: 7px;
    background: var(--success);
    border-radius: 50%;
    animation: pulse-glow 2s ease-in-out infinite;
  }

  /* ── Stats Grid ── */
  .stats {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 16px;
    margin-bottom: 28px;
  }
  .stat-card {
    background: var(--surface);
    backdrop-filter: blur(16px);
    -webkit-backdrop-filter: blur(16px);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 24px;
    transition: all 0.25s ease;
    position: relative;
    overflow: hidden;
  }
  .stat-card::before {
    content: '';
    position: absolute;
    top: 0; left: 0; right: 0;
    height: 2px;
    background: transparent;
    transition: background 0.25s ease;
  }
  .stat-card:hover {
    border-color: rgba(255,255,255,0.15);
    transform: translateY(-2px);
    box-shadow: 0 12px 40px rgba(0,0,0,0.3);
  }
  .stat-card.hero { border-color: rgba(22,163,74,0.3); }
  .stat-card.hero::before { background: linear-gradient(90deg, var(--primary), var(--primary-light)); }
  .stat-card.hero:hover { box-shadow: var(--shadow-glow); }

  .stat-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 16px;
  }
  .stat-label {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .stat-icon {
    width: 36px; height: 36px;
    border-radius: 10px;
    display: flex; align-items: center; justify-content: center;
  }
  .stat-icon svg { width: 18px; height: 18px; }
  .stat-icon.green  { background: rgba(22,163,74,0.12); color: var(--primary-light); }
  .stat-icon.blue   { background: rgba(59,130,246,0.12); color: var(--blue); }
  .stat-icon.purple { background: rgba(167,139,250,0.12); color: var(--purple); }
  .stat-icon.amber  { background: rgba(245,158,11,0.12); color: var(--amber); }

  .stat-value {
    font-family: 'JetBrains Mono', monospace;
    font-size: 30px;
    font-weight: 700;
    color: var(--text-primary);
    letter-spacing: -0.02em;
    line-height: 1;
  }
  .stat-value.accent { color: var(--primary-light); }
  .stat-sub {
    font-size: 12px;
    color: var(--text-muted);
    margin-top: 6px;
  }

  /* ── Budget Bar ── */
  .budget-bar-wrap {
    margin-bottom: 28px;
    background: var(--surface);
    backdrop-filter: blur(16px);
    -webkit-backdrop-filter: blur(16px);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 24px;
  }
  .budget-bar-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 14px;
  }
  .budget-bar-header .label {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-secondary);
  }
  .budget-bar-header .value {
    font-family: 'JetBrains Mono', monospace;
    font-size: 13px;
    font-weight: 500;
    color: var(--text-secondary);
  }
  .budget-track {
    width: 100%;
    height: 10px;
    background: var(--surface-highlight);
    border-radius: 5px;
    overflow: hidden;
  }
  .budget-fill {
    height: 100%;
    border-radius: 5px;
    transition: width 0.8s cubic-bezier(0.4,0,0.2,1);
  }
  .budget-fill.ok { background: linear-gradient(90deg, var(--primary), var(--primary-light)); }
  .budget-fill.warn { background: linear-gradient(90deg, #ca8a04, var(--warning)); }
  .budget-fill.danger { background: linear-gradient(90deg, #dc2626, var(--error)); }

  /* ── Table ── */
  .table-wrap {
    background: var(--surface);
    backdrop-filter: blur(16px);
    -webkit-backdrop-filter: blur(16px);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .table-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 20px 24px;
    border-bottom: 1px solid var(--border);
  }
  .table-title {
    font-size: 15px;
    font-weight: 700;
    color: var(--text-primary);
    letter-spacing: -0.01em;
  }
  .table-count {
    font-family: 'JetBrains Mono', monospace;
    font-size: 11px;
    color: var(--text-muted);
    background: var(--surface-highlight);
    padding: 4px 10px;
    border-radius: 20px;
  }
  .table-scroll { overflow-x: auto; }
  table {
    width: 100%;
    border-collapse: collapse;
    min-width: 600px;
  }
  th {
    text-align: left;
    padding: 14px 24px;
    font-size: 11px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    background: rgba(255,255,255,0.015);
    border-bottom: 1px solid var(--border);
  }
  td {
    padding: 16px 24px;
    font-size: 13px;
    color: var(--text-secondary);
    border-bottom: 1px solid var(--border-subtle);
    transition: background 0.15s ease;
  }
  tr:last-child td { border-bottom: none; }
  tr:hover td { background: rgba(255,255,255,0.025); }

  .cell-session {
    font-family: 'JetBrains Mono', monospace;
    font-size: 12px;
    color: var(--primary-light);
    font-weight: 500;
  }
  .cell-model {
    display: inline-block;
    font-family: 'JetBrains Mono', monospace;
    font-size: 11px;
    color: var(--purple);
    background: rgba(167,139,250,0.08);
    padding: 3px 10px;
    border-radius: 20px;
    border: 1px solid rgba(167,139,250,0.15);
  }
  .cell-cost {
    font-family: 'JetBrains Mono', monospace;
    font-size: 14px;
    font-weight: 700;
    color: var(--text-primary);
  }
  .cell-requests {
    font-family: 'JetBrains Mono', monospace;
    font-weight: 600;
    color: var(--text-secondary);
  }
  .cell-time {
    color: var(--text-muted);
    font-size: 12px;
  }

  /* ── Mini Bar ── */
  .mini-bar-wrap {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .mini-bar-track {
    width: 80px;
    height: 6px;
    background: var(--surface-highlight);
    border-radius: 3px;
    overflow: hidden;
  }
  .mini-bar-fill {
    height: 100%;
    border-radius: 3px;
    transition: width 0.6s ease;
  }
  .mini-bar-fill.ok { background: var(--primary-light); }
  .mini-bar-fill.warn { background: var(--warning); }
  .mini-bar-fill.danger { background: var(--error); }
  .mini-bar-pct {
    font-family: 'JetBrains Mono', monospace;
    font-size: 11px;
    color: var(--text-muted);
    min-width: 32px;
  }

  /* ── Empty State ── */
  .empty {
    text-align: center;
    padding: 72px 24px;
    color: var(--text-muted);
  }
  .empty-icon {
    width: 56px; height: 56px;
    margin: 0 auto 16px;
    background: var(--surface-highlight);
    border-radius: 16px;
    display: flex; align-items: center; justify-content: center;
    font-size: 24px;
  }
  .empty-title {
    font-size: 15px;
    font-weight: 600;
    color: var(--text-secondary);
    margin-bottom: 6px;
  }
  .empty-text {
    font-size: 13px;
    max-width: 280px;
    margin: 0 auto;
    line-height: 1.6;
  }

  /* ── Footer ── */
  .footer {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    margin-top: 32px;
    font-size: 12px;
    color: var(--text-muted);
  }
  .footer svg { width: 14px; height: 14px; opacity: 0.5; }

  /* ── Animations ── */
  @keyframes pulse-glow {
    0%, 100% { opacity: 1; box-shadow: 0 0 0 0 rgba(34,197,94,0.4); }
    50% { opacity: 0.6; box-shadow: 0 0 0 5px rgba(34,197,94,0); }
  }
  @keyframes fade-in-up {
    from { opacity: 0; transform: translateY(12px); }
    to { opacity: 1; transform: translateY(0); }
  }
  .stat-card { animation: fade-in-up 0.5s ease-out backwards; }
  .stat-card:nth-child(1) { animation-delay: 0s; }
  .stat-card:nth-child(2) { animation-delay: 0.06s; }
  .stat-card:nth-child(3) { animation-delay: 0.12s; }
  .stat-card:nth-child(4) { animation-delay: 0.18s; }
  .table-wrap { animation: fade-in-up 0.5s ease-out 0.25s backwards; }
  .budget-bar-wrap { animation: fade-in-up 0.5s ease-out 0.2s backwards; }

  @media (max-width: 768px) {
    .stats { grid-template-columns: repeat(2, 1fr); gap: 12px; }
    .stat-value { font-size: 24px; }
    .stat-card { padding: 20px; }
    .container { padding: 24px 16px; }
    .header { margin-bottom: 28px; }
    .header-text h1 { font-size: 18px; }
  }
  @media (max-width: 480px) {
    .stats { grid-template-columns: 1fr; }
  }
</style>
</head>
<body>
<div class="container">

<div class="header">
  <div class="header-icon">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/></svg>
  </div>
  <div class="header-text">
    <h1>Context Gateway</h1>
    <p>Cost &amp; Usage Dashboard</p>
  </div>
  <div class="header-badge"><span class="dot"></span>Live</div>
</div>

<div class="stats">
  <div class="stat-card hero">
    <div class="stat-header">
      <span class="stat-label">Total Spend</span>
      <div class="stat-icon green"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="1" x2="12" y2="23"/><path d="M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6"/></svg></div>
    </div>
    <div class="stat-value accent">`)
	fmt.Fprintf(&b, "$%.4f", totalCost)
	b.WriteString(`</div>
  </div>
  <div class="stat-card">
    <div class="stat-header">
      <span class="stat-label">Sessions</span>
      <div class="stat-icon blue"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="7" width="20" height="14" rx="2" ry="2"/><path d="M16 21V5a2 2 0 0 0-2-2h-4a2 2 0 0 0-2 2v16"/></svg></div>
    </div>
    <div class="stat-value">`)
	fmt.Fprintf(&b, "%d", len(sessions))
	b.WriteString(`</div>
  </div>
  <div class="stat-card">
    <div class="stat-header">
      <span class="stat-label">Requests</span>
      <div class="stat-icon purple"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/></svg></div>
    </div>
    <div class="stat-value">`)
	fmt.Fprintf(&b, "%d", totalRequests)
	b.WriteString(`</div>
  </div>
  <div class="stat-card">
    <div class="stat-header">
      <span class="stat-label">Budget</span>
      <div class="stat-icon amber"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/></svg></div>
    </div>
    <div class="stat-value">`)
	if cfg.Enabled && (cfg.SessionCap > 0 || cfg.GlobalCap > 0) {
		if cfg.GlobalCap > 0 {
			fmt.Fprintf(&b, "$%s", formatCost(cfg.GlobalCap))
			b.WriteString(`</div><div class="stat-sub">global cap</div>`)
		} else {
			fmt.Fprintf(&b, "$%s", formatCost(cfg.SessionCap))
			b.WriteString(`</div><div class="stat-sub">per session</div>`)
		}
	} else {
		b.WriteString(`&#8734;</div><div class="stat-sub">unlimited</div>`)
	}
	b.WriteString(`
  </div>
</div>
`)

	// Global budget bar (only when cap is set)
	if cfg.Enabled && cfg.GlobalCap > 0 {
		pct := totalCost / cfg.GlobalCap * 100
		if pct > 100 {
			pct = 100
		}
		barClass := "ok"
		if pct > 80 {
			barClass = "danger"
		} else if pct > 50 {
			barClass = "warn"
		}
		b.WriteString(`<div class="budget-bar-wrap">
  <div class="budget-bar-header">
    <span class="label">Global Budget Usage</span>
    <span class="value">`)
		fmt.Fprintf(&b, "$%.4f / $%s (%.1f%%)", totalCost, formatCost(cfg.GlobalCap), pct)
		b.WriteString(`</span>
  </div>
  <div class="budget-track">`)
		fmt.Fprintf(&b, `<div class="budget-fill %s" style="width:%.1f%%"></div>`, barClass, pct)
		b.WriteString(`</div>
</div>
`)
	}

	if len(sessions) == 0 {
		b.WriteString(`<div class="table-wrap">
  <div class="empty">
    <div class="empty-icon">&#128640;</div>
    <div class="empty-title">No sessions yet</div>
    <div class="empty-text">Requests will appear here as they are processed through the gateway.</div>
  </div>
</div>`)
	} else {
		b.WriteString(`<div class="table-wrap">
<div class="table-header">
  <span class="table-title">Active Sessions</span>
  <span class="table-count">`)
		fmt.Fprintf(&b, "%d session", len(sessions))
		if len(sessions) != 1 {
			b.WriteString("s")
		}
		b.WriteString(`</span>
</div>
<div class="table-scroll">
<table>
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
  <td class="cell-session">%s</td>
  <td><span class="cell-model">%s</span></td>
  <td class="cell-requests">%d</td>
  <td class="cell-cost">$%.4f</td>`, html.EscapeString(sessionDisplay), html.EscapeString(s.Model), s.RequestCount, s.Cost)

			if cfg.Enabled && cfg.SessionCap > 0 {
				pct := s.Cost / cfg.SessionCap * 100
				if pct > 100 {
					pct = 100
				}

				barClass := "ok"
				if pct > 80 {
					barClass = "danger"
				} else if pct > 50 {
					barClass = "warn"
				}

				fmt.Fprintf(&b, `
  <td><div class="mini-bar-wrap"><div class="mini-bar-track"><div class="mini-bar-fill %s" style="width:%.0f%%"></div></div><span class="mini-bar-pct">%.0f%%</span></div></td>`, barClass, pct, pct)
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
  <td class="cell-time">%s</td>
</tr>
`, agoStr)
		}
		b.WriteString(`</table>
</div>
</div>`)
	}

	b.WriteString(`
<div class="footer">
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="23 4 23 10 17 10"/><polyline points="1 20 1 14 7 14"/><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"/></svg>
  Auto-refreshes every 5s
</div>

</div>
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
