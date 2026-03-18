#!/bin/bash
# Context Gateway - Universal Installer
# Usage: curl -fsSL https://get.compresr.ai | sh
set -e

REPO="compresr/context-gateway"
BINARY_NAME="context-gateway"
VERSION="${VERSION:-latest}"

# Default install location
if [ "$(id -u)" = "0" ]; then
    INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
else
    INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
fi

# Colors
if [ -t 1 ]; then
    RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; BOLD='\033[1m'; NC='\033[0m'
else
    RED=''; GREEN=''; YELLOW=''; BLUE=''; BOLD=''; NC=''
fi

info() { echo -e "${BLUE}→${NC} $1"; }
success() { echo -e "${GREEN}✓${NC} $1"; }
warn() { echo -e "${YELLOW}!${NC} $1"; }
error() { echo -e "${RED}✗${NC} $1" >&2; exit 1; }

# Detect OS
detect_os() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    case "$OS" in
        darwin) OS="darwin" ;;
        linux) OS="linux" ;;
        mingw*|msys*|cygwin*) OS="windows" ;;
        *) error "Unsupported OS: $OS. Download manually from https://github.com/$REPO/releases" ;;
    esac
}

# Detect architecture
detect_arch() {
    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64|amd64) ARCH="amd64" ;;
        arm64|aarch64) ARCH="arm64" ;;
        armv7l) ARCH="arm" ;;
        i386|i686) error "32-bit systems not supported" ;;
        *) error "Unsupported architecture: $ARCH" ;;
    esac
}

# Get download URL
get_download_url() {
    if [ "$VERSION" = "latest" ]; then
        VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
    fi
    
    EXT="tar.gz"
    [ "$OS" = "windows" ] && EXT="zip"
    
    DOWNLOAD_URL="https://github.com/$REPO/releases/download/${VERSION}/${BINARY_NAME}-${OS}-${ARCH}.${EXT}"
    info "Version: ${BOLD}${VERSION}${NC}"
}

# Install
install_binary() {
    TMP_DIR=$(mktemp -d)
    trap "rm -rf $TMP_DIR" EXIT

    info "Downloading..."
    curl -fsSL "$DOWNLOAD_URL" -o "$TMP_DIR/archive.tar.gz" || error "Download failed"

    info "Extracting..."
    cd "$TMP_DIR"
    if [ "$OS" = "windows" ]; then
        unzip -q "archive.tar.gz"
    else
        tar -xzf "archive.tar.gz"
    fi

    # Create install dir if needed
    [ ! -d "$INSTALL_DIR" ] && mkdir -p "$INSTALL_DIR"

    info "Installing to ${BOLD}$INSTALL_DIR${NC}..."
    if [ -w "$INSTALL_DIR" ]; then
        mv "${BINARY_NAME}" "$INSTALL_DIR/"
    else
        sudo mv "${BINARY_NAME}" "$INSTALL_DIR/"
    fi
    chmod +x "$INSTALL_DIR/${BINARY_NAME}"
    
    success "Installed!"
}

# Setup PATH if needed
setup_path() {
    case ":$PATH:" in
        *":$INSTALL_DIR:"*) return 0 ;;
    esac

    SHELL_NAME=$(basename "$SHELL")
    case "$SHELL_NAME" in
        bash) SHELL_RC="$HOME/.bashrc" ;;
        zsh) SHELL_RC="$HOME/.zshrc" ;;
        fish) SHELL_RC="$HOME/.config/fish/config.fish" ;;
        *) SHELL_RC="$HOME/.profile" ;;
    esac

    echo "export PATH=\"\$PATH:$INSTALL_DIR\"" >> "$SHELL_RC"
    warn "Added to PATH in $SHELL_RC. Restart terminal or run: source $SHELL_RC"
}

# Check for Node.js/npm (required to install agents like Claude Code and Codex)
check_node() {
    if ! command -v npm &>/dev/null; then
        warn "npm not found. Both Claude Code and Codex require Node.js."
        warn "Install Node.js from https://nodejs.org/ then re-run this installer."
        warn "Continuing anyway — you can install Node.js later."
    fi
}

# Print next steps
print_next_steps() {
    echo ""
    echo -e "${BOLD}Quick Start${NC}"
    echo ""
    echo -e "  Run the interactive setup wizard:"
    echo ""
    echo "     context-gateway"
    echo ""
    echo -e "  ${BOLD}— or launch a specific agent directly:${NC}"
    echo ""
    echo "     context-gateway agent claude_code   # Claude Code (Anthropic)"
    echo "     context-gateway agent codex         # Codex (OpenAI)"
    echo "     context-gateway agent cursor        # Cursor"
    echo ""
    echo -e "  ${BOLD}The wizard handles all env var setup automatically.${NC}"
    echo ""
    echo -e "  To configure API keys manually:"
    echo "     # Claude Code / Anthropic:"
    echo "     echo 'ANTHROPIC_API_KEY=sk-ant-...' >> ~/.config/context-gateway/.env"
    echo ""
    echo "     # Codex / OpenAI (API key mode):"
    echo "     echo 'OPENAI_API_KEY=sk-proj-...' >> ~/.config/context-gateway/.env"
    echo ""
    echo "     # Compresr compression API key (required for compression features):"
    echo "     echo 'COMPRESR_API_KEY=...' >> ~/.config/context-gateway/.env"
    echo ""
    echo -e "  Docs: https://github.com/$REPO"
    echo ""
}

# Main
main() {
    echo -e "${BOLD}Context Gateway Installer${NC}"
    echo ""
    detect_os
    detect_arch
    info "Platform: ${BOLD}${OS}-${ARCH}${NC}"
    get_download_url
    install_binary
    setup_path
    check_node
    print_next_steps
}

main "$@"
