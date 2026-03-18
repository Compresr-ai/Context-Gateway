#!/usr/bin/env bash
#
# MCP Test Servers - Add/remove MCP servers for compression testing
#
# Usage:
#   ./scripts/mcp-test-servers.sh add --all           # Add to all agents
#   ./scripts/mcp-test-servers.sh add --agent codex   # Add to specific agent
#   ./scripts/mcp-test-servers.sh remove --all        # Remove from all agents
#   ./scripts/mcp-test-servers.sh remove --agent claude_code
#

set -euo pipefail

# Agent config locations
# Claude Code: ~/.claude.json (root level mcpServers)
# Codex: ~/.codex/config.json (mcpServers key)
# OpenClaw: ~/.openclaw/openclaw.json (agents.mcpServers key - different structure!)
CLAUDE_CODE_CONFIG="$HOME/.claude.json"
CODEX_CONFIG="$HOME/.codex/config.json"

AGENTS=("claude_code" "codex")

get_config_path() {
    local agent=$1
    case "$agent" in
        claude_code) echo "$CLAUDE_CODE_CONFIG" ;;
        codex) echo "$CODEX_CONFIG" ;;
        *) echo "" ;;
    esac
}

# MCP servers config (no API keys required)
MCP_SERVERS='{
  "everything": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-everything"]
  },
  "filesystem": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
  },
  "memory": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-memory"]
  },
  "thinking": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-sequential-thinking"]
  },
  "fetch": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-fetch"]
  },
  "time": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-time"]
  }
}'

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

usage() {
    echo "MCP Test Servers - Add/remove MCP servers for compression testing"
    echo ""
    echo "Usage:"
    echo "  $0 add --all              Add test MCP servers to supported agents"
    echo "  $0 add --agent <name>     Add test MCP servers to specific agent"
    echo "  $0 remove --all           Remove test MCP servers from all agents"
    echo "  $0 remove --agent <name>  Remove test MCP servers from specific agent"
    echo "  $0 list                   List current MCP servers for all agents"
    echo ""
    echo "Supported Agents: claude_code, codex"
    echo ""
    echo "MCP Servers Added:"
    echo "  - everything (17 tools) - All MCP protocol features"
    echo "  - filesystem (10 tools) - File operations"
    echo "  - memory (5 tools)      - Key-value storage"
    echo "  - thinking (3 tools)    - Sequential reasoning"
    echo "  - fetch (2 tools)       - URL fetching"
    echo "  - time (2 tools)        - Time/timezone"
    exit 1
}

# Check if jq is installed
check_jq() {
    if ! command -v jq &> /dev/null; then
        echo -e "${RED}Error: jq is required but not installed.${NC}"
        echo "Install with: brew install jq"
        exit 1
    fi
}

# Add MCP servers to a config file
add_servers() {
    local agent=$1
    local config_path
    config_path=$(get_config_path "$agent")
    
    local config_dir
    config_dir=$(dirname "$config_path")

    # Create directory if it doesn't exist (for codex)
    if [[ "$config_dir" != "$HOME" && ! -d "$config_dir" ]]; then
        mkdir -p "$config_dir"
        echo -e "${YELLOW}Created directory: $config_dir${NC}"
    fi

    # Create empty config if it doesn't exist
    if [[ ! -f "$config_path" ]]; then
        echo '{}' > "$config_path"
        echo -e "${YELLOW}Created config: $config_path${NC}"
    fi

    # Merge MCP servers into existing config
    local existing
    existing=$(cat "$config_path")
    
    local merged
    merged=$(echo "$existing" | jq --argjson servers "$MCP_SERVERS" '
        .mcpServers = ((.mcpServers // {}) + $servers)
    ')

    echo "$merged" > "$config_path"
    echo -e "${GREEN}✓ Added MCP servers to $agent${NC}"
    echo "  Config: $config_path"
}

# Remove MCP servers from a config file
remove_servers() {
    local agent=$1
    local config_path
    config_path=$(get_config_path "$agent")

    if [[ ! -f "$config_path" ]]; then
        echo -e "${YELLOW}⊘ No config found for $agent (skipped)${NC}"
        return
    fi

    # Remove our test servers
    local servers_to_remove='["everything", "filesystem", "memory", "thinking", "fetch", "time"]'
    
    local updated
    updated=$(cat "$config_path" | jq --argjson remove "$servers_to_remove" '
        .mcpServers = ((.mcpServers // {}) | to_entries | map(select(.key as $k | $remove | index($k) | not)) | from_entries)
    ')

    echo "$updated" > "$config_path"
    echo -e "${GREEN}✓ Removed MCP servers from $agent${NC}"
}

# List current MCP servers for an agent
list_servers() {
    local agent=$1
    local config_path
    config_path=$(get_config_path "$agent")

    echo -e "\n${YELLOW}=== $agent ===${NC}"
    echo "Config: $config_path"
    
    if [[ ! -f "$config_path" ]]; then
        echo "  (no config file)"
        return
    fi

    local servers
    servers=$(cat "$config_path" | jq -r '.mcpServers // {} | keys[]' 2>/dev/null)
    
    if [[ -z "$servers" ]]; then
        echo "  (no MCP servers configured)"
    else
        echo "$servers" | while read -r server; do
            echo "  - $server"
        done
    fi
}

# Main
main() {
    check_jq

    if [[ $# -lt 1 ]]; then
        usage
    fi

    local action=$1
    shift

    case "$action" in
        add)
            if [[ $# -lt 1 ]]; then
                usage
            fi
            
            case "$1" in
                --all)
                    for agent in "${AGENTS[@]}"; do
                        add_servers "$agent"
                    done
                    echo -e "\n${GREEN}Done! Run agents with:${NC}"
                    echo "  context-gateway agent claude_code"
                    echo "  context-gateway agent codex"
                    ;;
                --agent)
                    if [[ $# -lt 2 ]]; then
                        echo -e "${RED}Error: --agent requires an agent name${NC}"
                        usage
                    fi
                    local agent=$2
                    local config_path
                    config_path=$(get_config_path "$agent")
                    if [[ -z "$config_path" ]]; then
                        echo -e "${RED}Error: Unknown agent '$agent'${NC}"
                        echo "Valid agents: ${AGENTS[*]}"
                        exit 1
                    fi
                    add_servers "$agent"
                    echo -e "\n${GREEN}Run with:${NC} context-gateway agent $agent"
                    ;;
                *)
                    usage
                    ;;
            esac
            ;;
        
        remove)
            if [[ $# -lt 1 ]]; then
                usage
            fi
            
            case "$1" in
                --all)
                    for agent in "${AGENTS[@]}"; do
                        remove_servers "$agent"
                    done
                    echo -e "\n${GREEN}Done! Test MCP servers removed.${NC}"
                    ;;
                --agent)
                    if [[ $# -lt 2 ]]; then
                        echo -e "${RED}Error: --agent requires an agent name${NC}"
                        usage
                    fi
                    local agent=$2
                    local config_path
                    config_path=$(get_config_path "$agent")
                    if [[ -z "$config_path" ]]; then
                        echo -e "${RED}Error: Unknown agent '$agent'${NC}"
                        echo "Valid agents: ${AGENTS[*]}"
                        exit 1
                    fi
                    remove_servers "$agent"
                    ;;
                *)
                    usage
                    ;;
            esac
            ;;
        
        list)
            for agent in "${AGENTS[@]}"; do
                list_servers "$agent"
            done
            ;;
        
        *)
            usage
            ;;
    esac
}

main "$@"
