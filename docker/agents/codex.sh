#!/bin/bash
# Codex agent simulator
# Sends OpenAI Responses API requests through the gateway
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

# --- Test 1: Responses API format ---
echo "Test 1: Codex - Responses API format"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/responses" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "X-Target-URL: ${TARGET_URL}/v1/responses" \
  -d '{
    "model": "gpt-4o-mini",
    "input": "Say hello briefly."
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Responses API" "200" "$STATUS"
assert_json "Responses body" "$BODY"

# --- Test 2: Responses API with tool call output ---
echo "Test 2: Codex - Responses API with function call output"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/responses" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "X-Target-URL: ${TARGET_URL}/v1/responses" \
  -d '{
    "model": "gpt-4o-mini",
    "input": [
      {"type": "message", "role": "user", "content": "Read config.yaml"},
      {"type": "function_call", "id": "fc_01", "call_id": "call_cdx01", "name": "read_file", "arguments": "{\"path\": \"config.yaml\"}"},
      {"type": "function_call_output", "call_id": "call_cdx01", "output": "server:\n  port: 8080\n  host: 0.0.0.0\n  timeout: 30s"}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Tool call output" "200" "$STATUS"
assert_json "Tool output response" "$BODY"

# --- Summary ---
echo ""
echo "=== Codex Agent Results ==="
echo "  Passed: $PASS"
echo "  Failed: $FAIL"

[ "$FAIL" -eq 0 ] || exit 1
