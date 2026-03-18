#!/bin/bash
# Context Gateway - Uninstaller
set -e

BINARY_NAME="context-gateway"
LOCATIONS=(
    "/usr/local/bin/$BINARY_NAME"
    "$HOME/.local/bin/$BINARY_NAME"
    "/opt/bin/$BINARY_NAME"
)

echo "Uninstalling Context Gateway..."

for loc in "${LOCATIONS[@]}"; do
    if [ -f "$loc" ]; then
        echo "Removing $loc"
        rm -f "$loc" 2>/dev/null || sudo rm -f "$loc"
    fi
done

echo "✓ Context Gateway uninstalled"
