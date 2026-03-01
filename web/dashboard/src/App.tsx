import { useState, useEffect, Component, type ReactNode } from 'react'
import { Zap, DollarSign, TrendingDown, TrendingUp, Search, CheckCircle, XCircle, Wallet, BarChart3 } from 'lucide-react'
import type { DashboardData, Session, Savings, ExpandContext, AccountData } from './types'

// Error boundary to catch render errors
class ErrorBoundary extends Component<{ children: ReactNode }, { error: string | null }> {
  constructor(props: { children: ReactNode }) {
    super(props)
    this.state = { error: null }
  }
  static getDerivedStateFromError(error: Error) {
    return { error: error.message }
  }
  render() {
    if (this.state.error) {
      return (
        <div style={{ color: '#ef4444', padding: 24, fontFamily: 'monospace', fontSize: 14 }}>
          React Error: {this.state.error}
        </div>
      )
    }
    return this.props.children
  }
}

function formatCost(v: number): string {
  return v >= 1 ? v.toFixed(2) : v.toFixed(4)
}

function timeAgo(dateStr: string): string {
  const diff = Date.now() - new Date(dateStr).getTime()
  const seconds = Math.floor(diff / 1000)
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  return `${Math.floor(minutes / 60)}h ago`
}

function CostRow({ savings }: { savings?: Savings }) {
  const totalSpend = savings?.compressed_cost_usd ?? 0
  const saved = savings?.cost_saved_usd ?? 0
  const withoutProxy = savings?.original_cost_usd ?? 0

  const cards = [
    { label: 'Total Spending', value: `$${formatCost(totalSpend)}`, icon: <DollarSign size={18} />, color: '#22c55e', borderColor: 'rgba(22,163,74,0.3)', accent: true, subtext: 'actual cost with compression' },
    { label: 'You Saved', value: `$${formatCost(saved)}`, icon: <TrendingDown size={18} />, color: '#3b82f6', borderColor: 'rgba(59,130,246,0.3)', accent: false, subtext: saved > 0 && withoutProxy > 0 ? `${((saved / withoutProxy) * 100).toFixed(0)}% reduction` : 'original − compressed' },
    { label: 'Without Proxy', value: `$${formatCost(withoutProxy)}`, icon: <TrendingUp size={18} />, color: '#f59e0b', borderColor: 'rgba(245,158,11,0.3)', accent: false, subtext: 'what you would have paid' },
  ]

  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 16 }}>
      {cards.map((c) => (
        <div key={c.label} style={{
          background: 'rgba(26,26,26,0.8)',
          backdropFilter: 'blur(12px)',
          border: `1px solid ${c.borderColor}`,
          borderRadius: 16,
          padding: 24,
          position: 'relative',
          overflow: 'hidden',
        }}>
          {c.accent && <div style={{ position: 'absolute', top: 0, left: 0, right: 0, height: 2, background: 'linear-gradient(90deg, #16a34a, #22c55e)' }} />}
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
            <span style={{ fontSize: 12, fontWeight: 600, color: '#6b7280', textTransform: 'uppercase', letterSpacing: '0.06em' }}>{c.label}</span>
            <div style={{ width: 36, height: 36, borderRadius: 10, background: `${c.color}1f`, display: 'flex', alignItems: 'center', justifyContent: 'center', color: c.color }}>
              {c.icon}
            </div>
          </div>
          <div style={{ fontFamily: "'JetBrains Mono', monospace", fontSize: 30, fontWeight: 700, lineHeight: 1, color: c.accent ? '#22c55e' : '#ffffff' }}>
            {c.value}
          </div>
          <div style={{ fontSize: 11, color: '#4b5563', marginTop: 8 }}>{c.subtext}</div>
        </div>
      ))}
    </div>
  )
}

function DetailedStatsCard({ savings, expand }: { savings?: Savings; expand?: ExpandContext }) {
  const expandTotal = expand?.total ?? 0
  const compressedOutputs = savings?.compressed_requests ?? 0
  const totalRequests = savings?.total_requests ?? 0
  const hasToolDiscovery = savings?.tool_discovery_requests && savings.tool_discovery_requests > 0
  const toolsFiltered = hasToolDiscovery ? ((savings?.original_tool_count ?? 0) - (savings?.filtered_tool_count ?? 0)) : 0
  const compressionRatio = savings?.compression_ratio ?? 0

  const stats = [
    { label: 'Expand Context Calls', value: String(expandTotal), color: '#f97316', subtext: expandTotal > 0 ? `${expand?.found ?? 0} found, ${expand?.not_found ?? 0} missed` : 'none yet' },
    { label: 'Compressed Outputs', value: `${compressedOutputs} / ${totalRequests}`, color: '#3b82f6', subtext: totalRequests > 0 ? `${((compressedOutputs / totalRequests) * 100).toFixed(0)}% of requests` : 'no requests' },
    { label: 'Tools Filtered', value: hasToolDiscovery ? `${toolsFiltered}` : '0', color: '#a78bfa', subtext: hasToolDiscovery ? `${savings?.original_tool_count ?? 0} → ${savings?.filtered_tool_count ?? 0} tools` : 'none yet' },
  ]

  return (
    <div style={{
      background: 'rgba(26,26,26,0.8)',
      backdropFilter: 'blur(12px)',
      border: '1px solid rgba(167,139,250,0.2)',
      borderRadius: 16,
      overflow: 'hidden',
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '20px 24px', borderBottom: '1px solid rgba(255,255,255,0.1)' }}>
        <div style={{ width: 36, height: 36, borderRadius: 10, background: 'rgba(167,139,250,0.12)', display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#a78bfa' }}>
          <BarChart3 size={18} />
        </div>
        <span style={{ fontSize: 14, fontWeight: 600, color: '#ffffff' }}>Detailed Stats</span>
        {compressionRatio > 0 && (
          <span style={{ marginLeft: 'auto', fontFamily: "'JetBrains Mono', monospace", fontSize: 11, color: '#22c55e', background: 'rgba(34,197,94,0.1)', padding: '4px 10px', borderRadius: 20 }}>
            {compressionRatio.toFixed(1)}x compression ratio
          </span>
        )}
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', borderTop: '1px solid rgba(255,255,255,0.05)' }}>
        {stats.map((s, i) => (
          <div key={s.label} style={{
            padding: '20px 24px',
            borderRight: i < stats.length - 1 ? '1px solid rgba(255,255,255,0.05)' : 'none',
            textAlign: 'center',
          }}>
            <div style={{ fontFamily: "'JetBrains Mono', monospace", fontSize: 22, fontWeight: 700, color: s.color, marginBottom: 4 }}>
              {s.value}
            </div>
            <div style={{ fontSize: 11, color: '#6b7280', textTransform: 'uppercase', letterSpacing: '0.05em' }}>{s.label}</div>
            <div style={{ fontSize: 10, color: '#4b5563', marginTop: 2 }}>{s.subtext}</div>
          </div>
        ))}
      </div>
    </div>
  )
}


function AccountCard({ account }: { account: AccountData }) {
  if (!account.available) {
    return (
      <div style={{
        background: 'rgba(26,26,26,0.8)',
        backdropFilter: 'blur(12px)',
        border: '1px solid rgba(255,255,255,0.1)',
        borderRadius: 16,
        padding: 24,
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 8 }}>
          <div style={{ width: 36, height: 36, borderRadius: 10, background: 'rgba(107,114,128,0.12)', display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#6b7280' }}>
            <Wallet size={18} />
          </div>
          <span style={{ fontSize: 14, fontWeight: 600, color: '#ffffff' }}>Compresr Account</span>
        </div>
        <div style={{ color: '#6b7280', fontSize: 13, padding: '16px 0' }}>
          {account.error || 'Account data not available'}
        </div>
      </div>
    )
  }

  const tierColors: Record<string, string> = {
    free: '#6b7280',
    pro: '#3b82f6',
    business: '#a78bfa',
    enterprise: '#f59e0b',
  }
  const tierColor = tierColors[account.tier || 'free'] || '#6b7280'
  
  const balanceColor = account.credits_remaining_usd < 1 ? '#ef4444' : 
                       account.credits_remaining_usd < 5 ? '#eab308' : '#22c55e'

  return (
    <div style={{
      background: 'rgba(26,26,26,0.8)',
      backdropFilter: 'blur(12px)',
      border: '1px solid rgba(59,130,246,0.2)',
      borderRadius: 16,
      overflow: 'hidden',
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '20px 24px', borderBottom: '1px solid rgba(255,255,255,0.1)' }}>
        <div style={{ width: 36, height: 36, borderRadius: 10, background: 'rgba(59,130,246,0.12)', display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#3b82f6' }}>
          <Wallet size={18} />
        </div>
        <span style={{ fontSize: 14, fontWeight: 600, color: '#ffffff' }}>Compresr Account</span>
        <span style={{ marginLeft: 'auto', fontFamily: "'JetBrains Mono', monospace", fontSize: 11, color: tierColor, background: `${tierColor}1f`, padding: '4px 12px', borderRadius: 20, textTransform: 'capitalize', fontWeight: 600 }}>
          {account.tier || 'free'}{account.is_admin ? ' (Admin)' : ''}
        </span>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', borderTop: '1px solid rgba(255,255,255,0.05)' }}>
        <div style={{ padding: '20px 24px', borderRight: '1px solid rgba(255,255,255,0.05)', textAlign: 'center' }}>
          <div style={{ fontFamily: "'JetBrains Mono', monospace", fontSize: 22, fontWeight: 700, color: balanceColor, marginBottom: 4 }}>
            ${account.credits_remaining_usd.toFixed(2)}
          </div>
          <div style={{ fontSize: 11, color: '#6b7280', textTransform: 'uppercase', letterSpacing: '0.05em' }}>Balance</div>
        </div>
        <div style={{ padding: '20px 24px', borderRight: '1px solid rgba(255,255,255,0.05)', textAlign: 'center' }}>
          <div style={{ fontFamily: "'JetBrains Mono', monospace", fontSize: 22, fontWeight: 700, color: '#ffffff', marginBottom: 4 }}>
            ${account.credits_used_this_month.toFixed(2)}
          </div>
          <div style={{ fontSize: 11, color: '#6b7280', textTransform: 'uppercase', letterSpacing: '0.05em' }}>Used This Month</div>
        </div>
        <div style={{ padding: '20px 24px', borderRight: '1px solid rgba(255,255,255,0.05)', textAlign: 'center' }}>
          <div style={{ fontFamily: "'JetBrains Mono', monospace", fontSize: 22, fontWeight: 700, color: '#a78bfa', marginBottom: 4 }}>
            {account.requests_today}
          </div>
          <div style={{ fontSize: 11, color: '#6b7280', textTransform: 'uppercase', letterSpacing: '0.05em' }}>Requests Today</div>
        </div>
        <div style={{ padding: '20px 24px', textAlign: 'center' }}>
          <div style={{ fontFamily: "'JetBrains Mono', monospace", fontSize: 22, fontWeight: 700, color: '#ffffff', marginBottom: 4 }}>
            {account.requests_this_month}
          </div>
          <div style={{ fontSize: 11, color: '#6b7280', textTransform: 'uppercase', letterSpacing: '0.05em' }}>This Month</div>
        </div>
      </div>
      {account.monthly_budget_usd > 0 && (
        <div style={{ padding: '12px 24px', background: 'rgba(0,0,0,0.2)', borderTop: '1px solid rgba(255,255,255,0.05)' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
            <div style={{ flex: 1, height: 6, background: '#2a2a2a', borderRadius: 3, overflow: 'hidden' }}>
              <div style={{ 
                height: '100%', 
                width: `${Math.min(account.usage_percent, 100)}%`, 
                background: account.usage_percent > 90 ? '#ef4444' : account.usage_percent > 70 ? '#eab308' : '#22c55e',
                borderRadius: 3,
                transition: 'width 0.5s ease'
              }} />
            </div>
            <span style={{ fontFamily: "'JetBrains Mono', monospace", fontSize: 11, color: '#6b7280', minWidth: 80 }}>
              {account.usage_percent.toFixed(0)}% of ${account.monthly_budget_usd.toFixed(0)}
            </span>
          </div>
        </div>
      )}
    </div>
  )
}

function ExpandContextCard({ expand }: { expand: ExpandContext }) {
  if (expand.total === 0) {
    return (
      <div style={{
        background: 'rgba(26,26,26,0.8)',
        backdropFilter: 'blur(12px)',
        border: '1px solid rgba(255,255,255,0.1)',
        borderRadius: 16,
        padding: 24,
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
          <div style={{ width: 36, height: 36, borderRadius: 10, background: 'rgba(249,115,22,0.12)', display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#f97316' }}>
            <Search size={18} />
          </div>
          <span style={{ fontSize: 14, fontWeight: 600, color: '#ffffff' }}>Context Expansions</span>
        </div>
        <div style={{ color: '#6b7280', fontSize: 13, textAlign: 'center', padding: '24px 0' }}>
          No expand_context calls yet. When the model requests full content for a compressed output, it appears here.
        </div>
      </div>
    )
  }

  return (
    <div style={{
      background: 'rgba(26,26,26,0.8)',
      backdropFilter: 'blur(12px)',
      border: '1px solid rgba(249,115,22,0.2)',
      borderRadius: 16,
      overflow: 'hidden',
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '20px 24px', borderBottom: '1px solid rgba(255,255,255,0.1)' }}>
        <div style={{ width: 36, height: 36, borderRadius: 10, background: 'rgba(249,115,22,0.12)', display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#f97316' }}>
          <Search size={18} />
        </div>
        <span style={{ fontSize: 14, fontWeight: 600, color: '#ffffff' }}>Context Expansions</span>
        <div style={{ marginLeft: 'auto', display: 'flex', gap: 12 }}>
          <span style={{ fontFamily: "'JetBrains Mono', monospace", fontSize: 11, color: '#22c55e', background: 'rgba(34,197,94,0.1)', padding: '4px 10px', borderRadius: 20 }}>
            {expand.found} found
          </span>
          {expand.not_found > 0 && (
            <span style={{ fontFamily: "'JetBrains Mono', monospace", fontSize: 11, color: '#ef4444', background: 'rgba(239,68,68,0.1)', padding: '4px 10px', borderRadius: 20 }}>
              {expand.not_found} not found
            </span>
          )}
        </div>
      </div>
      {expand.recent && expand.recent.length > 0 && (
        <div style={{ overflowX: 'auto' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse', minWidth: 600 }}>
            <thead>
              <tr>
                {['Time', 'Shadow ID', 'Status', 'Content'].map((h) => (
                  <th key={h} style={{ textAlign: 'left', padding: '12px 24px', fontSize: 11, fontWeight: 600, color: '#6b7280', textTransform: 'uppercase', letterSpacing: '0.06em', background: 'rgba(255,255,255,0.015)', borderBottom: '1px solid rgba(255,255,255,0.1)' }}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {expand.recent.slice(0, 10).map((e, i) => (
                <tr key={i}>
                  <td style={{ padding: '12px 24px', borderBottom: '1px solid rgba(255,255,255,0.05)', color: '#6b7280', fontSize: 12 }}>
                    {timeAgo(e.timestamp)}
                  </td>
                  <td style={{ padding: '12px 24px', borderBottom: '1px solid rgba(255,255,255,0.05)', fontFamily: "'JetBrains Mono', monospace", fontSize: 11, color: '#a78bfa' }}>
                    {e.shadow_id.length > 16 ? e.shadow_id.slice(0, 16) + '...' : e.shadow_id}
                  </td>
                  <td style={{ padding: '12px 24px', borderBottom: '1px solid rgba(255,255,255,0.05)' }}>
                    {e.found ? (
                      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, color: '#22c55e', fontSize: 12 }}>
                        <CheckCircle size={14} /> found
                      </span>
                    ) : (
                      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, color: '#ef4444', fontSize: 12 }}>
                        <XCircle size={14} /> not found
                      </span>
                    )}
                  </td>
                  <td style={{ padding: '12px 24px', borderBottom: '1px solid rgba(255,255,255,0.05)', color: '#9ca3af', fontSize: 12, maxWidth: 300, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {e.content_preview || `${e.content_length} chars`}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function SessionsTable({ sessions, sessionCap }: { sessions: Session[]; sessionCap: number }) {
  if (sessions.length === 0) {
    return (
      <div style={{ background: 'rgba(26,26,26,0.8)', backdropFilter: 'blur(12px)', border: '1px solid rgba(255,255,255,0.1)', borderRadius: 16, textAlign: 'center', padding: '72px 24px' }}>
        <div style={{ fontSize: 24, marginBottom: 16 }}>&#128640;</div>
        <div style={{ fontSize: 15, fontWeight: 600, color: '#9ca3af', marginBottom: 6 }}>No sessions yet</div>
        <div style={{ fontSize: 13, color: '#6b7280', maxWidth: 280, margin: '0 auto', lineHeight: 1.6 }}>
          Requests will appear here as they are processed through the gateway.
        </div>
      </div>
    )
  }
  const sorted = [...sessions].sort((a, b) => new Date(b.last_updated).getTime() - new Date(a.last_updated).getTime())
  return (
    <div style={{ background: 'rgba(26,26,26,0.8)', backdropFilter: 'blur(12px)', border: '1px solid rgba(255,255,255,0.1)', borderRadius: 16, overflow: 'hidden' }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '20px 24px', borderBottom: '1px solid rgba(255,255,255,0.1)' }}>
        <span style={{ fontSize: 15, fontWeight: 700 }}>Active Sessions</span>
        <span style={{ fontFamily: "'JetBrains Mono', monospace", fontSize: 11, color: '#6b7280', background: '#2a2a2a', padding: '4px 10px', borderRadius: 20 }}>
          {sessions.length} session{sessions.length !== 1 ? 's' : ''}
        </span>
      </div>
      <div style={{ overflowX: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', minWidth: 600 }}>
          <thead>
            <tr>
              {['Session', 'Model', 'Requests', 'Cost', ...(sessionCap > 0 ? ['Budget'] : []), 'Last Activity'].map((h) => (
                <th key={h} style={{ textAlign: 'left', padding: '14px 24px', fontSize: 11, fontWeight: 600, color: '#6b7280', textTransform: 'uppercase', letterSpacing: '0.06em', background: 'rgba(255,255,255,0.015)', borderBottom: '1px solid rgba(255,255,255,0.1)' }}>{h}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {sorted.map((s) => {
              const pct = sessionCap > 0 ? Math.min((s.cost / sessionCap) * 100, 100) : 0
              const barColor = pct > 90 ? '#ef4444' : pct > 70 ? '#eab308' : '#22c55e'
              const sid = s.id.length > 12 ? s.id.slice(0, 12) + '...' : s.id
              return (
                <tr key={s.id}>
                  <td style={{ padding: '16px 24px', borderBottom: '1px solid rgba(255,255,255,0.05)', fontFamily: "'JetBrains Mono', monospace", fontSize: 12, color: '#22c55e', fontWeight: 500 }}>{sid}</td>
                  <td style={{ padding: '16px 24px', borderBottom: '1px solid rgba(255,255,255,0.05)' }}>
                    <span style={{ fontFamily: "'JetBrains Mono', monospace", fontSize: 11, color: '#a78bfa', background: 'rgba(167,139,250,0.08)', padding: '3px 10px', borderRadius: 20, border: '1px solid rgba(167,139,250,0.15)' }}>{s.model}</span>
                  </td>
                  <td style={{ padding: '16px 24px', borderBottom: '1px solid rgba(255,255,255,0.05)', fontFamily: "'JetBrains Mono', monospace", fontWeight: 600, color: '#9ca3af' }}>{s.request_count}</td>
                  <td style={{ padding: '16px 24px', borderBottom: '1px solid rgba(255,255,255,0.05)', fontFamily: "'JetBrains Mono', monospace", fontSize: 14, fontWeight: 700 }}>${s.cost.toFixed(4)}</td>
                  {sessionCap > 0 && (
                    <td style={{ padding: '16px 24px', borderBottom: '1px solid rgba(255,255,255,0.05)' }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                        <div style={{ width: 80, height: 6, background: '#2a2a2a', borderRadius: 3, overflow: 'hidden' }}>
                          <div style={{ height: '100%', borderRadius: 3, width: `${pct}%`, background: barColor, transition: 'width 0.6s ease' }} />
                        </div>
                        <span style={{ fontFamily: "'JetBrains Mono', monospace", fontSize: 11, color: '#6b7280', minWidth: 32 }}>{pct.toFixed(0)}%</span>
                      </div>
                    </td>
                  )}
                  <td style={{ padding: '16px 24px', borderBottom: '1px solid rgba(255,255,255,0.05)', color: '#6b7280', fontSize: 12 }}>{timeAgo(s.last_updated)}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function Dashboard() {
  const [data, setData] = useState<DashboardData | null>(null)
  const [account, setAccount] = useState<AccountData | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    const fetchData = async () => {
      try {
        const [dashRes, accRes] = await Promise.all([
          fetch('/api/dashboard'),
          fetch('/api/account'),
        ])
        if (!dashRes.ok) { setError(`API returned ${dashRes.status}`); return }
        const dashJson = await dashRes.json()
        setData(dashJson)
        if (accRes.ok) {
          const accJson = await accRes.json()
          setAccount(accJson)
        }
        setError(null)
      } catch (e) {
        setError(String(e))
      }
    }
    fetchData()
    const interval = setInterval(fetchData, 5000)
    return () => clearInterval(interval)
  }, [])

  return (
    <div style={{
      minHeight: '100vh',
      background: '#0a0a0a',
      backgroundImage: 'linear-gradient(to right, rgba(128,128,128,0.03) 1px, transparent 1px), linear-gradient(to bottom, rgba(128,128,128,0.03) 1px, transparent 1px)',
      backgroundSize: '32px 32px',
      color: '#ffffff',
      fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
    }}>
      <div style={{ maxWidth: 1000, margin: '0 auto', padding: '48px 32px' }}>
        {/* Header */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 16, marginBottom: 40 }}>
          <div style={{
            width: 44, height: 44, borderRadius: 14,
            background: 'linear-gradient(135deg, #16a34a, #22c55e)',
            display: 'flex', alignItems: 'center', justifyContent: 'center',
            boxShadow: '0 0 60px rgba(22,163,74,0.12)',
          }}>
            <Zap size={22} color="#fff" />
          </div>
          <div>
            <h1 style={{ fontSize: 22, fontWeight: 800, letterSpacing: '-0.02em', color: '#ffffff' }}>
              Context Gateway
            </h1>
            <p style={{ fontSize: 13, color: '#6b7280', marginTop: 2 }}>Cost &amp; Usage Dashboard</p>
          </div>
          <div style={{
            marginLeft: 'auto', fontFamily: "'JetBrains Mono', monospace", fontSize: 11, color: '#6b7280',
            background: '#1a1a1a', border: '1px solid rgba(255,255,255,0.1)', padding: '6px 14px', borderRadius: 20,
            display: 'flex', alignItems: 'center', gap: 8,
          }}>
            <span style={{ width: 7, height: 7, background: '#22c55e', borderRadius: '50%' }} />
            Live
          </div>
        </div>

        {error && (
          <div style={{ color: '#ef4444', padding: 16, background: '#1a1a1a', borderRadius: 12, marginBottom: 16, fontFamily: 'monospace', fontSize: 13 }}>
            Error: {error}
          </div>
        )}

        {!data && !error && (
          <div style={{ color: '#9ca3af', textAlign: 'center', padding: 48, fontSize: 14 }}>
            Loading dashboard...
          </div>
        )}

        {data && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 28 }}>
            {/* Row 1: Cost breakdown */}
            <CostRow savings={data.savings} />
            {/* Row 2: Account balance */}
            {account && <AccountCard account={account} />}
            {/* Row 3: Detailed stats */}
            <DetailedStatsCard savings={data.savings} expand={data.expand} />
            {data.expand && data.expand.total > 0 && <ExpandContextCard expand={data.expand} />}
            <SessionsTable sessions={data.sessions ?? []} sessionCap={data.enabled ? data.session_cap : 0} />
          </div>
        )}
      </div>

      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 8, paddingBottom: 32, fontSize: 12, color: '#6b7280' }}>
        Auto-refreshes every 5s
      </div>
    </div>
  )
}

function App() {
  return (
    <ErrorBoundary>
      <Dashboard />
    </ErrorBoundary>
  )
}

export default App
