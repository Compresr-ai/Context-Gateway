#!/bin/bash
# OpenClaw agent simulator
# Sends Anthropic Messages API requests (similar to Claude Code but with different patterns)
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

# --- Test 1: Anthropic format with tool result ---
echo "Test 1: OpenClaw - Tool result (Anthropic format)"
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
      {"role": "user", "content": "Read the README"},
      {"role": "assistant", "content": [
        {"type": "tool_use", "id": "toolu_oc01", "name": "read_file", "input": {"path": "README.md"}}
      ]},
      {"role": "user", "content": [
        {"type": "tool_result", "tool_use_id": "toolu_oc01", "content": "# My Project\n\nA simple Go application for processing data."}
      ]}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Tool result" "200" "$STATUS"
assert_json "Response body" "$BODY"

# --- Test 2: Multiple tool results in one turn ---
echo "Test 2: OpenClaw - Multiple tool results"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/messages" \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -H "x-api-key: ${API_KEY}" \
  -H "X-Target-URL: ${TARGET_URL}/v1/messages" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "max_tokens": 200,
    "messages": [
      {"role": "user", "content": "Read both files"},
      {"role": "assistant", "content": [
        {"type": "tool_use", "id": "toolu_oc02a", "name": "read_file", "input": {"path": "a.go"}},
        {"type": "tool_use", "id": "toolu_oc02b", "name": "read_file", "input": {"path": "b.go"}}
      ]},
      {"role": "user", "content": [
        {"type": "tool_result", "tool_use_id": "toolu_oc02a", "content": "package a\nfunc A() string { return \"a\" }"},
        {"type": "tool_result", "tool_use_id": "toolu_oc02b", "content": "package b\nfunc B() string { return \"b\" }"}
      ]}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Multiple tools" "200" "$STATUS"
assert_json "Multiple tools response" "$BODY"

# --- Summary ---
echo ""
echo "=== OpenClaw Agent Results ==="
echo "  Passed: $PASS"
echo "  Failed: $FAIL"

[ "$FAIL" -eq 0 ] || exit 1
