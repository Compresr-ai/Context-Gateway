// Embedded HTML for the monitoring dashboard SPA.
// Single-file HTML+CSS+JS — no build step needed.
package dashboard

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Context Gateway - Mission Control</title>
<style>
  :root {
    --bg: #0a0a0b;
    --surface: #141416;
    --surface-hover: #1c1c20;
    --border: #27272a;
    --text: #fafafa;
    --text-muted: #71717a;
    --green: #22c55e;
    --green-dim: #166534;
    --yellow: #eab308;
    --yellow-dim: #854d0e;
    --blue: #3b82f6;
    --blue-dim: #1e3a5f;
    --red: #ef4444;
    --red-dim: #7f1d1d;
    --purple: #a855f7;
  }
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    font-family: 'SF Mono', 'Cascadia Code', 'Fira Code', monospace;
    background: var(--bg);
    color: var(--text);
    min-height: 100vh;
  }

  .header {
    padding: 20px 24px;
    border-bottom: 1px solid var(--border);
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .header h1 { font-size: 16px; font-weight: 600; }
  .header .meta { font-size: 12px; color: var(--text-muted); display: flex; gap: 16px; align-items: center; }

  .summary-bar {
    display: flex;
    gap: 24px;
    padding: 12px 24px;
    border-bottom: 1px solid var(--border);
    font-size: 12px;
    color: var(--text-muted);
  }
  .summary-bar .val { color: var(--text); font-weight: 600; }
  .summary-bar .green { color: var(--green); }

  /* ---- Terminal Grid ---- */
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
    gap: 12px;
    padding: 20px 24px;
  }

  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 16px;
    cursor: pointer;
    transition: border-color 0.15s, background 0.15s;
  }
  .card:hover { background: var(--surface-hover); border-color: #3f3f46; }

  .card-top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 10px;
  }
  .card-title {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .card-title .icon { font-size: 16px; opacity: 0.7; }
  .card-title .name { font-size: 13px; font-weight: 600; }
  .card-title .port { font-size: 11px; color: var(--text-muted); }

  .badge {
    font-size: 10px;
    padding: 2px 8px;
    border-radius: 9999px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .badge-active  { background: var(--green-dim); color: var(--green); }
  .badge-idle    { background: var(--yellow-dim); color: var(--yellow); }
  .badge-waiting { background: var(--blue-dim); color: var(--blue); animation: pulse 2s infinite; }
  .badge-finished{ background: var(--red-dim); color: var(--red); }

  .info-btn {
    background: none; border: 1px solid var(--border); color: var(--text-muted);
    font-size: 11px; padding: 2px 7px; border-radius: 4px; cursor: pointer;
    font-family: inherit; margin-left: 6px;
  }
  .info-btn:hover { border-color: var(--text-muted); color: var(--text); }
  @keyframes pulse { 0%,100%{opacity:1} 50%{opacity:0.6} }

  .card-summary {
    font-size: 11px;
    color: var(--text-muted);
    line-height: 1.5;
    margin-bottom: 12px;
    max-height: 36px;
    overflow: hidden;
  }
  .card-summary em { color: var(--text); font-style: normal; }

  .card-stats {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 6px;
    border-top: 1px solid var(--border);
    padding-top: 10px;
  }
  .stat { text-align: center; }
  .stat-val { font-size: 13px; font-weight: 600; }
  .stat-val.green { color: var(--green); }
  .stat-lbl { font-size: 8px; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.05em; margin-top: 1px; }

  .card-foot {
    display: flex;
    justify-content: space-between;
    margin-top: 8px;
    font-size: 10px;
    color: var(--text-muted);
  }
  .tool-tag { background: #27272a; padding: 1px 5px; border-radius: 3px; }

  .empty {
    text-align: center;
    padding: 100px 20px;
    color: var(--text-muted);
  }
  .empty h2 { font-size: 18px; color: var(--text); margin-bottom: 8px; }
  .empty p { font-size: 13px; line-height: 1.6; }

  /* ---- Detail panel ---- */
  .overlay { display:none; position:fixed; inset:0; background:rgba(0,0,0,0.5); z-index:100; }
  .overlay.open { display:block; }
  .panel {
    position:fixed; right:0; top:0; bottom:0; width:520px; max-width:100%;
    background:var(--surface); border-left:1px solid var(--border);
    z-index:101; overflow-y:auto; padding:24px;
  }
  .panel h2 { font-size:15px; margin-bottom:16px; display:flex; justify-content:space-between; align-items:center; }
  .panel-close {
    background:none; border:1px solid var(--border); color:var(--text-muted);
    font-size:12px; padding:4px 10px; border-radius:4px; cursor:pointer; font-family:inherit;
  }
  .panel-close:hover { border-color:var(--text-muted); }
  .section { margin-bottom:20px; }
  .section h3 { font-size:10px; text-transform:uppercase; letter-spacing:0.05em; color:var(--text-muted); margin-bottom:8px; }
  .row { display:flex; justify-content:space-between; padding:4px 0; font-size:12px; }
  .row .lbl { color:var(--text-muted); }
  .row .val { color:var(--text); font-weight:500; }
  .row .val.green { color:var(--green); }
  .query-box {
    background:var(--bg); border:1px solid var(--border); border-radius:6px;
    padding:12px; font-size:12px; line-height:1.5; color:var(--text);
    white-space:pre-wrap; word-break:break-word; max-height:200px; overflow-y:auto;
  }

  /* ---- Activity feed in detail panel ---- */
  .feed { max-height: 300px; overflow-y: auto; }
  .feed-item {
    padding: 6px 0;
    border-bottom: 1px solid var(--border);
    font-size: 11px;
    line-height: 1.4;
  }
  .feed-item .ts { color: var(--text-muted); margin-right: 8px; }
  .feed-item .tool { color: var(--purple); }
  .feed-item .msg { color: var(--text); }
</style>
</head>
<body>

<div class="header">
  <h1>Mission Control</h1>
  <div class="meta">
    <span id="clock"></span>
  </div>
</div>

<div class="summary-bar" id="summary-bar">
  <span><span class="val" id="n-terminals">0</span> terminals</span>
  <span><span class="val" id="n-active">0</span> active</span>
  <span><span class="val" id="n-idle">0</span> idle</span>
  <span><span class="val" id="n-waiting">0</span> waiting</span>
  <span>Saved: <span class="val green" id="total-saved">$0.00</span></span>
  <span>Tokens saved: <span class="val green" id="total-tokens-saved">0</span></span>
</div>

<div class="grid" id="grid"></div>

<div class="overlay" id="overlay">
  <div class="panel" id="panel"></div>
</div>

<script>
let terminals = [];  // aggregated from all instances
let selectedPort = null;

// ---- Data fetching ----
async function refresh() {
  try {
    const resp = await fetch('/monitor/api/all');
    const data = await resp.json();
    terminals = data.terminals || [];
    render();
    if (selectedPort) renderDetail(selectedPort);
  } catch(e) { console.error('poll error', e); }
}
setInterval(refresh, 3000);
refresh();

// ---- Rendering ----
function render() {
  // Summary
  let active=0, idle=0, waiting=0, finished=0, totalSaved=0, totalTokensSaved=0;
  terminals.forEach(t => {
    if (t.status === 'active') active++;
    else if (t.status === 'idle') idle++;
    else if (t.status === 'waiting_for_human') waiting++;
    else if (t.status === 'finished') finished++;
    totalSaved += t.cost_saved_usd || 0;
    totalTokensSaved += t.tokens_saved || 0;
  });
  document.getElementById('n-terminals').textContent = terminals.length;
  document.getElementById('n-active').textContent = active;
  document.getElementById('n-idle').textContent = idle;
  document.getElementById('n-waiting').textContent = waiting;
  document.getElementById('total-saved').textContent = '$' + totalSaved.toFixed(2);
  document.getElementById('total-tokens-saved').textContent = fmtTokens(totalTokensSaved);

  const grid = document.getElementById('grid');
  if (terminals.length === 0) {
    grid.innerHTML = '<div class="empty"><h2>No terminals running</h2><p>Start an agent through the gateway to see it here.<br>Each terminal appears as a card in real-time.</p></div>';
    return;
  }

  grid.innerHTML = terminals.map(t => {
    const summary = t.last_user_query
      ? '<em>' + esc(t.last_user_query.substring(0, 100)) + '</em>'
      : (t.agent_name || 'Starting...');

    return '<div class="card" onclick="focusTerminal(' + t.port + ')">' +
      '<div class="card-top">' +
        '<div class="card-title">' +
          '<span class="icon">' + agentIcon(t.agent_name) + '</span>' +
          '<span class="name">' + agentLabel(t.agent_name) + '</span>' +
          '<span class="port">:' + t.port + '</span>' +
          '<button class="info-btn" onclick="event.stopPropagation();openDetail(' + t.port + ')">i</button>' +
        '</div>' +
        '<span class="badge badge-' + badgeClass(t.status) + '">' + statusLabel(t.status) + '</span>' +
      '</div>' +
      '<div class="card-summary">' + summary + '</div>' +
      '<div class="card-stats">' +
        '<div class="stat"><div class="stat-val">' + t.requests + '</div><div class="stat-lbl">Requests</div></div>' +
        '<div class="stat"><div class="stat-val">' + fmtTokens(t.total_tokens) + '</div><div class="stat-lbl">Tokens</div></div>' +
        '<div class="stat"><div class="stat-val green">' + fmtTokens(t.tokens_saved) + '</div><div class="stat-lbl">Saved</div></div>' +
        '<div class="stat"><div class="stat-val green">$' + (t.cost_saved_usd || 0).toFixed(2) + '</div><div class="stat-lbl">$ Saved</div></div>' +
      '</div>' +
      '<div class="card-foot">' +
        '<span>' + (t.model || '...') + '</span>' +
        (t.last_tool ? '<span class="tool-tag">' + esc(t.last_tool) + '</span>' : '') +
        '<span>' + timeAgo(t.last_activity) + '</span>' +
      '</div>' +
    '</div>';
  }).join('');
}

function openDetail(port) {
  selectedPort = port;
  document.getElementById('overlay').classList.add('open');
  renderDetail(port);
}
function closeDetail() {
  selectedPort = null;
  document.getElementById('overlay').classList.remove('open');
}
document.getElementById('overlay').addEventListener('click', e => { if (e.target.id === 'overlay') closeDetail(); });
document.addEventListener('keydown', e => { if (e.key === 'Escape') closeDetail(); });

function renderDetail(port) {
  const t = terminals.find(x => x.port === port);
  if (!t) { closeDetail(); return; }

  const p = document.getElementById('panel');
  p.innerHTML =
    '<h2>' + agentLabel(t.agent_name) + ' <span style="color:var(--text-muted);font-size:12px">:' + t.port + '</span> <button class="panel-close" onclick="closeDetail()">ESC</button></h2>' +

    '<div class="section"><h3>Status</h3>' +
      '<div class="row"><span class="lbl">State</span><span class="val"><span class="badge badge-' + badgeClass(t.status) + '">' + statusLabel(t.status) + '</span></span></div>' +
      '<div class="row"><span class="lbl">Agent</span><span class="val">' + agentLabel(t.agent_name) + '</span></div>' +
      '<div class="row"><span class="lbl">Model</span><span class="val">' + (t.model || '-') + '</span></div>' +
      '<div class="row"><span class="lbl">Port</span><span class="val">' + t.port + '</span></div>' +
      '<div class="row"><span class="lbl">Uptime</span><span class="val">' + duration(t.started_at) + '</span></div>' +
      '<div class="row"><span class="lbl">Last Activity</span><span class="val">' + timeAgo(t.last_activity) + '</span></div>' +
    '</div>' +

    '<div class="section"><h3>Compression Savings</h3>' +
      '<div class="row"><span class="lbl">Requests</span><span class="val">' + t.requests + ' (' + t.compressed_requests + ' compressed)</span></div>' +
      '<div class="row"><span class="lbl">Total Tokens</span><span class="val">' + fmtTokens(t.total_tokens) + '</span></div>' +
      '<div class="row"><span class="lbl">Tokens Saved</span><span class="val green">' + fmtTokens(t.tokens_saved) + (t.tokens_saved_pct > 0 ? ' (' + t.tokens_saved_pct.toFixed(1) + '%)' : '') + '</span></div>' +
      '<div class="row"><span class="lbl">Cost (actual)</span><span class="val">$' + (t.cost_usd || 0).toFixed(4) + '</span></div>' +
      '<div class="row"><span class="lbl">Cost Saved</span><span class="val green">$' + (t.cost_saved_usd || 0).toFixed(4) + '</span></div>' +
      '<div class="row"><span class="lbl">Cost (without gateway)</span><span class="val">$' + (t.original_cost_usd || 0).toFixed(4) + '</span></div>' +
    '</div>' +

    (t.last_user_query ? '<div class="section"><h3>Last User Message</h3><div class="query-box">' + esc(t.last_user_query) + '</div></div>' : '') +

    '<div class="section"><h3>Terminal</h3>' +
      '<button onclick="focusTerminal(' + t.port + ')" style="background:var(--green-dim);color:var(--green);border:1px solid var(--green);padding:8px 16px;border-radius:6px;cursor:pointer;font-family:inherit;font-size:12px;font-weight:600;width:100%;margin-bottom:8px;">Switch to Terminal :' + t.port + '</button>' +
      '<div style="font-size:11px;color:var(--text-muted);line-height:1.6;">' +
        '<a href="http://localhost:' + t.port + '/costs/" target="_blank" style="color:var(--text-muted)">Cost dashboard</a> · ' +
        '<a href="http://localhost:' + t.port + '/api/savings" target="_blank" style="color:var(--text-muted)">Savings API</a>' +
      '</div>' +
    '</div>';
}

// ---- Focus terminal ----
async function focusTerminal(port) {
  try {
    const resp = await fetch('/monitor/api/focus?port=' + port, {method:'POST'});
    if (!resp.ok) {
      const data = await resp.json();
      alert('Could not focus terminal: ' + (data.error || 'unknown error'));
    }
  } catch(e) { alert('Focus failed: ' + e.message); }
}

// ---- Helpers ----
function agentIcon(n) { return {claude_code:'>_',cursor:'{}',codex:'Cx',opencode:'OC',openclaw:'OW',windsurf:'WS',aider:'Ai'}[n]||'??'; }
function agentLabel(n) { return {claude_code:'Claude Code',cursor:'Cursor',codex:'Codex',opencode:'OpenCode',openclaw:'OpenClaw',windsurf:'Windsurf',aider:'Aider'}[n]||n||'Unknown'; }
function statusLabel(s) { return {active:'Active',waiting_for_human:'Waiting for human interaction',finished:'Finished'}[s]||s; }
function badgeClass(s) { return {active:'active',waiting_for_human:'waiting',finished:'finished'}[s]||'finished'; }
function fmtTokens(n) { if(!n)return '0'; if(n>=1e6)return (n/1e6).toFixed(1)+'M'; if(n>=1e3)return (n/1e3).toFixed(1)+'K'; return ''+n; }
function timeAgo(iso) {
  if(!iso) return '-';
  const s=(Date.now()-new Date(iso).getTime())/1000;
  if(s<5)return 'now'; if(s<60)return Math.floor(s)+'s ago'; if(s<3600)return Math.floor(s/60)+'m ago';
  return Math.floor(s/3600)+'h ago';
}
function duration(iso) {
  if(!iso) return '-';
  const s=(Date.now()-new Date(iso).getTime())/1000;
  const h=Math.floor(s/3600), m=Math.floor((s%3600)/60);
  if(h>0)return h+'h '+m+'m'; if(m>0)return m+'m'; return Math.floor(s)+'s';
}
function esc(s) { const d=document.createElement('div'); d.textContent=s; return d.innerHTML; }

// Clock
setInterval(()=>{ document.getElementById('clock').textContent=new Date().toLocaleTimeString(); },1000);
document.getElementById('clock').textContent=new Date().toLocaleTimeString();
</script>
</body>
</html>`
