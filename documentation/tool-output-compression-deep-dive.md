# Tool Output Compression Deep Dive

## Purpose

This document explains how tool output compression currently works in Context Gateway, what is enabled by default, all relevant configuration options, runtime expectations, and the tradeoffs of each approach.

It focuses on the **tool output compression pipe** (`pipes.tool_output`) and its interaction with the gateway request/response loop.

## Current State (As Implemented)

- Tool output compression is **implemented**.
- Tool output compression is **disabled by default** in production config (`configs/preemptive_summarization.yaml`).
- The default production setup prioritizes **preemptive summarization** and leaves compression pipes off.

Relevant files:
- `configs/preemptive_summarization.yaml`
- `internal/pipes/tool_output/tool_output.go`
- `internal/pipes/tool_output/tool_output_expand.go`
- `internal/pipes/tool_output/types.go`
- `internal/store/store.go`
- `internal/gateway/router.go`
- `internal/gateway/handler.go`

## High-Level Architecture

The tool output compression system is a request-side transformation plus an optional internal expansion loop.

### 1. Routing

Gateway routes a request to one pipe by priority:

1. If tool results are present, use `tool_output` pipe.
2. Else if tools list is present, use `tool_discovery` pipe.
3. Else passthrough.

Routing is content-based and adapter-driven.

Reference:
- `internal/gateway/router.go`

### 2. Adapter-Driven Extraction and Apply

Pipes do not parse provider JSON directly. They delegate to provider adapters:

- `ExtractToolOutput(body)`
- `ApplyToolOutput(body, results)`
- `ExtractUserQuery(body)`

This keeps compression logic provider-agnostic.

Reference:
- `internal/adapters/adapter.go`
- `internal/pipes/tool_output/tool_output.go`

### 3. Compression Decision Pipeline (Per Tool Output)

For each extracted tool output:

1. Check `skip_tools` resolution for current provider.
2. Skip if size `<= min_bytes`.
3. Skip if size `> max_bytes`.
4. Build deterministic `shadow_id` from content hash.
5. Attempt compressed cache hit (`GetCompressed`).
6. If cache miss, store original short-TTL content and queue compression.
7. Run compression strategy.
8. Keep compressed result only if strictly smaller than original.
9. Store compressed value in long-TTL cache.
10. Patch request with compressed + shadow prefix.

References:
- `internal/pipes/tool_output/tool_output.go`
- `internal/pipes/tool_output/skip_tools.go`
- `internal/pipes/tool_output/types.go`

### 4. Shadow Prefix Contract

Compressed content visible to model is prefixed:

```text
<<<SHADOW:shadow_xxx>>>
<compressed content>
```

This gives a stable retrieval key for later expansion.

Reference:
- `internal/pipes/tool_output/types.go`

### 5. Dual-TTL Store

Store keeps two representations:

- Original content: short TTL (used for expansion requests).
- Compressed content: long TTL (improves repeated cache reuse/KV stability).

Defaults in code:
- original: 5 minutes
- compressed: 24 hours

Reference:
- `internal/store/store.go`
- `internal/pipes/tool_output/types.go`

### 6. Phantom `expand_context` Tool

If enabled and compressed refs exist, gateway injects a hidden `expand_context` tool into the forwarded request.

If the model calls it:

1. Gateway intercepts call internally.
2. Looks up original content by `shadow_id`.
3. Appends tool result message internally.
4. Re-forwards to provider.
5. Repeats up to loop limit.

The phantom tool is filtered from final response; the client should not see it.

References:
- `internal/pipes/tool_output/tool_output_expand.go`
- `internal/gateway/handler.go`
- `internal/gateway/phantom_loop.go`

## Streaming vs Non-Streaming Behavior

### Non-Streaming

- Normal phantom loop behavior is used.
- Gateway can iterate multiple forwards (max loop count) to satisfy expansion requests.

### Streaming

- Gateway buffers stream chunks and detects suppressed `expand_context` calls.
- If expansion is requested, it rewrites history with full content and retries upstream.
- It filters phantom calls from outward stream.

Practical implication:
- When expansion is needed during streaming, user-perceived streaming can become less immediate because buffering/retry occurs.

Reference:
- `internal/gateway/handler.go`
- `internal/pipes/tool_output/stream_buffer.go`

## Strategies (What You Can Choose)

`pipes.tool_output.strategy` supports:

1. `passthrough`
2. `simple`
3. `api`
4. `external_provider`

Reference:
- `internal/pipes/config.go`

### 1) `passthrough`

Behavior:
- No compression; body forwarded unchanged.

Pros:
- Lowest risk.
- No extra latency/cost from compression calls.
- Simplest operationally.

Cons:
- No prompt/token reduction from tool outputs.

### 2) `simple`

Behavior:
- Aggressive truncation-like strategy (first N words) intended for testing.

Pros:
- Useful for force-testing expansion paths.
- No external service dependency.

Cons:
- Not semantically robust.
- Easy information loss.
- Not intended as production quality compression.

Reference:
- `internal/pipes/tool_output/simple_compressor.go`

### 3) `api`

Behavior:
- Calls configured compression API endpoint.

Pros:
- Centralized compression behavior.
- Easier policy consistency across deployments.

Cons:
- Adds network dependency.
- Added request latency per compressed output.
- If service unavailable, fallback quality depends on config.

Reference:
- `internal/pipes/tool_output/tool_output.go` (`compressViaAPI`)

### 4) `external_provider`

Behavior:
- Calls LLM provider directly (OpenAI/Anthropic/Gemini via shared external client).
- Uses provider endpoint/model/key or captured OAuth bearer token fallback.

Pros:
- No dependency on Compresr API service.
- Flexible provider/model choice.
- Works with OAuth capture scenarios.

Cons:
- Extra LLM cost for compression step.
- Extra latency.
- Compression quality and determinism vary by model/provider.

References:
- `internal/pipes/tool_output/tool_output.go` (`compressViaExternalProvider`)
- `external/llm.go`
- `external/prompts.go`

## Fallback Strategy

`fallback_strategy` controls behavior when primary compression fails.

Current practical path:
- `passthrough` fallback preserves original content and request completion.

Operational guidance:
- For production, use `fallback_strategy: passthrough` unless you explicitly want failures to propagate.

Reference:
- `internal/pipes/tool_output/tool_output.go` (`compressOne`)

## Configuration Surface

`pipes.tool_output` fields:

- `enabled`: boolean.
- `strategy`: `passthrough | simple | api | external_provider`.
- `fallback_strategy`.
- `provider`: provider reference from top-level `providers`.
- `api.endpoint`.
- `api.api_key`.
- `api.model`.
- `api.timeout`.
- `api.query_agnostic`.
- `min_bytes`.
- `max_bytes`.
- `target_ratio`.
- `enable_expand_context`.
- `include_expand_hint`.
- `skip_tools`.

References:
- `internal/pipes/config.go`
- `internal/pipes/tool_output/types.go`

## Important Nuances and Expectations

### 1) Not All Config Fields Are Equally Active

- `target_ratio` is present in config and state, but current tool output logic does not enforce it as a hard compression gate.
- `include_expand_hint` is present in config/state but not actively applied in current compression output path.

If you expected direct behavior from these fields, verify before relying on them for policy.

### 2) Header Threshold vs Pipe Threshold

- `X-Compression-Threshold` is parsed into context.
- Tool output compression currently makes decisions with `min_bytes`/`max_bytes`.

So for tool output behavior today, tune byte thresholds in pipe config.

References:
- `internal/gateway/handler.go`
- `internal/pipes/pipe.go`
- `internal/pipes/tool_output/tool_output.go`

### 3) Compression Is Opportunistic

If compressed output is not smaller than original, gateway keeps original and marks as passthrough/skipped. This avoids negative “compression”.

Reference:
- `internal/pipes/tool_output/tool_output.go`

### 4) Deterministic Shadow IDs

Shadow IDs come from hash of content. Repeated identical content maps to identical IDs, improving cache reuse behavior.

Reference:
- `internal/pipes/tool_output/tool_output.go` (`contentHash`)

### 5) Loop Limits Prevent Infinite Cycles

Both expand loop and generic phantom loop are capped (max 5 loops).

References:
- `internal/pipes/tool_output/types.go` (`MaxExpandLoops`)
- `internal/gateway/phantom_loop.go` (`MaxPhantomLoops`)

### 6) Missing/Expired Shadow Data

If a model requests expansion for unknown/expired `shadow_id`, tool result returns an error-like content message internally. This can reduce answer quality for that turn but avoids hard crash.

Reference:
- `internal/pipes/tool_output/tool_output_expand.go` (`CreateExpandResultMessages`)

## Skip Tools (`skip_tools`) Behavior

`skip_tools` accepts generic categories (`read`, `edit`, `write`, `bash`, `glob`, `grep`) mapped to provider-specific tool names.

- Anthropic/Bedrock mappings are explicitly defined.
- Other providers may fall back to “all known names for category.”
- Unknown categories are treated as exact tool names for backward compatibility.

Reference:
- `internal/pipes/tool_output/skip_tools.go`

## Rate Limiting and Concurrency

Compression tasks are processed in parallel with protections:

- Max concurrent compressions (default 10).
- Token-bucket rate limit (default 20/sec).

Expect tradeoff:
- Better protection under load.
- Potential delayed compression under bursty tool output traffic.

Reference:
- `internal/pipes/tool_output/types.go`
- `internal/pipes/tool_output/tool_output.go`

## Authentication and Provider Resolution

Compression auth source can come from:

1. `provider` reference (preferred).
2. Inline `api` config.
3. For `external_provider`: captured incoming bearer token fallback when API key absent (notably OAuth workflow).

References:
- `internal/pipes/tool_output/types.go`
- `internal/config/providers.go`
- `external/llm.go`

## Monitoring and Observability Expectations

Compression events are tracked via:

- In-memory metrics on pipe (`cache hits/misses`, compression success/fail, bytes saved).
- Gateway telemetry/comparison logs when enabled.
- Compression detail logs (`compression.jsonl`) when monitoring config enables it.

References:
- `internal/pipes/tool_output/types.go`
- `internal/gateway/handler.go`
- `configs/preemptive_summarization.yaml` (monitoring section)

## Failure Modes and Tradeoffs

### A. Compression Service/Provider Failures

Symptoms:
- Timeouts or errors on compression call.

Behavior:
- With passthrough fallback, request still succeeds with original content.

Tradeoff:
- Reliability over savings.

### B. Over-Compression Quality Loss

Symptoms:
- Model misses key details in tool output.

Mitigations:
- Enable expand context.
- Increase `min_bytes` so fewer items are compressed.
- Use higher-quality model/prompts for external strategy.

Tradeoff:
- Better answer quality vs less token reduction.

### C. Streaming Expansion Latency

Symptoms:
- Stream feels less immediate when expansion occurs.

Cause:
- Buffer, detect, rewrite, retry sequence.

Tradeoff:
- Correctness and hidden phantom behavior vs strict real-time streaming continuity.

### D. Store TTL Expiry

Symptoms:
- `shadow_id` requested but original missing.

Mitigation:
- Increase effective original TTL if your flows naturally need longer expansion window.

Tradeoff:
- More memory retention vs fewer expansion misses.

## Recommended Operating Patterns

### Conservative Production Pattern

Use when stability is priority:

- `enabled: true`
- `strategy: external_provider` or `api`
- `fallback_strategy: passthrough`
- `min_bytes`: relatively high (reduce over-compression)
- `enable_expand_context: true` if quality risk is a concern
- monitor compression logs before tightening thresholds

### Minimal-Risk Rollout Pattern

- Enable on limited environment first.
- Track compression ratio, failures, and expansion frequency.
- Tune `min_bytes`, `max_bytes`, `skip_tools` before broad rollout.

## Example Config Snippets

### 1) Disabled (Current Default)

```yaml
pipes:
  tool_output:
    enabled: false
```

### 2) External Provider with Safe Fallback

```yaml
providers:
  anthropic:
    auth: "oauth"
    model: "claude-haiku-4-5-20251001"

pipes:
  tool_output:
    enabled: true
    strategy: "external_provider"
    fallback_strategy: "passthrough"
    provider: "anthropic"
    min_bytes: 2048
    max_bytes: 65536
    enable_expand_context: true
    skip_tools: ["edit", "write"]
```

### 3) API Strategy

```yaml
pipes:
  tool_output:
    enabled: true
    strategy: "api"
    fallback_strategy: "passthrough"
    api:
      endpoint: "https://your-compression-service/v1/compress/tool-output"
      api_key: "${COMPRESR_API_KEY}"
      model: "cmprsr_tool_output_v1"
      timeout: 30s
      query_agnostic: true
    min_bytes: 2048
    max_bytes: 65536
    enable_expand_context: true
```

## Testing Checklist

When enabling in a new environment:

1. Confirm route hits `tool_output` for tool-result requests.
2. Confirm small outputs pass through unchanged.
3. Confirm large outputs above `max_bytes` pass through.
4. Confirm compressed outputs carry `<<<SHADOW:...>>>` prefix.
5. Confirm expansion works for non-streaming requests.
6. Confirm streaming behavior when expansion is triggered.
7. Confirm fallback path on forced compression timeout/error.
8. Confirm logs/telemetry reflect cache hit/miss and mapping status.

## Summary

Tool output compression in this codebase is robustly structured and feature-complete, but intentionally disabled by default in current production config. The system is designed around deterministic shadow references, adapter abstraction, safe fallback behavior, and hidden internal expansion tooling.

The main practical decision is not whether compression exists, but **which strategy to run and how aggressively to tune it** given latency, cost, and answer-quality requirements.
