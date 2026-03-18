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
#   --dev                Start Vite dev server for dashboard hot reload (script-only, not in binary)
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

# ── Parse --dev flag (strip it out before forwarding args to binary) ──
DEV_MODE=false
PASSTHROUGH_ARGS=()
for arg in "$@"; do
    if [ "$arg" = "--dev" ]; then
        DEV_MODE=true
    else
        PASSTHROUGH_ARGS+=("$arg")
    fi
done

cd "$SCRIPT_DIR"
DASHBOARD_SRC="$SCRIPT_DIR/web/dashboard/src"
DASHBOARD_DIST_INDEX="$SCRIPT_DIR/cmd/dashboard_dist/index.html"

if [ "$DEV_MODE" = "true" ]; then
    # ── Dev mode: start Vite dev server for hot reload ──
    # Skip the static build — Vite serves the dashboard directly.
    # The Vite dev server proxies /api calls to the gateway (port from GATEWAY_PORT or 18080).
    if ! command -v npm &>/dev/null; then
        echo "Error: npm not found. Cannot start dev server." >&2
        exit 1
    fi
    if [ ! -d "$SCRIPT_DIR/web/dashboard/node_modules" ]; then
        echo "Installing dashboard dependencies..."
        (cd "$SCRIPT_DIR/web/dashboard" && npm install)
    fi

    # Derive gateway port from --port flag or GATEWAY_PORT env (default 18080).
    # This is passed to Vite so its /api proxy targets the right port.
    RESOLVED_PORT="${GATEWAY_PORT:-18080}"
    for i in "${!PASSTHROUGH_ARGS[@]}"; do
        if [ "${PASSTHROUGH_ARGS[$i]}" = "--port" ] || [ "${PASSTHROUGH_ARGS[$i]}" = "-p" ]; then
            RESOLVED_PORT="${PASSTHROUGH_ARGS[$((i+1))]:-$RESOLVED_PORT}"
        fi
    done

    # Tell the gateway binary not to open the browser — Vite will do it instead.
    export CONTEXT_GATEWAY_DEV_FRONTEND=1

    echo "Starting Vite dev server (dashboard hot reload)..."
    echo "  → API proxied to: http://localhost:${RESOLVED_PORT}"
    echo ""
    (cd "$SCRIPT_DIR/web/dashboard" && GATEWAY_PORT="$RESOLVED_PORT" npm run dev) &
    VITE_PID=$!

    # Ensure Vite is killed when this script exits
    trap 'kill "$VITE_PID" 2>/dev/null; wait "$VITE_PID" 2>/dev/null' EXIT

else
    # ── Normal mode: rebuild static dashboard if sources changed ──
    if [ -d "$DASHBOARD_SRC" ] && command -v npm &>/dev/null; then
        NEEDS_REBUILD=false
        if [ ! -f "$DASHBOARD_DIST_INDEX" ]; then
            NEEDS_REBUILD=true
        elif find "$DASHBOARD_SRC" -newer "$DASHBOARD_DIST_INDEX" \( -name "*.tsx" -o -name "*.ts" -o -name "*.css" -o -name "*.json" \) 2>/dev/null | grep -q .; then
            NEEDS_REBUILD=true
        fi
        if [ "$NEEDS_REBUILD" = "true" ]; then
            echo "Dashboard source changed — rebuilding..."
            (cd "$SCRIPT_DIR/web/dashboard" && npm run build 2>&1) && echo "Dashboard rebuilt." || echo "Dashboard build failed (continuing with existing assets)."
        fi
    fi
fi

# Rebuild binary — serialize concurrent builds via lockdir (atomic mkdir)
BUILD_LOCK="/tmp/context-gateway-build.lock"
if mkdir "$BUILD_LOCK" 2>/dev/null; then
    trap 'rmdir "$BUILD_LOCK" 2>/dev/null; [ "$DEV_MODE" = "true" ] && kill "$VITE_PID" 2>/dev/null' EXIT
    echo "Building context-gateway binary..."
    GOTOOLCHAIN=auto go build -ldflags="-X main.Version=$VERSION" -o bin/context-gateway ./cmd
    rmdir "$BUILD_LOCK" 2>/dev/null
    trap - EXIT
    # Restore Vite cleanup trap in dev mode
    if [ "$DEV_MODE" = "true" ]; then
        trap 'kill "$VITE_PID" 2>/dev/null; wait "$VITE_PID" 2>/dev/null' EXIT
    fi
else
    echo "Waiting for another build to finish..."
    while [ -d "$BUILD_LOCK" ]; do sleep 1; done
    echo "Build done (reused existing binary)."
fi

# Return to original directory and run the agent there
cd "$ORIGINAL_DIR"
exec "$BINARY" agent "${PASSTHROUGH_ARGS[@]}"
