#!/usr/bin/env bash
# Claude Code hook script for Slack notifications.
#
# Sends a Slack message when Claude Code fires a Stop or Notification event.
#
# Install: run hooks/install.sh (or manually copy to ~/.claude/hooks/)
#
# Supports two modes:
#   1. Webhook (recommended): SLACK_WEBHOOK_URL only
#   2. Bot Token: SLACK_BOT_TOKEN + SLACK_CHANNEL_ID
#
# Requires:
#   jq - JSON parser (brew install jq / apt install jq)
#
# Usage (called by Claude Code, not manually):
#   echo '{"hook_event_name":"Stop","cwd":"/path/to/project",...}' | ./slack-notify.sh

set -euo pipefail

# ── Read hook JSON from stdin ──────────────────────────────────────────────
input="$(cat)"

# ── Check jq dependency ───────────────────────────────────────────────────
if ! command -v jq &>/dev/null; then
  echo "slack-notify: jq is required but not installed" >&2
  exit 1
fi

# ── Source project .env using cwd from hook JSON ─────────────────────────
cwd="$(echo "$input" | jq -r '.cwd // empty')"

# Try sourcing from multiple locations
load_env() {
  local env_file="$1"
  if [[ -f "$env_file" ]]; then
    set -a
    source "$env_file"
    set +a
  fi
}

# Source global config first, then project (project overrides global)
if [[ -z "${SLACK_WEBHOOK_URL:-}" && -z "${SLACK_BOT_TOKEN:-}" ]]; then
  load_env "$HOME/.config/context-gateway/.env"
fi
if [[ -n "$cwd" ]]; then
  load_env "$cwd/.env"
fi

# ── Env check ──────────────────────────────────────────────────────────────
# Support either webhook URL or bot token + channel
USE_WEBHOOK=false
if [[ -n "${SLACK_WEBHOOK_URL:-}" ]]; then
  USE_WEBHOOK=true
elif [[ -z "${SLACK_BOT_TOKEN:-}" || -z "${SLACK_CHANNEL_ID:-}" ]]; then
  exit 0  # silently skip if not configured
fi

# ── Debug: log raw input ─────────────────────────────────────────────────
if [[ -n "$cwd" ]]; then
  DEBUG_LOG="$cwd/logs/hook-debug.log"
  mkdir -p "$(dirname "$DEBUG_LOG")"
  echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] $input" >> "$DEBUG_LOG"
fi

hook_event="$(echo "$input" | jq -r '.hook_event_name // empty')"
hook_type="$(echo "$input" | jq -r '.notification_type // .type // empty')"

# Map Stop event to our case logic
if [[ "$hook_event" == "Stop" ]]; then
  hook_type="stop"
fi

# ── Build notification ─────────────────────────────────────────────────────
header=""
fallback=""

case "$hook_type" in
  permission_prompt)
    message="$(echo "$input" | jq -r '.message // .tool_name // empty')"
    if [[ -n "$message" ]]; then
      header=":bell:  *Claude needs your approval*  —  ${message}"
      fallback="Claude needs your approval — ${message}"
    else
      header=":bell:  *Claude needs your approval*"
      fallback="Claude needs your approval"
    fi
    ;;
  stop)
    header=":large_green_circle:  *Claude is done — your turn*"
    fallback="Claude is done — your turn"
    ;;
  idle_prompt)
    header=":large_green_circle:  *Claude is done — your turn*"
    fallback="Claude is done — your turn"
    ;;
  *)
    # Unknown hook type — skip
    exit 0
    ;;
esac

# ── Build Block Kit payload ────────────────────────────────────────────────
if [[ "$USE_WEBHOOK" == "true" ]]; then
  # Webhook payload (no channel field needed)
  payload="$(jq -n \
    --arg fallback "$fallback" \
    --arg header "$header" \
    '{
      text: $fallback,
      blocks: [
        {
          type: "section",
          text: { type: "mrkdwn", text: $header }
        }
      ]
    }'
  )"
else
  # Bot token payload (needs channel)
  payload="$(jq -n \
    --arg channel "$SLACK_CHANNEL_ID" \
    --arg fallback "$fallback" \
    --arg header "$header" \
    '{
      channel: $channel,
      text: $fallback,
      blocks: [
        {
          type: "section",
          text: { type: "mrkdwn", text: $header }
        }
      ]
    }'
  )"
fi

# ── Send to Slack ──────────────────────────────────────────────────────────
if [[ "$USE_WEBHOOK" == "true" ]]; then
  curl -s -o /dev/null \
    -X POST "$SLACK_WEBHOOK_URL" \
    -H "Content-Type: application/json" \
    -d "$payload"
else
  curl -s -o /dev/null \
    -X POST "https://slack.com/api/chat.postMessage" \
    -H "Content-Type: application/json; charset=utf-8" \
    -H "Authorization: Bearer ${SLACK_BOT_TOKEN}" \
    -d "$payload"
fi
