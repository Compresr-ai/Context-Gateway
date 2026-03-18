import { useState, useEffect, useCallback, useRef } from 'react'
import { Save, ExternalLink } from 'lucide-react'
import type { GatewayConfig, ConfigPatch } from '../types'
import SettingsSection from './SettingsSection'
import CustomSelect from './CustomSelect'
import Toggle from './Toggle'

const preemptiveStrategies = [
  { value: 'compresr', label: 'Compresr API' },
  { value: 'external_provider', label: 'External Provider' },
]

const toolOutputStrategies = [
  { value: 'compresr', label: 'Compresr API' },
  { value: 'external_provider', label: 'External Provider' },
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
  const [validationErrors, setValidationErrors] = useState<Record<string, string>>({})
  const toastTimeout = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Validate config and return errors
  const validateConfig = useCallback((cfg: GatewayConfig): Record<string, string> => {
    const errors: Record<string, string> = {}
    
    // Preemptive trigger_threshold: 1-99
    if (cfg.preemptive.trigger_threshold < 1 || cfg.preemptive.trigger_threshold > 99) {
      errors['preemptive.trigger_threshold'] = 'Must be between 1 and 99'
    }
    
    // Tool Output min_tokens: >= 0
    if (cfg.pipes.tool_output.min_tokens < 0 || !Number.isFinite(cfg.pipes.tool_output.min_tokens)) {
      errors['pipes.tool_output.min_tokens'] = 'Must be 0 or greater'
    }
    
    // Tool Output target_compression_ratio: 0.1-0.9
    if (cfg.pipes.tool_output.target_compression_ratio < 0.1 || cfg.pipes.tool_output.target_compression_ratio > 0.9) {
      errors['pipes.tool_output.target_compression_ratio'] = 'Must be between 0.1 and 0.9'
    }
    
    // Tool Discovery token_threshold: >= 0
    if (cfg.pipes.tool_discovery.token_threshold < 0 || !Number.isFinite(cfg.pipes.tool_discovery.token_threshold)) {
      errors['pipes.tool_discovery.token_threshold'] = 'Must be 0 or greater'
    }
    
    // Cost Control session_cap: >= 0
    if (cfg.cost_control.session_cap < 0 || !Number.isFinite(cfg.cost_control.session_cap)) {
      errors['cost_control.session_cap'] = 'Must be 0 or greater'
    }
    
    // Cost Control global_cap: >= 0
    if (cfg.cost_control.global_cap < 0 || !Number.isFinite(cfg.cost_control.global_cap)) {
      errors['cost_control.global_cap'] = 'Must be 0 or greater'
    }
    
    return errors
  }, [])

  // Update validation when config changes
  useEffect(() => {
    if (config) {
      setValidationErrors(validateConfig(config))
    }
  }, [config, validateConfig])

  const hasValidationErrors = Object.keys(validationErrors).length > 0

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
    if (hasValidationErrors) {
      showToast('Fix validation errors before saving')
      return
    }
    setSaving(true)
    try {
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

  const errorInputStyle: React.CSSProperties = {
    ...inputStyle,
    border: '1px solid #ef4444',
    background: 'rgba(239,68,68,0.1)',
  }

  const getInputStyle = (field: string) => validationErrors[field] ? errorInputStyle : inputStyle

  const errorTextStyle: React.CSSProperties = {
    fontSize: 11,
    color: '#ef4444',
    marginTop: 4,
  }

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
          border: hasValidationErrors ? '1px solid rgba(239,68,68,0.3)' : '1px solid rgba(34,197,94,0.2)',
          borderRadius: 12,
          padding: '12px 18px',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          boxShadow: '0 4px 20px rgba(0,0,0,0.4)',
        }}>
          <span style={{ fontSize: 13, color: hasValidationErrors ? '#ef4444' : '#9ca3af', fontFamily: "'Inter', system-ui, sans-serif" }}>
            {hasValidationErrors 
              ? `Fix ${Object.keys(validationErrors).length} validation error${Object.keys(validationErrors).length > 1 ? 's' : ''} before saving`
              : 'You have unsaved changes'}
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
              disabled={saving || hasValidationErrors}
              style={{
                background: (saving || hasValidationErrors) ? 'rgba(22,163,74,0.3)' : 'linear-gradient(135deg, #16a34a, #22c55e)',
                border: 'none',
                borderRadius: 8,
                padding: '7px 18px',
                color: (saving || hasValidationErrors) ? 'rgba(255,255,255,0.5)' : '#fff',
                fontSize: 13,
                fontWeight: 600,
                cursor: (saving || hasValidationErrors) ? 'not-allowed' : 'pointer',
                fontFamily: "'Inter', system-ui, sans-serif",
                display: 'flex',
                alignItems: 'center',
                gap: 6,
                boxShadow: hasValidationErrors ? 'none' : '0 0 20px rgba(34,197,94,0.15)',
              }}
            >
              <Save size={14} />
              {saving ? 'Saving...' : 'Save Changes'}
            </button>
          </div>
        </div>
      )}

      {/* ── Preemptive Summarization ── */}
      <SettingsSection
        title="Preemptive Summarization"
        description="Proactively compresses conversation history before you hit the context limit"
        defaultOpen
        enabled={config.preemptive.enabled}
        onToggle={() => setConfig({ ...config, preemptive: { ...config.preemptive, enabled: !config.preemptive.enabled } })}
      >
        <div style={rowStyle}>
          <div>
            <div style={labelStyle}>Trigger Threshold (%)</div>
            <div style={descStyle}>Context usage % that triggers background summarization</div>
          </div>
          <div>
            <input
              type="number"
              min={1}
              max={99}
              step={1}
              value={config.preemptive.trigger_threshold}
              style={getInputStyle('preemptive.trigger_threshold')}
              onChange={(e) => {
                setConfig({ ...config, preemptive: { ...config.preemptive, trigger_threshold: Number(e.target.value) } })
              }}
            />
            {validationErrors['preemptive.trigger_threshold'] && (
              <div style={errorTextStyle}>{validationErrors['preemptive.trigger_threshold']}</div>
            )}
          </div>
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

      {/* ── Tool Output Compression ── */}
      <SettingsSection
        title="Tool Output Compression"
        description="Compresses large tool outputs (file reads, search results) to save context space"
        enabled={config.pipes.tool_output.enabled}
        onToggle={() => setConfig({
          ...config,
          pipes: { ...config.pipes, tool_output: { ...config.pipes.tool_output, enabled: !config.pipes.tool_output.enabled } },
        })}
      >
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
            <div style={labelStyle}>Min Tokens</div>
            <div style={descStyle}>Skip outputs shorter than this many tokens (default 512)</div>
          </div>
          <div>
            <input
              type="number"
              min={0}
              step={64}
              value={config.pipes.tool_output.min_tokens || 512}
              style={getInputStyle('pipes.tool_output.min_tokens')}
              onChange={(e) => {
                setConfig({
                  ...config,
                  pipes: { ...config.pipes, tool_output: { ...config.pipes.tool_output, min_tokens: Number(e.target.value) } },
                })
              }}
            />
            {validationErrors['pipes.tool_output.min_tokens'] && (
              <div style={errorTextStyle}>{validationErrors['pipes.tool_output.min_tokens']}</div>
            )}
          </div>
        </div>
        <div style={rowStyle}>
          <div>
            <div style={labelStyle}>Target Compression Ratio</div>
            <div style={descStyle}>Fraction of tokens to remove (0.3 = remove 30%, keep 70%)</div>
          </div>
          <div>
            <input
              type="number"
              min={0.1}
              max={0.9}
              step={0.05}
              value={config.pipes.tool_output.target_compression_ratio}
              style={getInputStyle('pipes.tool_output.target_compression_ratio')}
              onChange={(e) => {
                setConfig({
                  ...config,
                  pipes: { ...config.pipes, tool_output: { ...config.pipes.tool_output, target_compression_ratio: Number(e.target.value) } },
                })
              }}
            />
            {validationErrors['pipes.tool_output.target_compression_ratio'] && (
              <div style={errorTextStyle}>{validationErrors['pipes.tool_output.target_compression_ratio']}</div>
            )}
          </div>
        </div>
      </SettingsSection>

      {/* ── Tool Discovery ── */}
      <SettingsSection
        title="Tool Discovery"
        description="Filters irrelevant tool definitions from requests to reduce token usage"
        enabled={config.pipes.tool_discovery.enabled}
        onToggle={() => setConfig({
          ...config,
          pipes: { ...config.pipes, tool_discovery: { ...config.pipes.tool_discovery, enabled: !config.pipes.tool_discovery.enabled } },
        })}
      >
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
            <div style={labelStyle}>Tool Token Limit</div>
            <div style={descStyle}>Trigger filtering when total tool definitions exceed this many tokens (default 512)</div>
          </div>
          <div>
            <input
              type="number"
              min={0}
              step={500}
              value={config.pipes.tool_discovery.token_threshold || 512}
              style={getInputStyle('pipes.tool_discovery.token_threshold')}
              onChange={(e) => {
                setConfig({
                  ...config,
                  pipes: { ...config.pipes, tool_discovery: { ...config.pipes.tool_discovery, token_threshold: Number(e.target.value) } },
                })
              }}
            />
            {validationErrors['pipes.tool_discovery.token_threshold'] && (
              <div style={errorTextStyle}>{validationErrors['pipes.tool_discovery.token_threshold']}</div>
            )}
          </div>
        </div>
        <div style={rowStyle}>
          <div>
            <div style={labelStyle}>Tool Description Compression</div>
            <div style={descStyle}>Stage 2: compress returned tool schemas using the tool output endpoint (toc_latter)</div>
          </div>
          <Toggle
            enabled={config.pipes.tool_discovery.enable_tool_description_compression}
            onToggle={() => setConfig({
              ...config,
              pipes: {
                ...config.pipes,
                tool_discovery: {
                  ...config.pipes.tool_discovery,
                  enable_tool_description_compression: !config.pipes.tool_discovery.enable_tool_description_compression,
                },
              },
            })}
          />
        </div>
        <div style={rowStyle}>
          <div>
            <div style={labelStyle}>Compress Search Results</div>
            <div style={descStyle}>Compress tool schemas returned by gateway_search_tools</div>
          </div>
          <Toggle
            enabled={config.pipes.tool_discovery.search_result_compression.enabled}
            onToggle={() => setConfig({
              ...config,
              pipes: { ...config.pipes, tool_discovery: { ...config.pipes.tool_discovery, search_result_compression: { ...config.pipes.tool_discovery.search_result_compression, enabled: !config.pipes.tool_discovery.search_result_compression.enabled } } },
            })}
          />
        </div>
      </SettingsSection>

      {/* ── Cost Control ── */}
      <SettingsSection
        title="Cost Control"
        description="Set spending limits per session or globally to manage API costs"
        enabled={config.cost_control.enabled}
        onToggle={() => setConfig({
          ...config,
          cost_control: { ...config.cost_control, enabled: !config.cost_control.enabled },
        })}
      >
        <div style={rowStyle}>
          <div>
            <div style={labelStyle}>Session Cap (USD)</div>
            <div style={descStyle}>Max spend per session (0 = unlimited)</div>
          </div>
          <div>
            <input
              type="number"
              min={0}
              step={0.5}
              value={config.cost_control.session_cap}
              style={getInputStyle('cost_control.session_cap')}
              onChange={(e) => {
                setConfig({
                  ...config,
                  cost_control: { ...config.cost_control, session_cap: Number(e.target.value) },
                })
              }}
            />
            {validationErrors['cost_control.session_cap'] && (
              <div style={errorTextStyle}>{validationErrors['cost_control.session_cap']}</div>
            )}
          </div>
        </div>
        <div style={rowStyle}>
          <div>
            <div style={labelStyle}>Global Cap (USD)</div>
            <div style={descStyle}>Max total spend across all sessions (0 = unlimited)</div>
          </div>
          <div>
            <input
              type="number"
              min={0}
              step={0.5}
              value={config.cost_control.global_cap}
              style={getInputStyle('cost_control.global_cap')}
              onChange={(e) => {
                setConfig({
                  ...config,
                  cost_control: { ...config.cost_control, global_cap: Number(e.target.value) },
                })
              }}
            />
            {validationErrors['cost_control.global_cap'] && (
              <div style={errorTextStyle}>{validationErrors['cost_control.global_cap']}</div>
            )}
          </div>
        </div>
      </SettingsSection>

      {/* ── Notifications ── */}
      <SettingsSection
        title="Notifications"
        description="Get notified when Claude needs your attention or finishes a task"
        enabled={config.notifications.slack.enabled}
        onToggle={() => setConfig({
          ...config,
          notifications: { ...config.notifications, slack: { ...config.notifications.slack, enabled: !config.notifications.slack.enabled } },
        })}
      >
        {/* Setup guide — shown when enabled but not yet configured */}
        {!config.notifications.slack.configured && (
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
        {config.notifications.slack.configured && (
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

      {/* ── Monitoring ── */}
      <SettingsSection
        title="Monitoring"
        description="Write session telemetry to JSONL log files"
        enabled={config.monitoring.telemetry_enabled}
        onToggle={() => setConfig({
          ...config,
          monitoring: { ...config.monitoring, telemetry_enabled: !config.monitoring.telemetry_enabled },
        })}
      >
        <div style={{ fontSize: 12, color: '#6b7280', padding: '4px 0', fontFamily: "'Inter', system-ui, sans-serif" }}>
          When enabled, each session writes telemetry.jsonl, tool_output_compression.jsonl, and tool_discovery.jsonl for analysis and debugging.
        </div>
      </SettingsSection>
    </div>
  )
}

export default SettingsTab
