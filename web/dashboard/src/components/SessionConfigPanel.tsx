import { useState, useEffect, useCallback, useRef } from 'react'
import { Save, X, ExternalLink } from 'lucide-react'
import type { GatewayConfig } from '../types'
import CustomSelect from './CustomSelect'
import SettingsSection from './SettingsSection'
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
    if (hasValidationErrors) {
      showToast('Fix validation errors before saving')
      return
    }
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

  const errorInputStyle: React.CSSProperties = {
    ...inputStyle,
    border: '1px solid #ef4444',
    background: 'rgba(239,68,68,0.1)',
  }

  const getInputStyle = (field: string) => validationErrors[field] ? errorInputStyle : inputStyle

  const errorTextStyle: React.CSSProperties = {
    fontSize: 10,
    color: '#ef4444',
    marginTop: 3,
  }

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
        <div style={{ flex: 1, overflowY: 'auto', padding: '16px 24px', display: 'flex', flexDirection: 'column', gap: 12 }}>
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
            <>
              {/* ── Preemptive Summarization ── */}
              <SettingsSection
                title="Preemptive Summarization"
                description="Background summarization before context limit"
                defaultOpen
                enabled={config.preemptive.enabled}
                onToggle={() => setConfig({ ...config, preemptive: { ...config.preemptive, enabled: !config.preemptive.enabled } })}
              >
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Trigger Threshold (%)</div>
                    <div style={descStyle}>Context usage % that triggers summarization</div>
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
                    <div style={descStyle}>Compression engine</div>
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
                description="Compress large tool results to save context"
                enabled={config.pipes.tool_output.enabled}
                onToggle={() => setConfig({
                  ...config,
                  pipes: { ...config.pipes, tool_output: { ...config.pipes.tool_output, enabled: !config.pipes.tool_output.enabled } },
                })}
              >
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
                    <div style={labelStyle}>Target Ratio</div>
                    <div style={descStyle}>Fraction to remove (0.3 = remove 30%, keep 70%)</div>
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
                description="Filter irrelevant tool definitions from requests"
                enabled={config.pipes.tool_discovery.enabled}
                onToggle={() => setConfig({
                  ...config,
                  pipes: { ...config.pipes, tool_discovery: { ...config.pipes.tool_discovery, enabled: !config.pipes.tool_discovery.enabled } },
                })}
              >
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
                description="Set spending limits for this session"
                enabled={config.cost_control.enabled}
                onToggle={() => setConfig({
                  ...config,
                  cost_control: { ...config.cost_control, enabled: !config.cost_control.enabled },
                })}
              >
                <div style={rowStyle}>
                  <div>
                    <div style={labelStyle}>Session Cap (USD)</div>
                    <div style={descStyle}>Max per session (0 = unlimited)</div>
                  </div>
                  <div>
                    <input
                      type="number"
                      min={0}
                      step={0.5}
                      value={config.cost_control.session_cap}
                      style={getInputStyle('cost_control.session_cap')}
                      onChange={(e) => {
                        setConfig({ ...config, cost_control: { ...config.cost_control, session_cap: Number(e.target.value) } })
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
                    <div style={descStyle}>Max total (0 = unlimited)</div>
                  </div>
                  <div>
                    <input
                      type="number"
                      min={0}
                      step={0.5}
                      value={config.cost_control.global_cap}
                      style={getInputStyle('cost_control.global_cap')}
                      onChange={(e) => {
                        setConfig({ ...config, cost_control: { ...config.cost_control, global_cap: Number(e.target.value) } })
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
                description="Notify on stop, permission prompts, and idle events"
                enabled={config.notifications.slack.enabled}
                onToggle={() => setConfig({
                  ...config,
                  notifications: { ...config.notifications, slack: { ...config.notifications.slack, enabled: !config.notifications.slack.enabled } },
                })}
              >
                {!config.notifications.slack.configured && (
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
                    <div style={{ fontSize: 11, color: '#9ca3af', lineHeight: 1.8, marginBottom: 10 }}>
                      <div><span style={{ color: '#22c55e', fontWeight: 600, marginRight: 6 }}>1.</span>
                        <a href="https://api.slack.com/apps?new_app=1" target="_blank" rel="noopener noreferrer"
                          style={{ color: '#22c55e', textDecoration: 'none', display: 'inline-flex', alignItems: 'center', gap: 3 }}>
                          Create new app <ExternalLink size={10} />
                        </a>, then <strong style={{ color: '#d1d5db' }}>From Scratch</strong>
                      </div>
                      <div><span style={{ color: '#22c55e', fontWeight: 600, marginRight: 6 }}>2.</span>Add an app name and workspace</div>
                      <div><span style={{ color: '#22c55e', fontWeight: 600, marginRight: 6 }}>3.</span>Choose <strong style={{ color: '#d1d5db' }}>Incoming Webhooks</strong></div>
                      <div><span style={{ color: '#22c55e', fontWeight: 600, marginRight: 6 }}>4.</span>Activate the slider</div>
                      <div><span style={{ color: '#22c55e', fontWeight: 600, marginRight: 6 }}>5.</span>Click <strong style={{ color: '#d1d5db' }}>Add New Webhook</strong></div>
                      <div><span style={{ color: '#22c55e', fontWeight: 600, marginRight: 6 }}>6.</span>Select a channel and authorize</div>
                      <div><span style={{ color: '#22c55e', fontWeight: 600, marginRight: 6 }}>7.</span>Copy the Webhook URL and paste below</div>
                    </div>
                    <input
                      type="text"
                      placeholder="https://hooks.slack.com/services/..."
                      value={config.notifications.slack.webhook_url || ''}
                      onChange={(e) => setConfig({
                        ...config,
                        notifications: { ...config.notifications, slack: { ...config.notifications.slack, webhook_url: e.target.value } },
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
                {config.notifications.slack.configured && (
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '6px 0 2px', fontSize: 11, color: '#6b7280' }}>
                    <span style={{ width: 5, height: 5, borderRadius: '50%', background: '#22c55e', display: 'inline-block' }} />
                    <span>Webhook: <span style={{ fontFamily: "'JetBrains Mono', monospace", color: '#9ca3af' }}>{config.notifications.slack.webhook_url}</span></span>
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
                <div style={{ fontSize: 11, color: '#6b7280', padding: '4px 0', fontFamily: "'Inter', system-ui, sans-serif" }}>
                  When enabled, writes telemetry.jsonl, tool_output_compression.jsonl, and tool_discovery.jsonl for analysis and debugging.
                </div>
              </SettingsSection>
            </>
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
            <span style={{ fontSize: 12, color: hasValidationErrors ? '#ef4444' : '#9ca3af' }}>
              {hasValidationErrors 
                ? `Fix ${Object.keys(validationErrors).length} error${Object.keys(validationErrors).length > 1 ? 's' : ''}`
                : 'Unsaved changes'}
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
                disabled={saving || hasValidationErrors}
                style={{
                  background: (saving || hasValidationErrors) ? 'rgba(22,163,74,0.3)' : 'linear-gradient(135deg, #16a34a, #22c55e)',
                  border: 'none',
                  borderRadius: 8,
                  padding: '6px 16px',
                  color: (saving || hasValidationErrors) ? 'rgba(255,255,255,0.5)' : '#fff',
                  fontSize: 12,
                  fontWeight: 600,
                  cursor: (saving || hasValidationErrors) ? 'not-allowed' : 'pointer',
                  fontFamily: "'Inter', system-ui, sans-serif",
                  display: 'flex',
                  alignItems: 'center',
                  gap: 5,
                  boxShadow: hasValidationErrors ? 'none' : '0 0 16px rgba(34,197,94,0.15)',
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

export default SessionConfigPanel
