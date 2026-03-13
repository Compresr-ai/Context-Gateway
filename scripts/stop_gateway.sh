#!/bin/bash
# Context Gateway - Stop Script
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
LOGS_DIR="$PROJECT_DIR/logs"

GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'

STOPPED=false

# Find the latest session's PID file
PID_FILE=$(find "$LOGS_DIR" -name "gateway.pid" -type f 2>/dev/null | head -1)

if [ -n "$PID_FILE" ] && [ -f "$PID_FILE" ]; then
    PID=$(cat "$PID_FILE")
    if kill -0 "$PID" 2>/dev/null; then
        echo -e "  ${BLUE}·${NC} Stopping gateway (PID: $PID)..."
        kill "$PID" 2>/dev/null || true
        sleep 2
        STOPPED=true
    fi
    rm -f "$PID_FILE"
fi

pgrep -f "$PROJECT_DIR/bin/gateway" > /dev/null 2>&1 && { pkill -f "$PROJECT_DIR/bin/gateway"; STOPPED=true; }
pgrep -f "/tmp/gateway" > /dev/null 2>&1 && { pkill -f "/tmp/gateway"; STOPPED=true; }

[ "$STOPPED" = true ] && echo -e "${GREEN}[OK]${NC} Gateway stopped" || echo -e "  ${BLUE}·${NC} No running gateway found"
