import { useState, useEffect, useCallback, useRef } from 'react'
import { Save, ExternalLink } from 'lucide-react'
import type { GatewayConfig, ConfigPatch } from '../types'
import SettingsSection from './SettingsSection'
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

function SettingsTab() {
  const [config, setConfig] = useState<GatewayConfig | null>(null)
  const [savedConfig, setSavedConfig] = useState<GatewayConfig | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [toast, setToast] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const toastTimeout = useRef<ReturnType<typeof setTimeout> | null>(null)

  const fetchConfig = useCallback(async () => {
    try {
      const res = await fetch('/api/config')
      if (!res.ok) { setError(`API returned ${res.status}`); return }
      const data = await res.json()
      setConfig(data)
      setSavedConfig(data)
      setError(null)
    } catch (e) {
      setError(String(e))
    }
  }, [])

  useEffect(() => {
    fetchConfig()
  }, [fetchConfig])

  // Listen for WebSocket config_updated events
  useEffect(() => {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${protocol}//${window.location.host}/ws`)
    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data)
        if (msg.type === 'config_updated') {
          fetchConfig()
        }
      } catch {
        // ignore parse errors
      }
    }
    return () => ws.close()
  }, [fetchConfig])

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
      // Build a full patch from local state
      const patch: ConfigPatch = {
        preemptive: config.preemptive,
        pipes: {
          tool_output: config.pipes.tool_output,
          tool_discovery: config.pipes.tool_discovery,
        },
        cost_control: config.cost_control,
        notifications: config.notifications,
        monitoring: config.monitoring,
      }
      const res = await fetch('/api/config', {
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

  if (error) {
    return (
      <div style={{ color: '#ef4444', padding: 24, fontFamily: 'monospace', fontSize: 14 }}>
        Failed to load config: {error}
      </div>
    )
  }

  if (!config) {
    return (
      <div style={{ color: '#6b7280', padding: 24, fontSize: 14 }}>
        Loading configuration...
      </div>
    )
  }

  const inputStyle: React.CSSProperties = {
    background: 'rgba(255,255,255,0.05)',
    border: '1px solid rgba(255,255,255,0.1)',
    borderRadius: 6,
    color: '#f3f4f6',
    padding: '8px 12px',
    fontSize: 14,
    fontFamily: "'JetBrains Mono', monospace",
    outline: 'none',
    width: 120,
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

  const rowStyle: React.CSSProperties = {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: '10px 0',
  }

  const labelStyle: React.CSSProperties = {
    fontSize: 14,
    color: '#9ca3af',
  }

  const descStyle: React.CSSProperties = {
    fontSize: 12,
    color: '#6b7280',
    marginTop: 2,
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      {/* Toast notification */}
      {toast && (
        <div style={{
          position: 'fixed',
          top: 24,
          right: 24,
          background: toast.startsWith('Error') ? '#dc2626' : '#16a34a',
          color: '#fff',
          padding: '10px 20px',
          borderRadius: 8,
          fontSize: 14,
          fontWeight: 500,
          zIndex: 1000,
          boxShadow: '0 4px 12px rgba(0,0,0,0.3)',
        }}>
          {toast}
        </div>
      )}

      {/* Save / Discard bar */}
      {hasChanges && (
        <div style={{
          position: 'sticky',
          top: 0,
          zIndex: 50,
          background: 'rgba(10,10,10,0.95)',
          backdropFilter: 'blur(12px)',
          border: '1px solid rgba(34,197,94,0.2)',
          borderRadius: 12,
          padding: '12px 18px',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          boxShadow: '0 4px 20px rgba(0,0,0,0.4)',
        }}>
          <span style={{ fontSize: 13, color: '#9ca3af', fontFamily: "'Inter', system-ui, sans-serif" }}>
            You have unsaved changes
          </span>
          <div style={{ display: 'flex', gap: 8 }}>
            <button
              onClick={discardChanges}
              style={{
                background: 'rgba(255,255,255,0.05)',
                border: '1px solid rgba(255,255,255,0.1)',
                borderRadius: 8,
                padding: '7px 16px',
                color: '#9ca3af',
                fontSize: 13,
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
                padding: '7px 18px',
                color: '#fff',
                fontSize: 13,
                fontWeight: 600,
                cursor: saving ? 'default' : 'pointer',
                fontFamily: "'Inter', system-ui, sans-serif",
                display: 'flex',
                alignItems: 'center',
                gap: 6,
                boxShadow: '0 0 20px rgba(34,197,94,0.15)',
              }}
            >
              <Save size={14} />
              {saving ? 'Saving...' : 'Save Changes'}
            </button>
          </div>
        </div>
      )}

      {/* Preemptive Summarization */}
      <SettingsSection title="Preemptive Summarization" description="Proactively compresses conversation history before you hit the context limit" defaultOpen>
        <div style={rowStyle}>
          <div>
            <div style={labelStyle}>Enabled</div>
            <div style={descStyle}>Enable background summarization before context limit</div>
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
            <div style={descStyle}>Context usage % that triggers background summarization</div>
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
            <div style={descStyle}>Compression engine for history summarization</div>
          </div>
          <CustomSelect
            value={config.preemptive.strategy}
            onChange={(v) => setConfig({ ...config, preemptive: { ...config.preemptive, strategy: v } })}
            options={preemptiveStrategies}
          />
        </div>
      </SettingsSection>

      {/* Tool Output Compression */}
      <SettingsSection title="Tool Output Compression" description="Compresses large tool outputs (file reads, search results) to save context space">
        <div style={rowStyle}>
          <div>
            <div style={labelStyle}>Enabled</div>
            <div style={descStyle}>Compress large tool results to save context space</div>
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
            <div style={descStyle}>Method used to compress tool outputs</div>
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
            <div style={descStyle}>Outputs below this size skip compression</div>
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
            <div style={labelStyle}>Target Compression Ratio</div>
            <div style={descStyle}>Target compressed-to-original size ratio (0-1)</div>
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
      </SettingsSection>

      {/* Tool Discovery */}
      <SettingsSection title="Tool Discovery" description="Filters irrelevant tool definitions from requests to reduce token usage">
        <div style={rowStyle}>
          <div>
            <div style={labelStyle}>Enabled</div>
            <div style={descStyle}>Filter irrelevant tool definitions from requests</div>
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
            <div style={descStyle}>Method used to select relevant tools</div>
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
            <div style={descStyle}>Minimum count below which no filtering occurs</div>
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
            <div style={descStyle}>Maximum tools to keep after filtering</div>
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
            <div style={descStyle}>Fraction of tools to keep (e.g. 0.8 = 80%)</div>
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
            <div style={descStyle}>Let the LLM search for filtered-out tools on demand</div>
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
      </SettingsSection>

      {/* Cost Control */}
      <SettingsSection title="Cost Control" description="Set spending limits per session or globally to manage API costs">
        <div style={rowStyle}>
          <div>
            <div style={labelStyle}>Enabled</div>
            <div style={descStyle}>Enforce spending limits on API usage</div>
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
            <div style={descStyle}>Max spend per session (0 = unlimited)</div>
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
            <div style={descStyle}>Max total spend across all sessions (0 = unlimited)</div>
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
      </SettingsSection>

      {/* Notifications */}
      <SettingsSection title="Notifications" description="Get notified when Claude needs your attention or finishes a task">
        <div style={rowStyle}>
          <div>
            <div style={labelStyle}>Slack Notifications</div>
            <div style={descStyle}>Send Slack messages on stop, permission prompts, and idle events</div>
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

        {/* Setup guide — shown when enabled but not yet configured */}
        {config.notifications.slack.enabled && !config.notifications.slack.configured && (
          <div style={{
            background: 'rgba(34,197,94,0.04)',
            border: '1px solid rgba(34,197,94,0.12)',
            borderRadius: 10,
            padding: '16px 18px',
            marginTop: 4,
            marginBottom: 8,
          }}>
            <div style={{ fontSize: 13, fontWeight: 600, color: '#e5e7eb', marginBottom: 12 }}>
              Setup Slack Webhook
            </div>
            <div style={{ fontSize: 12, color: '#9ca3af', lineHeight: 1.8 }}>
              <div style={{ marginBottom: 8 }}>
                <span style={{ color: '#22c55e', fontWeight: 600, marginRight: 8 }}>1.</span>
                <a
                  href="https://api.slack.com/apps?new_app=1"
                  target="_blank"
                  rel="noopener noreferrer"
                  style={{ color: '#22c55e', textDecoration: 'none', display: 'inline-flex', alignItems: 'center', gap: 4 }}
                >
                  Create new app <ExternalLink size={11} />
                </a>
                , then <strong style={{ color: '#d1d5db' }}>From Scratch</strong>
              </div>
              <div style={{ marginBottom: 8 }}>
                <span style={{ color: '#22c55e', fontWeight: 600, marginRight: 8 }}>2.</span>
                Add an app name and a workspace to develop your app
              </div>
              <div style={{ marginBottom: 8 }}>
                <span style={{ color: '#22c55e', fontWeight: 600, marginRight: 8 }}>3.</span>
                On the left side choose <strong style={{ color: '#d1d5db' }}>Incoming Webhooks</strong>
              </div>
              <div style={{ marginBottom: 8 }}>
                <span style={{ color: '#22c55e', fontWeight: 600, marginRight: 8 }}>4.</span>
                Activate by turning the slider
              </div>
              <div style={{ marginBottom: 8 }}>
                <span style={{ color: '#22c55e', fontWeight: 600, marginRight: 8 }}>5.</span>
                On the bottom of the page click on <strong style={{ color: '#d1d5db' }}>Add New Webhook</strong>
              </div>
              <div style={{ marginBottom: 8 }}>
                <span style={{ color: '#22c55e', fontWeight: 600, marginRight: 8 }}>6.</span>
                Select a channel and authorize
              </div>
              <div style={{ marginBottom: 12 }}>
                <span style={{ color: '#22c55e', fontWeight: 600, marginRight: 8 }}>7.</span>
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
                borderRadius: 8,
                color: '#f3f4f6',
                padding: '10px 14px',
                fontSize: 13,
                fontFamily: "'JetBrains Mono', monospace",
                outline: 'none',
                boxSizing: 'border-box',
              }}
            />
          </div>
        )}

        {/* Configured indicator */}
        {config.notifications.slack.enabled && config.notifications.slack.configured && (
          <div style={{
            display: 'flex',
            alignItems: 'center',
            gap: 8,
            padding: '8px 0 4px',
            fontSize: 12,
            color: '#6b7280',
          }}>
            <span style={{
              width: 6, height: 6, borderRadius: '50%',
              background: '#22c55e', display: 'inline-block',
            }} />
            <span>Webhook configured: <span style={{ fontFamily: "'JetBrains Mono', monospace", color: '#9ca3af' }}>{config.notifications.slack.webhook_url}</span></span>
          </div>
        )}
      </SettingsSection>

      {/* Monitoring */}
      <SettingsSection title="Monitoring" description="Enable telemetry logs for session analysis and debugging">
        <div style={rowStyle}>
          <div>
            <div style={labelStyle}>Telemetry</div>
            <div style={descStyle}>Write session telemetry to JSONL log files</div>
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
      </SettingsSection>
    </div>
  )
}

export default SettingsTab
