#!/bin/bash
# Check memory usage of Context Gateway processes

echo "🔍 Context Gateway Memory Usage Report"
echo "======================================="
echo ""

# Check if any processes are running
PIDS=$(pgrep -f "context-gateway" || true)

if [ -z "$PIDS" ]; then
    echo "❌ No Context Gateway processes running"
    exit 0
fi

# Count processes
COUNT=$(echo "$PIDS" | wc -l | tr -d ' ')
echo "📊 Found $COUNT running process(es)"
echo ""

# Show detailed memory info
echo "Memory Usage Details:"
echo "---------------------"
ps aux | head -1
ps aux | grep -v grep | grep context-gateway || true

echo ""
echo "Summary:"
echo "--------"

# Calculate total memory (RSS in KB)
TOTAL_MEM=0
while read -r PID; do
    MEM=$(ps -o rss= -p "$PID" 2>/dev/null || echo "0")
    TOTAL_MEM=$((TOTAL_MEM + MEM))
done <<< "$PIDS"

# Convert to MB
TOTAL_MB=$((TOTAL_MEM / 1024))
AVG_MB=$((TOTAL_MB / COUNT))

echo "  Total Processes: $COUNT"
echo "  Total Memory:    ${TOTAL_MB} MB"
echo "  Average per Process: ${AVG_MB} MB"

# Warn if memory is high
if [ $TOTAL_MB -gt 500 ]; then
    echo ""
    echo "⚠️  WARNING: High memory usage detected!"
    echo "   Consider:"
    echo "   1. Stop duplicate processes: ./scripts/stop_all_gateways.sh"
    echo "   2. Rebuild with optimizations: make build"
    echo "   3. Check logs for memory leaks"
elif [ $COUNT -gt 1 ]; then
    echo ""
    echo "⚠️  WARNING: Multiple processes detected!"
    echo "   Run: ./scripts/stop_all_gateways.sh"
else
    echo ""
    echo "✅ Memory usage is normal"
fi

echo ""
echo "Process Tree:"
echo "-------------"
pstree -p $(pgrep -f "context-gateway" | head -1) 2>/dev/null || \
    echo "(pstree not available - install with: brew install pstree)"
