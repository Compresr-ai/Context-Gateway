# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

A transparent LLM proxy that sits between AI agents (Claude Code, Cursor, etc.) and provider APIs. Two main features:

1. **Preemptive Summarization** (primary, production): Background summarization of conversation history before the context window fills up. When the client requests compaction, the summary is already ready — zero wait time.
2. **Compression Pipes** (tool output compression): Compresses tool outputs in-flight before forwarding to providers. Uses a phantom `expand_context` tool to let the LLM retrieve originals on demand.

## Quick Commands

```bash
make build          # Build binary to bin/gateway
make test           # Run all tests
make lint           # golangci-lint
make fmt            # Format code
make security       # gosec + govulncheck
make coverage       # Tests with HTML coverage report
make dev            # Build + run with default config (preemptive_summarization.yaml)
make dev-debug      # Same but with debug logging

# Run a single test
go test ./tests/anthropic/unit/... -run TestSpecificName -v

# Run the gateway server directly
make run-config CONFIG=configs/preemptive_summarization.yaml
```

## Running Tests

```bash
# Unit tests (no API keys needed)
go test ./tests/anthropic/unit/... -v
go test ./tests/openai/unit/... -v
go test ./tests/common/... -v

# Integration tests (need ANTHROPIC_API_KEY, COMPRESR_API_KEY)
go test ./tests/anthropic/integration/... -v

# Benchmarks
go test ./tests/anthropic/performance/... -bench=.
```

## CLI Subcommands

The binary (`context-gateway` or `bin/gateway`) supports:

- `(default)` — Launch agent with interactive selection (gateway + Claude Code). Replaces old `start_agent.sh`.
- `serve` — Start the gateway proxy server only. Flags: `--config`, `--debug`, `--no-banner`.
- `update` — Self-update to latest version.
- `uninstall` — Remove context-gateway.
- `version` — Print version.

Config resolution order for `serve`: user `--config` flag → `~/.config/context-gateway/configs/` → local `configs/` → embedded config.

## Architecture

### Preemptive Summarization Flow (primary feature)

```
Client → Gateway:18080
  1. Every request: parse messages, identify session (hash of first user message)
  2. Track token usage as % of model's context window
  3. At 85% usage: start background Haiku summarization (non-blocking)
  4. Normal requests continue forwarded unchanged
  5. When client requests compaction:
     a. Cache HIT → return precomputed summary instantly (synthetic response)
     b. Job pending → wait for background job to finish
     c. Cache MISS → synchronous summarization (fallback)
  6. Result: [Summary of M1..Mn] + [Recent messages as originals]
```

Key files: `internal/preemptive/manager.go` (orchestrator), `worker.go` (background jobs), `summarizer.go` (Haiku API calls), `session.go` (session tracking + fuzzy matching), `detector.go` (compaction request detection).

### Compression Pipeline Flow (tool output compression)

```
Client → Gateway:8080
  1. IdentifyAndGetAdapter(path, headers) → provider + adapter
  2. Router.Route(ctx) picks ONE pipe by priority:
       P1: tool_result messages → ToolOutput pipe
       P2: tools[] present     → ToolDiscovery pipe (stub)
       else                    → passthrough
  3. Pipe.Process(): adapter.Extract*() → compress → adapter.Apply*()
  4. If compressed & non-streaming: inject expand_context phantom tool
  5. Forward modified request to X-Target-URL
  6. Expand loop (up to 5x): if LLM calls expand_context, retrieve original, re-forward
  7. Filter expand_context from final response
  8. Return to client
```

## Key Interfaces

**Adapter** (`internal/adapters/adapter.go`): Provider-specific Extract/Apply pairs for each pipe type. Pipes never contain provider logic — they delegate to adapters. All provider detection goes through `IdentifyAndGetAdapter()` in `provider_identification.go`.

**Pipe** (`internal/pipes/pipe.go`): `Process(*PipeContext) ([]byte, error)` — receives request body, returns modified body. Both tool_output (compression) and tool_discovery (relevance filtering) are implemented.

**Store** (`internal/store/store.go`): Dual-TTL shadow context storage. Original content: 5 min TTL (for expand_context). Compressed content: 24h TTL (for KV-cache reuse).

**Preemptive Manager** (`internal/preemptive/manager.go`): `ProcessRequest(headers, body, model, provider)` → handles session tracking, background summarization triggers, and compaction responses. Integrated into the gateway handler.

## Key Design Rules

- **Pipes are provider-agnostic**: They call adapter.Extract/Apply, never parse provider formats directly
- **No config defaults in YAML**: All configuration must be explicit
- **Phantom tool**: `expand_context` is injected into requests and filtered from responses — the client never sees it
- **Content-hash shadow IDs**: `SHA256(content)` → deterministic IDs for cache dedup
- **Session matching is hierarchical**: First user message hash → fuzzy match (message count + recency) → legacy hash fallback
- **Summaries are not refreshed**: After trigger, summary covers M1..Mn. Newer messages are appended as originals during compaction.

## Config Files

- `configs/preemptive_summarization.yaml` — **Default production config**. Preemptive summarization only, compression pipes disabled.
- `configs/tool_output_passthrough.yaml` — No compression, pure proxy
- `configs/tool_output_compresr_api_0.5.yaml` — API compression via Compresr service
- `configs/tool_output_reranker_api.yaml` — Reranker-based compression
- `configs/templates/` — Templates for all pipe types

Environment variables in configs use `${VAR:-default}` syntax. Session log paths are injected via env vars (`SESSION_TELEMETRY_LOG`, `SESSION_TRAJECTORY_LOG`, etc.).

## Environment Variables (.env)

Required: `ANTHROPIC_API_KEY`
Required for API compression: `COMPRESR_BASE_URL`, `COMPRESR_API_KEY`
Optional: `OPENAI_API_KEY`, `CURSOR_API_KEY`, `QWEN_CODER_API_KEY`
Slack notifications: `SLACK_WEBHOOK_URL` (or legacy: `SLACK_BOT_TOKEN` + `SLACK_CHANNEL_ID`)

Loaded from `~/.config/context-gateway/.env` then local `.env` (local overrides).

## Slack Notifications

Enable Slack notifications via the config wizard (`gateway` → enable Slack) — it opens your browser and guides you through creating a webhook.

- **`Stop`** — fires immediately when Claude finishes responding
- **`Notification`** — fires on `permission_prompt` (tool approval needed) and `idle_prompt` (60s idle fallback)

See `docs/slack-setup.md` for manual setup.

## CI (GitHub Actions)

4 jobs: **Lint** → **Tests** (unit + integration) → **Security** (gosec, govulncheck, secret scan) → **Build**

## Unimplemented (Stubs)

(None — all adapters and pipes are implemented)

## Logging

Session logs go to `logs/session_<n>_<date>/`:
- `trajectory.json` — ATIF v1.6 format, agent trajectory
- `telemetry.jsonl` — Per-request proxy telemetry
- `compression.jsonl` — Per-tool compression details
- `compaction.jsonl` — Preemptive summarization events
- `summary.json` — End-of-session aggregate
- `gateway.log` — Server runtime logs

## Dependencies (go.mod)

Go 1.23. zerolog (logging), uuid, godotenv, testify (testing), yaml.v3.
