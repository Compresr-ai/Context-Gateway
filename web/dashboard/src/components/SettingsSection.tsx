import { useState, useEffect, type ReactNode } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'

interface SettingsSectionProps {
  title: string
  description?: string
  children: ReactNode
  defaultOpen?: boolean
  // When provided, a toggle is shown in the header.
  // disabled = collapsed + locked; enabled = collapsible normally.
  enabled?: boolean
  onToggle?: () => void
}

const toggleStyle = (active: boolean): React.CSSProperties => ({
  width: 44,
  height: 24,
  borderRadius: 12,
  background: active ? '#16a34a' : 'rgba(255,255,255,0.1)',
  border: 'none',
  cursor: 'pointer',
  position: 'relative',
  transition: 'background 0.2s',
  flexShrink: 0,
})

const toggleDot = (active: boolean): React.CSSProperties => ({
  position: 'absolute',
  top: 3,
  left: active ? 23 : 3,
  width: 18,
  height: 18,
  borderRadius: '50%',
  background: '#fff',
  transition: 'left 0.2s',
})

function SettingsSection({
  title,
  description,
  children,
  defaultOpen = false,
  enabled,
  onToggle,
}: SettingsSectionProps) {
  const hasToggle = enabled !== undefined

  // Start open only if no toggle (always-on sections) or already enabled
  const [open, setOpen] = useState(defaultOpen && enabled !== false)

  // When the enabled state changes:
  // - flips to true  → auto-open so user sees the settings they just enabled
  // - flips to false → force collapse
  useEffect(() => {
    if (enabled === true) setOpen(true)
    else if (enabled === false) setOpen(false)
  }, [enabled])

  // Can only expand when there's no toggle (section has no on/off) or when enabled
  const canExpand = !hasToggle || enabled === true
  const isOpen = canExpand && open

  const handleHeaderClick = () => {
    if (canExpand) setOpen(o => !o)
  }

  return (
    <div style={{
      background: 'rgba(255,255,255,0.02)',
      border: `1px solid ${hasToggle && !enabled ? 'rgba(255,255,255,0.04)' : 'rgba(255,255,255,0.06)'}`,
      borderRadius: 12,
      overflow: 'visible',
      opacity: hasToggle && !enabled ? 0.6 : 1,
      transition: 'opacity 0.2s, border-color 0.2s',
    }}>
      {/* Header row */}
      <div style={{
        display: 'flex',
        alignItems: 'center',
        gap: 10,
        padding: '16px 20px',
      }}>
        {/* Chevron */}
        <button
          onClick={handleHeaderClick}
          style={{
            background: 'transparent',
            border: 'none',
            cursor: canExpand ? 'pointer' : 'default',
            padding: 0,
            display: 'flex',
            alignItems: 'center',
            color: canExpand ? '#6b7280' : 'rgba(107,114,128,0.25)',
            flexShrink: 0,
          }}
          tabIndex={canExpand ? 0 : -1}
        >
          {isOpen
            ? <ChevronDown size={16} />
            : <ChevronRight size={16} />
          }
        </button>

        {/* Title + description — also clickable to expand */}
        <button
          onClick={handleHeaderClick}
          style={{
            flex: 1,
            background: 'transparent',
            border: 'none',
            cursor: canExpand ? 'pointer' : 'default',
            color: '#f3f4f6',
            fontSize: 15,
            fontWeight: 600,
            fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
            letterSpacing: '-0.01em',
            textAlign: 'left',
            padding: 0,
            display: 'flex',
            flexDirection: 'column',
            alignItems: 'flex-start',
          }}
          tabIndex={canExpand ? 0 : -1}
        >
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
        </button>

        {/* Toggle pinned to the right of the header */}
        {hasToggle && onToggle && (
          <button
            style={toggleStyle(enabled!)}
            onClick={(e) => { e.stopPropagation(); onToggle() }}
          >
            <span style={toggleDot(enabled!)} />
          </button>
        )}
      </div>

      {/* Body — only shown when expanded */}
      {isOpen && (
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
