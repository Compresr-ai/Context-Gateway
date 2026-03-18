import { useState, useEffect, Component, type ReactNode } from 'react'
import type { DashboardData } from './types'
import TabBar from './components/TabBar'
import SavingsTab from './components/SavingsTab'
import PromptHistoryTab from './components/PromptHistoryTab'
import MonitorTab from './components/MonitorTab'
import SettingsTab from './components/SettingsTab'

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

function Dashboard() {
  const [data, setData] = useState<DashboardData | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [activeTab, setActiveTabState] = useState<'savings' | 'history' | 'monitor' | 'settings'>(() => {
    // Check URL hash for direct navigation (e.g., #/settings, #/monitor)
    if (window.location.hash === '#/settings') return 'settings'
    if (window.location.hash === '#/history') return 'history'
    if (window.location.hash === '#/savings') return 'savings'
    if (window.location.hash === '#/monitor') return 'monitor'
    return 'savings'
  })
  const [selectedSession, setSelectedSession] = useState('all')

  // Update URL hash when tab changes (so refresh stays on same tab)
  const setActiveTab = (tab: typeof activeTab) => {
    setActiveTabState(tab)
    window.location.hash = '#/' + tab
  }

  useEffect(() => {
    const fetchData = async () => {
      try {
        const params = selectedSession && selectedSession !== 'all' ? `?session=${encodeURIComponent(selectedSession)}` : ''
        const dashRes = await fetch(`/api/dashboard${params}`)
        if (!dashRes.ok) { setError(`API returned ${dashRes.status}`); return }
        const dashJson: DashboardData = await dashRes.json()
        setData(prev => {
          if (!prev) return dashJson
          // Preserve sessions reference when unchanged to avoid re-rendering
          // components that only depend on session list (e.g. PromptHistoryTab)
          const sessionsUnchanged = prev.sessions.length === dashJson.sessions.length &&
            prev.sessions.every((s, i) => s.id === dashJson.sessions[i]?.id && s.last_updated === dashJson.sessions[i]?.last_updated)
          return {
            ...dashJson,
            sessions: sessionsUnchanged ? prev.sessions : dashJson.sessions,
          }
        })
        setError(null)
      } catch (e) {
        setError(String(e))
      }
    }
    fetchData()
    const interval = setInterval(fetchData, 5000)
    return () => clearInterval(interval)
  }, [selectedSession])

  return (
    <div style={{
      minHeight: '100vh',
      background: '#0a0a0a',
      backgroundImage: 'linear-gradient(to right, rgba(128,128,128,0.03) 1px, transparent 1px), linear-gradient(to bottom, rgba(128,128,128,0.03) 1px, transparent 1px)',
      backgroundSize: '32px 32px',
      color: '#f3f4f6',
      fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
      display: 'flex',
      flexDirection: 'column',
      justifyContent: 'center',
    }}>
      {/* Pulse animation for the live indicator */}
      <style>{`
        @keyframes livePulse {
          0% {
            box-shadow: 0 0 0 0 rgba(34, 197, 94, 0.5);
          }
          70% {
            box-shadow: 0 0 0 6px rgba(34, 197, 94, 0);
          }
          100% {
            box-shadow: 0 0 0 0 rgba(34, 197, 94, 0);
          }
        }
      `}</style>

      <div style={{ maxWidth: 1100, width: '100%', margin: '0 auto', padding: '48px 32px', flex: 1 }}>
        {/* Header */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 18, marginBottom: 0, paddingBottom: 32 }}>
          <img
            src="/dashboard/logo.png"
            alt="compresr"
            style={{ height: 48, width: 'auto', display: 'block' }}
          />
          <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
            <span style={{
              fontSize: 26,
              fontWeight: 700,
              color: '#178044',
              letterSpacing: '-0.03em',
              lineHeight: 1.1,
            }}>Context Gateway</span>
            <span style={{
              fontSize: 13,
              color: '#6b7280',
              letterSpacing: '0.02em',
              fontWeight: 500,
            }}>Dashboard</span>
          </div>
          <div style={{
            marginLeft: 'auto',
            fontFamily: "'JetBrains Mono', monospace",
            fontSize: 11,
            color: error ? '#eab308' : '#9ca3af',
            background: error ? 'rgba(234,179,8,0.06)' : 'rgba(255,255,255,0.03)',
            border: `1px solid ${error ? 'rgba(234,179,8,0.25)' : 'rgba(255,255,255,0.08)'}`,
            padding: '7px 16px',
            borderRadius: 20,
            display: 'flex',
            alignItems: 'center',
            gap: 8,
            letterSpacing: '0.04em',
            textTransform: 'uppercase' as const,
            transition: 'all 0.3s ease',
          }}>
            <span
              className="live-dot"
              style={{
                width: 7,
                height: 7,
                background: error ? '#eab308' : '#22c55e',
                borderRadius: '50%',
                display: 'inline-block',
                animation: error ? 'none' : 'livePulse 2s ease-in-out infinite',
              }}
            />
            {error ? 'Offline' : 'Live'}
          </div>
        </div>

        {/* Header divider */}
        <div style={{
          height: 1,
          background: 'linear-gradient(to right, transparent, rgba(255,255,255,0.06), transparent)',
          marginBottom: 32,
        }} />

        {/* Tab bar */}
        <div style={{ marginBottom: 24 }}>
          <TabBar activeTab={activeTab} onTabChange={setActiveTab} />
        </div>

        {/* Tab content */}
        {activeTab === 'savings' && (
          <SavingsTab
            data={data}
            error={error}
            selectedSession={selectedSession}
            onSessionChange={setSelectedSession}
          />
        )}
        {activeTab === 'history' && (
          <PromptHistoryTab sessionNames={
            (data?.sessions ?? []).reduce<Record<string, string>>((acc, s) => {
              if (s.agent_name && s.id) acc[s.id] = s.agent_name
              return acc
            }, {})
          } />
        )}
        {activeTab === 'monitor' && (
          <MonitorTab dashboardData={data} />
        )}
        {activeTab === 'settings' && (
          <SettingsTab />
        )}
      </div>

      {/* Footer */}
      <div style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        gap: 6,
        paddingBottom: 32,
        paddingTop: 16,
        fontSize: 11,
        color: '#6b7280',
        fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
        letterSpacing: '0.02em',
      }}>
        <span style={{
          width: 5,
          height: 5,
          background: '#22c55e',
          borderRadius: '50%',
          display: 'inline-block',
          opacity: 0.7,
        }} />
        <span style={{ color: '#4b5563' }}>powered by</span>
        <span style={{ color: '#6b7280', fontWeight: 600 }}>compresr</span>
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
