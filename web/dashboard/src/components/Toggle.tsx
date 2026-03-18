// Toggle is a simple on/off switch component.

interface ToggleProps {
  enabled: boolean
  onToggle: () => void
  disabled?: boolean
}

const toggleStyle = (active: boolean, disabled: boolean): React.CSSProperties => ({
  width: 44,
  height: 24,
  borderRadius: 12,
  background: disabled ? 'rgba(255,255,255,0.05)' : (active ? '#16a34a' : 'rgba(255,255,255,0.1)'),
  border: 'none',
  cursor: disabled ? 'not-allowed' : 'pointer',
  position: 'relative',
  transition: 'background 0.2s',
  flexShrink: 0,
  opacity: disabled ? 0.5 : 1,
  pointerEvents: disabled ? 'none' : 'auto',
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

export default function Toggle({ enabled, onToggle, disabled = false }: ToggleProps) {
  return (
    <button
      style={toggleStyle(enabled, disabled)}
      onClick={disabled ? undefined : onToggle}
      type="button"
      disabled={disabled}
    >
      <span style={toggleDot(enabled)} />
    </button>
  )
}
