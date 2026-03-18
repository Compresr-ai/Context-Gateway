#!/bin/bash
# Stop all running Context Gateway processes

set -e

echo "🔍 Checking for running Context Gateway processes..."

# Find all context-gateway processes (exclude grep and this script)
PIDS=$(pgrep -f "context-gateway" || true)

if [ -z "$PIDS" ]; then
    echo "✅ No Context Gateway processes running"
    exit 0
fi

echo "📋 Found running processes:"
ps aux | grep -v grep | grep context-gateway || true

echo ""
echo "🛑 Stopping all Context Gateway processes..."

# Kill processes gracefully (SIGTERM first)
for PID in $PIDS; do
    echo "  Stopping PID $PID..."
    kill -TERM "$PID" 2>/dev/null || true
done

# Wait up to 5 seconds for graceful shutdown
TIMEOUT=5
while [ $TIMEOUT -gt 0 ]; do
    REMAINING=$(pgrep -f "context-gateway" | wc -l | tr -d ' ')
    if [ "$REMAINING" -eq 0 ]; then
        break
    fi
    echo "  Waiting for processes to stop... ($TIMEOUT seconds remaining)"
    sleep 1
    TIMEOUT=$((TIMEOUT - 1))
done

# Force kill any remaining processes
REMAINING_PIDS=$(pgrep -f "context-gateway" || true)
if [ -n "$REMAINING_PIDS" ]; then
    echo "⚠️  Force stopping remaining processes..."
    for PID in $REMAINING_PIDS; do
        kill -KILL "$PID" 2>/dev/null || true
    done
fi

# Clean up PID file
PID_FILE="/tmp/context-gateway.pid"
if [ -f "$PID_FILE" ]; then
    rm -f "$PID_FILE"
    echo "🗑️  Removed PID file: $PID_FILE"
fi

# Clean up port file
PORT_FILE="$HOME/.config/context-gateway/port"
if [ -f "$PORT_FILE" ]; then
    rm -f "$PORT_FILE"
    echo "🗑️  Removed port file: $PORT_FILE"
fi

echo "✅ All Context Gateway processes stopped"

# Show final confirmation
sleep 1
FINAL_CHECK=$(pgrep -f "context-gateway" | wc -l | tr -d ' ')
if [ "$FINAL_CHECK" -eq 0 ]; then
    echo "✅ Verified: No processes running"
else
    echo "⚠️  Warning: Some processes may still be running:"
    ps aux | grep -v grep | grep context-gateway || true
    exit 1
fi
