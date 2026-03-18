import { useState, useEffect, useRef, useCallback, useMemo } from 'react'
import { Search, ChevronLeft, ChevronRight, Trash2, MessageSquare, Loader2, Filter, X, SlidersHorizontal, ChevronDown, Check } from 'lucide-react'
import type { PromptEntry, FilterOptions, PromptsResponse } from '../types'
import PromptCard from './PromptCard'

const FONT_INTER = "'Inter', system-ui, -apple-system, sans-serif"
const FONT_MONO = "'JetBrains Mono', monospace"

const eraseButtonStyle: React.CSSProperties = {
  background: 'none',
  border: 'none',
  cursor: 'pointer',
  color: '#ef4444',
  fontSize: 11,
  fontFamily: FONT_INTER,
  padding: '4px 8px',
  display: 'inline-flex',
  alignItems: 'center',
  gap: 4,
  transition: 'color 0.2s ease',
  borderRadius: 6,
}

interface FilterDropdownProps {
  value: string
  onChange: (value: string) => void
  options: string[]
  placeholder: string
  accentColor: string
  icon: React.ReactNode
  truncate?: boolean
  labelMap?: Record<string, string>
}

function FilterDropdown({ value, onChange, options, placeholder, accentColor, icon, truncate, labelMap }: FilterDropdownProps) {
  const [hovered, setHovered] = useState(false)
  const [open, setOpen] = useState(false)
  const [highlightIdx, setHighlightIdx] = useState(-1)
  const containerRef = useRef<HTMLDivElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const isActive = value !== ''

  // All items: placeholder ("") + real options
  const allItems = useMemo(() => [{ value: '', label: placeholder }, ...options.map(o => {
    const mapped = labelMap?.[o]
    const label = mapped || (truncate && o.length > 20 ? '...' + o.slice(-20) : o)
    return { value: o, label }
  })], [options, placeholder, truncate, labelMap])

  // Close on outside click
  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  // Scroll highlighted item into view
  useEffect(() => {
    if (!open || highlightIdx < 0 || !listRef.current) return
    const el = listRef.current.children[highlightIdx] as HTMLElement | undefined
    el?.scrollIntoView({ block: 'nearest' })
  }, [highlightIdx, open])

  // Keyboard navigation
  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (!open) {
      if (e.key === 'Enter' || e.key === ' ' || e.key === 'ArrowDown') {
        e.preventDefault()
        setOpen(true)
        setHighlightIdx(allItems.findIndex(i => i.value === value))
      }
      return
    }
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        setHighlightIdx(i => Math.min(i + 1, allItems.length - 1))
        break
      case 'ArrowUp':
        e.preventDefault()
        setHighlightIdx(i => Math.max(i - 1, 0))
        break
      case 'Enter':
        e.preventDefault()
        if (highlightIdx >= 0) {
          onChange(allItems[highlightIdx].value)
          setOpen(false)
        }
        break
      case 'Escape':
        e.preventDefault()
        setOpen(false)
        break
    }
  }

  const displayLabel = isActive
    ? (labelMap?.[value] || (truncate && value.length > 20 ? '...' + value.slice(-20) : value))
    : placeholder

  return (
    <div
      ref={containerRef}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      style={{ position: 'relative', display: 'inline-flex', alignItems: 'center' }}
    >
      {/* Trigger button */}
      <button
        type="button"
        tabIndex={0}
        onClick={() => { setOpen(o => !o); setHighlightIdx(allItems.findIndex(i => i.value === value)) }}
        onKeyDown={handleKeyDown}
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          gap: 6,
          background: isActive
            ? `linear-gradient(135deg, rgba(17,17,17,0.95) 0%, ${accentColor}08 100%)`
            : 'rgba(17,17,17,0.9)',
          border: `1px solid ${open ? `${accentColor}50` : isActive ? `${accentColor}40` : hovered ? 'rgba(255,255,255,0.12)' : 'rgba(255,255,255,0.06)'}`,
          borderRadius: 12,
          padding: '10px 12px 10px 10px',
          color: isActive ? accentColor : '#e5e7eb',
          fontSize: 12,
          fontWeight: isActive ? 600 : 400,
          fontFamily: FONT_INTER,
          outline: 'none',
          cursor: 'pointer',
          transition: 'all 0.25s ease',
          minWidth: 0,
          boxShadow: open
            ? `0 0 0 3px ${accentColor}12, 0 4px 20px rgba(0,0,0,0.3)`
            : isActive
              ? `0 0 20px ${accentColor}10, inset 0 1px 0 ${accentColor}08`
              : hovered
                ? '0 2px 12px rgba(0,0,0,0.2)'
                : 'none',
        }}
      >
        <span style={{
          color: isActive ? accentColor : hovered ? '#9ca3af' : '#4b5563',
          transition: 'color 0.2s ease',
          display: 'flex',
          alignItems: 'center',
          flexShrink: 0,
        }}>
          {icon}
        </span>
        <span style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', maxWidth: 140 }}>
          {displayLabel}
        </span>
        <ChevronDown
          size={12}
          style={{
            color: isActive ? accentColor : '#6b7280',
            transition: 'transform 0.2s ease, color 0.2s ease',
            transform: open ? 'rotate(180deg)' : 'rotate(0deg)',
            flexShrink: 0,
          }}
        />
      </button>

      {/* Custom dropdown menu */}
      {open && (
        <div
          ref={listRef}
          role="listbox"
          style={{
            position: 'absolute',
            top: 'calc(100% + 6px)',
            left: 0,
            minWidth: '100%',
            maxHeight: 240,
            overflowY: 'auto',
            background: 'rgba(20,20,22,0.98)',
            backdropFilter: 'blur(20px)',
            border: `1px solid ${accentColor}25`,
            borderRadius: 12,
            padding: 4,
            zIndex: 999,
            boxShadow: `0 12px 40px rgba(0,0,0,0.5), 0 0 0 1px rgba(255,255,255,0.04), inset 0 1px 0 rgba(255,255,255,0.04)`,
          }}
        >
          {allItems.map((item, idx) => {
            const isSelected = item.value === value
            const isHighlighted = idx === highlightIdx
            return (
              <div
                key={item.value || '__placeholder__'}
                role="option"
                aria-selected={isSelected}
                onClick={() => { onChange(item.value); setOpen(false) }}
                onMouseEnter={() => setHighlightIdx(idx)}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  gap: 8,
                  padding: '8px 10px',
                  borderRadius: 8,
                  cursor: 'pointer',
                  fontSize: 12,
                  fontFamily: FONT_INTER,
                  fontWeight: isSelected ? 600 : 400,
                  color: isSelected ? accentColor : item.value === '' ? '#6b7280' : '#d1d5db',
                  background: isHighlighted
                    ? isSelected ? `${accentColor}15` : 'rgba(255,255,255,0.06)'
                    : 'transparent',
                  transition: 'background 0.1s ease, color 0.1s ease',
                  whiteSpace: 'nowrap',
                }}
              >
                <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  {item.label}
                </span>
                {isSelected && item.value !== '' && (
                  <Check size={12} style={{ color: accentColor, flexShrink: 0 }} />
                )}
              </div>
            )
          })}
        </div>
      )}

      {/* Animated count badge */}
      {options.length > 0 && (
        <span
          style={{
            position: 'absolute',
            top: -7,
            right: -7,
            background: isActive ? accentColor : `${accentColor}20`,
            border: `1px solid ${isActive ? accentColor : `${accentColor}40`}`,
            color: isActive ? '#000' : accentColor,
            fontSize: 9,
            fontWeight: 700,
            fontFamily: FONT_MONO,
            padding: '1px 5px',
            borderRadius: 10,
            lineHeight: '14px',
            minWidth: 16,
            textAlign: 'center',
            transition: 'all 0.25s ease',
            boxShadow: isActive ? `0 0 12px ${accentColor}40` : 'none',
          }}
        >
          {options.length}
        </span>
      )}

      {/* Bottom accent line when active */}
      {isActive && (
        <div
          style={{
            position: 'absolute',
            bottom: 0,
            left: 8,
            right: 8,
            height: 2,
            borderRadius: 1,
            background: `linear-gradient(90deg, transparent, ${accentColor}, transparent)`,
            opacity: 0.6,
          }}
        />
      )}
    </div>
  )
}

interface EmptyStateProps {
  icon: React.ReactNode
  title: string
  subtitle: string
}

function EmptyState({ icon, title, subtitle }: EmptyStateProps) {
  return (
    <div style={{
      display: 'flex',
      flexDirection: 'column',
      alignItems: 'center',
      justifyContent: 'center',
      padding: '72px 24px',
    }}>
      <div
        style={{
          width: 56,
          height: 56,
          borderRadius: 16,
          background: 'rgba(255,255,255,0.03)',
          border: '1px solid rgba(255,255,255,0.06)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          marginBottom: 16,
        }}
      >
        {icon}
      </div>
      <div style={{
        fontSize: 16,
        fontWeight: 600,
        color: '#6b7280',
        marginBottom: 8,
        fontFamily: FONT_INTER,
      }}>
        {title}
      </div>
      <div style={{
        fontSize: 13,
        color: '#4b5563',
        textAlign: 'center',
        maxWidth: 400,
        lineHeight: 1.5,
        fontFamily: FONT_INTER,
      }}>
        {subtitle}
      </div>
    </div>
  )
}

function ActiveFilterPill({ label, color, onClear }: { label: string; color: string; onClear: () => void }) {
  const [hovered, setHovered] = useState(false)
  return (
    <span
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 5,
        background: hovered ? `${color}18` : `${color}0c`,
        border: `1px solid ${hovered ? `${color}35` : `${color}20`}`,
        borderRadius: 20,
        padding: '4px 10px 4px 12px',
        fontSize: 11,
        fontWeight: 500,
        color: color,
        fontFamily: FONT_INTER,
        transition: 'all 0.2s ease',
        cursor: 'default',
      }}
    >
      <span style={{ maxWidth: 160, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {label}
      </span>
      <button
        onClick={onClear}
        style={{
          background: hovered ? `${color}25` : 'transparent',
          border: 'none',
          cursor: 'pointer',
          color: color,
          padding: 2,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          borderRadius: '50%',
          width: 16,
          height: 16,
          transition: 'background 0.15s ease',
        }}
      >
        <X size={10} />
      </button>
    </span>
  )
}

function EraseButton({ onErase }: { onErase: () => void }) {
  return (
    <button
      onClick={onErase}
      style={eraseButtonStyle}
      onMouseEnter={(e) => { e.currentTarget.style.color = '#dc2626' }}
      onMouseLeave={(e) => { e.currentTarget.style.color = '#ef4444' }}
    >
      <Trash2 size={11} />
      Erase all prompts
    </button>
  )
}

function PromptHistoryTab({ sessionNames = {} }: { sessionNames?: Record<string, string> }) {
  const [prompts, setPrompts] = useState<PromptEntry[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [totalPages, setTotalPages] = useState(0)
  const [searchQuery, setSearchQuery] = useState('')
  const [debouncedSearch, setDebouncedSearch] = useState('')
  const [sessionFilter, setSessionFilter] = useState('')
  const [modelFilter, setModelFilter] = useState('')
  const [providerFilter, setProviderFilter] = useState('')
  const [filters, setFilters] = useState<FilterOptions>({ sessions: [], models: [], providers: [] })
  const [expandedId, setExpandedId] = useState<number | null>(null)
  const [loading, setLoading] = useState(false)
  const [searchFocused, setSearchFocused] = useState(false)

  const abortControllerRef = useRef<AbortController | null>(null)

  // Debounce search input by 300ms: updates debouncedSearch and resets page
  useEffect(() => {
    const timer = setTimeout(() => {
      setDebouncedSearch(searchQuery)
      setPage(1)
    }, 300)
    return () => clearTimeout(timer)
  }, [searchQuery])

  // Single fetch effect: fires when any fetch-relevant state changes
  const fetchPrompts = useCallback(async () => {
    // Cancel any in-flight request
    if (abortControllerRef.current) {
      abortControllerRef.current.abort()
    }
    const controller = new AbortController()
    abortControllerRef.current = controller

    setLoading(true)

    const params = new URLSearchParams()
    if (debouncedSearch) params.set('q', debouncedSearch)
    if (sessionFilter) params.set('session', sessionFilter)
    if (modelFilter) params.set('model', modelFilter)
    if (providerFilter) params.set('provider', providerFilter)
    params.set('page', String(page))
    params.set('limit', '50')

    try {
      const res = await fetch(`/api/prompts?${params.toString()}`, {
        signal: controller.signal,
      })
      if (!res.ok) {
        if (!controller.signal.aborted) {
          setLoading(false)
        }
        return
      }
      const json: PromptsResponse = await res.json()
      if (!controller.signal.aborted) {
        setPrompts(json.prompts || [])
        setTotal(json.total)
        setTotalPages(json.total_pages)
        if (json.filters) {
          setFilters(json.filters)
        }
        setLoading(false)
      }
    } catch (err: unknown) {
      if (err instanceof DOMException && err.name === 'AbortError') {
        return
      }
      if (!controller.signal.aborted) {
        setLoading(false)
      }
    }
  }, [debouncedSearch, sessionFilter, modelFilter, providerFilter, page])

  useEffect(() => {
    fetchPrompts()
    // Abort in-flight request on unmount or before next fetch
    return () => {
      if (abortControllerRef.current) {
        abortControllerRef.current.abort()
      }
    }
  }, [fetchPrompts])

  // Generic filter change handler — resets page to 1
  const makeFilterHandler = (setter: (v: string) => void) => (value: string) => {
    setter(value)
    setPage(1)
  }

  const hasActiveSearch = searchQuery || sessionFilter || modelFilter || providerFilter
  const hasActiveFilters = sessionFilter || modelFilter || providerFilter
  const activeFilterCount = [sessionFilter, modelFilter, providerFilter].filter(Boolean).length

  const handleEraseAll = async () => {
    if (!window.confirm('Are you sure? This will permanently delete all prompts.')) {
      return
    }
    try {
      const res = await fetch('/api/prompts/erase', { method: 'DELETE' })
      if (res.ok) {
        setPage(1)
        fetchPrompts()
      }
    } catch {
      // ignore network errors
    }
  }

  const handleDeletePrompt = async (id: number) => {
    try {
      const res = await fetch(`/api/prompts/${id}`, { method: 'DELETE' })
      if (res.ok) {
        // Remove from local state immediately for snappy UX
        setPrompts((prev) => prev.filter((p) => p.id !== id))
        setTotal((prev) => Math.max(0, prev - 1))
      }
    } catch {
      // ignore network errors
    }
  }

  // Build page numbers for pagination
  const getPageNumbers = (): (number | 'ellipsis')[] => {
    if (totalPages <= 7) {
      return Array.from({ length: totalPages }, (_, i) => i + 1)
    }
    const pages: (number | 'ellipsis')[] = [1]
    if (page > 3) {
      pages.push('ellipsis')
    }
    const start = Math.max(2, page - 1)
    const end = Math.min(totalPages - 1, page + 1)
    for (let i = start; i <= end; i++) {
      pages.push(i)
    }
    if (page < totalPages - 2) {
      pages.push('ellipsis')
    }
    pages.push(totalPages)
    return pages
  }

  const showingStart = (page - 1) * 50 + 1
  const showingEnd = Math.min(page * 50, total)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
      {/* Search bar */}
      <div style={{ position: 'relative' }}>
        <Search
          size={18}
          style={{
            position: 'absolute',
            left: 16,
            top: '50%',
            transform: 'translateY(-50%)',
            color: searchFocused ? '#22c55e' : '#6b7280',
            pointerEvents: 'none',
            transition: 'color 0.2s ease',
          }}
        />
        <input
          type="text"
          placeholder="Search your prompt bank..."
          value={searchQuery}
          onChange={(e) => setSearchQuery(e.target.value)}
          style={{
            width: '100%',
            background: 'rgba(17,17,17,0.9)',
            border: `1px solid ${searchFocused ? 'rgba(34,197,94,0.4)' : 'rgba(255,255,255,0.06)'}`,
            borderRadius: 14,
            padding: '14px 16px 14px 46px',
            color: '#f3f4f6',
            fontSize: 15,
            fontFamily: FONT_INTER,
            outline: 'none',
            boxSizing: 'border-box',
            transition: 'all 0.25s ease',
            boxShadow: searchFocused
              ? '0 0 0 3px rgba(34,197,94,0.08), 0 2px 12px rgba(0,0,0,0.2)'
              : '0 1px 3px rgba(0,0,0,0.1)',
            backdropFilter: 'blur(12px)',
          }}
          onFocus={() => setSearchFocused(true)}
          onBlur={() => setSearchFocused(false)}
        />
      </div>

      {/* Filters section */}
      <div
        style={{
          background: 'linear-gradient(135deg, rgba(17,17,17,0.8) 0%, rgba(17,17,17,0.6) 100%)',
          backdropFilter: 'blur(16px)',
          border: `1px solid ${hasActiveFilters ? 'rgba(34,197,94,0.12)' : 'rgba(255,255,255,0.04)'}`,
          borderRadius: 16,
          padding: '16px 20px',
          position: 'relative',
          overflow: 'visible',
          transition: 'border-color 0.3s ease',
          zIndex: 20,
        }}
      >
        {/* Decorative layer — clipped to container so glow doesn't bleed */}
        <div style={{ position: 'absolute', inset: 0, overflow: 'hidden', borderRadius: 16, pointerEvents: 'none' }}>
          {/* Top accent bar — lights up when filters are active */}
          <div
            style={{
              position: 'absolute',
              top: 0,
              left: 0,
              right: 0,
              height: 2,
              background: hasActiveFilters
                ? 'linear-gradient(90deg, transparent, #22c55e, #a78bfa, transparent)'
                : 'linear-gradient(90deg, transparent, rgba(255,255,255,0.06), transparent)',
              transition: 'background 0.4s ease',
            }}
          />

          {/* Subtle corner glow when active */}
          {hasActiveFilters && (
            <div
              style={{
                position: 'absolute',
                top: -30,
                right: -30,
                width: 100,
                height: 100,
                borderRadius: '50%',
                background: 'rgba(34,197,94,0.06)',
                filter: 'blur(30px)',
              }}
            />
          )}
        </div>

        {/* Header row */}
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            marginBottom: 14,
            position: 'relative',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <div
              style={{
                width: 28,
                height: 28,
                borderRadius: 8,
                background: hasActiveFilters ? 'rgba(34,197,94,0.1)' : 'rgba(255,255,255,0.04)',
                border: `1px solid ${hasActiveFilters ? 'rgba(34,197,94,0.2)' : 'rgba(255,255,255,0.06)'}`,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                transition: 'all 0.25s ease',
              }}
            >
              <SlidersHorizontal
                size={13}
                style={{
                  color: hasActiveFilters ? '#22c55e' : '#6b7280',
                  transition: 'color 0.25s ease',
                }}
              />
            </div>
            <span
              style={{
                fontSize: 11,
                fontWeight: 600,
                color: hasActiveFilters ? '#9ca3af' : '#6b7280',
                textTransform: 'uppercase',
                letterSpacing: '0.06em',
                fontFamily: FONT_INTER,
                transition: 'color 0.25s ease',
              }}
            >
              Filters
            </span>
            {hasActiveFilters && (
              <span
                style={{
                  fontSize: 9,
                  fontWeight: 700,
                  fontFamily: FONT_MONO,
                  color: '#22c55e',
                  background: 'rgba(34,197,94,0.1)',
                  border: '1px solid rgba(34,197,94,0.2)',
                  padding: '2px 8px',
                  borderRadius: 10,
                  letterSpacing: '0.04em',
                }}
              >
                {activeFilterCount} ACTIVE
              </span>
            )}
          </div>

          {/* Clear all button */}
          {hasActiveFilters && (
            <button
              onClick={() => {
                setSessionFilter('')
                setModelFilter('')
                setProviderFilter('')
                setPage(1)
              }}
              style={{
                background: 'rgba(239,68,68,0.06)',
                border: '1px solid rgba(239,68,68,0.15)',
                borderRadius: 8,
                padding: '4px 10px',
                color: '#ef4444',
                fontSize: 10,
                fontWeight: 600,
                fontFamily: FONT_INTER,
                cursor: 'pointer',
                display: 'flex',
                alignItems: 'center',
                gap: 4,
                transition: 'all 0.2s ease',
                letterSpacing: '0.02em',
              }}
              onMouseEnter={(e) => {
                e.currentTarget.style.background = 'rgba(239,68,68,0.12)'
                e.currentTarget.style.borderColor = 'rgba(239,68,68,0.3)'
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.background = 'rgba(239,68,68,0.06)'
                e.currentTarget.style.borderColor = 'rgba(239,68,68,0.15)'
              }}
            >
              <X size={10} />
              Clear all
            </button>
          )}
        </div>

        {/* Dropdowns row */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
          <FilterDropdown
            value={sessionFilter}
            onChange={makeFilterHandler(setSessionFilter)}
            options={filters.sessions}
            placeholder="All Sessions"
            accentColor="#22c55e"
            icon={<Filter size={12} />}
            truncate
            labelMap={sessionNames}
          />
          <FilterDropdown
            value={modelFilter}
            onChange={makeFilterHandler(setModelFilter)}
            options={filters.models}
            placeholder="All Models"
            accentColor="#a78bfa"
            icon={<Filter size={12} />}
          />
          <FilterDropdown
            value={providerFilter}
            onChange={makeFilterHandler(setProviderFilter)}
            options={filters.providers}
            placeholder="All Providers"
            accentColor="#38bdf8"
            icon={<Filter size={12} />}
          />
        </div>

        {/* Active filter pills */}
        {hasActiveFilters && (
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginTop: 12, paddingTop: 12, borderTop: '1px solid rgba(255,255,255,0.04)' }}>
            {sessionFilter && (
              <ActiveFilterPill
                label={`Session: ${sessionNames[sessionFilter] || (sessionFilter.length > 16 ? '...' + sessionFilter.slice(-16) : sessionFilter)}`}
                color="#22c55e"
                onClear={() => makeFilterHandler(setSessionFilter)('')}
              />
            )}
            {modelFilter && (
              <ActiveFilterPill
                label={`Model: ${modelFilter}`}
                color="#a78bfa"
                onClear={() => makeFilterHandler(setModelFilter)('')}
              />
            )}
            {providerFilter && (
              <ActiveFilterPill
                label={`Provider: ${providerFilter}`}
                color="#38bdf8"
                onClear={() => makeFilterHandler(setProviderFilter)('')}
              />
            )}
          </div>
        )}
      </div>

      {/* Stats row / divider */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 12,
          padding: '0 4px',
        }}
      >
        <div
          style={{
            height: 1,
            flex: 1,
            background: 'linear-gradient(90deg, rgba(255,255,255,0.06), transparent)',
          }}
        />
        <span
          style={{
            fontSize: 12,
            color: '#6b7280',
            fontFamily: FONT_INTER,
            whiteSpace: 'nowrap',
            fontWeight: 500,
          }}
        >
          {total > 0
            ? `Showing ${showingStart}${'\u2013'}${showingEnd} of ${total} prompt${total !== 1 ? 's' : ''}`
            : `${total} prompt${total !== 1 ? 's' : ''}`
          }
        </span>
        <div
          style={{
            height: 1,
            flex: 1,
            background: 'linear-gradient(90deg, transparent, rgba(255,255,255,0.06))',
          }}
        />
      </div>

      {/* Prompt list */}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6, position: 'relative' }}>
        {/* Loading bar */}
        {loading && (
          <div
            style={{
              position: 'absolute',
              top: 0,
              left: 0,
              right: 0,
              height: 3,
              borderRadius: 2,
              overflow: 'hidden',
              zIndex: 10,
            }}
          >
            <div
              style={{
                width: '100%',
                height: '100%',
                background: 'linear-gradient(90deg, transparent 0%, #22c55e 40%, #4ade80 60%, transparent 100%)',
                animation: 'promptLoadingSlide 1.5s ease-in-out infinite',
              }}
            />
            <style>{`
              @keyframes promptLoadingSlide {
                0% { transform: translateX(-100%); }
                100% { transform: translateX(100%); }
              }
              @keyframes promptLoadingPulse {
                0%, 100% { opacity: 0.4; }
                50% { opacity: 1; }
              }
            `}</style>
          </div>
        )}

        {/* Timeline line connecting cards */}
        {prompts.length > 1 && (
          <div
            style={{
              position: 'absolute',
              left: 18,
              top: 20,
              bottom: 20,
              width: 1,
              background: 'linear-gradient(180deg, rgba(34,197,94,0.15), rgba(34,197,94,0.05), rgba(34,197,94,0.15))',
              pointerEvents: 'none',
              zIndex: 0,
            }}
          />
        )}

        {/* Empty state: no prompts at all */}
        {!loading && prompts.length === 0 && !hasActiveSearch && (
          <EmptyState
            icon={<MessageSquare size={24} style={{ color: '#4b5563' }} />}
            title="No prompts in the bank yet"
            subtitle="Prompts will appear here as you interact with models through the gateway."
          />
        )}

        {/* Empty state: no search results */}
        {!loading && prompts.length === 0 && hasActiveSearch && (
          <EmptyState
            icon={<Search size={24} style={{ color: '#4b5563' }} />}
            title="No prompts match your search"
            subtitle="Try adjusting your search or filters."
          />
        )}

        {/* Loading placeholder when loading and no prompts yet */}
        {loading && prompts.length === 0 && (
          <div style={{
            display: 'flex',
            flexDirection: 'column',
            alignItems: 'center',
            justifyContent: 'center',
            padding: '72px 24px',
          }}>
            <Loader2
              size={24}
              style={{
                color: '#22c55e',
                animation: 'promptLoadingPulse 1.5s ease-in-out infinite',
              }}
            />
          </div>
        )}

        {prompts.map((prompt) => (
          <PromptCard
            key={prompt.id}
            prompt={prompt}
            expanded={expandedId === prompt.id}
            onToggle={() => setExpandedId(expandedId === prompt.id ? null : prompt.id)}
            onDelete={handleDeletePrompt}
            sessionName={sessionNames[prompt.session_id]}
          />
        ))}
      </div>

      {/* Pagination */}
      {totalPages > 1 && (
        <div
          style={{
            display: 'flex',
            flexDirection: 'column',
            alignItems: 'center',
            gap: 16,
            paddingTop: 8,
          }}
        >
          {/* Page navigation */}
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 4,
            }}
          >
            {/* Previous button */}
            <button
              onClick={() => setPage((p) => Math.max(1, p - 1))}
              disabled={page <= 1}
              style={{
                background: 'rgba(255,255,255,0.04)',
                border: '1px solid rgba(255,255,255,0.06)',
                borderRadius: 10,
                padding: '8px 10px',
                color: '#e5e7eb',
                cursor: page <= 1 ? 'not-allowed' : 'pointer',
                opacity: page <= 1 ? 0.3 : 1,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                fontSize: 13,
                fontFamily: FONT_INTER,
                transition: 'all 0.2s ease',
                width: 36,
                height: 36,
              }}
            >
              <ChevronLeft size={16} />
            </button>

            {/* Page number pills */}
            {getPageNumbers().map((pageNum, idx) =>
              pageNum === 'ellipsis' ? (
                <span
                  key={`ellipsis-${idx}`}
                  style={{
                    padding: '0 4px',
                    color: '#4b5563',
                    fontSize: 13,
                    fontFamily: FONT_INTER,
                    userSelect: 'none',
                  }}
                >
                  ...
                </span>
              ) : (
                <button
                  key={pageNum}
                  onClick={() => setPage(pageNum)}
                  style={{
                    background: page === pageNum
                      ? 'rgba(34,197,94,0.15)'
                      : 'rgba(255,255,255,0.04)',
                    border: `1px solid ${page === pageNum ? 'rgba(34,197,94,0.3)' : 'rgba(255,255,255,0.06)'}`,
                    borderRadius: 10,
                    padding: 0,
                    width: 36,
                    height: 36,
                    color: page === pageNum ? '#22c55e' : '#9ca3af',
                    cursor: 'pointer',
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    fontSize: 13,
                    fontWeight: page === pageNum ? 600 : 400,
                    fontFamily: FONT_INTER,
                    transition: 'all 0.2s ease',
                  }}
                >
                  {pageNum}
                </button>
              )
            )}

            {/* Next button */}
            <button
              onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
              disabled={page >= totalPages}
              style={{
                background: 'rgba(255,255,255,0.04)',
                border: '1px solid rgba(255,255,255,0.06)',
                borderRadius: 10,
                padding: '8px 10px',
                color: '#e5e7eb',
                cursor: page >= totalPages ? 'not-allowed' : 'pointer',
                opacity: page >= totalPages ? 0.3 : 1,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                fontSize: 13,
                fontFamily: FONT_INTER,
                transition: 'all 0.2s ease',
                width: 36,
                height: 36,
              }}
            >
              <ChevronRight size={16} />
            </button>
          </div>
        </div>
      )}

      {/* Erase all — single instance, always shown when there are prompts */}
      {total > 0 && (
        <div style={{ display: 'flex', justifyContent: 'center', paddingTop: totalPages > 1 ? 0 : 8 }}>
          <EraseButton onErase={handleEraseAll} />
        </div>
      )}
    </div>
  )
}

export default PromptHistoryTab
