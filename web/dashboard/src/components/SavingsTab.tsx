import { useState } from 'react'
import { DollarSign, Layers, Activity, Radio } from 'lucide-react'
import type { DashboardData, Savings, Session } from '../types'
import CustomSelect from './CustomSelect'

interface SavingsTabProps {
  data: DashboardData | null
  error: string | null
  onSessionChange: (sessionId: string) => void
  selectedSession: string
}

function formatCost(v: number): string {
  return v >= 1 ? v.toFixed(2) : v.toFixed(4)
}

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}

function formatSessionLabel(session: Session): string {
  const name = session.agent_name || session.model || 'unknown'
  const date = new Date(session.created_at)
  const timeStr = date.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
  return `${name} · ${timeStr} · $${formatCost(session.cost)} · ${session.request_count} reqs`
}

function timeAgo(dateStr: string): string {
  const now = Date.now()
  const then = new Date(dateStr).getTime()
  const diffMs = now - then
  const diffMin = Math.floor(diffMs / 60000)
  if (diffMin < 1) return 'just now'
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHr = Math.floor(diffMin / 60)
  if (diffHr < 24) return `${diffHr}h ago`
  const diffDay = Math.floor(diffHr / 24)
  return `${diffDay}d ago`
}

// Summary cards row showing totals
function SummaryCards({ savings, totalCost }: { savings?: Savings; totalCost: number }) {
  const totalSpend = (savings?.billed_spend_usd != null && savings.billed_spend_usd > 0) ? savings.billed_spend_usd : totalCost
  const tokensSaved = savings?.tokens_saved ?? 0

  const cards = [
    {
      label: 'Total Spending',
      value: `$${formatCost(totalSpend)}`,
      icon: <DollarSign size={20} />,
      color: '#22c55e',
      glowColor: 'rgba(34,197,94,0.15)',
      borderColor: 'rgba(34,197,94,0.3)',
      accent: true,
      subtext: 'actual API cost across all sessions',
    },
    {
      label: 'Tokens Compressed',
      value: formatTokens(tokensSaved),
      icon: <Layers size={20} />,
      color: '#a78bfa',
      glowColor: 'rgba(167,139,250,0.12)',
      borderColor: 'rgba(167,139,250,0.3)',
      accent: false,
      subtext: tokensSaved > 0
        ? `${savings?.token_saved_pct?.toFixed(0) ?? 0}% of input removed`
        : 'tokens removed by compression',
    },
  ]

  return (
    <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }}>
      {cards.map((c) => (
        <div
          key={c.label}
          style={{
            background: c.accent
              ? 'linear-gradient(135deg, rgba(17,17,17,0.9) 0%, rgba(22,163,74,0.06) 100%)'
              : 'rgba(17,17,17,0.9)',
            backdropFilter: 'blur(12px)',
            border: `1px solid ${c.borderColor}`,
            borderRadius: 16,
            padding: 28,
            position: 'relative',
            overflow: 'hidden',
          }}
        >
          {c.accent && (
            <div style={{ position: 'absolute', top: 0, left: 0, right: 0, height: 2, background: 'linear-gradient(90deg, #16a34a, #22c55e, #4ade80)' }} />
          )}
          <div style={{ position: 'absolute', top: -40, right: -40, width: 120, height: 120, borderRadius: '50%', background: c.glowColor, filter: 'blur(40px)', pointerEvents: 'none' }} />
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 18, position: 'relative' }}>
            <span style={{ fontSize: 11, fontWeight: 600, color: '#6b7280', textTransform: 'uppercase', letterSpacing: '0.08em', fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
              {c.label}
            </span>
            <div style={{ width: 40, height: 40, borderRadius: 12, background: `${c.color}15`, border: `1px solid ${c.color}25`, display: 'flex', alignItems: 'center', justifyContent: 'center', color: c.color, boxShadow: `0 0 20px ${c.glowColor}` }}>
              {c.icon}
            </div>
          </div>
          <div style={{ fontFamily: "'JetBrains Mono', monospace", fontSize: 36, fontWeight: 700, lineHeight: 1, color: c.accent ? '#22c55e' : '#f3f4f6', position: 'relative', paddingBottom: 10 }}>
            {c.value}
            <div style={{ position: 'absolute', bottom: 0, left: 0, width: 48, height: 2, borderRadius: 1, background: c.accent ? 'linear-gradient(90deg, #22c55e, #4ade80)' : `linear-gradient(90deg, ${c.color}, ${c.color}80)`, opacity: 0.6 }} />
          </div>
          <div style={{ fontSize: 11, color: '#4b5563', marginTop: 10, fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
            {c.subtext}
          </div>
        </div>
      ))}
    </div>
  )
}

// Individual session card
function SessionCard({ session }: { session: Session }) {
  const isActive = session.active ?? false

  return (
    <div
      style={{
        background: 'rgba(17,17,17,0.9)',
        backdropFilter: 'blur(12px)',
        border: `1px solid ${isActive ? 'rgba(34,197,94,0.2)' : 'rgba(255,255,255,0.06)'}`,
        borderRadius: 14,
        padding: '18px 20px',
        position: 'relative',
        overflow: 'hidden',
        transition: 'border-color 0.2s ease',
      }}
    >
      {/* Active indicator bar */}
      {isActive && (
        <div style={{ position: 'absolute', top: 0, left: 0, right: 0, height: 2, background: 'linear-gradient(90deg, #16a34a, #22c55e)' }} />
      )}

      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 12 }}>
        {/* Status dot */}
        <div style={{
          width: 8, height: 8, borderRadius: '50%',
          background: isActive ? '#22c55e' : '#4b5563',
          boxShadow: isActive ? '0 0 8px rgba(34,197,94,0.4)' : 'none',
          flexShrink: 0,
        }} />

        {/* Agent name */}
        {session.agent_name && (
          <span style={{
            fontSize: 13, fontWeight: 600, color: '#f3f4f6',
            fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
          }}>
            {session.agent_name}
          </span>
        )}

        {/* Model pill */}
        <span style={{
          fontSize: 12, fontWeight: 600, color: '#a78bfa',
          background: 'rgba(167,139,250,0.1)', border: '1px solid rgba(167,139,250,0.2)',
          padding: '2px 10px', borderRadius: 20,
          fontFamily: "'JetBrains Mono', monospace",
        }}>
          {session.model || 'unknown'}
        </span>

        {/* Port indicator */}
        {session.gateway_port && (
          <span style={{
            fontSize: 11, color: '#6b7280',
            fontFamily: "'JetBrains Mono', monospace",
          }}>
            :{session.gateway_port}
          </span>
        )}

        {/* Timestamp */}
        <span style={{
          fontSize: 11, color: '#4b5563', marginLeft: 'auto',
          fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
        }}>
          {timeAgo(session.last_updated)}
        </span>
      </div>

      {/* Metrics row */}
      <div style={{ display: 'flex', gap: 24, alignItems: 'baseline' }}>
        <div>
          <span style={{
            fontFamily: "'JetBrains Mono', monospace",
            fontSize: 22, fontWeight: 700, color: '#22c55e',
          }}>
            ${formatCost(session.cost)}
          </span>
          <span style={{ fontSize: 11, color: '#4b5563', marginLeft: 6 }}>spent</span>
        </div>
        <div>
          <span style={{
            fontFamily: "'JetBrains Mono', monospace",
            fontSize: 16, fontWeight: 600, color: '#e5e7eb',
          }}>
            {session.request_count}
          </span>
          <span style={{ fontSize: 11, color: '#4b5563', marginLeft: 6 }}>requests</span>
        </div>
        {session.cap > 0 && (
          <div style={{ marginLeft: 'auto' }}>
            <span style={{ fontSize: 11, color: '#6b7280' }}>
              cap: ${formatCost(session.cap)}
            </span>
          </div>
        )}
      </div>
    </div>
  )
}

function SavingsTab({ data, error, onSessionChange, selectedSession }: SavingsTabProps) {
  const [showActiveOnly, setShowActiveOnly] = useState(false)

  if (error) {
    return (
      <div style={{ color: '#ef4444', padding: 16, background: '#111111', border: '1px solid rgba(239,68,68,0.2)', borderRadius: 12, marginBottom: 16, fontFamily: "'JetBrains Mono', monospace", fontSize: 13 }}>
        Error: {error}
      </div>
    )
  }

  if (!data) {
    return (
      <div style={{ color: '#9ca3af', textAlign: 'center', padding: 48, fontSize: 14, fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
        Loading...
      </div>
    )
  }

  // Only show the main agent session (first/primary), filter out sub-agent sessions
  const allSessions = data.sessions ?? []
  const sessions = allSessions.length > 0 ? [allSessions[0]] : []
  const activePorts = data.active_ports ?? []
  const filteredSessions = showActiveOnly ? sessions.filter(s => s.active) : sessions

  // For the "All Sessions" selector (used by the savings data API) - main agent only
  const sessionOptions = sessions

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
      {/* Session scope selector */}
      {sessionOptions.length > 0 && (
        <div style={{
          background: 'rgba(17,17,17,0.9)', border: '1px solid rgba(255,255,255,0.06)',
          borderRadius: 12, padding: '14px 18px',
          display: 'flex', alignItems: 'center', gap: 12,
        }}>
          <div style={{ width: 32, height: 32, borderRadius: 8, background: 'rgba(34,197,94,0.1)', border: '1px solid rgba(34,197,94,0.15)', display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#22c55e', flexShrink: 0 }}>
            <Activity size={15} />
          </div>
          <span style={{ fontSize: 11, fontWeight: 600, color: '#6b7280', textTransform: 'uppercase', letterSpacing: '0.08em', whiteSpace: 'nowrap', fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
            Scope
          </span>
          <CustomSelect
            value={selectedSession}
            onChange={onSessionChange}
            options={[
              { value: 'all', label: 'Main Agent Session' },
              ...sessionOptions.map((s) => ({ value: s.id, label: formatSessionLabel(s) })),
            ]}
            style={{ flex: 1 }}
          />
        </div>
      )}

      {/* Summary cards */}
      <SummaryCards savings={data.savings} totalCost={data.total_cost} />

      {/* Active gateways indicator */}
      {activePorts.length > 0 && (
        <div style={{
          display: 'flex', alignItems: 'center', gap: 8, padding: '0 4px',
        }}>
          <Radio size={12} style={{ color: '#22c55e' }} />
          <span style={{ fontSize: 11, color: '#6b7280', fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
            {activePorts.length} active gateway{activePorts.length !== 1 ? 's' : ''} on port{activePorts.length !== 1 ? 's' : ''} {activePorts.join(', ')}
          </span>
        </div>
      )}

      {/* Session cards header with active filter toggle */}
      {sessions.length > 0 && (
        <>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '0 4px' }}>
            <div style={{ height: 1, flex: 1, background: 'linear-gradient(90deg, rgba(255,255,255,0.06), transparent)' }} />
            <span style={{ fontSize: 12, color: '#6b7280', fontFamily: "'Inter', system-ui, -apple-system, sans-serif", whiteSpace: 'nowrap', fontWeight: 500 }}>
              {filteredSessions.length} session{filteredSessions.length !== 1 ? 's' : ''}
            </span>
            {sessions.some(s => s.active) && (
              <button
                onClick={() => setShowActiveOnly(!showActiveOnly)}
                style={{
                  background: showActiveOnly ? 'rgba(34,197,94,0.1)' : 'rgba(255,255,255,0.04)',
                  border: `1px solid ${showActiveOnly ? 'rgba(34,197,94,0.3)' : 'rgba(255,255,255,0.06)'}`,
                  borderRadius: 8, padding: '4px 12px', cursor: 'pointer',
                  color: showActiveOnly ? '#22c55e' : '#6b7280',
                  fontSize: 11, fontWeight: 500,
                  fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
                  transition: 'all 0.2s ease',
                  display: 'flex', alignItems: 'center', gap: 4,
                }}
              >
                <div style={{
                  width: 6, height: 6, borderRadius: '50%',
                  background: showActiveOnly ? '#22c55e' : '#6b7280',
                }} />
                Active only
              </button>
            )}
            <div style={{ height: 1, flex: 1, background: 'linear-gradient(90deg, transparent, rgba(255,255,255,0.06))' }} />
          </div>

          {/* Session cards list */}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {filteredSessions.map((s) => (
              <SessionCard key={`${s.id}-${s.gateway_port ?? 0}`} session={s} />
            ))}
          </div>
        </>
      )}

      {/* Empty state */}
      {sessions.length === 0 && !data.savings && (
        <div style={{ color: '#4b5563', textAlign: 'center', padding: 48, fontSize: 14, fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
          No sessions yet. Start using the gateway to see savings data here.
        </div>
      )}
    </div>
  )
}

export default SavingsTab
