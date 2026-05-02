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

## What you'll notice

- **No more waiting** when conversation hits context limits
- Compaction happens instantly (summary was pre-computed in background)
- Check `logs/history_compaction.jsonl` to see what's happening


## ❓ FAQ

### General

**What is Context Gateway?**
Context Gateway is an agentic proxy that sits between your AI agent (Claude Code, Cursor, etc.) and the LLM API. It provides instant history compaction and context optimization, so your conversation never stalls when hitting context limits.

**How is it different from built-in context management?**
Built-in compaction pauses your workflow while summarizing. Context Gateway compresses history **in the background**, so compaction is instant when triggered — you never wait.

### Installation & Setup

**How do I install Context Gateway?**
Run the installer script:
```bash
curl -fsSL https://compresr.ai/api/install | sh
```
Then run `context-gateway` to launch the interactive TUI wizard for configuration.

**Which agents are supported?**
- **claude_code**: Claude Code IDE integration
- **cursor**: Cursor IDE integration
- **openclaw**: Open-source Claude Code alternative
- **custom**: Bring your own agent configuration

**What are the system requirements?**
A modern Linux/macOS system with internet access. The gateway binary is lightweight (~10MB) and runs as a local proxy.

### Configuration

**How do I configure the summarizer model?**
During the TUI wizard setup, you'll be prompted to select a summarizer model and provide its API key. Common choices include Claude, GPT-4o-mini, or any model with a chat completions endpoint.

**What is the compression trigger threshold?**
The default threshold is 75% — when your conversation reaches 75% of the context window, the gateway pre-computes a summary in the background. You can adjust this in the configuration file.

**Can I enable notifications?**
Yes. The gateway supports Slack notifications when compression occurs. Enable it in the configuration during setup or edit the config file directly.

### Troubleshooting

**The gateway isn't intercepting my agent's requests. What should I check?**
1. Verify the gateway is running: `context-gateway status`
2. Check your agent's proxy settings point to the gateway's local address
3. Review logs: `logs/history_compaction.jsonl`

**I'm getting authentication errors from the summarizer model.**
Ensure your API key is correctly set in the configuration. You can re-run the TUI wizard with `context-gateway` to update credentials.

**Compression seems slow. Is this normal?**
Background compaction is pre-computed, so it should be instant when triggered. If you're experiencing delays, check:
- Your summarizer model's API response time
- Network connectivity to the model provider
- The size of your conversation history

**Where can I get help?**
Join our [Discord](https://discord.gg/PeaVWNjT) community or check the [documentation](https://compresr.ai/docs).
## Contributing

We welcome contributions! Please join our [Discord](https://discord.gg/PeaVWNjT) to contribute.
