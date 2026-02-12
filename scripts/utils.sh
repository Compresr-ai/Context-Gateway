#!/bin/bash
# Common Shell Functions Library
# ===============================
# Minimal shared functions for Context Gateway scripts

# =============================================================================
# COLORS
# =============================================================================

export GREEN='\033[0;32m'
export BLUE='\033[0;34m'
export CYAN='\033[0;36m'
export YELLOW='\033[1;33m'
export RED='\033[0;31m'
export BOLD='\033[1m'
export DIM='\033[2m'
export NC='\033[0m'

# =============================================================================
# PRINT FUNCTIONS
# =============================================================================

print_success() {
    echo -e "${GREEN}[OK]${NC} $1"
}

print_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

print_step() {
    echo -e "${CYAN}>>>${NC} $1"
}

# =============================================================================
# GATEWAY MANAGEMENT
# =============================================================================

# Check if gateway is running on a port
check_gateway_running() {
    local port="${1:-18080}"
    local response
    response=$(curl -s --max-time 2 "http://localhost:$port/health" 2>/dev/null || echo "")
    if [ -n "$response" ]; then
        return 0
    fi
    return 1
}

# Kill a gateway process by port
kill_gateway_on_port() {
    local port="${1:-18080}"
    local pid
    pid=$(lsof -ti tcp:"$port" 2>/dev/null || echo "")
    if [ -n "$pid" ]; then
        kill -9 "$pid" 2>/dev/null || true
        print_info "Killed process on port $port (PID: $pid)"
        return 0
    fi
    return 1
}

# Stop gateway using PID file or port
stop_gateway() {
    local pid_file="${1:-}"
    local port="${2:-18080}"

    # Try PID file first
    if [ -n "$pid_file" ] && [ -f "$pid_file" ]; then
        local pid
        pid=$(cat "$pid_file" 2>/dev/null || echo "")
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null || true
            rm -f "$pid_file"
            print_success "Stopped gateway (PID: $pid)"
            return 0
        fi
        rm -f "$pid_file"
    fi

    # Fall back to port-based kill
    if kill_gateway_on_port "$port"; then
        return 0
    fi

    print_warn "No gateway found to stop"
    return 1
}
