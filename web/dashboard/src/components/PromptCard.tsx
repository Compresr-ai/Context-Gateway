import { useState, useRef, useEffect } from 'react'
import { Copy, Check, ChevronDown, ChevronUp } from 'lucide-react'
import type { PromptEntry } from '../types'

interface PromptCardProps {
  prompt: PromptEntry
  expanded: boolean
  onToggle: () => void
  sessionName?: string
}

function timeAgo(isoString: string): string {
  const now = Date.now()
  const then = new Date(isoString).getTime()
  const seconds = Math.floor((now - then) / 1000)

  if (seconds < 60) return 'just now'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} minute${minutes === 1 ? '' : 's'} ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} hour${hours === 1 ? '' : 's'} ago`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days} day${days === 1 ? '' : 's'} ago`
  const months = Math.floor(days / 30)
  if (months < 12) return `${months} month${months === 1 ? '' : 's'} ago`
  const years = Math.floor(months / 12)
  return `${years} year${years === 1 ? '' : 's'} ago`
}

function formatTimestamp(isoString: string): string {
  const date = new Date(isoString)
  return date.toLocaleDateString('en-US', {
    month: 'short',
    day: 'numeric',
  }) + ', ' + date.toLocaleTimeString('en-US', {
    hour: 'numeric',
    minute: '2-digit',
    hour12: true,
  })
}

function PromptCard({ prompt, expanded, onToggle, sessionName }: PromptCardProps) {
  const [copied, setCopied] = useState(false)
  const [hovered, setHovered] = useState(false)
  const [copyHovered, setCopyHovered] = useState(false)
  const copyTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Clear copy timeout on unmount to prevent state update after unmount
  useEffect(() => {
    return () => {
      if (copyTimeoutRef.current) {
        clearTimeout(copyTimeoutRef.current)
      }
    }
  }, [])

  const isTruncated = prompt.text.length > 200
  const displayText = expanded ? prompt.text : (isTruncated ? prompt.text.slice(0, 200) + '...' : prompt.text)

  const handleCopy = (e: React.MouseEvent) => {
    e.stopPropagation()
    navigator.clipboard.writeText(prompt.text).catch(() => {
      // Clipboard API may be unavailable (e.g., non-HTTPS context)
    })
    setCopied(true)
    if (copyTimeoutRef.current) {
      clearTimeout(copyTimeoutRef.current)
    }
    copyTimeoutRef.current = setTimeout(() => setCopied(false), 2000)
  }

  const handleToggle = (e: React.MouseEvent) => {
    e.stopPropagation()
    onToggle()
  }

  return (
    <div
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      style={{
        background: expanded ? 'rgba(17,17,17,0.95)' : 'rgba(17,17,17,0.9)',
        border: `1px solid ${hovered ? 'rgba(34,197,94,0.15)' : 'rgba(255,255,255,0.06)'}`,
        borderRadius: 14,
        padding: 0,
        position: 'relative',
        transition: 'all 0.25s ease',
        overflow: 'hidden',
        boxShadow: hovered
          ? '0 4px 24px rgba(34,197,94,0.06), 0 1px 4px rgba(0,0,0,0.3)'
          : '0 1px 3px rgba(0,0,0,0.2)',
        borderTop: expanded ? '1px solid rgba(34,197,94,0.2)' : undefined,
      }}
    >
      {/* Left accent bar */}
      <div
        style={{
          position: 'absolute',
          left: 0,
          top: 0,
          bottom: 0,
          width: 3,
          background: 'linear-gradient(180deg, #22c55e, #16a34a)',
          borderRadius: '14px 0 0 14px',
          opacity: hovered || expanded ? 1 : 0.5,
          transition: 'opacity 0.25s ease',
        }}
      />

      {/* Top glow when expanded */}
      {expanded && (
        <div
          style={{
            position: 'absolute',
            top: 0,
            left: 0,
            right: 0,
            height: 1,
            background: 'linear-gradient(90deg, transparent, rgba(34,197,94,0.4), transparent)',
          }}
        />
      )}

      <div style={{ padding: '16px 20px 16px 20px', marginLeft: 3 }}>
        {/* Header row: prompt number + copy button */}
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            marginBottom: 10,
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            {/* Prompt number */}
            <span
              style={{
                fontSize: 11,
                fontFamily: "'JetBrains Mono', monospace",
                color: '#4b5563',
                fontWeight: 500,
                letterSpacing: '0.02em',
              }}
            >
              #{prompt.id}
            </span>
            {/* Session / agent name */}
            {(sessionName || prompt.session_id) && (
              <span
                style={{
                  fontSize: 11,
                  fontWeight: 600,
                  color: '#22c55e',
                  background: 'rgba(34,197,94,0.08)',
                  border: '1px solid rgba(34,197,94,0.15)',
                  padding: '2px 8px',
                  borderRadius: 8,
                  fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
                }}
              >
                {sessionName || prompt.session_id}
              </span>
            )}
          </div>

          {/* Copy button */}
          <div style={{ position: 'relative' }}>
            <button
              onClick={handleCopy}
              onMouseEnter={() => setCopyHovered(true)}
              onMouseLeave={() => setCopyHovered(false)}
              style={{
                background: copyHovered ? 'rgba(255,255,255,0.08)' : 'transparent',
                border: 'none',
                cursor: 'pointer',
                padding: 6,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                color: copied ? '#22c55e' : '#6b7280',
                transition: 'all 0.2s ease',
                borderRadius: 8,
                width: 30,
                height: 30,
              }}
            >
              {copied ? <Check size={14} /> : <Copy size={14} />}
            </button>
            {/* Copied tooltip */}
            {copied && (
              <div
                style={{
                  position: 'absolute',
                  top: -28,
                  left: '50%',
                  transform: 'translateX(-50%)',
                  background: '#22c55e',
                  color: '#000',
                  fontSize: 10,
                  fontWeight: 600,
                  fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
                  padding: '3px 8px',
                  borderRadius: 6,
                  whiteSpace: 'nowrap',
                  pointerEvents: 'none',
                }}
              >
                Copied!
              </div>
            )}
          </div>
        </div>

        {/* Prompt text */}
        {expanded ? (
          <pre
            style={{
              whiteSpace: 'pre-wrap',
              wordBreak: 'break-word',
              maxHeight: 400,
              overflowY: 'auto',
              color: '#e5e7eb',
              fontSize: 13,
              fontFamily: "'JetBrains Mono', monospace",
              lineHeight: 1.7,
              margin: 0,
              paddingRight: 8,
              background: 'rgba(0,0,0,0.2)',
              padding: '12px 14px',
              borderRadius: 10,
              border: '1px solid rgba(255,255,255,0.04)',
            }}
          >
            {displayText}
          </pre>
        ) : (
          <div
            style={{
              color: '#f3f4f6',
              fontSize: 14,
              fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
              lineHeight: 1.6,
              paddingRight: 8,
            }}
          >
            {displayText}
          </div>
        )}

        {/* Show more / Show less toggle */}
        {isTruncated && (
          <button
            onClick={handleToggle}
            style={{
              background: 'none',
              border: 'none',
              cursor: 'pointer',
              color: '#22c55e',
              fontSize: 12,
              fontWeight: 500,
              fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
              padding: '6px 0 0 0',
              display: 'inline-flex',
              alignItems: 'center',
              gap: 4,
              transition: 'color 0.2s ease',
            }}
          >
            {expanded ? (
              <>
                Show less <ChevronUp size={12} />
              </>
            ) : (
              <>
                Show more <ChevronDown size={12} />
              </>
            )}
          </button>
        )}

        {/* Metadata grid */}
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            marginTop: 14,
            paddingTop: 12,
            borderTop: '1px solid rgba(255,255,255,0.04)',
          }}
        >
          {/* Left: timestamp */}
          <span
            title={timeAgo(prompt.timestamp)}
            style={{
              fontSize: 12,
              color: '#6b7280',
              fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
              fontWeight: 400,
            }}
          >
            {formatTimestamp(prompt.timestamp)}
          </span>

          {/* Right: model + provider pills */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            {prompt.model && (
              <span
                style={{
                  background: 'rgba(167,139,250,0.1)',
                  border: '1px solid rgba(167,139,250,0.15)',
                  padding: '3px 10px',
                  borderRadius: 8,
                  fontSize: 11,
                  fontWeight: 500,
                  color: '#a78bfa',
                  fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
                  letterSpacing: '0.01em',
                }}
              >
                {prompt.model}
              </span>
            )}

            {prompt.provider && (
              <span
                style={{
                  background: 'rgba(34,197,94,0.08)',
                  border: '1px solid rgba(34,197,94,0.15)',
                  padding: '3px 10px',
                  borderRadius: 8,
                  fontSize: 11,
                  fontWeight: 500,
                  color: '#4ade80',
                  fontFamily: "'Inter', system-ui, -apple-system, sans-serif",
                  letterSpacing: '0.01em',
                }}
              >
                {prompt.provider}
              </span>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

export default PromptCard
