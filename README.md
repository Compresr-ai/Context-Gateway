<p align="center">
  <img src="https://compresr.ai/logo.png" alt="Compresr" width="200"/>
</p>

<p align="center">
  <b>Instant history compaction and context optimization for AI agents</b>
</p>


<p align="center">
  <a href="https://compresr.ai">Website</a> •
  <a href="https://compresr.ai/docs">Docs</a> •
  <a href="https://discord.gg/PeaVWNjT">Discord</a>
</p>

---

# Context Gateway

**[Compresr](https://compresr.ai)** is a YC-backed company building LLM prompt compression and context optimization.

Context Gateway sits between your AI agent (Claude Code, Cursor, etc.) and the LLM API. When your conversation gets too long, it compresses history **in the background** so you never wait for compaction.

## Quick Start

```bash
# Install gateway binary
curl -fsSL https://compresr.ai/api/install | sh

# Then select an agent (opens interactive TUI wizard)
context-gateway
```

The TUI wizard will help you:
- Choose an agent (claude_code, cursor, openclaw, or custom)
- Create/edit configuration: 
  - Summarizer model and api key
  - enable slack notifications if needed
  - Set trigger threshold for compression (default: 75%)

Supported agents:
- **claude_code**: Claude Code IDE integration
- **cursor**: Cursor IDE integration  
- **openclaw**: Open-source Claude Code alternative
- **custom**: Bring your own agent configuration

## Supported Providers

Context Gateway supports all major LLM providers:

| Provider | Format | Detection |
|----------|--------|-----------|
| **Anthropic** (Claude) | Native | Auto-detected via `anthropic-version` header |
| **OpenAI** (GPT, o1, o3) | Native | Auto-detected via path `/v1/chat/completions` |
| **Google Gemini** | Native | Auto-detected via `x-goog-api-key` header |
| **AWS Bedrock** | Native | Auto-detected via Bedrock URL patterns |
| **MiniMax** (M2.5) | OpenAI-compatible | Via `X-Provider: minimax` header |
| **Ollama** | OpenAI-compatible | Auto-detected via `/api/chat` path |
| **LiteLLM** | OpenAI-compatible | Via `X-Provider: litellm` header |
| **OpenRouter** | OpenAI-compatible | Via `X-Provider: openrouter` header |

To use MiniMax with Context Gateway, set the `X-Provider: minimax` header and configure `MINIMAX_API_KEY` in your environment. MiniMax supports the `MiniMax-M2.5` and `MiniMax-M2.5-highspeed` models with 204K context window. Learn more at [MiniMax Platform](https://platform.minimax.io).

## What you'll notice

- **No more waiting** when conversation hits context limits
- Compaction happens instantly (summary was pre-computed in background)
- Check `logs/history_compaction.jsonl` to see what's happening

## Contributing

We welcome contributions! Please join our [Discord](https://discord.gg/PeaVWNjT) to contribute.
