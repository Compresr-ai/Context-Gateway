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
#   -p, --port PORT      Gateway port override (default: 18080)
#   -d, --debug          Enable debug logging
#   --proxy MODE         auto (default), start, skip
#   -l, --list           List available agents
#   -h, --help           Show help

set -e

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

# Always rebuild binary to ensure latest changes
echo "Building context-gateway binary..."
cd "$SCRIPT_DIR"
go build -o bin/context-gateway ./cmd

# Run the binary with agent subcommand
exec "$BINARY" agent "$@"
