#!/bin/bash
set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

echo ""
echo -e "${GREEN}=====================================${NC}"
echo -e "${GREEN}  Context Gateway - Local CI Testing ${NC}"
echo -e "${GREEN}=====================================${NC}"
echo ""

# Detect repo from git remote
REPO=$(gh repo view --json nameWithOwner -q '.nameWithOwner' 2>/dev/null || echo "compresr-founders/Context-Gateway-Private")

echo -e "${CYAN}▶ Loading secrets from GitHub (repo: $REPO)${NC}"

# Pull secrets from GitHub Secrets (same as GH CI)
# Note: gh CLI can read secrets if you have admin access
load_secret() {
    local name=$1
    # Check if already set in environment
    if [ -n "${!name}" ]; then
        echo "  $name: ✓ (already set)"
        return 0
    fi
    
    # Try to get from .env file as fallback (GitHub Actions injects from secrets)
    if [ -f .env ]; then
        local value=$(grep "^${name}=" .env 2>/dev/null | cut -d'=' -f2-)
        if [ -n "$value" ]; then
            export "$name=$value"
            echo "  $name: ✓ (loaded from .env)"
            return 0
        fi
    fi
    
    echo "  $name: ✗ (not found)"
    return 1
}

SECRETS_OK=true
load_secret "ANTHROPIC_API_KEY" || SECRETS_OK=false
load_secret "COMPRESR_BASE_URL" || SECRETS_OK=false
load_secret "COMPRESR_API_KEY" || SECRETS_OK=false
load_secret "OPENAI_API_KEY" || SECRETS_OK=false

echo ""

if [ "$SECRETS_OK" = false ]; then
    echo -e "${YELLOW}⚠ Some secrets missing - integration tests may fail${NC}"
    echo -e "${YELLOW}  Make sure secrets are configured in GitHub or .env file${NC}"
    echo ""
fi

FAILED=0

# Function to run a step
run_step() {
    local step_name=$1
    shift
    echo -e "${CYAN}▶ $step_name${NC}"
    if "$@"; then
        echo -e "${GREEN}✓ $step_name passed${NC}"
        echo ""
        return 0
    else
        echo -e "${RED}✗ $step_name failed${NC}"
        echo ""
        FAILED=1
        return 1
    fi
}

# Prepare embedded files for go:embed directives
echo -e "${CYAN}▶ Preparing embedded files${NC}"
make embed-prep > /dev/null 2>&1
echo -e "${GREEN}✓ Embedded files prepared${NC}"
echo ""

# Build
run_step "Build" go build -v -o bin/gateway ./cmd

# Unit Tests
run_step "Unit Tests" go test -v -short -race \
    ./tests/anthropic/unit/... \
    ./tests/common/... \
    ./tests/preemptive/unit/... \
    ./tests/external/...

# Integration Tests (if API keys are set)
if [ -n "$ANTHROPIC_API_KEY" ]; then
    echo -e "${CYAN}▶ Integration Tests${NC}"
    echo "Running integration tests with real API..."
    if go test -v -race ./tests/anthropic/integration/... -timeout 10m; then
        echo -e "${GREEN}✓ Integration Tests passed${NC}"
    else
        echo -e "${RED}✗ Integration Tests failed${NC}"
        FAILED=1
    fi
    echo ""
else
    echo -e "${YELLOW}⚠ Skipping integration tests - ANTHROPIC_API_KEY not set${NC}"
    echo ""
fi

# Lint (if golangci-lint installed) - warnings only
if command -v golangci-lint &> /dev/null; then
    echo -e "${CYAN}▶ Lint${NC}"
    if golangci-lint run --timeout=5m; then
        echo -e "${GREEN}✓ Lint passed${NC}"
    else
        echo -e "${YELLOW}⚠ Lint has warnings (non-blocking)${NC}"
    fi
    echo ""
else
    echo -e "${YELLOW}⚠ golangci-lint not installed, skipping lint${NC}"
    echo ""
fi

# Security (gosec) - if installed
if command -v gosec &> /dev/null; then
    echo -e "${CYAN}▶ Security (gosec)${NC}"
    if gosec -exclude-dir=tests -quiet ./...; then
        echo -e "${GREEN}✓ Security scan passed${NC}"
    else
        echo -e "${RED}✗ Security scan failed${NC}"
        FAILED=1
    fi
    echo ""
else
    echo -e "${YELLOW}⚠ gosec not installed, skipping security scan${NC}"
    echo ""
fi

# Summary
echo -e "${GREEN}=====================================${NC}"
if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}  ✓ All checks passed!${NC}"
    echo -e "${GREEN}=====================================${NC}"
    exit 0
else
    echo -e "${RED}  ✗ Some checks failed${NC}"
    echo -e "${GREEN}=====================================${NC}"
    exit 1
fi
