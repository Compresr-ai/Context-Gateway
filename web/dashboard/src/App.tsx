import { useState, useEffect, Component, type ReactNode } from 'react'
import { Zap, DollarSign, Layers } from 'lucide-react'
import type { DashboardData, Savings } from './types'

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

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}

function CostRow({ savings, totalCost }: { savings?: Savings; totalCost: number }) {
  const totalSpend = (savings?.billed_spend_usd != null && savings.billed_spend_usd > 0) ? savings.billed_spend_usd : totalCost
  const tokensSaved = savings?.tokens_saved ?? 0
  const compressedReqs = savings?.compressed_requests ?? 0
  const totalReqs = savings?.total_requests ?? 0

  const cards = [
    { label: 'Total Spending', value: `$${formatCost(totalSpend)}`, icon: <DollarSign size={18} />, color: '#22c55e', borderColor: 'rgba(22,163,74,0.3)', accent: true, subtext: 'actual API cost' },
    { label: 'Tokens Compressed', value: formatTokens(tokensSaved), icon: <Layers size={18} />, color: '#a78bfa', borderColor: 'rgba(167,139,250,0.3)', accent: false, subtext: tokensSaved > 0 ? `${savings?.token_saved_pct?.toFixed(0) ?? 0}% of input removed` : 'tokens removed by compression' },
  ]

  return (
    <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }}>
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
      {totalReqs > 0 && (
        <div style={{ gridColumn: '1 / -1', textAlign: 'center', fontSize: 12, color: '#4b5563', paddingTop: 4 }}>
          {compressedReqs} compressed / {totalReqs} total requests
        </div>
      )}
    </div>
  )
}

function Dashboard() {
  const [data, setData] = useState<DashboardData | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    const fetchData = async () => {
      try {
        const dashRes = await fetch('/api/dashboard')
        if (!dashRes.ok) { setError(`API returned ${dashRes.status}`); return }
        const dashJson = await dashRes.json()
        setData(dashJson)
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
      display: 'flex',
      flexDirection: 'column',
      justifyContent: 'center',
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
            <p style={{ fontSize: 13, color: '#6b7280', marginTop: 2 }}>Compression Savings</p>
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
            Loading...
          </div>
        )}

        {data && (
          <CostRow savings={data.savings} totalCost={data.total_cost} />
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
