#!/bin/sh
# Context-Gateway Install Script
# Usage: curl -fsSL https://compresr.ai/install | sh
#
# Downloads and installs the context-gateway binary.
# All configs and agent definitions are embedded in the binary.

set -e

# Configuration
REPO="Compresr-ai/Context-Gateway"
BINARY_NAME="context-gateway"
INSTALL_DIR="${HOME}/.local/bin"

# Colors for output
RED='\033[0;31m'
GREEN='\033[38;2;23;128;68m'  # #178044
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m' # No Color

# ASCII Banner
print_banner() {
    printf "${GREEN}${BOLD}"
    cat << 'EOF'

  ██████╗ ██████╗ ███╗  ██╗████████╗███████╗██╗ ██╗████████╗  ██████╗  █████╗ ████████╗███████╗██╗    ██╗ █████╗ ██╗   ██╗
 ██╔════╝██╔═══██╗████╗ ██║╚══██╔══╝██╔════╝╚██╗██╔╝╚══██╔══╝ ██╔════╝ ██╔══██╗╚══██╔══╝██╔════╝██║    ██║██╔══██╗╚██╗ ██╔╝
 ██║     ██║   ██║██╔██╗██║   ██║   █████╗   ╚███╔╝    ██║    ██║  ███╗███████║   ██║   █████╗  ██║ █╗ ██║███████║ ╚████╔╝
 ██║     ██║   ██║██║╚████║   ██║   ██╔══╝   ██╔██╗    ██║    ██║   ██║██╔══██║   ██║   ██╔══╝  ██║███╗██║██╔══██║  ╚██╔╝
 ╚██████╗╚██████╔╝██║ ╚███║   ██║   ███████╗██╔╝ ██╗   ██║    ╚██████╔╝██║  ██║   ██║   ███████╗╚███╔███╔╝██║  ██║   ██║
  ╚═════╝ ╚═════╝ ╚═╝  ╚══╝   ╚═╝   ╚══════╝╚═╝  ╚═╝   ╚═╝     ╚═════╝ ╚═╝  ╚═╝   ╚═╝   ╚══════╝ ╚══╝╚══╝ ╚═╝  ╚═╝   ╚═╝

EOF
    printf "${NC}\n\n"
}

info() { printf "${GREEN}[INFO]${NC} %s\n" "$1"; }
warn() { printf "${YELLOW}[WARN]${NC} %s\n" "$1"; }
error() { printf "${RED}[ERROR]${NC} %s\n" "$1"; exit 1; }
success() { printf "${GREEN}${BOLD}[✓]${NC} %s\n" "$1"; }

# Detect OS
detect_os() {
    case "$(uname -s)" in
        Linux*)     OS="linux";;
        Darwin*)    OS="darwin";;
        CYGWIN*|MINGW*|MSYS*) OS="windows";;
        *)          error "Unsupported OS: $(uname -s)";;
    esac
}

# Detect architecture
detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)   ARCH="amd64";;
        arm64|aarch64)  ARCH="arm64";;
        armv7l)         ARCH="arm";;
        i386|i686)      ARCH="386";;
        *)              error "Unsupported architecture: $(uname -m)";;
    esac
}

# Get latest version from GitHub
get_latest_version() {
    VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
    if [ -z "$VERSION" ]; then
        error "Failed to get latest version"
    fi
}

# Download and install binary
install_binary() {
    detect_os
    detect_arch

    # Use provided VERSION or get latest
    if [ -z "$VERSION" ]; then
        get_latest_version
    fi

    info "Installing Context-Gateway ${VERSION} for ${OS}/${ARCH}..."

    # Create install directory
    mkdir -p "${INSTALL_DIR}"

    # Construct download URL
    FILENAME="gateway-${OS}-${ARCH}"
    if [ "$OS" = "windows" ]; then
        FILENAME="${FILENAME}.exe"
    fi
    URL="https://github.com/${REPO}/releases/download/${VERSION}/${FILENAME}"

    # Download binary
    info "Downloading from ${URL}..."
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "${URL}" -o "${INSTALL_DIR}/${BINARY_NAME}"
    elif command -v wget >/dev/null 2>&1; then
        wget -q "${URL}" -O "${INSTALL_DIR}/${BINARY_NAME}"
    else
        error "Neither curl nor wget found. Please install one of them."
    fi

    # Make executable
    chmod +x "${INSTALL_DIR}/${BINARY_NAME}"

    # Create compresr alias
    if [ "$OS" != "windows" ]; then
        ln -sf "${INSTALL_DIR}/${BINARY_NAME}" "${INSTALL_DIR}/compresr"
    fi

    success "Installed to ${INSTALL_DIR}/${BINARY_NAME}"
}

# Check PATH
check_path() {
    case ":${PATH}:" in
        *":${INSTALL_DIR}:"*) ;;
        *)
            warn "${INSTALL_DIR} is not in your PATH"
            echo ""
            echo "  Add to your shell profile (~/.bashrc or ~/.zshrc):"
            printf "  ${CYAN}export PATH=\"\$PATH:${INSTALL_DIR}\"${NC}\n"
            echo ""
            ;;
    esac
}

# Print usage
print_usage() {
    printf "\n"
    printf "${GREEN}${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}\n"
    printf "${GREEN}${BOLD}  ✅ INSTALLATION COMPLETE${NC}\n"
    printf "${GREEN}${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}\n"
    printf "\n"
    printf "  ${BOLD}Run with an agent:${NC}\n"
    printf "\n"
    printf "     ${CYAN}context-gateway -a claude_code${NC}    # Claude Code CLI\n"
    printf "     ${CYAN}context-gateway -a openclaw${NC}       # OpenClaw\n"
    printf "     ${CYAN}context-gateway -a codex${NC}          # Codex CLI\n"
    printf "     ${CYAN}context-gateway -a cursor${NC}         # Cursor IDE\n"
    printf "\n"
    printf "  ${BOLD}Or interactive selection:${NC}\n"
    printf "\n"
    printf "     ${CYAN}context-gateway${NC}\n"
    printf "\n"
    printf "  ${BOLD}List available agents:${NC}\n"
    printf "\n"
    printf "     ${CYAN}context-gateway -l${NC}\n"
    printf "\n"
    printf "  ${BOLD}Docs:${NC} ${CYAN}https://docs.compresr.ai${NC}\n"
    printf "\n"
    printf "${GREEN}${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}\n"
}

# Main
main() {
    print_banner
    install_binary
    check_path
    print_usage
}

main
