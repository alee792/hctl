#!/bin/sh
set -eu

model=${OLLAMA_CLAUDE_MODEL:-gemma3:4b}

if ! command -v ollama >/dev/null 2>&1; then
  echo "claude-ollama: ollama is not installed or not on PATH" >&2
  exit 1
fi
if ! command -v claude >/dev/null 2>&1; then
  echo "claude-ollama: Claude Code is not installed or not on PATH" >&2
  exit 1
fi
if [ "$#" -eq 1 ] && [ "$1" = "--version" ]; then
  exec claude --version
fi
if ! ollama show "$model" >/dev/null 2>&1; then
  echo "claude-ollama: $model is not installed; run 'ollama pull $model'" >&2
  exit 1
fi

export ANTHROPIC_AUTH_TOKEN=ollama
export ANTHROPIC_API_KEY=
export ANTHROPIC_BASE_URL=http://localhost:11434
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
# Keep local responses bounded. This especially matters for reasoning models,
# which can otherwise spend minutes on a trivial prompt.
export CLAUDE_CODE_MAX_OUTPUT_TOKENS=${CLAUDE_CODE_MAX_OUTPUT_TOKENS:-1024}

# gemma3:4b is the reliable installed smoke-test model, but Ollama does not
# expose tool use for it. Ignore both built-in tools and project MCP servers so
# generated hctl configuration cannot make the Anthropic request fail. A later
# --tools flag can override the built-in tool list for another local model.
exec claude --model "$model" --tools "" --strict-mcp-config \
  --mcp-config '{"mcpServers":{}}' "$@"
