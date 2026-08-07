# Claude Code through local Ollama

This spike runs Claude Code against the installed local `gemma3:4b` model via
Ollama's Anthropic-compatible API. It does not change the normal Claude Code
configuration or credentials, and it disables Claude Code's nonessential
network traffic for the local session. The wrapper also caps each response at
1,024 tokens so small local models stay responsive; override that cap with
`CLAUDE_CODE_MAX_OUTPUT_TOKENS` when a task needs a longer response.
See Ollama's [Claude Code integration documentation][ollama-claude].

Ollama reports that `gemma3:4b` does not support tools, so the wrapper disables
Claude Code tools and ignores project MCP servers by default. This validates
the Claude Code transport and the hctl harness, not a complete coding-agent
workflow.

Run an interactive session:

```sh
./spikes/claude-ollama/claude-ollama.sh
```

Run a headless smoke test without tools:

```sh
./spikes/claude-ollama/claude-ollama.sh \
  --bare -p 'Reply with exactly: OLLAMA_CLAUDE_OK'
```

Use the wrapper as hctl's Claude executable:

```sh
go build -o hctl ./cmd/hctl
./hctl apply agents/maintainer --workspace . --harness claude \
  --command "$PWD/spikes/claude-ollama/claude-ollama.sh"
```

Pass the override again for a headless hctl run; `apply` verifies the wrapper
but does not persist the executable choice:

```sh
printf '%s\n' '{"input_id":"ollama-1","text":"Say hello"}' \
  | ./hctl run agents/maintainer --workspace . --harness claude \
      --input jsonl \
      --command "$PWD/spikes/claude-ollama/claude-ollama.sh"
```

When trying a tool-capable model, Claude Code still controls filesystem and
shell tools. Keep its normal permission prompts enabled for interactive use.

The other installed model, `qwen3:4b`, advertises tool support. It can be tried
without changing the wrapper:

```sh
OLLAMA_CLAUDE_MODEL=qwen3:4b \
  ./spikes/claude-ollama/claude-ollama.sh --tools Read
```

On this 8 GiB machine, the Qwen model exhausted a 1,024-token response while
reasoning about a simple `Read` request and never emitted a tool call. Treat it
as an experiment, not a working local coding-agent setup. Ollama recommends a
64k context window for coding tools, while this machine currently allocates 4k.

[ollama-claude]: https://docs.ollama.com/integrations/claude-code
