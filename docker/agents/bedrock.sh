#!/bin/bash
# Bedrock agent simulator
# Sends Anthropic Messages format requests with X-Provider: bedrock header
# Routes through gateway → LiteLLM → Ollama (since no local Bedrock emulator exists)
set -euo pipefail

PASS=0
FAIL=0

LITELLM_URL="${TARGET_URL:-http://litellm:4000}"
LITELLM_KEY="${LITELLM_API_KEY:-sk-test-litellm-key}"

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

# --- Test 1: Bedrock-style request with tool result ---
echo "Test 1: Bedrock - Tool result (Anthropic format, X-Provider: bedrock)"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${LITELLM_KEY}" \
  -H "X-Provider: bedrock" \
  -H "X-Target-URL: ${LITELLM_URL}/v1/chat/completions" \
  -d '{
    "model": "bedrock-model",
    "max_tokens": 100,
    "messages": [
      {"role": "user", "content": "What does the config contain?"},
      {"role": "assistant", "content": null, "tool_calls": [
        {"id": "call_br01", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\": \"config.yaml\"}"}}
      ]},
      {"role": "tool", "tool_call_id": "call_br01", "content": "server:\n  port: 8080\n  host: localhost\n  timeout: 30s"}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Bedrock tool result" "200" "$STATUS"
assert_json "Bedrock response" "$BODY"

# --- Test 2: Simple chat through Bedrock adapter ---
echo "Test 2: Bedrock - Simple chat"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${LITELLM_KEY}" \
  -H "X-Provider: bedrock" \
  -H "X-Target-URL: ${LITELLM_URL}/v1/chat/completions" \
  -d '{
    "model": "bedrock-model",
    "max_tokens": 50,
    "messages": [
      {"role": "user", "content": "Say hello in one word."}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Simple chat" "200" "$STATUS"
assert_json "Chat response" "$BODY"

# --- Test 3: Multiple tool results ---
echo "Test 3: Bedrock - Multiple tool results"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${LITELLM_KEY}" \
  -H "X-Provider: bedrock" \
  -H "X-Target-URL: ${LITELLM_URL}/v1/chat/completions" \
  -d '{
    "model": "bedrock-model",
    "max_tokens": 100,
    "messages": [
      {"role": "user", "content": "Read both files"},
      {"role": "assistant", "content": null, "tool_calls": [
        {"id": "call_br02a", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\": \"a.go\"}"}},
        {"id": "call_br02b", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\": \"b.go\"}"}}
      ]},
      {"role": "tool", "tool_call_id": "call_br02a", "content": "package a\nfunc A() {}"},
      {"role": "tool", "tool_call_id": "call_br02b", "content": "package b\nfunc B() {}"}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Multiple tools" "200" "$STATUS"
assert_json "Multiple tools response" "$BODY"

# --- Summary ---
echo ""
echo "=== Bedrock Agent Results ==="
echo "  Passed: $PASS"
echo "  Failed: $FAIL"

[ "$FAIL" -eq 0 ] || exit 1
