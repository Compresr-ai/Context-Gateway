export interface Session {
  id: string
  cost: number
  cap: number
  request_count: number
  model: string
  created_at: string
  last_updated: string
}

export interface Savings {
  total_requests: number
  compressed_requests: number
  tokens_saved: number
  token_saved_pct: number
  cost_saved_usd: number
  original_cost_usd: number
  compressed_cost_usd: number
  compression_ratio: number
  // Tool discovery
  tool_discovery_requests?: number
  original_tool_count?: number
  filtered_tool_count?: number
  tool_discovery_tokens?: number
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

export interface GatewayStats {
  uptime: string
  total_requests: number
  successful_requests: number
  compressions: number
  cache_hits: number
  cache_misses: number
}

export interface DashboardData {
  sessions: Session[] | null
  total_cost: number
  total_requests: number
  session_cap: number
  global_cap: number
  enabled: boolean
  savings?: Savings
  expand?: ExpandContext
  gateway?: GatewayStats
}

export interface AccountData {
  available: boolean
  tier?: string
  credits_remaining_usd: number
  credits_used_this_month: number
  monthly_budget_usd: number
  usage_percent: number
  requests_today: number
  requests_this_month: number
  is_admin: boolean
  error?: string
}
