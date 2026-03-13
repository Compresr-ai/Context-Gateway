#!/bin/bash
# Start Agent with Gateway Proxy
# ===============================
# This script is a thin wrapper around the Go binary.
# All logic is now in cmd/agent.go via internal/tui.
#
# USAGE:
#   ./start_agent.sh                    # Interactive menu (recommended)
#   ./start_agent.sh [AGENT]            # Select agent, then config menu
#   ./start_agent.sh [AGENT] [OPTIONS]  # Direct mode with specific config
#
# FLAGS:
#   -c, --config FILE    Gateway config (optional - shows menu if not specified)
#   -p, --port PORT      Gateway port override (default: 18081)
#   -d, --debug          Enable debug logging
#   --proxy MODE         auto (default), start, skip
#   -l, --list           List available agents
#   -h, --help           Show help

set -e

# ── Version (bump here when releasing) ──
VERSION="v0.5.2-dev"

# Resolve script directory (handles symlinks)
SCRIPT_PATH="${BASH_SOURCE[0]}"
while [ -L "$SCRIPT_PATH" ]; do
    SCRIPT_DIR="$(cd "$(dirname "$SCRIPT_PATH")" && pwd -P)"
    SCRIPT_PATH="$(readlink "$SCRIPT_PATH")"
    [[ "$SCRIPT_PATH" != /* ]] && SCRIPT_PATH="$SCRIPT_DIR/$SCRIPT_PATH"
done
SCRIPT_DIR="$(cd "$(dirname "$SCRIPT_PATH")" && pwd -P)"

# Binary path
BINARY="$SCRIPT_DIR/bin/context-gateway"

# Save original directory (where user invoked the script)
ORIGINAL_DIR="$(pwd)"

# Rebuild binary — serialize concurrent builds via lockdir (atomic mkdir)
BUILD_LOCK="/tmp/context-gateway-build.lock"
cd "$SCRIPT_DIR"
if mkdir "$BUILD_LOCK" 2>/dev/null; then
    trap 'rmdir "$BUILD_LOCK" 2>/dev/null' EXIT
    echo "Building context-gateway binary..."
    GOTOOLCHAIN=auto go build -ldflags="-X main.Version=$VERSION" -o bin/context-gateway ./cmd
    rmdir "$BUILD_LOCK" 2>/dev/null
    trap - EXIT
else
    echo "Waiting for another build to finish..."
    while [ -d "$BUILD_LOCK" ]; do sleep 1; done
    echo "Build done (reused existing binary)."
fi

# Return to original directory and run the agent there
cd "$ORIGINAL_DIR"
exec "$BINARY" agent "$@"
