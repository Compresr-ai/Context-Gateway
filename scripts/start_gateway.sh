#!/bin/bash
# Start Context Gateway with optimization

set -e

echo "🚀 Starting Context Gateway"
echo "============================"
echo ""

# Stop any existing processes
echo "1️⃣  Stopping existing processes..."
./scripts/stop_all_gateways.sh
echo ""

# Show current binary info
echo "2️⃣  Binary info:"
ls -lh bin/context-gateway | awk '{print "   Size: " $5 "  Modified: " $6 " " $7 " " $8}'
echo ""

# Start the gateway
echo "3️⃣  Starting Context Gateway (port 8080)..."
./bin/context-gateway agent &
GATEWAY_PID=$!

# Wait for it to be ready
sleep 3

# Verify it's running
if ps -p $GATEWAY_PID > /dev/null; then
    echo "   ✅ Gateway started (PID: $GATEWAY_PID)"
else
    echo "   ❌ Gateway failed to start!"
    exit 1
fi

echo ""
echo "4️⃣  Memory check:"
./scripts/check_memory.sh

echo ""
echo "✅ Context Gateway is running!"
echo ""
echo "📊 Monitor memory with:"
echo "   watch -n 5 './scripts/check_memory.sh'"
echo ""
echo "🛑 Stop with:"
echo "   ./scripts/stop_all_gateways.sh"
echo ""
echo "📈 Dashboard:"
echo "   http://localhost:8080/dashboard/"
