#!/bin/bash
# Context Gateway - Start Gateway Script
# =======================================
# Usage:
#   ./start_gateway.sh                               # Start with default config
#   ./start_gateway.sh -c config.yaml                # Start with config
#   ./start_gateway.sh -c config.yaml -d             # Debug logging
#   ./start_gateway.sh stop                          # Stop gateway
#
# Options:
#   -c, --config FILE    Config file (default: preemptive_summarization.yaml)
#   -d, --debug          Enable debug logging
#   -p, --port PORT      Port for cleanup (actual port set in config)

set -e

# Get project root (parent of scripts/)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

# Source utilities
source "$SCRIPT_DIR/utils.sh"

# Find binary - check multiple locations
find_binary() {
    # 1. Check project bin directory (symlink or local build)
    if [ -x "$PROJECT_DIR/bin/context-gateway" ]; then
        echo "$PROJECT_DIR/bin/context-gateway"
        return 0
    fi
    
    # 2. Check if context-gateway is in PATH
    if command -v context-gateway >/dev/null 2>&1; then
        local cmd_path
        cmd_path="$(command -v context-gateway)"
        # Make sure it's the binary, not a symlink to start_agent.sh
        if [ -x "$cmd_path" ] && ! file "$cmd_path" 2>/dev/null | grep -q "shell script\|text"; then
            echo "$cmd_path"
            return 0
        fi
    fi
    
    # 3. Check ~/.local/bin/context-gateway
    if [ -x "$HOME/.local/bin/context-gateway" ]; then
        echo "$HOME/.local/bin/context-gateway"
        return 0
    fi
    
    return 1
}

# Configuration
BINARY_PATH="$(find_binary 2>/dev/null || echo "")"
CONFIGS_DIR="$PROJECT_DIR/configs"
LOGS_DIR="${LOG_DIR:-$PROJECT_DIR/logs}"
GATEWAY_LOG="${SESSION_GATEWAY_LOG:-$LOGS_DIR/gateway.log}"
GATEWAY_PID="${SESSION_GATEWAY_PID:-$LOGS_DIR/gateway.pid}"
PID_FILE="$GATEWAY_PID"

# Defaults
CONFIG_FILE=""
PORT=""
DEBUG_MODE=false

# Parse arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        -c|--config)
            CONFIG_FILE="$2"
            shift 2
            ;;
        -p|--port)
            PORT="$2"
            shift 2
            ;;
        -d|--debug)
            DEBUG_MODE=true
            shift
            ;;
        -h|--help)
            echo "Start Context Gateway"
            echo ""
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  -c, --config FILE    Config file (default: preemptive_summarization.yaml)"
            echo "  -d, --debug          Enable debug logging"
            echo "  -p, --port PORT      Port for cleanup (actual port set in config)"
            echo "  -h, --help           Show this help"
            exit 0
            ;;
        stop)
            "$SCRIPT_DIR/stop_gateway.sh"
            exit 0
            ;;
        *)
            shift
            ;;
    esac
done

# Default config if not provided
if [ -z "$CONFIG_FILE" ]; then
    CONFIG_FILE="preemptive_summarization.yaml"
fi

# Resolve config path
if [[ "$CONFIG_FILE" != /* ]]; then
    if [ -f "$CONFIGS_DIR/$CONFIG_FILE" ]; then
        CONFIG_FILE="$CONFIGS_DIR/$CONFIG_FILE"
    elif [ -f "$PROJECT_DIR/$CONFIG_FILE" ]; then
        CONFIG_FILE="$PROJECT_DIR/$CONFIG_FILE"
    fi
fi

if [ ! -f "$CONFIG_FILE" ]; then
    print_error "Config not found: $CONFIG_FILE"
    exit 1
fi

# Extract port from config if not provided
if [ -z "$PORT" ]; then
    PORT=$(grep -A2 "^server:" "$CONFIG_FILE" | grep "port:" | awk '{print $2}' | head -n1)
    [ -n "$PORT" ] && print_info "Detected port from config: $PORT"
fi

# Ensure directories
mkdir -p "$LOGS_DIR"

# Build or use existing binary
if [ -z "$BINARY_PATH" ] || [ ! -x "$BINARY_PATH" ]; then
    # No binary found - try to build if source exists
    if [ -f "$PROJECT_DIR/cmd/main.go" ] && command -v go >/dev/null 2>&1; then
        mkdir -p "$PROJECT_DIR/bin"
        BINARY_PATH="$PROJECT_DIR/bin/context-gateway"
        print_step "Building gateway..."
        cd "$PROJECT_DIR"
        if ! go build -o "$BINARY_PATH" ./cmd 2>&1; then
            print_error "Build failed"
            exit 1
        fi
        print_success "Build complete"
    else
        print_error "Gateway binary not found. Install with: curl -fsSL https://compresr.ai/install_gateway.sh | sh"
        exit 1
    fi
fi

# Build command (binary uses serve subcommand with --config and --debug flags)
CMD="$BINARY_PATH serve --config $CONFIG_FILE"
[ "$DEBUG_MODE" = "true" ] && CMD="$CMD --debug"

# Check if already running
if check_gateway_running "$PORT"; then
    print_warn "Gateway already running"
    exit 0
fi

# Stop any existing on same port
if [ -n "$PORT" ]; then
    kill_gateway_on_port "$PORT" "$PROJECT_DIR"
fi

print_step "Starting gateway..."
print_info "Config: $(basename "$CONFIG_FILE")"
[ "$DEBUG_MODE" = "true" ] && print_info "Debug mode enabled"

# Run in background
$CMD > "$GATEWAY_LOG" 2>&1 &
echo $! > "$PID_FILE"

# Wait for startup
sleep 1
if check_gateway_running "$PORT"; then
    print_success "Gateway started (PID: $(cat "$PID_FILE"))"
    print_info "Logs: $GATEWAY_LOG"
else
    print_error "Gateway failed to start. Check $GATEWAY_LOG"
    exit 1
fi
