# Authentication Deep Dive

This document describes how authentication works in the gateway today, based on current implementation.

## Scope

Authentication behavior in this repo appears in four main areas:

1. Request forwarding to upstream LLMs (`internal/gateway/handler.go`)
2. Subscription/OAuth fallback logic (`internal/gateway/auth_fallback.go`)
3. Preemptive summarization auth (`internal/preemptive/summarizer.go`)
4. Tool output compression auth for external providers (`internal/pipes/tool_output/tool_output.go`)

It also depends on provider configuration rules in `internal/config/providers.go`.

## Auth Modes and Terminology

The gateway distinguishes these auth modes:

- `api_key`: explicit key headers (`x-api-key`, `x-goog-api-key`, `api-key`) or API-style bearer keys
- `subscription`: currently Anthropic subscription OAuth token pattern (`Bearer sk-ant-oat...`)
- `bearer`: generic bearer token not classified as subscription
- `none`: no auth headers present

Mode detection is implemented in `detectAuthMode` in `internal/gateway/auth_fallback.go`.

## Case 1: Standard API Key Pass-Through

### Incoming request

Client sends one of:

- Anthropic: `x-api-key`
- OpenAI-compatible: `Authorization: Bearer <key>`
- Gemini: `x-goog-api-key` (or `api-key`)

### Gateway behavior

In `forwardPassthrough`:

1. Detect target URL (from `X-Target-URL` or auto-detection)
2. Build upstream request
3. Forward relevant auth headers unchanged:
   - `Authorization`
   - `x-api-key`
   - `x-goog-api-key`
   - `api-key`
   - provider-specific headers like `anthropic-version`, `anthropic-beta`
4. Send request upstream

No auth rewriting is done unless sticky fallback mode is active (Case 3).

## Case 2: Subscription/OAuth Token Pass-Through (Anthropic)

### Incoming request

Anthropic subscription traffic commonly arrives as:

- `Authorization: Bearer sk-ant-oat...`

`detectAuthMode` classifies this as `subscription`.

### Gateway behavior

On first attempt, gateway forwards the bearer token unchanged.

If upstream succeeds, request stays in subscription mode.

## Case 3: Subscription -> API Key Fallback (Anthropic Only)

This is currently the only explicit automatic auth fallback path.

### Preconditions

Fallback is eligible only when all are true:

1. Provider supports fallback (currently only Anthropic)
2. Incoming auth was classified as `subscription`
3. A fallback API key is configured for that provider in `providers.<name>.api_key`

### First attempt

Gateway sends request with original subscription bearer token.

### Exhaustion detection

If response indicates likely quota/rate/subscription exhaustion, gateway retries once with API key.

Signals are provider-specific and include:

- status codes (for Anthropic: `429`, `529`, `402`)
- body patterns like `rate limit`, `quota exceeded`, `billing`, `subscription`

Matching logic: `isLikelySubscriptionExhausted` in `internal/gateway/auth_fallback.go`.

### Sticky session mode

When fallback triggers, gateway marks the session as "API key mode" for 1 hour TTL:

- session key: `preemptive.ComputeSessionID(body)` (hash of first user message)
- store: `authFallbackStore`

Subsequent requests for that session go directly with API key (gateway removes `Authorization`, sets `x-api-key`).

### Important limits

- Fallback is provider-gated; only Anthropic is enabled now.
- Session ID can be empty (for malformed/non-standard bodies), in which case stickiness is not applied.
- Stickiness is in-memory per gateway instance.

## Case 4: Bedrock Authentication (SigV4)

Bedrock does not use API keys in the gateway forwarding path.

When Bedrock is enabled and request path matches Bedrock patterns:

1. Gateway uses `BedrockSigner` to SigV4-sign upstream request
2. API key forwarding is skipped for that path

For preemptive summarization with provider `bedrock`, `external.CallLLM` uses an HTTP client with signing transport.

## Case 5: Preemptive Summarizer Authentication

Preemptive summarization can authenticate two ways:

1. Configured summarizer API key (`preemptive.summarizer.api_key`)
2. Captured auth token from incoming request (when no summarizer key configured)

### Capture path

In `handleProxy`, before preemptive processing:

- if incoming `x-api-key` exists, capture that token
- else if incoming `Authorization` exists, capture bearer token value
- capture endpoint (`X-Target-URL` or auto-detected endpoint)

Manager forwards these to summarizer via:

- `SetAuthToken(token, isFromXAPIKeyHeader)`
- `SetEndpoint(endpoint)`

### Call path

In summarizer `callAPI`:

- endpoint chosen with `getEndpoint()`
- auth token chosen with `getAuthToken()` (configured key first, then captured token)
- request sent via `external.CallLLM`

### Current implementation nuance

Summarizer tracks whether captured token came from `x-api-key` vs `Authorization`, but `callAPI` currently passes token through `APISecret` field. For Anthropic in `external.CallLLM`, `APISecret` maps to `x-api-key`.

Practical implication: header-origin metadata is recorded, but not fully used to preserve original header type at call time.

### Concurrency and scope

Summarizer captured auth/endpoint fields are mutex-protected, but shared at summarizer instance level (not keyed per session). This is thread-safe for memory access, but operationally "latest captured token/endpoint wins" across concurrent sessions.

## Case 6: Tool Output External Provider Authentication

When `pipes.tool_output.strategy: external_provider` is used:

1. Pipe uses configured provider endpoint/model/key if present
2. If no configured key and request has captured bearer token, it uses bearer fallback
3. For Anthropic OAuth compatibility, it forwards captured `anthropic-beta` header too

This path is separate from main request forwarding auth and is used for compression API calls made by the pipe itself.

## Provider Configuration Rules

Provider-level auth rules are validated in `internal/config/providers.go`:

- `auth` allowed values: `api_key`, `oauth`, `bedrock`
- if `auth: oauth`, `api_key` must be empty
- if `auth: bedrock`, `api_key` must be empty
- default auth mode is `api_key` when omitted

Also note:

- API key presence validation is intentionally relaxed for some flows because auth can be captured from inbound requests.

## Header Forwarding Summary

Main forwarding path (`forwardPassthrough`) forwards:

- `Content-Type`
- `Authorization`
- `x-api-key`
- `x-goog-api-key`
- `api-key`
- `anthropic-version`
- `anthropic-beta`

Except in sticky fallback API-key mode, where `Authorization` is removed and `x-api-key` is set to fallback key.

## Auth State, Storage, and TTL

- Subscription fallback sticky mode:
  - Store: in-memory map
  - Key: session ID from first user message hash
  - TTL: 1 hour
  - Cleanup: periodic

- Preemptive captured auth/endpoint:
  - Store: summarizer instance fields
  - Protection: RW mutex
  - TTL: no explicit TTL (replaced on new capture)

## Failure and Edge Cases

1. Missing target URL:
   - If neither `X-Target-URL` nor auto-detection succeeds, forwarding fails.

2. Unsupported host:
   - Request rejected by SSRF allowlist before upstream call.

3. Subscription fallback unavailable:
   - If no provider API key configured, gateway cannot retry with API key.

4. Session ID unavailable:
   - Fallback retry may still happen once, but sticky mode cannot persist without session ID.

5. Multi-instance deployments:
   - Sticky auth fallback state is instance-local (not shared across replicas).

## Concurrency Characteristics Related to Auth

- Gateway handles requests concurrently.
- Auth fallback store uses `sync.RWMutex`.
- Preemptive summarizer auth capture uses `sync.RWMutex`.
- Tool output compression spawns concurrent workers and has its own semaphore/rate limiter.

Concurrency is protected for race safety, but some auth state is global-instance scoped rather than request/session scoped.

## Code References

- `internal/gateway/handler.go` (`forwardPassthrough`, request capture before preemptive)
- `internal/gateway/auth_fallback.go` (mode detection, exhaustion matching, sticky fallback)
- `internal/config/providers.go` (provider auth config and validation)
- `internal/preemptive/manager.go` and `internal/preemptive/summarizer.go` (captured auth usage)
- `internal/pipes/tool_output/tool_output.go` (external provider auth fallback)
- `external/llm.go` (provider-specific auth header mapping)
