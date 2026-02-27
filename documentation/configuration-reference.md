# Configuration Reference

This document describes all configuration surfaces used by Context Gateway:
- Gateway runtime config (`config.yaml` style files, e.g. `cmd/configs/default_config.yaml`)
- Agent launcher config (`cmd/agents/*.yaml`)
- Environment variables used for interpolation and runtime overrides

## 1. Gateway Runtime Config (`internal/config.Config`)

Top-level keys consumed by the gateway runtime:
- `server`
- `urls`
- `providers`
- `pipes`
- `store`
- `monitoring`
- `preemptive`
- `bedrock`
- `cost_control`

Unknown keys are currently ignored by YAML unmarshalling (for example `metadata` and `notifications` are accepted in sample files but not used by runtime validation).

### 1.1 `server`
- `port` (`int`, required): listening port, must be `1..65535`
- `read_timeout` (`duration`, required): request read timeout (e.g. `30s`)
- `write_timeout` (`duration`, required): response write timeout (e.g. `1000s`)

### 1.2 `urls`
- `gateway` (`string`): externally visible gateway URL
- `compresr` (`string`): Compresr base URL (documented as not used in some current code paths)

### 1.3 `providers`
Map of provider name to provider config.

Provider fields:
- `api_key` (`string`): API key credential (supports env interpolation)
- `auth` (`string`, optional): `api_key` (default), `oauth`, or `bedrock`
- `model` (`string`, required): model identifier
- `endpoint` (`string`, optional): override resolved endpoint

Validation rules:
- `model` is required for every provider entry
- `auth=oauth` cannot be combined with `api_key`
- `auth=bedrock` cannot be combined with `api_key`
- Any provider referenced by `pipes.*.provider` or `preemptive.summarizer.provider` must exist, except literal `bedrock`

Endpoint auto-resolution when `endpoint` is omitted:
- `anthropic` -> `https://api.anthropic.com/v1/messages`
- `gemini` -> `https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent`
- `openai` (and unknown providers) -> `https://api.openai.com/v1/chat/completions`

### 1.4 `pipes`

#### `pipes.tool_output`
- `enabled` (`bool`)
- `strategy` (`string`): `passthrough`, `simple`, `api`, `external_provider`
- `fallback_strategy` (`string`)
- `provider` (`string`, optional): provider reference
- `api` (`object`, optional)
- `min_bytes` (`int`)
- `max_bytes` (`int`)
- `target_ratio` (`float`)
- `enable_expand_context` (`bool`)
- `include_expand_hint` (`bool`)
- `skip_tools` (`[]string`)

`api` fields:
- `endpoint` (`string`)
- `api_key` (`string`)
- `model` (`string`)
- `timeout` (`duration`)
- `query_agnostic` (`bool`)

Validation:
- if `enabled=false`, strategy validation is skipped
- for `strategy=api` or `strategy=external_provider`, either `provider` or `api.endpoint` must be present

#### `pipes.tool_discovery`
- `enabled` (`bool`)
- `strategy` (`string`): `passthrough`, `relevance`, `tool-search`, `api`
- `fallback_strategy` (`string`)
- `provider` (`string`, optional)
- `api` (`object`, optional)
- `min_tools` (`int`)
- `max_tools` (`int`)
- `target_ratio` (`float`)
- `always_keep` (`[]string`)
- `enable_search_fallback` (`bool`)
- `search_tool_name` (`string`)
- `max_search_results` (`int`)

Validation:
- if `enabled=false`, strategy validation is skipped
- for `strategy=api`, either `provider` or `api.endpoint` must be present

### 1.5 `store`
- `type` (`string`, required): currently expected `"memory"`
- `ttl` (`duration`, required)

### 1.6 `monitoring`
- `log_level` (`string`): `debug|info|warn|error` (project samples also use `off`)
- `log_format` (`string`): `json|console`
- `log_output` (`string`): `stdout|stderr|<file path>`
- `telemetry_enabled` (`bool`)
- `telemetry_path` (`string`)
- `log_to_stdout` (`bool`)
- `verbose_payloads` (`bool`)
- `compression_log_path` (`string`)
- `tool_discovery_log_path` (`string`)
- `failed_request_log_path` (`string`)
- `trajectory_enabled` (`bool`)
- `trajectory_path` (`string`)
- `agent_name` (`string`)

### 1.7 `preemptive`
- `enabled` (`bool`)
- `trigger_threshold` (`float`): must be `(0, 100]` when enabled
- `pending_job_timeout` (`duration`, optional)
- `sync_timeout` (`duration`, optional)
- `token_estimate_ratio` (`int`, optional)
- `test_context_window_override` (`int`, optional)
- `log_dir` (`string`, optional)
- `compaction_log_path` (`string`, optional)
- `summarizer` (`object`, required when enabled)
- `session` (`object`, required when enabled)
- `detectors` (`object`, optional in YAML, struct includes `claude_code`, `codex`, `generic`)
- `add_response_headers` (`bool`)

#### `preemptive.summarizer`
- `strategy` (`string`): `provider` (default if omitted) or `api`
- `provider` (`string`, optional): provider reference
- `model` (`string`)
- `api_key` (`string`)
- `endpoint` (`string`)
- `max_tokens` (`int`)
- `timeout` (`duration`)
- `keep_recent_tokens` (`int`)
- `keep_recent` (`int`)
- `token_estimate_ratio` (`int`)
- `system_prompt` (`string`)
- `api` (`object`, required only when `strategy=api`)

`preemptive.summarizer.api` fields:
- `endpoint` (`string`, required)
- `api_key` (`string`, required)
- `model` (`string`, required)
- `timeout` (`duration`, required and `>0`)

Validation when `strategy=provider`:
- `model` required unless `provider` is set
- `max_tokens > 0`
- `timeout > 0`

#### `preemptive.session`
- `summary_ttl` (`duration`, must be `>0`)
- `hash_message_count` (`int`, must be `>0`)
- `disable_fuzzy_matching` (`bool`)

#### `preemptive.detectors`
- `claude_code.enabled` (`bool`)
- `claude_code.prompt_patterns` (`[]string`)
- `codex.enabled` (`bool`)
- `codex.prompt_patterns` (`[]string`)
- `generic.enabled` (`bool`)
- `generic.prompt_patterns` (`[]string`)
- `generic.header_name` (`string`)
- `generic.header_value` (`string`)

### 1.8 `bedrock`
- `enabled` (`bool`): must be `true` to enable Bedrock provider detection/signing paths

### 1.9 `cost_control`
- `enabled` (`bool`): enforcement toggle
- `session_cap` (`float`, USD): must be `>=0`; `0` means unlimited
- `global_cap` (`float`, USD): must be `>=0`; `0` means unlimited

## 2. Environment Variable Handling

### 2.1 YAML interpolation syntax
Gateway and agent YAML parsing both support:
- `${VAR}`
- `${VAR:-default}`

Interpolation happens before YAML unmarshal.

### 2.2 Monitoring/compaction path overrides applied after unmarshal
If set, these env vars override config file values:
- `SESSION_TELEMETRY_LOG` -> `monitoring.telemetry_path`
- `SESSION_COMPRESSION_LOG` -> `monitoring.compression_log_path`
- `SESSION_TOOL_DISCOVERY_LOG` -> `monitoring.tool_discovery_log_path`
- `SESSION_TRAJECTORY_LOG` -> `monitoring.trajectory_path` (also sets `trajectory_enabled=true`)
- `SESSION_COMPACTION_LOG` -> `preemptive.compaction_log_path`

### 2.3 Common project env vars used in sample configs
From `.env.example` and sample YAMLs:
- `GATEWAY_PORT`
- `COMPRESR_BASE_URL`
- `COMPRESR_API_KEY`
- `ANTHROPIC_API_KEY`
- `OPENAI_API_KEY`
- `GEMINI_API_KEY`
- `OPENAI_PROVIDER_URL`
- `SESSION_DIR`
- `SESSION_TELEMETRY_LOG`
- `SESSION_COMPRESSION_LOG`
- `SESSION_TOOL_DISCOVERY_LOG`
- `SESSION_COMPACTION_LOG`
- `SESSION_TRAJECTORY_LOG`

## 3. Agent Launcher Config (`cmd/agents/*.yaml`)

Top-level shape:
- `agent` (object)

### 3.1 `agent` fields
- `name` (`string`, required)
- `display_name` (`string`)
- `description` (`string`)
- `models` (`[]object`): selectable models for agents that support it
- `default_model` (`string`)
- `environment` (`[]object`)
- `unset` (`[]string`): env vars to remove before launching the agent
- `command` (`object`, required)

`agent.models[]`:
- `id` (`string`)
- `name` (`string`)
- `provider` (`string`)

`agent.environment[]`:
- `name` (`string`)
- `value` (`string`, supports `${VAR}` and `${VAR:-default}`)

`agent.command`:
- `check` (`string`, legacy shell-like form, normalized)
- `check_cmd` (`[]string`, preferred)
- `run` (`string`, required executable name/path)
- `args` (`[]string`)
- `install` (`string`, legacy)
- `install_cmd` (`[]string`, preferred)
- `fallback_message` (`string`)

Command safety normalization rules:
- `run` cannot be empty
- `run` must not contain shell metacharacters
- `run` must not contain spaces (arguments go in `args`)

## 4. Config File Roles In This Repository

- `cmd/configs/default_config.yaml`: full feature example (preemptive + pipes)
- `cmd/configs/preemptive_summarization.yaml`: preemptive-first production profile
- `cmd/configs/oauth_example.yaml`: OAuth/captured token auth example
- `cmd/configs/test_1pct_threshold.yaml`: test-oriented low-threshold config
- `cmd/configs/test_cost_control.yaml`: budget enforcement test profile
- `cmd/configs/external_providers.yaml`: wizard metadata only, not gateway runtime config

## 5. Important Notes

- The runtime validator requires critical sections (`server`, `store`, and valid `preemptive`/`pipes`/`cost_control` shapes) but does not reject unknown YAML keys.
- Some sample/generated files include convenience metadata sections that are ignored by gateway runtime.
- Provider references are the preferred pattern; inline `api` blocks remain supported for compatibility.
