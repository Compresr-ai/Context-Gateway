import { useState, type ReactNode } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'

interface SettingsSectionProps {
  title: string
  description?: string
  children: ReactNode
  defaultOpen?: boolean
}

function SettingsSection({ title, description, children, defaultOpen = false }: SettingsSectionProps) {
  const [open, setOpen] = useState(defaultOpen)

  return (
    <div style={{
      background: 'rgba(255,255,255,0.02)',
      border: '1px solid rgba(255,255,255,0.06)',
      borderRadius: 12,
      overflow: 'visible',
    }}>
      <button
        onClick={() => setOpen(!open)}
        style={{
          width: '100%',
          display: 'flex',
          alignItems: 'center',
          gap: 10,
          padding: '16px 20px',
          background: 'transparent',
          border: 'none',
          cursor: 'pointer',
          color: '#f3f4f6',
          fontSize: 15,
          fontWeight: 600,
          fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
          letterSpacing: '-0.01em',
        }}
      >
        {open
          ? <ChevronDown size={16} style={{ color: '#6b7280' }} />
          : <ChevronRight size={16} style={{ color: '#6b7280' }} />
        }
        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-start' }}>
          <span>{title}</span>
          {description && (
            <span style={{
              fontSize: 12,
              fontWeight: 400,
              color: '#6b7280',
              marginTop: 2,
            }}>
              {description}
            </span>
          )}
        </div>
      </button>
      {open && (
        <div style={{
          padding: '0 20px 16px',
          borderTop: '1px solid rgba(255,255,255,0.04)',
        }}>
          {children}
        </div>
      )}
    </div>
  )
}

export default SettingsSection
