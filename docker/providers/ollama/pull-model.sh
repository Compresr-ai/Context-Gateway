#!/bin/bash
# Wait for Ollama server to be ready, then pull the test model
MODEL="${OLLAMA_MODEL:-qwen2:0.5b}"

echo "Waiting for Ollama server..."
until curl -sf http://localhost:11434/api/tags > /dev/null 2>&1; do
  sleep 1
done

echo "Pulling model: $MODEL"
ollama pull "$MODEL"
echo "Model $MODEL ready"
