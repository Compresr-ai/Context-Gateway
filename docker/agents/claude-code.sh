#!/bin/bash
# Claude Code agent simulator
# Sends Anthropic Messages API requests through the gateway
set -euo pipefail

PASS=0
FAIL=0

assert_status() {
  local test_name="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then
    echo "  PASS: $test_name (status $actual)"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $test_name (expected $expected, got $actual)"
    FAIL=$((FAIL + 1))
  fi
}

assert_json() {
  local test_name="$1" body="$2"
  if echo "$body" | jq empty 2>/dev/null; then
    echo "  PASS: $test_name (valid JSON)"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $test_name (invalid JSON)"
    FAIL=$((FAIL + 1))
  fi
}

# Large tool output to trigger compression
LARGE_OUTPUT="$(python3 -c "print('Line ' + 'x'*200 + '\\n' for i in range(50))" 2>/dev/null || printf '%0.s=Line of log output with important debugging information that needs to be preserved for analysis\n' {1..50})"

# --- Test 1: Simple chat with tool result (Anthropic format) ---
echo "Test 1: Claude Code - Anthropic format tool result"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/messages" \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -H "x-api-key: ${API_KEY}" \
  -H "X-Target-URL: ${TARGET_URL}/v1/messages" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "max_tokens": 100,
    "messages": [
      {"role": "user", "content": "What does this file contain?"},
      {"role": "assistant", "content": [
        {"type": "tool_use", "id": "toolu_01cc", "name": "read_file", "input": {"path": "config.yaml"}}
      ]},
      {"role": "user", "content": [
        {"type": "tool_result", "tool_use_id": "toolu_01cc", "content": "server:\n  port: 8080\n  host: localhost"}
      ]}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Anthropic tool result" "200" "$STATUS"
assert_json "Anthropic response body" "$BODY"

# --- Test 2: Large tool output (should trigger compression) ---
echo "Test 2: Claude Code - Large tool output compression"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/messages" \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -H "x-api-key: ${API_KEY}" \
  -H "X-Target-URL: ${TARGET_URL}/v1/messages" \
  -d "{
    \"model\": \"claude-sonnet-4-20250514\",
    \"max_tokens\": 200,
    \"messages\": [
      {\"role\": \"user\", \"content\": \"Summarize this log file.\"},
      {\"role\": \"assistant\", \"content\": [
        {\"type\": \"tool_use\", \"id\": \"toolu_02lg\", \"name\": \"read_file\", \"input\": {\"path\": \"app.log\"}}
      ]},
      {\"role\": \"user\", \"content\": [
        {\"type\": \"tool_result\", \"tool_use_id\": \"toolu_02lg\", \"content\": \"$(printf '%0.s=CRITICAL ERROR - Database connection failed with timeout after 30 seconds. Stack trace shows connection pool exhausted. WARNING - Memory usage at 95 percent. INFO - Retry attempt for database connection. ERROR - SSL certificate validation failed for external API endpoint. DEBUG - Request payload size 2.3MB response time 450ms.\n' {1..20})\"}
      ]}
    ]
  }")

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Large tool output" "200" "$STATUS"
assert_json "Large output response" "$BODY"

# --- Summary ---
echo ""
echo "=== Claude Code Agent Results ==="
echo "  Passed: $PASS"
echo "  Failed: $FAIL"

[ "$FAIL" -eq 0 ] || exit 1
