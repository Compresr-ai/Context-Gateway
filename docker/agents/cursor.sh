#!/bin/bash
# Cursor agent simulator
# Sends OpenAI Chat Completions requests through the gateway
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

# --- Test 1: Simple chat (OpenAI format) ---
echo "Test 1: Cursor - Simple chat"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "X-Target-URL: ${TARGET_URL}/v1/chat/completions" \
  -d '{
    "model": "gpt-4o-mini",
    "max_tokens": 50,
    "messages": [
      {"role": "user", "content": "Say hello."}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Simple chat" "200" "$STATUS"
assert_json "Chat response" "$BODY"

# --- Test 2: Tool call with function calling ---
echo "Test 2: Cursor - Tool call with function result"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "X-Target-URL: ${TARGET_URL}/v1/chat/completions" \
  -d '{
    "model": "gpt-4o-mini",
    "max_tokens": 100,
    "messages": [
      {"role": "user", "content": "What does main.go contain?"},
      {"role": "assistant", "content": null, "tool_calls": [
        {"id": "call_cur01", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\": \"main.go\"}"}}
      ]},
      {"role": "tool", "tool_call_id": "call_cur01", "content": "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello\")\n}"}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Tool call" "200" "$STATUS"
assert_json "Tool response" "$BODY"

# --- Test 3: Multi-turn with tool calls ---
echo "Test 3: Cursor - Multi-turn conversation"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "X-Target-URL: ${TARGET_URL}/v1/chat/completions" \
  -d '{
    "model": "gpt-4o-mini",
    "max_tokens": 100,
    "messages": [
      {"role": "system", "content": "You are a helpful coding assistant."},
      {"role": "user", "content": "Read both files"},
      {"role": "assistant", "content": null, "tool_calls": [
        {"id": "call_m1", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\": \"a.txt\"}"}},
        {"id": "call_m2", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\": \"b.txt\"}"}}
      ]},
      {"role": "tool", "tool_call_id": "call_m1", "content": "File A content"},
      {"role": "tool", "tool_call_id": "call_m2", "content": "File B content"},
      {"role": "user", "content": "Now summarize both files."}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Multi-turn" "200" "$STATUS"
assert_json "Multi-turn response" "$BODY"

# --- Summary ---
echo ""
echo "=== Cursor Agent Results ==="
echo "  Passed: $PASS"
echo "  Failed: $FAIL"

[ "$FAIL" -eq 0 ] || exit 1
