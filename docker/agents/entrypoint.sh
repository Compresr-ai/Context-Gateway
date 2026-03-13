#!/bin/bash
set -euo pipefail

AGENT_NAME="${AGENT_NAME:?AGENT_NAME env var required}"
GATEWAY_URL="${GATEWAY_URL:-http://gateway:18081}"
TARGET_URL="${TARGET_URL:-}"
API_KEY="${API_KEY:-}"

SCRIPT="/scripts/${AGENT_NAME}.sh"

if [ ! -f "$SCRIPT" ]; then
  echo "ERROR: Unknown agent: $AGENT_NAME (no script at $SCRIPT)"
  exit 1
fi

echo "=== Running agent: $AGENT_NAME ==="
echo "    Gateway: $GATEWAY_URL"
echo "    Target:  $TARGET_URL"

export GATEWAY_URL TARGET_URL API_KEY
exec bash "$SCRIPT"
