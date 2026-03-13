import { useState } from 'react'
import { DollarSign, History, Monitor, Settings } from 'lucide-react'

type TabKey = 'savings' | 'history' | 'monitor' | 'settings'

interface TabBarProps {
  activeTab: TabKey
  onTabChange: (tab: TabKey) => void
}

function TabBar({ activeTab, onTabChange }: TabBarProps) {
  const [hoveredTab, setHoveredTab] = useState<string | null>(null)

  const tabs: { key: TabKey; label: string; icon: (active: boolean) => React.ReactNode }[] = [
    {
      key: 'monitor',
      label: 'Monitoring',
      icon: (active: boolean) => (
        <Monitor
          size={16}
          style={{
            transition: 'color 0.25s ease',
            color: active ? '#22c55e' : '#6b7280',
          }}
        />
      ),
    },
    {
      key: 'history',
      label: 'Prompt Bank',
      icon: (active: boolean) => (
        <History
          size={16}
          style={{
            transition: 'color 0.25s ease',
            color: active ? '#22c55e' : '#6b7280',
          }}
        />
      ),
    },
    {
      key: 'savings',
      label: 'Savings',
      icon: (active: boolean) => (
        <DollarSign
          size={16}
          style={{
            transition: 'color 0.25s ease',
            color: active ? '#22c55e' : '#6b7280',
          }}
        />
      ),
    },
    {
      key: 'settings',
      label: 'Global Config',
      icon: (active: boolean) => (
        <Settings
          size={16}
          style={{
            transition: 'color 0.25s ease',
            color: active ? '#22c55e' : '#6b7280',
          }}
        />
      ),
    },
  ]

  return (
    <div
      style={{
        width: '100%',
        background: 'transparent',
        borderBottom: '1px solid rgba(255,255,255,0.06)',
        display: 'flex',
        alignItems: 'stretch',
        position: 'relative',
      }}
    >
      {tabs.map((tab, index) => {
        const isActive = activeTab === tab.key
        const isHovered = hoveredTab === tab.key

        return (
          <div key={tab.key} style={{ display: 'flex', alignItems: 'stretch' }}>
            {/* Vertical separator between tabs */}
            {index > 0 && (
              <div
                style={{
                  width: 1,
                  alignSelf: 'center',
                  height: 20,
                  background: 'rgba(255,255,255,0.06)',
                  flexShrink: 0,
                }}
              />
            )}
            <button
              onClick={() => onTabChange(tab.key)}
              onMouseEnter={() => setHoveredTab(tab.key)}
              onMouseLeave={() => setHoveredTab(null)}
              style={{
                padding: '14px 28px',
                fontSize: 14,
                fontWeight: 500,
                cursor: 'pointer',
                transition: 'all 0.25s ease',
                display: 'flex',
                alignItems: 'center',
                gap: 8,
                background: 'transparent',
                color: isActive ? '#f3f4f6' : isHovered ? '#9ca3af' : '#6b7280',
                border: 'none',
                borderBottom: isActive
                  ? '2px solid transparent'
                  : '2px solid transparent',
                backgroundImage: isActive
                  ? 'none'
                  : 'none',
                fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
                outline: 'none',
                position: 'relative',
                letterSpacing: '0.01em',
              }}
            >
              {tab.icon(isActive)}
              {tab.label}
              {/* Active indicator — green gradient bottom border */}
              {isActive && (
                <span
                  style={{
                    position: 'absolute',
                    bottom: -1,
                    left: 12,
                    right: 12,
                    height: 2,
                    borderRadius: 1,
                    background: 'linear-gradient(90deg, #16a34a, #22c55e, #4ade80)',
                  }}
                />
              )}
            </button>
          </div>
        )
      })}
    </div>
  )
}

export default TabBar
