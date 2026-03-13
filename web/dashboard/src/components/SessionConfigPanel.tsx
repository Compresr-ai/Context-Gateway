import { useState, useEffect, useCallback, useRef } from 'react'
import { Save, X, ExternalLink } from 'lucide-react'
import type { GatewayConfig } from '../types'
import CustomSelect from './CustomSelect'

const preemptiveStrategies = [
  { value: 'compresr', label: 'Compresr API' },
  { value: 'external_provider', label: 'External Provider' },
]

const toolOutputStrategies = [
  { value: 'compresr', label: 'Compresr API' },
  { value: 'external_provider', label: 'External Provider' },
  { value: 'simple', label: 'Simple' },
  { value: 'passthrough', label: 'Passthrough' },
]

const toolDiscoveryStrategies = [
  { value: 'compresr', label: 'Compresr API' },
  { value: 'tool-search', label: 'Tool Search' },
  { value: 'relevance', label: 'Relevance Scoring' },
  { value: 'passthrough', label: 'Passthrough' },
]

interface SessionConfigPanelProps {
  port: number
  name: string
  onClose: () => void
}

function SessionConfigPanel({ port, name, onClose }: SessionConfigPanelProps) {
  const [config, setConfig] = useState<GatewayConfig | null>(null)
  const [savedConfig, setSavedConfig] = useState<GatewayConfig | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [toast, setToast] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const toastTimeout = useRef<ReturnType<typeof setTimeout> | null>(null)

  const fetchConfig = useCallback(async () => {
    try {
      const res = await fetch(`/api/instance/config?port=${port}`)
      if (!res.ok) { setError(`API returned ${res.status}`); return }
      const data = await res.json()
      setConfig(data)
      setSavedConfig(data)
      setError(null)
    } catch (e) {
      setError(String(e))
    }
  }, [port])

  useEffect(() => {
    fetchConfig()
  }, [fetchConfig])

  // Close on Escape
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [onClose])

  const hasChanges = config && savedConfig && JSON.stringify(config) !== JSON.stringify(savedConfig)

  const showToast = (msg: string) => {
    if (toastTimeout.current) clearTimeout(toastTimeout.current)
    setToast(msg)
    toastTimeout.current = setTimeout(() => setToast(null), 3000)
  }

  const saveAll = async () => {
    if (!config || !savedConfig) return
    setSaving(true)
    try {
      const patch = {
        preemptive: config.preemptive,
        pipes: {
          tool_output: config.pipes.tool_output,
          tool_discovery: config.pipes.tool_discovery,
        },
        cost_control: config.cost_control,
        notifications: config.notifications,
        monitoring: config.monitoring,
      }
      const res = await fetch(`/api/instance/config?port=${port}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(patch),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => ({ error: { message: 'Unknown error' } }))
        showToast(`Error: ${body.error?.message || res.statusText}`)
        return
      }
      const data = await res.json()
      setConfig(data)
      setSavedConfig(data)
      showToast('Config saved')
    } catch (e) {
      showToast(`Error: ${String(e)}`)
    } finally {
      setSaving(false)
    }
  }

  const discardChanges = () => {
    if (savedConfig) setConfig(savedConfig)
  }

  const inputStyle: React.CSSProperties = {
    background: 'rgba(255,255,255,0.05)',
    border: '1px solid rgba(255,255,255,0.1)',
    borderRadius: 6,
    color: '#f3f4f6',
    padding: '8px 12px',
    fontSize: 13,
    fontFamily: "'JetBrains Mono', monospace",
    outline: 'none',
    width: 100,
  }

  const toggleStyle = (active: boolean): React.CSSProperties => ({
    width: 40,
    height: 22,
    borderRadius: 11,
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
    left: active ? 21 : 3,
    width: 16,
    height: 16,
    borderRadius: '50%',
    background: '#fff',
    transition: 'left 0.2s',
  })

  const rowStyle: React.CSSProperties = {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: '8px 0',
  }

  const labelStyle: React.CSSProperties = {
    fontSize: 13,
    color: '#9ca3af',
  }

  const descStyle: React.CSSProperties = {
    fontSize: 11,
    color: '#6b7280',
    marginTop: 1,
  }

  return (
    <>
      {/* Backdrop */}
      <div
        onClick={onClose}
        style={{
          position: 'fixed',
          inset: 0,
          background: 'rgba(0,0,0,0.5)',
          backdropFilter: 'blur(4px)',
          zIndex: 200,
        }}
      />

      {/* Panel */}
      <div style={{
        position: 'fixed',
        right: 0,
        top: 0,
        bottom: 0,
        width: 520,
        maxWidth: '100%',
        background: '#111111',
        borderLeft: '1px solid rgba(255,255,255,0.08)',
        zIndex: 201,
        overflowY: 'auto',
        display: 'flex',
        flexDirection: 'column',
      }}>
        {/* Header */}
        <div style={{
          padding: '20px 24px',
          borderBottom: '1px solid rgba(255,255,255,0.06)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          flexShrink: 0,
        }}>
          <div>
            <div style={{ fontSize: 15, fontWeight: 600, color: '#f3f4f6' }}>
              Session Config
            </div>
            <div style={{
              fontSize: 12,
              color: '#6b7280',
              fontFamily: "'JetBrains Mono', monospace",
              marginTop: 2,
              display: 'flex',
              alignItems: 'center',
              gap: 6,
            }}>
              <span style={{ color: '#9ca3af' }}>{name}</span>
              <span style={{ color: '#3f3f46' }}>|</span>
              <span>:{port}</span>
            </div>
          </div>
          <button
            onClick={onClose}
            style={{
              background: 'rgba(255,255,255,0.05)',
              border: '1px solid rgba(255,255,255,0.08)',
              color: '#9ca3af',
              padding: 6,
              borderRadius: 6,
              cursor: 'pointer',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
            }}
          >
            <X size={14} />
          </button>
        </div>

        {/* Toast */}
        {toast && (
          <div style={{
            position: 'absolute',
            top: 12,
            left: '50%',
            transform: 'translateX(-50%)',
            background: toast.startsWith('Error') ? '#dc2626' : '#16a34a',
            color: '#fff',
            padding: '8px 18px',
            borderRadius: 8,
            fontSize: 13,
            fontWeight: 500,
            zIndex: 210,
            boxShadow: '0 4px 12px rgba(0,0,0,0.3)',
          }}>
            {toast}
          </div>
        )}

        {/* Content */}
        <div style={{ flex: 1, overflowY: 'auto', padding: '16px 24px' }}>
          {error && (
            <div style={{
              color: '#ef4444',
              padding: 16,
              background: 'rgba(239,68,68,0.1)',
              border: '1px solid rgba(239,68,68,0.2)',
              borderRadius: 8,
              fontSize: 13,
              fontFamily: "'JetBrains Mono', monospace",
            }}>
              {error}
            </div>
          )}

          {!config && !error && (
            <div style={{ color: '#6b7280', padding: 24, fontSize: 13, textAlign: 'center' }}>
              Loading configuration...
            </div>
          )}

          {config && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
              {/* Preemptive Summarization */}
              <Section title="Preemptive Summarization">
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Enabled</div>
                    <div style={descStyle}>Background summarization before context limit</div>
                  </div>
                  <button
                    style={toggleStyle(config.preemptive.enabled)}
                    onClick={() => setConfig({ ...config, preemptive: { ...config.preemptive, enabled: !config.preemptive.enabled } })}
                  >
                    <span style={toggleDot(config.preemptive.enabled)} />
                  </button>
                </div>
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Trigger Threshold (%)</div>
                    <div style={descStyle}>Context usage % that triggers summarization</div>
                  </div>
                  <input
                    type="number"
                    min={1}
                    max={99}
                    step={1}
                    value={config.preemptive.trigger_threshold}
                    style={inputStyle}
                    onChange={(e) => setConfig({ ...config, preemptive: { ...config.preemptive, trigger_threshold: Number(e.target.value) } })}
                  />
                </div>
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Strategy</div>
                    <div style={descStyle}>Compression engine</div>
                  </div>
                  <CustomSelect
                    value={config.preemptive.strategy}
                    onChange={(v) => setConfig({ ...config, preemptive: { ...config.preemptive, strategy: v } })}
                    options={preemptiveStrategies}
                  />
                </div>
              </Section>

              {/* Tool Output Compression */}
              <Section title="Tool Output Compression">
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Enabled</div>
                    <div style={descStyle}>Compress large tool results</div>
                  </div>
                  <button
                    style={toggleStyle(config.pipes.tool_output.enabled)}
                    onClick={() => setConfig({
                      ...config,
                      pipes: { ...config.pipes, tool_output: { ...config.pipes.tool_output, enabled: !config.pipes.tool_output.enabled } },
                    })}
                  >
                    <span style={toggleDot(config.pipes.tool_output.enabled)} />
                  </button>
                </div>
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Strategy</div>
                    <div style={descStyle}>Compression method</div>
                  </div>
                  <CustomSelect
                    value={config.pipes.tool_output.strategy}
                    onChange={(v) => setConfig({
                      ...config,
                      pipes: { ...config.pipes, tool_output: { ...config.pipes.tool_output, strategy: v } },
                    })}
                    options={toolOutputStrategies}
                  />
                </div>
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Min Bytes</div>
                    <div style={descStyle}>Skip below this size</div>
                  </div>
                  <input
                    type="number"
                    min={0}
                    step={256}
                    value={config.pipes.tool_output.min_bytes}
                    style={inputStyle}
                    onChange={(e) => setConfig({
                      ...config,
                      pipes: { ...config.pipes, tool_output: { ...config.pipes.tool_output, min_bytes: Number(e.target.value) } },
                    })}
                  />
                </div>
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Target Ratio</div>
                    <div style={descStyle}>Compressed-to-original (0-1)</div>
                  </div>
                  <input
                    type="number"
                    min={0.1}
                    max={1.0}
                    step={0.05}
                    value={config.pipes.tool_output.target_compression_ratio}
                    style={inputStyle}
                    onChange={(e) => setConfig({
                      ...config,
                      pipes: { ...config.pipes, tool_output: { ...config.pipes.tool_output, target_compression_ratio: Number(e.target.value) } },
                    })}
                  />
                </div>
              </Section>

              {/* Tool Discovery */}
              <Section title="Tool Discovery">
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Enabled</div>
                    <div style={descStyle}>Filter irrelevant tool definitions</div>
                  </div>
                  <button
                    style={toggleStyle(config.pipes.tool_discovery.enabled)}
                    onClick={() => setConfig({
                      ...config,
                      pipes: { ...config.pipes, tool_discovery: { ...config.pipes.tool_discovery, enabled: !config.pipes.tool_discovery.enabled } },
                    })}
                  >
                    <span style={toggleDot(config.pipes.tool_discovery.enabled)} />
                  </button>
                </div>
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Strategy</div>
                    <div style={descStyle}>Tool selection method</div>
                  </div>
                  <CustomSelect
                    value={config.pipes.tool_discovery.strategy}
                    onChange={(v) => setConfig({
                      ...config,
                      pipes: { ...config.pipes, tool_discovery: { ...config.pipes.tool_discovery, strategy: v } },
                    })}
                    options={toolDiscoveryStrategies}
                  />
                </div>
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Min Tools</div>
                    <div style={descStyle}>Below this, no filtering</div>
                  </div>
                  <input
                    type="number"
                    min={1}
                    value={config.pipes.tool_discovery.min_tools}
                    style={inputStyle}
                    onChange={(e) => setConfig({
                      ...config,
                      pipes: { ...config.pipes, tool_discovery: { ...config.pipes.tool_discovery, min_tools: Number(e.target.value) } },
                    })}
                  />
                </div>
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Max Tools</div>
                    <div style={descStyle}>Max tools after filtering</div>
                  </div>
                  <input
                    type="number"
                    min={1}
                    value={config.pipes.tool_discovery.max_tools}
                    style={inputStyle}
                    onChange={(e) => setConfig({
                      ...config,
                      pipes: { ...config.pipes, tool_discovery: { ...config.pipes.tool_discovery, max_tools: Number(e.target.value) } },
                    })}
                  />
                </div>
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Target Ratio</div>
                    <div style={descStyle}>Fraction to keep (0-1)</div>
                  </div>
                  <input
                    type="number"
                    min={0.1}
                    max={1.0}
                    step={0.05}
                    value={config.pipes.tool_discovery.target_ratio}
                    style={inputStyle}
                    onChange={(e) => setConfig({
                      ...config,
                      pipes: { ...config.pipes, tool_discovery: { ...config.pipes.tool_discovery, target_ratio: Number(e.target.value) } },
                    })}
                  />
                </div>
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Search Fallback</div>
                    <div style={descStyle}>LLM can search for filtered tools</div>
                  </div>
                  <button
                    style={toggleStyle(config.pipes.tool_discovery.search_fallback)}
                    onClick={() => setConfig({
                      ...config,
                      pipes: { ...config.pipes, tool_discovery: { ...config.pipes.tool_discovery, search_fallback: !config.pipes.tool_discovery.search_fallback } },
                    })}
                  >
                    <span style={toggleDot(config.pipes.tool_discovery.search_fallback)} />
                  </button>
                </div>
              </Section>

              {/* Cost Control */}
              <Section title="Cost Control">
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Enabled</div>
                    <div style={descStyle}>Enforce spending limits</div>
                  </div>
                  <button
                    style={toggleStyle(config.cost_control.enabled)}
                    onClick={() => setConfig({
                      ...config,
                      cost_control: { ...config.cost_control, enabled: !config.cost_control.enabled },
                    })}
                  >
                    <span style={toggleDot(config.cost_control.enabled)} />
                  </button>
                </div>
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Session Cap (USD)</div>
                    <div style={descStyle}>Max per session (0 = unlimited)</div>
                  </div>
                  <input
                    type="number"
                    min={0}
                    step={0.5}
                    value={config.cost_control.session_cap}
                    style={inputStyle}
                    onChange={(e) => setConfig({
                      ...config,
                      cost_control: { ...config.cost_control, session_cap: Number(e.target.value) },
                    })}
                  />
                </div>
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Global Cap (USD)</div>
                    <div style={descStyle}>Max total (0 = unlimited)</div>
                  </div>
                  <input
                    type="number"
                    min={0}
                    step={0.5}
                    value={config.cost_control.global_cap}
                    style={inputStyle}
                    onChange={(e) => setConfig({
                      ...config,
                      cost_control: { ...config.cost_control, global_cap: Number(e.target.value) },
                    })}
                  />
                </div>
              </Section>

              {/* Notifications */}
              <Section title="Notifications">
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Slack</div>
                    <div style={descStyle}>Notify on stop, permission, idle</div>
                  </div>
                  <button
                    style={toggleStyle(config.notifications.slack.enabled)}
                    onClick={() => setConfig({
                      ...config,
                      notifications: { ...config.notifications, slack: { ...config.notifications.slack, enabled: !config.notifications.slack.enabled } },
                    })}
                  >
                    <span style={toggleDot(config.notifications.slack.enabled)} />
                  </button>
                </div>

                {config.notifications.slack.enabled && !config.notifications.slack.configured && (
                  <div style={{
                    background: 'rgba(34,197,94,0.04)',
                    border: '1px solid rgba(34,197,94,0.12)',
                    borderRadius: 8,
                    padding: '12px 14px',
                    marginTop: 2,
                    marginBottom: 6,
                  }}>
                    <div style={{ fontSize: 12, fontWeight: 600, color: '#e5e7eb', marginBottom: 10 }}>
                      Setup Slack Webhook
                    </div>
                    <div style={{ fontSize: 11, color: '#9ca3af', lineHeight: 1.8 }}>
                      <div style={{ marginBottom: 4 }}>
                        <span style={{ color: '#22c55e', fontWeight: 600, marginRight: 6 }}>1.</span>
                        <a href="https://api.slack.com/apps?new_app=1" target="_blank" rel="noopener noreferrer"
                          style={{ color: '#22c55e', textDecoration: 'none', display: 'inline-flex', alignItems: 'center', gap: 3 }}>
                          Create new app <ExternalLink size={10} />
                        </a>, then <strong style={{ color: '#d1d5db' }}>From Scratch</strong>
                      </div>
                      <div style={{ marginBottom: 4 }}>
                        <span style={{ color: '#22c55e', fontWeight: 600, marginRight: 6 }}>2.</span>
                        Add an app name and a workspace to develop your app
                      </div>
                      <div style={{ marginBottom: 4 }}>
                        <span style={{ color: '#22c55e', fontWeight: 600, marginRight: 6 }}>3.</span>
                        On the left side choose <strong style={{ color: '#d1d5db' }}>Incoming Webhooks</strong>
                      </div>
                      <div style={{ marginBottom: 4 }}>
                        <span style={{ color: '#22c55e', fontWeight: 600, marginRight: 6 }}>4.</span>
                        Activate by turning the slider
                      </div>
                      <div style={{ marginBottom: 4 }}>
                        <span style={{ color: '#22c55e', fontWeight: 600, marginRight: 6 }}>5.</span>
                        On the bottom click <strong style={{ color: '#d1d5db' }}>Add New Webhook</strong>
                      </div>
                      <div style={{ marginBottom: 4 }}>
                        <span style={{ color: '#22c55e', fontWeight: 600, marginRight: 6 }}>6.</span>
                        Select a channel and authorize
                      </div>
                      <div style={{ marginBottom: 10 }}>
                        <span style={{ color: '#22c55e', fontWeight: 600, marginRight: 6 }}>7.</span>
                        Copy the Webhook URL and paste below
                      </div>
                    </div>
                    <input
                      type="text"
                      placeholder="https://hooks.slack.com/services/..."
                      value={config.notifications.slack.webhook_url || ''}
                      onChange={(e) => setConfig({
                        ...config,
                        notifications: {
                          ...config.notifications,
                          slack: { ...config.notifications.slack, webhook_url: e.target.value },
                        },
                      })}
                      style={{
                        width: '100%',
                        background: 'rgba(255,255,255,0.05)',
                        border: '1px solid rgba(255,255,255,0.1)',
                        borderRadius: 6,
                        color: '#f3f4f6',
                        padding: '8px 12px',
                        fontSize: 12,
                        fontFamily: "'JetBrains Mono', monospace",
                        outline: 'none',
                        boxSizing: 'border-box',
                      }}
                    />
                  </div>
                )}

                {config.notifications.slack.enabled && config.notifications.slack.configured && (
                  <div style={{
                    display: 'flex', alignItems: 'center', gap: 6,
                    padding: '6px 0 2px', fontSize: 11, color: '#6b7280',
                  }}>
                    <span style={{ width: 5, height: 5, borderRadius: '50%', background: '#22c55e', display: 'inline-block' }} />
                    <span>Webhook: <span style={{ fontFamily: "'JetBrains Mono', monospace", color: '#9ca3af' }}>{config.notifications.slack.webhook_url}</span></span>
                  </div>
                )}
              </Section>

              {/* Monitoring */}
              <Section title="Monitoring">
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Telemetry</div>
                    <div style={descStyle}>Write session telemetry logs</div>
                  </div>
                  <button
                    style={toggleStyle(config.monitoring.telemetry_enabled)}
                    onClick={() => setConfig({
                      ...config,
                      monitoring: { ...config.monitoring, telemetry_enabled: !config.monitoring.telemetry_enabled },
                    })}
                  >
                    <span style={toggleDot(config.monitoring.telemetry_enabled)} />
                  </button>
                </div>
              </Section>
            </div>
          )}
        </div>

        {/* Sticky footer — save/discard */}
        {hasChanges && (
          <div style={{
            padding: '12px 24px',
            borderTop: '1px solid rgba(34,197,94,0.2)',
            background: 'rgba(10,10,10,0.95)',
            backdropFilter: 'blur(12px)',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            flexShrink: 0,
          }}>
            <span style={{ fontSize: 12, color: '#9ca3af' }}>
              Unsaved changes
            </span>
            <div style={{ display: 'flex', gap: 8 }}>
              <button
                onClick={discardChanges}
                style={{
                  background: 'rgba(255,255,255,0.05)',
                  border: '1px solid rgba(255,255,255,0.1)',
                  borderRadius: 8,
                  padding: '6px 14px',
                  color: '#9ca3af',
                  fontSize: 12,
                  fontWeight: 500,
                  cursor: 'pointer',
                  fontFamily: "'Inter', system-ui, sans-serif",
                }}
              >
                Discard
              </button>
              <button
                onClick={saveAll}
                disabled={saving}
                style={{
                  background: saving ? 'rgba(22,163,74,0.5)' : 'linear-gradient(135deg, #16a34a, #22c55e)',
                  border: 'none',
                  borderRadius: 8,
                  padding: '6px 16px',
                  color: '#fff',
                  fontSize: 12,
                  fontWeight: 600,
                  cursor: saving ? 'default' : 'pointer',
                  fontFamily: "'Inter', system-ui, sans-serif",
                  display: 'flex',
                  alignItems: 'center',
                  gap: 5,
                  boxShadow: '0 0 16px rgba(34,197,94,0.15)',
                }}
              >
                <Save size={12} />
                {saving ? 'Saving...' : 'Save'}
              </button>
            </div>
          </div>
        )}
      </div>
    </>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <div style={{
        fontSize: 10,
        fontWeight: 600,
        textTransform: 'uppercase',
        letterSpacing: '0.08em',
        color: '#6b7280',
        marginBottom: 8,
      }}>
        {title}
      </div>
      <div style={{
        background: 'rgba(255,255,255,0.02)',
        border: '1px solid rgba(255,255,255,0.05)',
        borderRadius: 10,
        padding: '4px 14px',
      }}>
        {children}
      </div>
    </div>
  )
}

export default SessionConfigPanel
