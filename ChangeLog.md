# 📋 Compresr Context Gateway — Changelog


## 🚀 v0.1.0 — Initial Release (Feb 10, 2026)
> The very first public release of the Context Gateway.

🔹 Core proxy architecture between AI agents and LLM APIs
🔹 **Tool output compression** — automatically compress large tool results before they consume context
🔹 **Expand context** — intelligent context expansion with reranker support
🔹 **Preemptive summarization** — background history compaction so agents never wait
🔹 **Slack notifications** — rich formatting, smart filtering, and debounce
🔹 **Session management** — isolated session directory per agent invocation
🔹 **Trajectory logging** in ATIF format
🔹 CI/CD pipeline with linting, security scanning, and integration tests
🔹 SWE-bench evaluation harness
🔹 Go 1.23 upgrade


## 🧙 v0.2.0 — Multi-Port Gateway & TUI Wizard (Feb 12, 2026)
> Major UX overhaul and provider expansion.

🔹 **Multi-port gateway** — run parallel terminals, each with its own port
🔹 **TUI wizard** — interactive terminal setup for agent selection and provider config
🔹 **Claude Code OAuth** — use OAuth as API key fallback
🔹 **OpenRouter provider** — route through OpenRouter for model flexibility
🔹 **OpenCode & OpenCode Zen** — first-class open-source agent support
🔹 **Docker support** — automated image publishing
🔹 Shared `CallLLM()` with Gemini support
🔹 Trigger-threshold UI (default 85%)
🔹 Simplified Slack webhook setup
🔹 Update/uninstall commands in the gateway binary


## 🔌 v0.3.0 — Adapters & Provider Expansion (Feb 13, 2026)
> Broad provider support and new compression features.

🔹 **AWS Bedrock adapter** — first-class Bedrock provider
🔹 **Ollama adapter** — run local models through the gateway
🔹 **Gemini adapter** — direct Google Gemini support
🔹 **Explicit auth field** for provider configs
🔹 **skip_tools** — provider-aware category mapping for tool output compression
🔹 **OAuth bearer token capture** for compression requests
🔹 **Pass-through agent args** via `--` separator
🔹 SSRF hardening and security improvements


## 🔍 v0.4.0 — Tool Discovery & Cost Control (Feb 19, 2026)
> Intelligence layer for tool management and budget enforcement.

🔹 **Tool discovery pipe** — relevance-based tool filtering
🔹 **Aggregate cost tracking** with budget enforcement
🔹 Windows cross-compilation fix


## ⚙️ v0.4.1 — Centralized Defaults & Stability (Feb 21, 2026)
> Internal architecture improvements.

🔹 **Centralized defaults** — single source of truth for all config
🔹 **Graceful shutdown** — clean process termination
🔹 **Type consolidation** — unified type system
🔹 Compresr API model selection in wizard
🔹 Client refactor into types/subscription/client modules


## ☁️ v0.4.2 — Compresr API Compression (Feb 25, 2026)
> New compression strategy powered by our own API.

🔹 **Compresr API compression** — use Compresr cloud for conversation history compression
🔹 Gemini & Compresr E2E integration tests
🔹 Codex integration improvements
🔹 SSRF localhost protection and security fixes


## 📊 v0.4.3 — Dashboard & Savings (Feb 27, 2026)
> Visibility into gateway performance.

🔹 **Dashboard** — compression savings, usage stats, and session info
🔹 **Savings tracking** — see how much context and cost the gateway saves
🔹 **Usage limits** — configurable caps
🔹 HCC Lingua integration
🔹 Internal `api` → `compresr` package rename


## 🔒 v0.4.4 — Security Hardening & OAuth Fix (Feb 28, 2026)
> Critical security pass and OAuth reliability.

🔹 **Security hardening** — XSS fixes, restricted `/expand`, bounded streams
🔹 **OAuth URL fix** — corrected base URL to `api.compresr.ai`
🔹 **Centralized URL constants**
🔹 Status bar improvements
🔹 WebSocket auth updated for onboarding


## ⚡ v0.4.5 — Preemptive V2 & Open Tool Discovery (Mar 4, 2026)
> Smarter compression and tool injection.

🔹 **Preemptive summarization V2** — improved background compaction
🔹 **Open tool discovery** — always inject `expand_context` with usage hints
🔹 **Provider model in compression logs**
🔹 **Tiktoken token counting** — proper `cl100k_base` tokenizer replaces byte estimates
🔹 Cross-platform binary builds
🔹 Proxy comparison benchmarks


## 🏗️ v0.5.0 — Multi-Agent Compression Gateway (Mar 5, 2026)
> The gateway grows up — now a true multi-agent platform.

🔹 **Multi-agent support** — manage multiple agents from one gateway
🔹 **Target Compression Ratio** setting in wizard
🔹 Onboarding auth flow improvements


## 🧹 v0.5.1 — Stability & Onboarding (Mar 6, 2026)
> Polish pass before the next major release.

🔹 Onboarding update and auth flow changes
🔹 Handler updates across auth, gateway, and preemptive modules
🔹 Linting and security fixes


## 🔥 v0.5.2 — Phantom Tools, Dashboard V2 & LiteLLM (In Progress)
> The biggest feature drop since v0.2.0.

🔹 **Phantom tools** — virtual tool injection without modifying the agent's tool list
🔹 **LiteLLM adapter** — connect any LiteLLM-compatible model
🔹 **Dashboard V2** — redesigned with monitoring, message classification, real-time stats
🔹 **Hot-reload config** — change settings without restarting
🔹 **Codex/Responses API** — OpenAI Responses API with SSE streaming
🔹 **Structured data prefix preservation** in tool output compression
🔹 **always_keep guarantee** — critical tools never filtered out
🔹 **expand_context fix for OpenAI models**
🔹 **bypass_cost_check** config option
🔹 **Just-passthrough mode** for transparent proxying
🔹 14-bug audit fix pass
🔹 Comprehensive test coverage improvements
