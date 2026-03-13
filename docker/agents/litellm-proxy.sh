#!/bin/bash
# LiteLLM proxy agent simulator
# Sends OpenAI-format requests with X-Provider: litellm header through the gateway
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

# --- Test 1: Simple chat through LiteLLM with local model ---
echo "Test 1: LiteLLM - Local model (Ollama backend)"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${LITELLM_KEY}" \
  -H "X-Provider: litellm" \
  -H "X-Target-URL: ${LITELLM_URL}/v1/chat/completions" \
  -d '{
    "model": "local-model",
    "max_tokens": 50,
    "messages": [
      {"role": "user", "content": "Say hello."}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Local model" "200" "$STATUS"
assert_json "Local model response" "$BODY"

# --- Test 2: Tool result through LiteLLM ---
echo "Test 2: LiteLLM - Tool result"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${LITELLM_KEY}" \
  -H "X-Provider: litellm" \
  -H "X-Target-URL: ${LITELLM_URL}/v1/chat/completions" \
  -d '{
    "model": "local-model",
    "max_tokens": 100,
    "messages": [
      {"role": "user", "content": "What does the config contain?"},
      {"role": "assistant", "content": null, "tool_calls": [
        {"id": "call_ll01", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\": \"config.yaml\"}"}}
      ]},
      {"role": "tool", "tool_call_id": "call_ll01", "content": "server:\n  port: 8080\n  host: localhost"}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Tool result" "200" "$STATUS"
assert_json "Tool result response" "$BODY"

# --- Test 3: Model alias resolution ---
echo "Test 3: LiteLLM - Model alias resolution"
RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${LITELLM_KEY}" \
  -H "X-Provider: litellm" \
  -H "X-Target-URL: ${LITELLM_URL}/v1/chat/completions" \
  -d '{
    "model": "local-model",
    "max_tokens": 30,
    "messages": [
      {"role": "user", "content": "Reply with just the word OK."}
    ]
  }')

STATUS=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')
assert_status "Model alias" "200" "$STATUS"
assert_json "Model alias response" "$BODY"

# Verify the response contains a model field
MODEL=$(echo "$BODY" | jq -r '.model // empty')
if [ -n "$MODEL" ]; then
  echo "  PASS: Model in response: $MODEL"
  PASS=$((PASS + 1))
else
  echo "  FAIL: No model in response"
  FAIL=$((FAIL + 1))
fi

# --- Summary ---
echo ""
echo "=== LiteLLM Proxy Agent Results ==="
echo "  Passed: $PASS"
echo "  Failed: $FAIL"

[ "$FAIL" -eq 0 ] || exit 1
