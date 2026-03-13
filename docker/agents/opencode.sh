#!/bin/bash
# OpenCode agent simulator
# Sends OpenAI Chat Completions requests with multiple tool results
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

# --- Test 1: Multiple tool results in single request ---
echo "Test 1: OpenCode - Multiple tool results"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "X-Target-URL: ${TARGET_URL}/v1/chat/completions" \
  -d '{
    "model": "gpt-4o-mini",
    "max_tokens": 150,
    "messages": [
      {"role": "user", "content": "Compare these two files."},
      {"role": "assistant", "content": null, "tool_calls": [
        {"id": "call_oc01", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\": \"old.go\"}"}},
        {"id": "call_oc02", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\": \"new.go\"}"}}
      ]},
      {"role": "tool", "tool_call_id": "call_oc01", "content": "package main\nfunc old() {}"},
      {"role": "tool", "tool_call_id": "call_oc02", "content": "package main\nfunc new() {}"}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Multiple tools" "200" "$STATUS"
assert_json "Multiple tools response" "$BODY"

# --- Test 2: With system prompt and tool definitions ---
echo "Test 2: OpenCode - With tool definitions"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "X-Target-URL: ${TARGET_URL}/v1/chat/completions" \
  -d '{
    "model": "gpt-4o-mini",
    "max_tokens": 100,
    "messages": [
      {"role": "system", "content": "You are a coding assistant."},
      {"role": "user", "content": "List files in the current directory."}
    ],
    "tools": [
      {"type": "function", "function": {"name": "list_dir", "description": "List directory contents", "parameters": {"type": "object", "properties": {"path": {"type": "string"}}}}},
      {"type": "function", "function": {"name": "read_file", "description": "Read file contents", "parameters": {"type": "object", "properties": {"path": {"type": "string"}}}}}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Tool definitions" "200" "$STATUS"
assert_json "Tool definitions response" "$BODY"

# --- Summary ---
echo ""
echo "=== OpenCode Agent Results ==="
echo "  Passed: $PASS"
echo "  Failed: $FAIL"

[ "$FAIL" -eq 0 ] || exit 1
