# Tool Discovery Deep Diagnosis

This report analyzes why tool discovery can appear "not working" in the current codebase.
It is based only on repository code and tests.

## Executive Findings

1. In many normal runs, tool discovery is simply disabled by configuration defaults.
2. For OpenAI/Codex Responses-style tool schemas, extraction currently returns no tools, so the route never enters the tool discovery pipe.
3. In `api` strategy, failures are intentionally fail-open (return original request), which makes problems look like "feature inactive."
4. Search fallback only exists for `tool-search` strategy; it is explicitly disabled for `relevance` and `api`.
5. With low log verbosity, all of the above can happen with minimal visible signal.

## End-to-End Flow (What Must Be True)

Tool discovery only runs if all of these conditions hold:
- `pipes.tool_discovery.enabled == true`
- Router can extract at least one tool definition from request body
- Strategy is not passthrough/empty
- Strategy-specific execution succeeds (or, for `api`, returns meaningful selection)

If any condition fails, requests pass through unchanged.

## Detailed Root Causes

### 1) Disabled by default in shipped configs and wizard defaults

Evidence:
- `cmd/configs/preemptive_summarization.yaml:141` sets `tool_discovery.enabled: false`
- `cmd/configs/default_config.yaml:102` sets `tool_discovery.enabled: false`
- Wizard state defaults to disabled: `cmd/agent_wizard.go:201`

Impact:
- If you are using defaults, tool discovery never executes.

### 2) OpenAI adapter only extracts Chat Completions-style tool definitions

Evidence:
- OpenAI extraction expects `tools[].function.name`: `internal/adapters/openai.go:291`
- If `function` object is absent, tool is skipped.
- Stub test explicitly expects empty extraction for schema like `{"type":"function","name":"read_file"}`:
  `tests/openai/unit/adapter_test.go:122`

Impact:
- For Responses-style tool schema (commonly used by Codex/OpenAI Responses API), router may see zero tools and skip tool discovery entirely.

### 3) Router only routes to tool discovery when extraction returns tools

Evidence:
- Route condition: `internal/gateway/router.go:96` to `internal/gateway/router.go:99`

Impact:
- Any adapter extraction miss results in complete bypass of tool discovery logic.

### 4) `api` strategy is fail-open and often silently becomes no-op

Evidence:
- Compresr client requires API key: `internal/compresr/client.go:308`
- Compresr client requires non-empty query: `internal/compresr/client.go:313`
- Pipe catches API errors and returns original request:
  `internal/pipes/tool_discovery/tool_discovery.go:327` to `internal/pipes/tool_discovery/tool_discovery.go:330`
- Empty API selection also returns original request:
  `internal/pipes/tool_discovery/tool_discovery.go:334` to `internal/pipes/tool_discovery/tool_discovery.go:336`

Impact:
- Missing key, empty query extraction, API issues, or conservative backend selection all appear as "tool discovery does nothing."

### 5) Strategy behavior differs from user expectation for search fallback

Evidence:
- Search fallback forced off for `relevance`: `internal/pipes/tool_discovery/tool_discovery.go:115`
- Search fallback forced off for `api`: `internal/pipes/tool_discovery/tool_discovery.go:118`
- Only `tool-search` forces search fallback on: `internal/pipes/tool_discovery/tool_discovery.go:121`
- Non-streaming handler only sets search loop when strategy is `tool-search`:
  `internal/gateway/handler.go:389`

Impact:
- If expecting `gateway_search_tools` during `relevance`/`api`, it will never appear.

### 6) Enabled + missing strategy can still become passthrough

Evidence:
- Config validation treats empty strategy as valid passthrough:
  `internal/pipes/config.go:201`
- Runtime process returns original for passthrough:
  `internal/pipes/tool_discovery/tool_discovery.go:206`

Impact:
- `enabled: true` alone is not sufficient; strategy must be explicit and supported.

## Why this feels like a bug in practice

Multiple fail-open gates stack together:
- default disabled
- schema mismatch extraction
- API fail-open
- low/disabled logging

So from user perspective, behavior is often indistinguishable from "feature not wired," even though the code path exists.

## Quick Validation Checklist

1. Confirm active config actually has:
   - `pipes.tool_discovery.enabled: true`
   - `pipes.tool_discovery.strategy: relevance|api|tool-search`
2. Verify your request payload's `tools` schema:
   - If OpenAI and tools are not under `function`, current extraction likely misses them.
3. If using `api` strategy:
   - Ensure `COMPRESR_API_KEY` is present
   - Ensure query extraction is non-empty
4. Turn on logging:
   - `monitoring.log_level: info` (or `debug`) to surface skip/fallback warnings.

## Recommended Fix Order (Code)

1. Add OpenAI Responses-style tool extraction support in:
   - `internal/adapters/openai.go`
   - extraction and apply paths should handle both:
     - `tools[].function.{name,description,parameters}`
     - `tools[].{name,description,parameters}`
2. Add explicit telemetry counters/reasons for tool-discovery bypass:
   - "disabled", "no_tools_extracted", "api_error", "empty_selection", "passthrough_strategy"
3. Make fail-open behavior visible in response headers (optional debug mode).
4. Tighten config ergonomics:
   - warn when `enabled=true` and `strategy` empty.

## Bottom Line

The strongest likely root cause for "not working" with Codex/OpenAI is schema mismatch in OpenAI tool extraction, combined with defaults that keep tool discovery off and fail-open API behavior that hides errors.
