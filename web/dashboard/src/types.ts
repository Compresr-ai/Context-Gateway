export interface Session {
  id: string
  cost: number
  cap: number
  request_count: number
  models: string[]
  created_at: string
  last_updated: string
  gateway_port?: number
  active?: boolean
  agent_name?: string
}

export interface Savings {
  total_requests: number
  compressed_requests: number
  tokens_saved: number
  token_saved_pct: number
  billed_spend_usd?: number
  cost_saved_usd: number
  original_cost_usd: number
  compressed_cost_usd: number
  compression_ratio: number
  user_turns?: number
  compaction_triggers?: number
  tool_search_calls?: number
  // Tool discovery
  tool_discovery_requests?: number
  original_tool_count?: number
  filtered_tool_count?: number
  tool_discovery_tokens?: number
  tool_discovery_cost_usd?: number
  tool_discovery_pct?: number
}

export interface ExpandEntry {
  timestamp: string
  request_id: string
  shadow_id: string
  found: boolean
  content_preview?: string
  content_length: number
}

export interface ExpandContext {
  total: number
  found: number
  not_found: number
  recent?: ExpandEntry[]
}

export interface SearchEntry {
  timestamp: string
  request_id: string
  session_id?: string
  query: string
  deferred_count: number
  results_count: number
  tools_found: string[]
  strategy: string
}

export interface SearchContext {
  total: number
  recent?: SearchEntry[]
}

export interface SearchLogEntry {
  event_type: string
  timestamp: string
  request_id: string
  session_id?: string
  query: string
  deferred_count: number
  results_count: number
  tools_provided: string[]
  // Stage 1 (tool selection)
  stage1_original_tokens?: number
  stage1_compressed_tokens?: number
  stage1_compression_ratio?: number
  // Stage 2 (tool description compression)
  stage2_original_tokens?: number
  stage2_compressed_tokens?: number
  stage2_compression_ratio?: number
  stage2_strategy?: string
  // End-to-end
  end_to_end_original_tokens?: number
  end_to_end_compression_ratio?: number
}

export interface InitAgentToolsEntry {
  event_type: 'init_agent_tools'
  timestamp: string
  request_id: string
  session_id?: string
  tool_count: number
  stub_count: number
  tool_names: string[]
  original_tokens: number
  compressed_tokens: number
  compression_ratio: number
}

export interface GatewayStats {
  uptime: string
  total_requests: number
  successful_requests: number
  compressions: number
  cache_hits: number
  cache_misses: number
}

export interface DashboardData {
  sessions: Session[]
  total_cost: number
  total_requests: number
  session_cap: number
  global_cap: number
  enabled: boolean
  savings?: Savings
  global_savings?: Savings
  expand?: ExpandContext
  search?: SearchContext
  gateway?: GatewayStats
  active_ports?: number[]
}

export interface AccountData {
  available: boolean
  tier?: string
  credits_remaining_usd: number
  credits_used_this_month: number
  monthly_budget_usd: number
  usage_percent: number
  is_admin: boolean
  error?: string
}

export interface PromptEntry {
  id: number
  text: string
  timestamp: string
  session_id: string
  model: string
  provider: string
  request_id: string
  agent_name?: string
}

export interface FilterOptions {
  sessions: string[]
  models: string[]
  providers: string[]
}

export interface PromptsResponse {
  prompts: PromptEntry[]
  total: number
  page: number
  limit: number
  total_pages: number
  filters: FilterOptions
}

// Monitor API types
export interface MonitorInstance {
  name: string
  port: number
  provider: string
  model: string
  status: string
  started_at: string
  last_activity_at: string
  request_count: number
  tokens_in: number
  tokens_out: number
  tokens_saved: number
  cost_usd: number
  compression_count: number
  last_user_query: string
  last_tool_used: string
  working_dir: string
}

export interface MonitorData {
  instances: MonitorInstance[]
  timestamp: string
}

// Config API types
export interface GatewayConfig {
  preemptive: {
    enabled: boolean
    trigger_threshold: number
    strategy: string
  }
  pipes: {
    tool_output: {
      enabled: boolean
      strategy: string
      min_tokens: number
      target_compression_ratio: number
    }
    tool_discovery: {
      enabled: boolean
      strategy: string
      token_threshold: number
      enable_tool_description_compression: boolean
      search_result_compression: {
        enabled: boolean
      }
    }
  }
  cost_control: {
    enabled: boolean
    session_cap: number
    global_cap: number
  }
  notifications: {
    slack: {
      enabled: boolean
      configured: boolean
      webhook_url: string
    }
  }
  monitoring: {
    telemetry_enabled: boolean
  }
}

export interface ConfigPatch {
  preemptive?: Partial<GatewayConfig['preemptive']>
  pipes?: {
    tool_output?: Partial<GatewayConfig['pipes']['tool_output']>
    tool_discovery?: Partial<GatewayConfig['pipes']['tool_discovery']>
  }
  cost_control?: Partial<GatewayConfig['cost_control']>
  notifications?: { slack?: Partial<GatewayConfig['notifications']['slack']> }
  monitoring?: Partial<GatewayConfig['monitoring']>
}
