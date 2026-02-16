#!/usr/bin/env bash
set -euo pipefail

HF_MODEL="Nanbeige/Nanbeige4.1-3B"
HF_TOKEN="${HF_TOKEN:-}"
HF_TOKEN_FLAG=()
if [[ -n "$HF_TOKEN" ]]; then
  HF_TOKEN_FLAG=(--env "HF_TOKEN=$HF_TOKEN")
fi

mkdir -p "$HOME/.cache/huggingface"

exec docker run --gpus all \
  -v "$HOME/.cache/huggingface:/root/.cache/huggingface" \
  "${HF_TOKEN_FLAG[@]}" \
  -e LD_LIBRARY_PATH=/lib \
  -p 8000:8000 \
  --ipc=host \
  vllm/vllm-openai:latest \
  --model "$HF_MODEL" \
  --gpu-memory-utilization 0.8 \
  --max-model-len 4096 \
  --enable-auto-tool-choice \
  --tool-call-parser hermes
