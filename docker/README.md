# Docker E2E Test Infrastructure

Real end-to-end tests using Docker containers for providers and agent simulators.

## Quick Start

```bash
make docker-test-e2e
```

This builds all images, starts services, runs Go integration tests and agent simulators, then tears everything down.

## Architecture

```
                    +------------------+
                    |   Agent Scripts  |
                    | (curl + jq)     |
                    +--------+---------+
                             |
                    +--------v---------+
                    |  Context Gateway |
                    |   (port 18081)   |
                    +--------+---------+
                             |
              +--------------+--------------+
              |                             |
    +---------v----------+      +-----------v--------+
    |      Ollama        |      |     LiteLLM        |
    | (port 11434)       |      | (port 4000)        |
    | qwen2:0.5b model   |      | Routes to Ollama   |
    +--------------------+      +--------------------+
```

## Directory Structure

```
docker/
  Dockerfile.agents          Single Dockerfile for all agent simulators
  docker-compose.test.yaml   Orchestrates all services
  agents/                    Agent simulator scripts
    entrypoint.sh              Dispatches to per-agent script
    claude-code.sh             Anthropic Messages API format
    cursor.sh                  OpenAI Chat Completions format
    codex.sh                   OpenAI Responses API format
    openclaw.sh                Anthropic format
    opencode.sh                OpenAI format, multiple tool results
    ollama-direct.sh           Ollama native + OpenAI-compat endpoints
    litellm-proxy.sh           LiteLLM proxy with model aliases
    bedrock.sh                 Bedrock adapter via LiteLLM (X-Provider: bedrock)
  providers/                 Provider configs (official images used directly)
    ollama/
      pull-model.sh            Pulls qwen2:0.5b on startup
    litellm/
      litellm_config.yaml      Model routing config
```

## Step-by-Step

```bash
# Build images
make docker-test-build

# Start providers + gateway
make docker-test-up

# Run Go integration tests
make docker-test-go

# Run agent simulator containers
make docker-test-agents

# Tear down
make docker-test-down
```

## Go Integration Tests

Tests auto-skip when services aren't running or API keys are missing:

```bash
# All integration tests (cloud tests auto-skip without keys)
make docker-test-go

# Individual providers
go test ./tests/ollama/integration/... -v
go test ./tests/litellm/integration/... -v
go test ./tests/bedrock/integration/... -v
go test ./tests/agents/integration/... -v

# Cloud providers (require API keys)
go test ./tests/anthropic/integration/... -v
go test ./tests/openai/integration/... -v
go test ./tests/gemini/integration/... -v
```

Environment variables:
- `OLLAMA_URL` - Ollama server URL (default: `http://localhost:11434`)
- `OLLAMA_MODEL` - Model to use (default: `qwen2:0.5b`)
- `LITELLM_URL` - LiteLLM server URL (default: `http://localhost:4000`)
- `LITELLM_API_KEY` - LiteLLM master key (default: `sk-test-litellm-key`)
- `LITELLM_MODEL` - LiteLLM model alias (default: `local-model`)

## Adding a New Agent

1. Create `docker/agents/my-agent.sh`
2. Add service to `docker/docker-compose.test.yaml`:
   ```yaml
   agent-my-agent:
     build:
       context: .
       dockerfile: Dockerfile.agents
     environment:
       - AGENT_NAME=my-agent
       - GATEWAY_URL=http://gateway:18081
       - TARGET_URL=http://ollama:11434
     depends_on:
       gateway:
         condition: service_healthy
   ```

## Adding a New Provider

1. Add config/scripts to `docker/providers/my-provider/`
2. Add service to `docker/docker-compose.test.yaml` using the official image + volume mounts
3. Add to gateway's `GATEWAY_ALLOWED_HOSTS` env var
