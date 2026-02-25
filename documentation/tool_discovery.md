# Tool Discovery Deep Dive

This document describes the current tool discovery behavior in Context Gateway.
It reflects runtime code paths for `passthrough`, `relevance`, `tool-search`, and `api`.

## 1. Purpose

Tool discovery transforms incoming request `tools[]` before forwarding upstream. Goals:

- reduce token footprint from large tool lists
- reduce model confusion from irrelevant tools
- optionally allow deferred tool lookup via `gateway_search_tools`

Tool discovery does not fetch tools from external registries. It only works with tools already present in the request.

## 2. Where It Runs

Router selection order:

1. If tool output messages are present and `tool_output` is enabled, `tool_output` pipe is selected.
2. Else, if tools are present and `tool_discovery` is enabled, `tool_discovery` pipe is selected.
3. Else, request passes through unchanged.

Only one pipe is selected for request-side processing.

## 3. Config Surface

`pipes.tool_discovery` fields:

- `enabled`
- `strategy`: `passthrough | relevance | tool-search | api`
- `provider` (for `api` strategy provider reference)
- `api.endpoint`
- `api.api_key`
- `api.timeout`
- `min_tools`
- `max_tools`
- `target_ratio`
- `always_keep`
- `enable_search_fallback`
- `search_tool_name`
- `max_search_results`

Behavior notes:

- `enable_search_fallback` is forced `false` for `relevance`.
- `enable_search_fallback` is forced `true` for `tool-search` and `api`.
- `search_tool_name` defaults to `gateway_search_tools`.
- `max_search_results` defaults to `5`.

## 4. Strategy Semantics

### 4.1 `passthrough`

- Returns original request unchanged.
- No filtering, no deferred storage, no phantom search loop dependency.

### 4.2 `relevance`

Local score-based filtering:

1. Parse once using `ParsedRequestAdapter`.
2. Extract tools from parsed request.
3. Skip filtering if:
   - no tools
   - `total_tools <= min_tools`
   - computed keep count `>= total_tools`
4. Extract user query from parsed request.
5. Extract recently used tools from parsed tool-output history.
6. Read `ExpandedTools` from session context.
7. Score tools, sort descending, keep top tools with force-keep rules.
8. Apply filtered list back to request.
9. Store deferred tools in request context (`ctx.DeferredTools`).

Scoring weights:

- expanded tool from prior search: `+1000`
- `always_keep`: `+100`
- recently used: `+100`
- exact tool-name match in query: `+50`
- per query-token overlap: `+10`

Keep-count:

1. `by_ratio = int(total * target_ratio)`
2. `keep = min(by_ratio, max_tools)`
3. `keep = max(keep, min_tools)`

Tokenization for overlap scoring:

- lowercase
- alphanumeric tokenization
- short words and stop words filtered

Important:

- `relevance` does not inject `gateway_search_tools`.
- deferred tools can still be stored for observability/session continuity.

### 4.3 `tool-search`

Two-phase deferred discovery:

1. Extract all tools.
2. Store all as deferred (`ctx.DeferredTools`).
3. Replace request `tools[]` with only `gateway_search_tools`.
4. Let model call `gateway_search_tools(query)`.
5. Gateway intercepts call and runs local regex/keyword matching on deferred tools.
6. Gateway appends tool-result message and reinjects matched full tool definitions.
7. Request is re-forwarded by phantom loop.

Search backend for `tool-search`:

- query treated as case-insensitive regex (`(?i)` + query)
- on invalid regex, fallback to keyword search
- searchable text includes:
  - tool name
  - tool description
  - parameter names/descriptions (extracted from stored raw tool definition)
- results capped by `max_search_results`
- `always_keep` names are always included in regex search results

### 4.4 `api`

Same two-phase flow as `tool-search` at pipe stage (defer all + search tool only), but search resolution uses backend API:

- request to selector API includes `pattern`, `top_k`, `always_keep`, and deferred tools
- response uses `selected_names`
- selected names are mapped back to deferred tools, then reinjected

Fail-open behavior:

- missing endpoint
- HTTP error/timeout
- empty/non-meaningful/unknown selection

In all these cases, handler restores all deferred tools for the model rather than hard-failing.

## 5. Phantom Tool Loop

`gateway_search_tools` is a phantom tool:

- injected by gateway
- consumed by gateway
- removed from client-visible final response

Loop behavior:

1. Forward request.
2. Parse model response for phantom tool calls.
3. Handle calls and append tool-result messages.
4. Optionally modify request (inject discovered tools).
5. Re-forward.
6. Stop when no phantom calls or max loop reached (`5`).

## 6. Session State

Tool discovery session store tracks:

- `DeferredTools`: currently deferred full tool set
- `ExpandedTools`: names discovered via search

Session details:

- session ID is derived from first user message hash
- TTL defaults to 1 hour
- cleanup interval is 5 minutes

How it is used:

- expanded tool names are loaded into `PipeContext` on later requests
- in `relevance`, expanded tools are force-prioritized (`+1000`) and effectively sticky
- in search flow, gateway combines deferred tools from session and current request for same-turn discoverability

## 7. Reinjection Mechanics

When matches are found:

- gateway injects original full tool definitions from `Metadata["raw_json"]`
- duplicate tool names are skipped
- injection preserves provider-specific original tool shape because raw definitions are reused

If raw tool JSON is missing or invalid, that tool is skipped and a warning is logged.

## 8. Provider Behavior

Tool discovery is provider-agnostic through adapters:

- OpenAI: supported
- Anthropic: supported
- Bedrock: delegates to Anthropic adapter behavior
- Gemini: tool discovery is currently stubbed/disabled behavior in adapter path

## 9. Streaming vs Non-Streaming

Current search fallback loop (`gateway_search_tools`) is wired in the non-streaming handler path.

Streaming handler currently focuses on `expand_context` behavior and does not run the same search phantom loop.

Tool discovery filtering itself still occurs request-side before forwarding in both modes.

## 10. Telemetry and Logging

Useful signals:

- total tools
- kept tools
- deferred tools
- search queries
- matched tool names
- API fallback reasons for `api` search
- loop count

Monitoring files can include:

- `tool_discovery.jsonl`
- compression/telemetry logs configured under monitoring settings

## 11. Failure Modes and Guardrails

Guardrails:

- parse/extract/apply errors degrade to original request
- unknown strategy degrades to passthrough behavior
- deterministic keep-count/scoring
- bounded search results
- bounded phantom loops (`MaxPhantomLoops = 5`)

Operational limitations:

- discovery quality depends on tool names/descriptions/schema quality
- regex-based `tool-search` can miss intent if pattern is poor
- streaming path does not currently execute search phantom loop

## 12. Example Configs

### Relevance Filtering

```yaml
pipes:
  tool_discovery:
    enabled: true
    strategy: relevance
    min_tools: 5
    max_tools: 25
    target_ratio: 0.8
    always_keep: ["read_file"]
```

### Local Deferred Search (`tool-search`)

```yaml
pipes:
  tool_discovery:
    enabled: true
    strategy: tool-search
    search_tool_name: gateway_search_tools
    max_search_results: 5
    always_keep: ["read_file"]
```

### API-Backed Deferred Search (`api`)

```yaml
pipes:
  tool_discovery:
    enabled: true
    strategy: api
    api:
      endpoint: "https://<backend>/v1/tool-discovery/search"
      api_key: "${TOOL_DISCOVERY_API_KEY}"
      timeout: 2s
    max_search_results: 5
```

## 13. Repository Defaults

Default production-oriented config keeps compression pipes disabled:

- `configs/preemptive_summarization.yaml`
- `pipes.tool_discovery.enabled: false`

Enable it explicitly to activate any behavior described above.
