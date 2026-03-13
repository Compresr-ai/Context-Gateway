#!/bin/bash
# Ollama direct agent simulator
# Sends requests to Ollama through the gateway using both native and OpenAI-compat endpoints
set -euo pipefail

PASS=0
FAIL=0

OLLAMA_URL="${TARGET_URL:-http://ollama:11434}"

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

# --- Test 1: Ollama native /api/chat endpoint ---
echo "Test 1: Ollama - Native /api/chat"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/api/chat" \
  -H "Content-Type: application/json" \
  -H "X-Target-URL: ${OLLAMA_URL}/api/chat" \
  -d '{
    "model": "qwen2:0.5b",
    "stream": false,
    "messages": [
      {"role": "user", "content": "Say hello in one word."}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Native /api/chat" "200" "$STATUS"
assert_json "Native response" "$BODY"

# --- Test 2: Ollama OpenAI-compatible endpoint ---
echo "Test 2: Ollama - OpenAI-compatible /v1/chat/completions"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "X-Provider: ollama" \
  -H "X-Target-URL: ${OLLAMA_URL}/v1/chat/completions" \
  -d '{
    "model": "qwen2:0.5b",
    "messages": [
      {"role": "user", "content": "Say hi."}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "OpenAI-compat" "200" "$STATUS"
assert_json "OpenAI-compat response" "$BODY"

# --- Test 3: Tool result via Ollama ---
echo "Test 3: Ollama - Tool result"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "X-Provider: ollama" \
  -H "X-Target-URL: ${OLLAMA_URL}/v1/chat/completions" \
  -d '{
    "model": "qwen2:0.5b",
    "messages": [
      {"role": "user", "content": "What is in the file?"},
      {"role": "assistant", "content": null, "tool_calls": [
        {"id": "call_ol01", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\": \"test.txt\"}"}}
      ]},
      {"role": "tool", "tool_call_id": "call_ol01", "content": "Hello from test file"}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Tool result" "200" "$STATUS"
assert_json "Tool result response" "$BODY"

# --- Test 4: Verify no auth needed ---
echo "Test 4: Ollama - No auth headers"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/api/chat" \
  -H "Content-Type: application/json" \
  -H "X-Target-URL: ${OLLAMA_URL}/api/chat" \
  -d '{
    "model": "qwen2:0.5b",
    "stream": false,
    "messages": [
      {"role": "user", "content": "Hi"}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
assert_status "No auth" "200" "$STATUS"

# --- Summary ---
echo ""
echo "=== Ollama Direct Agent Results ==="
echo "  Passed: $PASS"
echo "  Failed: $FAIL"

[ "$FAIL" -eq 0 ] || exit 1
