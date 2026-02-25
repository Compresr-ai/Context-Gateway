# Tool Discovery Forensic Analysis (V2)

This is a deeper, code-trace diagnosis of why tool discovery can look inactive.
It focuses on runtime behavior, not intended design.

## 1. Runtime Activation Chain (Where It Can Short-Circuit)

Tool discovery only runs when all gates pass:
1. Config enables the pipe.
2. Router successfully extracts tool definitions from request.
3. Strategy path executes and returns modified request.
4. For `api` strategy, external selector returns meaningful names.

Relevant code:
- Route gate: `internal/gateway/router.go:96`
- Pipe process gate: `internal/pipes/tool_discovery/tool_discovery.go:206`

## 2. High-Confidence Failure Modes

### A) Disabled by defaults in several entry points

- Predefined configs ship disabled:
  - `cmd/configs/preemptive_summarization.yaml:141`
  - `cmd/configs/default_config.yaml:102`
- Wizard default state is disabled:
  - `cmd/agent_wizard.go:201`

Consequence:
- Most out-of-box runs never execute tool discovery.

### B) OpenAI tool schema mismatch (Responses-style not supported)

OpenAI extraction expects:
- `tools[].function.name`

Code:
- `internal/adapters/openai.go:291`
- Parsed apply path also assumes nested `function` object:
  - `internal/adapters/openai.go:628`

Evidence in tests:
- Stub test expects empty extraction for tool form `{"type":"function","name":"read_file"}`:
  - `tests/openai/unit/adapter_test.go:122`

Consequence:
- For Codex/Responses-style tool definitions without nested `function`, router sees no tools and bypasses tool discovery.

### C) API strategy has explicit fail-open behavior

If selector fails, request is returned unchanged:
- `internal/pipes/tool_discovery/tool_discovery.go:327` to `internal/pipes/tool_discovery/tool_discovery.go:330`
- Empty selected list also returns unchanged:
  - `internal/pipes/tool_discovery/tool_discovery.go:334`

Compresr selector prerequisites:
- API key required: `internal/compresr/client.go:308`
- Query required: `internal/compresr/client.go:313`

Consequence:
- Any auth/query/service issue appears as "feature did nothing" rather than explicit failure.

### D) `provider` config for tool discovery is validated but effectively unused in runtime API path

Validation allows `provider` as alternative:
- `internal/pipes/config.go:210`

But runtime initialization for tool discovery API mode uses:
- `cfg.URLs.Compresr` + `cfg.Pipes.ToolDiscovery.API.APISecret`
- `internal/pipes/tool_discovery/tool_discovery.go:163`
- no provider resolution call analogous to tool output path.

Consequence:
- Configs relying on `pipes.tool_discovery.provider` may validate but still run with missing credentials.

### E) `api.endpoint`/`api.model` are effectively ignored by Compresr client path

`tool_discovery.New` computes `apiEndpoint`, but `filterByAPI` calls:
- `p.callToolSelectionAPI(...)` -> `compresrClient.FilterTools(...)`
- Client posts to fixed `"/api/compress/tool-discovery/"` with default model unless set in params:
  - `internal/compresr/client.go:349`
  - model default: `internal/compresr/client.go:323`

No model from `pipes.tool_discovery.api.model` is passed into `FilterToolsParams` in current code.

Consequence:
- Operators may think endpoint/model config is applied when it is not.

### F) `tool-search` strategy likely breaks for OpenAI Responses response format

Phantom loop OpenAI parser expects Chat Completions response shape:
- `choices[0].message.tool_calls`
- `internal/gateway/phantom_loop.go:237`

Search tool response filtering for OpenAI also expects `choices[].message.tool_calls`:
- `internal/gateway/search_tool_handler.go:556`

Consequence:
- In Responses API style flows, gateway may not detect phantom calls, so tool-search recovery won't trigger as expected.

### G) Common no-op by thresholds even when enabled

Relevance strategy explicitly skips filtering when:
- `totalTools <= minTools`:
  - `internal/pipes/tool_discovery/tool_discovery.go:564`
- computed keep count >= total:
  - `internal/pipes/tool_discovery/tool_discovery.go:586`

Consequence:
- With small tool sets or conservative thresholds, output is unchanged by design.

## 3. Config Source Pitfalls That Mask Diagnosis

### A) Embedded configs vs file configs

`serve` resolves from local `configs/*.yaml`, then embedded defaults:
- `cmd/main.go:117`
- embedded config loader: `cmd/embedded.go:13`

If local config file is absent, embedded config is used.

### B) Wizard-generated custom config default Compresr URL differs from examples

Wizard generator writes:
- `${COMPRESR_BASE_URL:-https://api.compresr.com}`
- `cmd/agent_yaml.go:193`

Other repo examples use:
- `https://api.compresr.ai` (for example `cmd/configs/default_config.yaml`)

Consequence:
- If env var is unset and `.com` endpoint is not serving expected API, selector fails and API strategy degrades to pass-through.

## 4. Strategy Behavior Matrix (Actual)

- `relevance`
  - local scoring filter
  - no external API
  - no search fallback injection (forced off): `internal/pipes/tool_discovery/tool_discovery.go:115`
- `api`
  - external selector via Compresr client
  - fail-open on errors/empty selection
  - no search fallback injection (forced off): `internal/pipes/tool_discovery/tool_discovery.go:118`
- `tool-search`
  - replaces tools with `gateway_search_tools`
  - search fallback forced on: `internal/pipes/tool_discovery/tool_discovery.go:121`
  - requires phantom loop support in provider response format

## 5. Why It Feels Broken (Observed UX Pattern)

The system is currently optimized for safety and continuity:
- multiple fail-open returns,
- thresholds that skip filtering,
- defaults with discovery disabled,
- and low logging defaults (`monitoring.log_level: off` in sample configs).

This creates a "silent pass-through" profile where misconfiguration and unsupported formats are not obvious.

## 6. Highest-Value Fixes (Ordered)

1. Add dual-schema OpenAI tool discovery support:
   - support both `tools[].function.*` and `tools[].{name,description,parameters}` in extract/apply parsed+unparsed paths.
2. Add OpenAI Responses-format phantom parsing in `phantom_loop` and `SearchToolHandler.FilterFromResponse`.
3. Align tool discovery API runtime with config semantics:
   - respect `provider` resolution, `api.model`, and endpoint override.
4. Emit explicit telemetry reason codes for bypass/fail-open outcomes.
5. Normalize wizard default Compresr URL to the canonical domain used by shipped configs.
