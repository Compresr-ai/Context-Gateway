import { useState, useEffect, useRef } from 'react'
import { Activity, Clock, Cpu, Layers, Zap, Terminal, Maximize2, Pencil, Settings } from 'lucide-react'
import type { MonitorInstance, MonitorData } from '../types'
import SessionConfigPanel from './SessionConfigPanel'

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}

function formatCost(v: number): string {
  if (v === 0) return '$0.00'
  return v >= 1 ? `$${v.toFixed(2)}` : `$${v.toFixed(4)}`
}

function timeAgo(iso: string): string {
  if (!iso) return '-'
  const s = (Date.now() - new Date(iso).getTime()) / 1000
  if (s < 5) return 'just now'
  if (s < 60) return `${Math.floor(s)}s ago`
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  return `${Math.floor(s / 3600)}h ago`
}

function duration(iso: string): string {
  if (!iso) return '-'
  const s = (Date.now() - new Date(iso).getTime()) / 1000
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m`
  return `${Math.floor(s)}s`
}

// Two statuses: active -> Running, waiting_for_human -> Waiting
const statusConfig: Record<string, { label: string; color: string; bg: string; glow: string; topBar?: string }> = {
  active: {
    label: 'Running',
    color: '#22c55e',
    bg: 'rgba(22,101,52,0.3)',
    glow: '0 0 8px rgba(34,197,94,0.3)',
    topBar: 'linear-gradient(90deg, #16a34a, #22c55e, #4ade80)',
  },
  waiting_for_human: {
    label: 'Waiting',
    color: '#eab308',
    bg: 'rgba(133,77,14,0.3)',
    glow: '0 0 8px rgba(234,179,8,0.3)',
    topBar: 'linear-gradient(90deg, #ca8a04, #eab308, #fde047)',
  },
}

function getStatusConfig(status: string) {
  return statusConfig[status] || statusConfig.waiting_for_human
}

// Inline editable text component
function InlineEdit({ value, onSave, style }: {
  value: string
  onSave: (newValue: string) => void
  style?: React.CSSProperties
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(value)
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (editing && inputRef.current) {
      inputRef.current.focus()
      inputRef.current.select()
    }
  }, [editing])

  useEffect(() => {
    setDraft(value)
  }, [value])

  const commit = () => {
    const trimmed = draft.trim()
    if (trimmed && trimmed !== value) {
      onSave(trimmed)
    } else {
      setDraft(value)
    }
    setEditing(false)
  }

  if (editing) {
    return (
      <input
        ref={inputRef}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === 'Enter') commit()
          if (e.key === 'Escape') { setDraft(value); setEditing(false) }
        }}
        onClick={(e) => e.stopPropagation()}
        style={{
          background: 'rgba(255,255,255,0.08)',
          border: '1px solid rgba(34,197,94,0.3)',
          borderRadius: 6,
          color: '#f3f4f6',
          padding: '2px 8px',
          fontSize: 'inherit',
          fontWeight: 'inherit',
          fontFamily: 'inherit',
          outline: 'none',
          width: '100%',
          maxWidth: 220,
          ...style,
        }}
      />
    )
  }

  return (
    <span
      onClick={(e) => { e.stopPropagation(); setEditing(true) }}
      style={{
        cursor: 'pointer',
        display: 'inline-flex',
        alignItems: 'center',
        gap: 5,
      }}
      title="Click to rename"
    >
      {value}
      <Pencil size={11} style={{ color: '#4b5563', opacity: 0.6, flexShrink: 0 }} />
    </span>
  )
}

// Summary stat card
function StatCard({ icon, label, value, color, glowColor }: {
  icon: React.ReactNode
  label: string
  value: string
  color: string
  glowColor: string
}) {
  return (
    <div style={{
      background: 'rgba(17,17,17,0.9)',
      border: '1px solid rgba(255,255,255,0.06)',
      borderRadius: 14,
      padding: '18px 20px',
      display: 'flex',
      alignItems: 'center',
      gap: 14,
      position: 'relative',
      overflow: 'hidden',
    }}>
      <div style={{
        position: 'absolute',
        top: -30,
        right: -30,
        width: 80,
        height: 80,
        borderRadius: '50%',
        background: glowColor,
        filter: 'blur(30px)',
        pointerEvents: 'none',
      }} />
      <div style={{
        width: 38,
        height: 38,
        borderRadius: 10,
        background: `${color}12`,
        border: `1px solid ${color}25`,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        color,
        flexShrink: 0,
      }}>
        {icon}
      </div>
      <div>
        <div style={{
          fontFamily: "'JetBrains Mono', monospace",
          fontSize: 22,
          fontWeight: 700,
          color: '#f3f4f6',
          lineHeight: 1,
        }}>
          {value}
        </div>
        <div style={{
          fontSize: 10,
          fontWeight: 600,
          color: '#6b7280',
          textTransform: 'uppercase',
          letterSpacing: '0.08em',
          marginTop: 4,
        }}>
          {label}
        </div>
      </div>
    </div>
  )
}

// Detail panel
function DetailPanel({ inst, onClose, onRename }: { inst: MonitorInstance; onClose: () => void; onRename: (port: number, name: string) => void }) {
  const sc = getStatusConfig(inst.status)
  const totalTokens = inst.tokens_in + inst.tokens_out

  return (
    <>
      <div
        onClick={onClose}
        style={{
          position: 'fixed',
          inset: 0,
          background: 'rgba(0,0,0,0.5)',
          backdropFilter: 'blur(4px)',
          zIndex: 100,
        }}
      />
      <div style={{
        position: 'fixed',
        right: 0,
        top: 0,
        bottom: 0,
        width: 480,
        maxWidth: '100%',
        background: '#111111',
        borderLeft: '1px solid rgba(255,255,255,0.08)',
        zIndex: 101,
        overflowY: 'auto',
        padding: 28,
      }}>
        <div style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          marginBottom: 24,
        }}>
          <div>
            <div style={{ fontSize: 16, fontWeight: 600, color: '#f3f4f6' }}>
              <InlineEdit
                value={inst.name || `Port ${inst.port}`}
                onSave={(name) => onRename(inst.port, name)}
              />
            </div>
            <div style={{
              fontSize: 11,
              color: '#6b7280',
              fontFamily: "'JetBrains Mono', monospace",
              marginTop: 2,
            }}>
              :{inst.port}
            </div>
          </div>
          <button
            onClick={onClose}
            style={{
              background: 'rgba(255,255,255,0.05)',
              border: '1px solid rgba(255,255,255,0.08)',
              color: '#9ca3af',
              fontSize: 12,
              padding: '6px 14px',
              borderRadius: 6,
              cursor: 'pointer',
              fontFamily: "'Inter', system-ui, sans-serif",
            }}
          >
            ESC
          </button>
        </div>

        <Section title="Status">
          <Row label="State">
            <span style={{
              fontSize: 11,
              fontWeight: 600,
              padding: '3px 10px',
              borderRadius: 9999,
              background: sc.bg,
              color: sc.color,
              textTransform: 'uppercase',
              letterSpacing: '0.04em',
            }}>
              {sc.label}
            </span>
          </Row>
          <Row label="Model" value={inst.model || '-'} />
          <Row label="Provider" value={inst.provider || '-'} />
          <Row label="Uptime" value={duration(inst.started_at)} />
          <Row label="Last Activity" value={timeAgo(inst.last_activity_at)} />
        </Section>

        <Section title="Metrics">
          <Row label="Requests" value={String(inst.request_count)} />
          <Row label="Tokens In" value={formatTokens(inst.tokens_in)} />
          <Row label="Tokens Out" value={formatTokens(inst.tokens_out)} />
          <Row label="Total Tokens" value={formatTokens(totalTokens)} />
          <Row label="Tokens Saved" value={formatTokens(inst.tokens_saved)} green />
          <Row label="Compressions" value={String(inst.compression_count)} />
          <Row label="Cost" value={formatCost(inst.cost_usd)} />
        </Section>

        {(inst.last_tool_used || inst.working_dir) && (
          <Section title="Context">
            {inst.last_tool_used && (
              <Row label="Last Tool">
                <span style={{
                  fontFamily: "'JetBrains Mono', monospace",
                  fontSize: 12,
                  color: '#a78bfa',
                  background: 'rgba(167,139,250,0.1)',
                  padding: '2px 8px',
                  borderRadius: 4,
                }}>
                  {inst.last_tool_used}
                </span>
              </Row>
            )}
            {inst.working_dir && (
              <Row label="Working Dir">
                <span style={{
                  fontFamily: "'JetBrains Mono', monospace",
                  fontSize: 11,
                  color: '#9ca3af',
                  wordBreak: 'break-all',
                }}>
                  {inst.working_dir}
                </span>
              </Row>
            )}
          </Section>
        )}

      </div>
    </>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 24 }}>
      <div style={{
        fontSize: 10,
        fontWeight: 600,
        textTransform: 'uppercase',
        letterSpacing: '0.08em',
        color: '#6b7280',
        marginBottom: 10,
      }}>
        {title}
      </div>
      <div style={{
        background: 'rgba(255,255,255,0.02)',
        border: '1px solid rgba(255,255,255,0.05)',
        borderRadius: 10,
        overflow: 'hidden',
      }}>
        {children}
      </div>
    </div>
  )
}

function Row({ label, value, green, children }: {
  label: string
  value?: string
  green?: boolean
  children?: React.ReactNode
}) {
  return (
    <div style={{
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'space-between',
      padding: '10px 14px',
      borderBottom: '1px solid rgba(255,255,255,0.03)',
      fontSize: 13,
    }}>
      <span style={{ color: '#6b7280' }}>{label}</span>
      {children || (
        <span style={{
          color: green ? '#22c55e' : '#e5e7eb',
          fontWeight: 500,
          fontFamily: "'JetBrains Mono', monospace",
          fontSize: 12,
        }}>
          {value}
        </span>
      )}
    </div>
  )
}

// Focus terminal via backend API
async function focusTerminal(port: number) {
  try {
    await fetch(`/api/focus?port=${port}`, { method: 'POST' })
  } catch { /* ignore */ }
}

// Rename instance via backend API
async function renameInstance(port: number, name: string) {
  try {
    await fetch('/api/monitor/rename', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ port, name }),
    })
  } catch { /* ignore */ }
}

// Small icon button used in instance cards
function IconButton({ icon, title, onClick }: { icon: React.ReactNode; title: string; onClick: (e: React.MouseEvent) => void }) {
  const [hovered, setHovered] = useState(false)
  return (
    <button
      onClick={onClick}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      style={{
        background: hovered ? 'rgba(255,255,255,0.1)' : 'rgba(255,255,255,0.04)',
        border: '1px solid rgba(255,255,255,0.08)',
        borderRadius: 6,
        padding: 4,
        cursor: 'pointer',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        color: hovered ? '#e5e7eb' : '#6b7280',
        transition: 'all 0.15s ease',
      }}
      title={title}
    >
      {icon}
    </button>
  )
}

// Instance card
function InstanceCard({ inst, onExpand, onConfig, onRename }: { inst: MonitorInstance; onExpand: () => void; onConfig: () => void; onRename: (port: number, name: string) => void }) {
  const [hovered, setHovered] = useState(false)
  const sc = getStatusConfig(inst.status)
  const totalTokens = inst.tokens_in + inst.tokens_out

  return (
    <div
      onClick={() => focusTerminal(inst.port)}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      style={{
        background: hovered ? 'rgba(26,26,26,0.95)' : 'rgba(17,17,17,0.9)',
        border: `1px solid ${
          inst.status === 'active' ? 'rgba(34,197,94,0.15)' :
          inst.status === 'waiting_for_human' ? 'rgba(234,179,8,0.2)' :
          hovered ? 'rgba(255,255,255,0.1)' : 'rgba(255,255,255,0.06)'
        }`,
        borderRadius: 14,
        padding: '18px 20px',
        cursor: 'pointer',
        transition: 'all 0.2s ease',
        position: 'relative',
        overflow: 'hidden',
      }}
    >
      {sc.topBar && (
        <div style={{
          position: 'absolute',
          top: 0,
          left: 0,
          right: 0,
          height: 2,
          background: sc.topBar,
        }} />
      )}

      {/* Header */}
      <div style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        marginBottom: 12,
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <div>
            <div style={{
              fontSize: 14,
              fontWeight: 600,
              color: '#f3f4f6',
              display: 'flex',
              alignItems: 'center',
              gap: 6,
            }}>
              <InlineEdit
                value={inst.name || `Port ${inst.port}`}
                onSave={(name) => onRename(inst.port, name)}
              />
              <span style={{
                fontSize: 11,
                color: '#4b5563',
                fontFamily: "'JetBrains Mono', monospace",
                fontWeight: 400,
              }}>
                :{inst.port}
              </span>
            </div>
            <div style={{
              fontSize: 11,
              color: '#6b7280',
              fontFamily: "'JetBrains Mono', monospace",
              marginTop: 2,
            }}>
              {inst.model || '...'}
              {inst.provider && (
                <span style={{ color: '#3f3f46' }}> | {inst.provider}</span>
              )}
            </div>
          </div>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
          <IconButton
            icon={<Settings size={13} />}
            title="Session config"
            onClick={(e) => { e.stopPropagation(); onConfig() }}
          />
          <IconButton
            icon={<Maximize2 size={13} />}
            title="Expand details"
            onClick={(e) => { e.stopPropagation(); onExpand() }}
          />
          <span style={{
            fontSize: 10,
            fontWeight: 600,
            padding: '3px 10px',
            borderRadius: 9999,
            background: sc.bg,
            color: sc.color,
            textTransform: 'uppercase',
            letterSpacing: '0.04em',
            boxShadow: sc.glow,
          }}>
            {sc.label}
          </span>
        </div>
      </div>

      {/* Stats */}
      <div style={{
        display: 'grid',
        gridTemplateColumns: 'repeat(4, 1fr)',
        gap: 4,
        borderTop: '1px solid rgba(255,255,255,0.05)',
        paddingTop: 12,
      }}>
        {[
          { val: String(inst.request_count), lbl: 'Requests', green: false },
          { val: formatTokens(totalTokens), lbl: 'Tokens', green: false },
          { val: formatTokens(inst.tokens_saved), lbl: 'Saved', green: true },
          { val: formatCost(inst.cost_usd), lbl: 'Cost', green: true },
        ].map(s => (
          <div key={s.lbl} style={{ textAlign: 'center' }}>
            <div style={{
              fontFamily: "'JetBrains Mono', monospace",
              fontSize: 14,
              fontWeight: 600,
              color: s.green ? '#22c55e' : '#e5e7eb',
            }}>
              {s.val}
            </div>
            <div style={{
              fontSize: 9,
              color: '#4b5563',
              textTransform: 'uppercase',
              letterSpacing: '0.05em',
              marginTop: 2,
            }}>
              {s.lbl}
            </div>
          </div>
        ))}
      </div>

      {/* Footer */}
      <div style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        marginTop: 10,
        fontSize: 10,
        color: '#4b5563',
      }}>
        <span>{duration(inst.started_at)} uptime</span>
        {inst.last_tool_used && (
          <span style={{
            background: 'rgba(167,139,250,0.1)',
            color: '#a78bfa',
            padding: '1px 7px',
            borderRadius: 4,
            fontFamily: "'JetBrains Mono', monospace",
            fontSize: 10,
          }}>
            {inst.last_tool_used}
          </span>
        )}
        <span>{timeAgo(inst.last_activity_at)}</span>
      </div>
    </div>
  )
}

const SUMMARY_TITLE_KEY = 'compresr_monitor_summary_title'

interface MonitorTabProps {
  dashboardData?: { total_cost: number; savings?: { tokens_saved: number } } | null
}

function MonitorTab({ dashboardData: propDashboardData }: MonitorTabProps = {}) {
  const [data, setData] = useState<MonitorData | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<MonitorInstance | null>(null)
  const [configPort, setConfigPort] = useState<{ port: number; name: string } | null>(null)
  const [filter, setFilter] = useState<string>('all')
  const [summaryTitle, setSummaryTitle] = useState(() => {
    try { return localStorage.getItem(SUMMARY_TITLE_KEY) || 'Overview' } catch { return 'Overview' }
  })

  const handleSummaryRename = (newTitle: string) => {
    setSummaryTitle(newTitle)
    try { localStorage.setItem(SUMMARY_TITLE_KEY, newTitle) } catch { /* storage unavailable */ }
  }

  const handleInstanceRename = (port: number, name: string) => {
    renameInstance(port, name)
    // Optimistically update in local state
    if (data) {
      setData({
        ...data,
        instances: data.instances.map(i => i.port === port ? { ...i, name } : i),
      })
    }
    if (selected && selected.port === port) {
      setSelected({ ...selected, name })
    }
  }

  // Use dashboard data passed from parent to avoid redundant API calls.
  // Falls back to local state if parent doesn't provide it.
  const [localDashboardData, setLocalDashboardData] = useState<{ total_cost: number; savings?: { tokens_saved: number } } | null>(null)
  const dashboardData = propDashboardData !== undefined ? propDashboardData : localDashboardData

  useEffect(() => {
    const fetchData = async () => {
      try {
        // Only fetch /api/dashboard if parent didn't pass it
        const requests: Promise<Response>[] = [fetch('/api/monitor')]
        if (propDashboardData === undefined) {
          requests.push(fetch('/api/dashboard'))
        }
        const [monitorResp, dashResp] = await Promise.all(requests)
        if (!monitorResp.ok) {
          // Gateway unreachable — clear stale data so we show a clean offline state.
          setData(null)
          setError(`API returned ${monitorResp.status}`)
          return
        }
        const json = await monitorResp.json()
        setData(json)
        if (dashResp?.ok) {
          const dashJson = await dashResp.json()
          setLocalDashboardData(dashJson)
        }
        setError(null)
      } catch (e) {
        // Gateway unreachable — clear stale data so we show a clean offline state.
        setData(null)
        setError(String(e))
      }
    }
    fetchData()
    const interval = setInterval(fetchData, 3000)
    return () => clearInterval(interval)
  }, [propDashboardData === undefined]) // eslint-disable-line react-hooks/exhaustive-deps

  // Keep selected panel fresh
  useEffect(() => {
    if (selected && data) {
      const fresh = data.instances.find(i => i.port === selected.port)
      if (fresh) setSelected(fresh)
    }
  }, [data]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!data) {
    if (error) {
      return (
        <div style={{
          color: '#ef4444',
          padding: 16,
          background: '#111111',
          border: '1px solid rgba(239,68,68,0.2)',
          borderRadius: 12,
          fontFamily: "'JetBrains Mono', monospace",
          fontSize: 13,
        }}>
          Error: {error}
        </div>
      )
    }
    return (
      <div style={{ color: '#9ca3af', textAlign: 'center', padding: 48, fontSize: 14 }}>
        Loading...
      </div>
    )
  }

  const instances = data.instances || []
  const running = instances.filter(i => i.status === 'active').length
  const waiting = instances.filter(i => i.status === 'waiting_for_human').length
  const totalTokens = instances.reduce((sum, i) => sum + i.tokens_in + i.tokens_out, 0)
  // Use dashboard API data for accurate totals (same as savings tab)
  const totalSaved = dashboardData?.savings?.tokens_saved ?? instances.reduce((sum, i) => sum + i.tokens_saved, 0)
  const totalCost = dashboardData?.total_cost ?? instances.reduce((sum, i) => sum + i.cost_usd, 0)

  const filtered = filter === 'all'
    ? instances
    : instances.filter(i => i.status === filter)

  // Sort by port only — stable order, cards never reorder on status change.
  const sorted = [...filtered].sort((a, b) => a.port - b.port)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>

      {/* Summary section title - editable */}
      <div style={{
        display: 'flex',
        alignItems: 'center',
        gap: 8,
        fontSize: 13,
        fontWeight: 600,
        color: '#9ca3af',
        textTransform: 'uppercase',
        letterSpacing: '0.06em',
        fontFamily: "'Inter', system-ui, sans-serif",
      }}>
        <InlineEdit
          value={summaryTitle}
          onSave={handleSummaryRename}
          style={{ textTransform: 'uppercase', letterSpacing: '0.06em' }}
        />
      </div>

      {/* Summary stats */}
      <div style={{
        display: 'grid',
        gridTemplateColumns: 'repeat(4, 1fr)',
        gap: 12,
      }}>
        <StatCard
          icon={<Activity size={18} />}
          label="Instances"
          value={String(instances.length)}
          color="#3b82f6"
          glowColor="rgba(59,130,246,0.1)"
        />
        <StatCard
          icon={<Cpu size={18} />}
          label="Running"
          value={String(running)}
          color="#22c55e"
          glowColor="rgba(34,197,94,0.1)"
        />
        <StatCard
          icon={<Layers size={18} />}
          label="Tokens Saved"
          value={formatTokens(totalSaved)}
          color="#a78bfa"
          glowColor="rgba(167,139,250,0.1)"
        />
        <StatCard
          icon={<Zap size={18} />}
          label="Total Cost"
          value={formatCost(totalCost)}
          color="#f59e0b"
          glowColor="rgba(245,158,11,0.1)"
        />
      </div>

      {/* Filters */}
      <div style={{
        display: 'flex',
        alignItems: 'center',
        gap: 8,
      }}>
        {[
          { key: 'all', label: `All (${instances.length})` },
          { key: 'active', label: `Running (${running})` },
          { key: 'waiting_for_human', label: `Waiting (${waiting})` },
        ].map(f => (
          <button
            key={f.key}
            onClick={() => setFilter(f.key)}
            style={{
              background: filter === f.key ? 'rgba(34,197,94,0.1)' : 'rgba(255,255,255,0.03)',
              border: `1px solid ${filter === f.key ? 'rgba(34,197,94,0.25)' : 'rgba(255,255,255,0.06)'}`,
              borderRadius: 8,
              padding: '6px 14px',
              cursor: 'pointer',
              color: filter === f.key ? '#22c55e' : '#6b7280',
              fontSize: 12,
              fontWeight: 500,
              fontFamily: "'Inter', system-ui, sans-serif",
              transition: 'all 0.2s ease',
            }}
          >
            {f.label}
          </button>
        ))}
        <div style={{ flex: 1 }} />
        <div style={{
          fontSize: 11,
          color: '#4b5563',
          fontFamily: "'JetBrains Mono', monospace",
          display: 'flex',
          alignItems: 'center',
          gap: 6,
        }}>
          <Clock size={12} />
          {totalTokens > 0 ? `${formatTokens(totalTokens)} total tokens` : 'No traffic yet'}
        </div>
      </div>

      {/* Card grid */}
      {sorted.length > 0 ? (
        <div style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(auto-fill, minmax(340px, 1fr))',
          gap: 12,
        }}>
          {sorted.map(inst => (
            <InstanceCard
              key={inst.port}
              inst={inst}
              onExpand={() => setSelected(inst)}
              onConfig={() => setConfigPort({ port: inst.port, name: inst.name || `Port ${inst.port}` })}
              onRename={handleInstanceRename}
            />
          ))}
        </div>
      ) : (
        <div style={{ textAlign: 'center', padding: '80px 20px' }}>
          <div style={{
            width: 56,
            height: 56,
            borderRadius: 16,
            background: 'rgba(255,255,255,0.03)',
            border: '1px solid rgba(255,255,255,0.06)',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            margin: '0 auto 16px',
          }}>
            <Terminal size={24} color="#4b5563" />
          </div>
          <div style={{ fontSize: 16, fontWeight: 600, color: '#e5e7eb', marginBottom: 8 }}>
            No active instances
          </div>
          <div style={{ fontSize: 13, color: '#6b7280', lineHeight: 1.6 }}>
            Start an agent through the gateway to see it here.
          </div>
        </div>
      )}

      {/* Detail panel */}
      {selected && (
        <DetailPanel
          inst={selected}
          onClose={() => setSelected(null)}
          onRename={handleInstanceRename}
        />
      )}

      {/* Session config panel */}
      {configPort && (
        <SessionConfigPanel
          port={configPort.port}
          name={configPort.name}
          onClose={() => setConfigPort(null)}
        />
      )}
    </div>
  )
}

export default MonitorTab
