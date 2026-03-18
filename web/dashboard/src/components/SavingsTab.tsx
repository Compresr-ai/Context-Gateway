import { useState } from 'react'
import { DollarSign, Layers, Activity, Radio, Search, X, Trash2, TrendingDown, ChevronDown, ChevronUp, ChevronRight } from 'lucide-react'
import type { DashboardData, Savings, Session } from '../types'

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

function timeAgo(dateStr: string): string {
  if (!dateStr) return ''
  const then = new Date(dateStr).getTime()
  if (isNaN(then)) return ''
  const diffMin = Math.floor((Date.now() - then) / 60000)
  if (diffMin < 1) return 'just now'
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHr = Math.floor(diffMin / 60)
  if (diffHr < 24) return `${diffHr}h ago`
  return `${Math.floor(diffHr / 24)}d ago`
}

// Summary cards row showing totals or per-session data
function SummaryCards({ savings, totalCost, isScoped, sessionCount }: { savings?: Savings; totalCost: number; isScoped: boolean; sessionCount: number }) {
  const totalSpend = isScoped ? totalCost : (savings?.billed_spend_usd ?? totalCost)
  const tokensSaved = (savings?.tokens_saved ?? 0) + (savings?.tool_discovery_tokens ?? 0)
  // cost_saved_usd is the total (tool output + tool discovery + preemptive summarization)
  // DON'T add tool_discovery_cost_usd - it's already included in cost_saved_usd
  const costSaved = savings?.cost_saved_usd ?? 0
  const sessionLabel = sessionCount === 1 ? '1 session' : `${sessionCount} sessions`

  const originalCost = totalSpend + costSaved
  const savedPct = originalCost > 0 ? Math.round((costSaved / originalCost) * 100) : 0

  const compressedReqs = savings?.compressed_requests ?? 0
  const discoverReqs = savings?.tool_discovery_requests ?? 0
  const totalCompressed = compressedReqs + discoverReqs

  const cards = [
    {
      label: isScoped ? 'Session Spending' : 'Total Spending',
      value: `$${formatCost(totalSpend)}`,
      icon: <DollarSign size={20} />,
      color: '#22c55e',
      glowColor: 'rgba(34,197,94,0.15)',
      borderColor: 'rgba(34,197,94,0.3)',
      accent: true,
      subtext: isScoped ? 'session API cost' : 'actual API cost',
    },
    {
      label: 'Total Saved',
      value: `$${formatCost(costSaved)}`,
      icon: <TrendingDown size={20} />,
      color: '#34d399',
      glowColor: 'rgba(52,211,153,0.12)',
      borderColor: 'rgba(52,211,153,0.3)',
      accent: false,
      subtext: costSaved > 0
        ? `${savedPct}% of cost eliminated · ${sessionLabel}`
        : 'no savings yet',
    },
    {
      label: 'Tokens Saved',
      value: formatTokens(tokensSaved),
      icon: <Layers size={20} />,
      color: '#a78bfa',
      glowColor: 'rgba(167,139,250,0.12)',
      borderColor: 'rgba(167,139,250,0.3)',
      accent: false,
      subtext: totalCompressed > 0
        ? `${totalCompressed} requests compressed`
        : 'no compressions yet',
    },
  ]

  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 16 }}>
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

// Savings detail row used inside expanded session card
function SavingsDetailRow({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', padding: '6px 0', borderBottom: '1px solid rgba(255,255,255,0.04)' }}>
      <span style={{ fontSize: 11, color: '#6b7280', fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>{label}</span>
      <div style={{ textAlign: 'right' }}>
        <span style={{ fontSize: 13, fontWeight: 600, color: '#e5e7eb', fontFamily: "'JetBrains Mono', monospace" }}>{value}</span>
        {sub && <span style={{ fontSize: 10, color: '#4b5563', marginLeft: 6, fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>{sub}</span>}
      </div>
    </div>
  )
}

// Individual session card — clickable, expands when selected
function SessionCard({
  session,
  isSelected,
  onClick,
  savings,
  onDelete,
}: {
  session: Session
  isSelected: boolean
  onClick: () => void
  savings?: Savings
  onDelete?: (sessionId: string) => void
}) {
  const isActive = session.active ?? false
  const [deleteHovered, setDeleteHovered] = useState(false)

  return (
    <div
      style={{
        background: isSelected
          ? 'linear-gradient(135deg, rgba(17,17,17,0.95) 0%, rgba(22,163,74,0.06) 100%)'
          : 'rgba(17,17,17,0.9)',
        backdropFilter: 'blur(12px)',
        border: `1px solid ${isSelected ? 'rgba(34,197,94,0.35)' : isActive ? 'rgba(34,197,94,0.2)' : 'rgba(255,255,255,0.06)'}`,
        borderRadius: 14,
        padding: '18px 20px',
        position: 'relative',
        overflow: 'hidden',
        transition: 'border-color 0.2s ease, background 0.2s ease',
      }}
    >
      {/* Top accent bar */}
      {(isActive || isSelected) && (
        <div style={{ position: 'absolute', top: 0, left: 0, right: 0, height: 2, background: isSelected ? 'linear-gradient(90deg, #16a34a, #22c55e, #4ade80)' : 'linear-gradient(90deg, #16a34a, #22c55e)' }} />
      )}

      {/* Header row — chevron + name only are clickable */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: isSelected ? 14 : 12 }}>
        {/* Chevron toggle — top-left expand/collapse trigger */}
        <button
          onClick={onClick}
          style={{
            background: 'transparent',
            border: 'none',
            cursor: 'pointer',
            padding: 0,
            display: 'flex',
            alignItems: 'center',
            color: '#6b7280',
            flexShrink: 0,
          }}
        >
          {isSelected ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
        </button>

        {/* Status dot */}
        <div style={{
          width: 8, height: 8, borderRadius: '50%',
          background: isActive ? '#22c55e' : '#4b5563',
          boxShadow: isActive ? '0 0 8px rgba(34,197,94,0.4)' : 'none',
          flexShrink: 0,
        }} />

        {/* Session name */}
        <span style={{ fontSize: 14, fontWeight: 700, color: '#f3f4f6', fontFamily: "'JetBrains Mono', monospace" }}>
          {session.id}
        </span>

        {/* Active/Ended badge */}
        <span style={{
          fontSize: 10, fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.06em',
          color: isActive ? '#22c55e' : '#6b7280',
          background: isActive ? 'rgba(34,197,94,0.1)' : 'rgba(255,255,255,0.04)',
          border: `1px solid ${isActive ? 'rgba(34,197,94,0.25)' : 'rgba(255,255,255,0.06)'}`,
          padding: '2px 8px', borderRadius: 6,
          fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
        }}>
          {isActive ? 'active' : 'ended'}
        </span>

        {/* Model pills - show all models used in session */}
        {session.models && session.models.length > 0 && session.models.map((model, idx) => (
          <span key={idx} style={{
            fontSize: 11, fontWeight: 600, color: '#a78bfa',
            background: 'rgba(167,139,250,0.1)', border: '1px solid rgba(167,139,250,0.2)',
            padding: '2px 10px', borderRadius: 20,
            fontFamily: "'JetBrains Mono', monospace",
          }}>
            {model}
          </span>
        ))}

        {/* Timestamp */}
        <span style={{ fontSize: 11, color: '#4b5563', marginLeft: 'auto', fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
          {timeAgo(session.last_updated || session.created_at)}
        </span>

        {/* Delete button — only for inactive sessions */}
        {!isActive && onDelete && (
          <button
            onClick={(e) => {
              e.stopPropagation()
              if (window.confirm(`Delete session "${session.id}"? This cannot be undone.`)) {
                onDelete(session.id)
              }
            }}
            onMouseEnter={() => setDeleteHovered(true)}
            onMouseLeave={() => setDeleteHovered(false)}
            title="Delete session"
            style={{
              background: deleteHovered ? 'rgba(239,68,68,0.12)' : 'transparent',
              border: 'none',
              cursor: 'pointer',
              padding: 6,
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              color: deleteHovered ? '#ef4444' : '#6b7280',
              transition: 'all 0.2s ease',
              borderRadius: 8,
              width: 30,
              height: 30,
            }}
          >
            <Trash2 size={14} />
          </button>
        )}
      </div>

      {/* Metrics row */}
      <div style={{ display: 'flex', gap: 24, alignItems: 'baseline' }}>
        <div>
          <span style={{ fontFamily: "'JetBrains Mono', monospace", fontSize: 22, fontWeight: 700, color: '#22c55e' }}>
            ${formatCost(session.cost)}
          </span>
          <span style={{ fontSize: 11, color: '#4b5563', marginLeft: 6 }}>spent</span>
        </div>
        <div>
          <span style={{ fontFamily: "'JetBrains Mono', monospace", fontSize: 16, fontWeight: 600, color: '#e5e7eb' }}>
            {session.request_count}
          </span>
          <span style={{ fontSize: 11, color: '#4b5563', marginLeft: 6 }}>API calls</span>
        </div>
        {session.cap > 0 && (
          <div style={{ marginLeft: 'auto' }}>
            <span style={{ fontSize: 11, color: '#6b7280' }}>cap: ${formatCost(session.cap)}</span>
          </div>
        )}
      </div>

      {/* Expanded detail — shown when this session is selected */}
      {isSelected && savings && (
        <div style={{ marginTop: 16, paddingTop: 16, borderTop: '1px solid rgba(255,255,255,0.06)', display: 'flex', flexDirection: 'column', gap: 14 }}>

          {/* API Calls */}
          <div>
            <div style={{ fontSize: 10, fontWeight: 600, color: '#6b7280', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 6, fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
              API Calls
            </div>
            <SavingsDetailRow label="Total requests" value={String(session.request_count)} />
            {(savings.user_turns ?? 0) > 0 && (
              <SavingsDetailRow label="User turns" value={String(savings.user_turns)} />
            )}
            {(savings.compaction_triggers ?? 0) > 0 && (
              <SavingsDetailRow label="Compactions" value={String(savings.compaction_triggers)} sub="history summarization" />
            )}
          </div>

          {/* Tool Output Compression */}
          <div>
            <div style={{ fontSize: 10, fontWeight: 600, color: '#a78bfa', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 6, fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
              Tool Output Compression
            </div>
            {savings.tokens_saved > 0 ? (
              <>
                <SavingsDetailRow label="Requests" value={String(savings.total_requests)} />
                <SavingsDetailRow
                  label="Compressed"
                  value={String(savings.compressed_requests)}
                  sub={savings.total_requests > 0 ? `${Math.round(savings.compressed_requests / savings.total_requests * 100)}%` : undefined}
                />
                <SavingsDetailRow
                  label="Tokens saved"
                  value={formatTokens(savings.tokens_saved)}
                  sub={savings.token_saved_pct > 0 ? `${savings.token_saved_pct.toFixed(0)}% of output` : undefined}
                />
              </>
            ) : (
              <div style={{ fontSize: 11, color: '#4b5563', padding: '4px 0', fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
                No savings — outputs were too small or ineffective
              </div>
            )}
          </div>

          {/* Tool Discovery */}
          <div>
            <div style={{ fontSize: 10, fontWeight: 600, color: '#38bdf8', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 6, fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
              Tool Discovery
            </div>
            {(savings.tool_discovery_requests ?? 0) > 0 ? (() => {
              const reqs = savings.tool_discovery_requests ?? 0
              const origTotal = savings.original_tool_count ?? 0
              const keptTotal = savings.filtered_tool_count ?? 0
              // These are cumulative sums — compute per-request averages
              const avgOrig = reqs > 0 ? Math.round(origTotal / reqs) : 0
              const avgKept = reqs > 0 ? Math.round(keptTotal / reqs) : 0
              const didFilter = avgOrig > avgKept
              const tdTokens = savings.tool_discovery_tokens ?? 0
              const searchCalls = savings.tool_search_calls ?? 0
              return (
                <>
                  <SavingsDetailRow label="Requests" value={String(reqs)} />
                  {didFilter && (
                    <SavingsDetailRow
                      label="Tools/request"
                      value={`${avgOrig} → ${avgKept}`}
                      sub="avg original → kept"
                    />
                  )}
                  {tdTokens > 0 ? (
                    <SavingsDetailRow
                      label="Tokens saved"
                      value={formatTokens(tdTokens)}
                      sub={didFilter ? undefined : 'via search compression'}
                    />
                  ) : (
                    <SavingsDetailRow label="Tokens saved" value="—" />
                  )}
                  {searchCalls > 0 && (
                    <SavingsDetailRow
                      label="Tool searches"
                      value={String(searchCalls)}
                      sub="gateway_search_tools"
                    />
                  )}
                </>
              )
            })() : (
              <div style={{ fontSize: 11, color: '#4b5563', padding: '4px 0', fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
                No tool filtering applied
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function SavingsTab({ data, error, onSessionChange, selectedSession }: SavingsTabProps) {
  const [showActiveOnly, setShowActiveOnly] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const [sessionsExpanded, setSessionsExpanded] = useState(true)

  const handleDeleteSession = async (sessionId: string) => {
    try {
      const res = await fetch(`/api/session?id=${encodeURIComponent(sessionId)}`, { method: 'DELETE' })
      if (!res.ok) {
        const body = await res.json().catch(() => ({ error: { message: 'unknown error' } }))
        const errMsg = body.error?.message || body.error || res.statusText
        alert(`Failed to delete session: ${errMsg}`)
        return
      }
      // If the deleted session was selected, clear the selection
      if (sessionId === selectedSession) onSessionChange('all')
      // Trigger a refresh by re-fetching data
      window.location.reload()
    } catch (e) {
      alert(`Failed to delete session: ${String(e)}`)
    }
  }

  if (!data) {
    if (error) {
      return (
        <div style={{ color: '#ef4444', padding: 16, background: '#111111', border: '1px solid rgba(239,68,68,0.2)', borderRadius: 12, fontFamily: "'JetBrains Mono', monospace", fontSize: 13 }}>
          Error: {error}
        </div>
      )
    }
    return (
      <div style={{ color: '#9ca3af', textAlign: 'center', padding: 48, fontSize: 14, fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
        Loading...
      </div>
    )
  }

  const isSessionSelected = selectedSession && selectedSession !== 'all' && selectedSession !== ''
  const activePorts = data.active_ports ?? []

  // Sort: active first, then by most recent activity
  // Keep backend order — already sorted stable (active-first, newest-first by ID).
  // Slice to avoid mutating the original array.
  const allSessions = (data.sessions ?? []).slice()

  // Apply search filter
  const searchFiltered = searchQuery
    ? allSessions.filter(s => s.id.toLowerCase().includes(searchQuery.toLowerCase()))
    : allSessions

  // Apply active-only filter
  const filteredSessions = showActiveOnly ? searchFiltered.filter(s => s.active) : searchFiltered

  const hasActiveSessions = allSessions.some(s => s.active)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
      {/* Stale-data warning — shown when fetch fails but we have prior data */}
      {error && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 14px', background: 'rgba(234,179,8,0.06)', border: '1px solid rgba(234,179,8,0.2)', borderRadius: 10, fontSize: 12, color: '#eab308', fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
          <span style={{ width: 6, height: 6, borderRadius: '50%', background: '#eab308', flexShrink: 0, display: 'inline-block' }} />
          Connection lost — showing last known data
        </div>
      )}

      {/* Selected session banner */}
      {isSessionSelected && (
        <div style={{
          display: 'flex', alignItems: 'center', gap: 10,
          background: 'rgba(34,197,94,0.06)', border: '1px solid rgba(34,197,94,0.2)',
          borderRadius: 10, padding: '10px 16px',
        }}>
          <Activity size={14} style={{ color: '#22c55e', flexShrink: 0 }} />
          <span style={{ fontSize: 12, color: '#9ca3af', fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
            Showing data for
          </span>
          <span style={{ fontSize: 12, fontWeight: 700, color: '#22c55e', fontFamily: "'JetBrains Mono', monospace" }}>
            {selectedSession}
          </span>
          <button
            onClick={() => onSessionChange('all')}
            style={{
              marginLeft: 'auto', background: 'none', border: 'none', cursor: 'pointer',
              color: '#6b7280', display: 'flex', alignItems: 'center', gap: 4, padding: '2px 6px',
              borderRadius: 6, fontSize: 11, fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
            }}
          >
            <X size={12} /> clear
          </button>
        </div>
      )}

      {/* Summary cards - always show global totals */}
      <SummaryCards
        savings={data.global_savings ?? data.savings}
        totalCost={data.total_cost}
        isScoped={false}
        sessionCount={allSessions.length}
      />

      {/* Active gateways */}
      {activePorts.length > 0 && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '0 4px' }}>
          <Radio size={12} style={{ color: '#22c55e' }} />
          <span style={{ fontSize: 11, color: '#6b7280', fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
            {activePorts.length} active gateway{activePorts.length !== 1 ? 's' : ''} on port{activePorts.length !== 1 ? 's' : ''} {activePorts.join(', ')}
          </span>
        </div>
      )}

      {/* Sessions section */}
      {allSessions.length > 0 && (
        <>
          {/* Collapsible header */}
          <button
            onClick={() => setSessionsExpanded(v => !v)}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              background: 'rgba(17,17,17,0.9)',
              border: '1px solid rgba(255,255,255,0.08)',
              borderRadius: 10,
              padding: '10px 14px',
              cursor: 'pointer',
              color: '#e5e7eb',
              fontSize: 13,
              fontWeight: 500,
              fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
              width: '100%',
              justifyContent: 'space-between',
            }}
          >
            <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <Radio size={14} style={{ color: '#22c55e' }} />
              Sessions
              <span style={{ color: '#6b7280', fontWeight: 400 }}>({allSessions.length})</span>
            </span>
            {sessionsExpanded ? <ChevronUp size={16} style={{ color: '#6b7280' }} /> : <ChevronDown size={16} style={{ color: '#6b7280' }} />}
          </button>

          {sessionsExpanded && (
            <>
          {/* Controls: search + active filter */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            {/* Search input */}
            <div style={{
              flex: 1, display: 'flex', alignItems: 'center', gap: 8,
              background: 'rgba(17,17,17,0.9)', border: '1px solid rgba(255,255,255,0.08)',
              borderRadius: 10, padding: '8px 12px',
            }}>
              <Search size={13} style={{ color: '#4b5563', flexShrink: 0 }} />
              <input
                type="text"
                placeholder="Filter sessions…"
                value={searchQuery}
                onChange={e => setSearchQuery(e.target.value)}
                style={{
                  flex: 1, background: 'none', border: 'none', outline: 'none',
                  color: '#e5e7eb', fontSize: 13, fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
                }}
              />
              {searchQuery && (
                <button
                  onClick={() => setSearchQuery('')}
                  style={{ background: 'none', border: 'none', cursor: 'pointer', color: '#4b5563', display: 'flex', padding: 0 }}
                >
                  <X size={12} />
                </button>
              )}
            </div>

            {/* Active only toggle */}
            {hasActiveSessions && (
              <button
                onClick={() => setShowActiveOnly(v => !v)}
                style={{
                  background: showActiveOnly ? 'rgba(34,197,94,0.1)' : 'rgba(255,255,255,0.04)',
                  border: `1px solid ${showActiveOnly ? 'rgba(34,197,94,0.3)' : 'rgba(255,255,255,0.06)'}`,
                  borderRadius: 8, padding: '8px 14px', cursor: 'pointer',
                  color: showActiveOnly ? '#22c55e' : '#6b7280',
                  fontSize: 12, fontWeight: 500,
                  fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
                  display: 'flex', alignItems: 'center', gap: 6,
                  whiteSpace: 'nowrap',
                }}
              >
                <div style={{ width: 6, height: 6, borderRadius: '50%', background: showActiveOnly ? '#22c55e' : '#6b7280' }} />
                Active only
              </button>
            )}
          </div>

          {/* Session count */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '0 4px' }}>
            <div style={{ height: 1, flex: 1, background: 'linear-gradient(90deg, rgba(255,255,255,0.06), transparent)' }} />
            <span style={{ fontSize: 11, color: '#6b7280', fontFamily: "'Inter', system-ui, -apple-system, sans-serif", fontWeight: 500, whiteSpace: 'nowrap' }}>
              {filteredSessions.length} session{filteredSessions.length !== 1 ? 's' : ''}
              {isSessionSelected && ' · click to deselect'}
            </span>
            <div style={{ height: 1, flex: 1, background: 'linear-gradient(90deg, transparent, rgba(255,255,255,0.06))' }} />
          </div>

          {/* Session cards */}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {filteredSessions.map((s) => (
              <SessionCard
                key={`${s.id}-${s.gateway_port ?? 0}`}
                session={s}
                isSelected={s.id === selectedSession}
                onClick={() => onSessionChange(s.id === selectedSession ? 'all' : s.id)}
                savings={s.id === selectedSession ? data.savings : undefined}
                onDelete={handleDeleteSession}
              />
            ))}
            {filteredSessions.length === 0 && (
              <div style={{ color: '#4b5563', textAlign: 'center', padding: 24, fontSize: 13, fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
                No sessions match your filter
              </div>
            )}
          </div>
            </>
          )}
        </>
      )}

      {/* Empty state */}
      {allSessions.length === 0 && !data.savings && (
        <div style={{ color: '#4b5563', textAlign: 'center', padding: 48, fontSize: 14, fontFamily: "'Inter', system-ui, -apple-system, sans-serif" }}>
          No sessions yet. Start using the gateway to see savings data here.
        </div>
      )}
    </div>
  )
}

export default SavingsTab
