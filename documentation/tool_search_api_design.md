# Tool Search Behind API (Design Note)

This note describes how to implement `gateway_search_tools` fully behind your backend API while keeping the proxy thin and deterministic.

## Goal

- Keep proxy-side relevance filtering.
- Move tool search/ranking decisions to backend API.
- Let proxy orchestrate loop + reinjection, but never do local search ranking.

## High-Level Flow

1. Proxy receives request with full `tools[]`.
2. Tool discovery filters tools (relevance) and stores deferred tools in session context.
3. Proxy injects phantom tool `gateway_search_tools` into forwarded `tools[]`.
4. Model calls `gateway_search_tools(query)`.
5. Proxy sends deferred tools + query to backend API.
6. Backend returns selected tool names (ranked/top-k).
7. Proxy injects matched full tool definitions (from stored `raw_json`) into next forwarded request.
8. Loop continues until model no longer calls search tool (or max loop reached).
9. Proxy strips phantom tool calls/results from final response.

## Proxy Responsibilities

- Session state:
  - store `deferred_tools` per session
  - store `expanded_tools` for force-keep on later turns
- Phantom orchestration:
  - detect search tool calls
  - append tool_result messages
  - re-forward request
- Safety:
  - max loop limit (e.g., 5)
  - dedupe injected tool names
  - hide phantom artifacts from client response
- Fallback:
  - API timeout/error/invalid response => restore all deferred tools (fail-open)

## Backend API Contract

Endpoint:

- `POST /v1/tool-discovery/search`

Request:

```json
{
  "pattern": "find files with auth middleware",
  "top_k": 5,
  "always_keep": ["read_file"],
  "tools": [
    {
      "name": "search_code",
      "description": "Search for code patterns",
      "definition": { "type": "function", "function": { "name": "search_code" } }
    }
  ]
}
```

Response:

```json
{
  "selected_names": ["search_code", "read_file"]
}
```

Rules:

- `selected_names` must be subset of request tool names.
- Preserve rank order (most relevant first).
- Respect `top_k`.
- Never return empty unless intentionally signaling “no useful match”.

## Proxy Validation Rules for API Responses

- Reject/ignore names not in deferred set.
- If all returned names are unknown or empty, treat as non-meaningful and fail-open.
- Cap final injected tools at `max_search_results`.

## Latency Budget

Total added latency per search call:

- `T_added = T_api + T_loop_reforward`

Practical target:

- backend selector p95 <= 150 ms
- API timeout 1-2 s (hard cap)
- keep `max_search_results` small (3-5)

## Recommended Rollout

1. Dark launch:
  - call backend API, log decisions, but do not alter tool list.
2. Shadow compare:
  - compare backend picks vs current relevance picks.
3. Partial traffic:
  - enable for small percentage of sessions.
4. Full traffic:
  - enable globally, keep fail-open fallback.

## Observability

Log per request:

- query
- deferred tool count
- API latency/status
- selected names
- fallback reason (if any)
- loop count

Track metrics:

- selector success rate
- empty/invalid selection rate
- average tools reinjected
- added latency p50/p95/p99

## Security and Data Handling

- Send only required tool metadata (`name`, `description`, optional minimal definition).
- Redact sensitive tool descriptions if needed.
- Authenticate backend API (service token / mTLS).
- Enforce request size caps.

## Minimal Config You Need in Proxy

```yaml
pipes:
  tool_discovery:
    enabled: true
    strategy: api
    api:
      endpoint: "https://<your-backend>/v1/tool-discovery/search"
      api_key: "${TOOL_DISCOVERY_API_KEY}"
      timeout: 2s
    max_search_results: 5
```

If you keep current code behavior where `api` falls back to relevance, re-enable the phantom search path first, then wire it strictly to backend API selection.
