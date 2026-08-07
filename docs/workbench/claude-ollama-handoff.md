# Claude Code through Ollama validation

Run these commands from the repository checkout under test. On 2026-08-07,
`./scripts/check.sh` passed and the full `hctl run` below exited successfully
after emitting deltas spelling `HCTL_OLLAMA_OK` and a `turn.completed` event.

## Fast smoke test

Confirm that Ollama, Claude Code, and the two local models are available:

```sh
command -v ollama
ollama --version
ollama list
command -v claude
claude --version
```

The verified text-only path uses `gemma3:4b` and disables Claude Code tools and
project MCP servers:

```sh
./spikes/claude-ollama/claude-ollama.sh \
  --bare --output-format json \
  -p 'Reply with exactly: OLLAMA_CLAUDE_OK'
```

Expect a successful Claude Code result whose `result` is
`OLLAMA_CLAUDE_OK`. The first response may take about a minute on this machine.

For an interactive text-only session:

```sh
./spikes/claude-ollama/claude-ollama.sh
```

## Exact hctl protocol smoke test

The following reproduces the stream-JSON invocation used by hctl without
requiring the Go toolchain:

```sh
printf '%s\n' '{"type":"user","message":{"role":"user","content":"Reply with exactly: HCTL_OLLAMA_OK"},"parent_tool_use_id":null}' \
  | ./spikes/claude-ollama/claude-ollama.sh \
      -p --input-format stream-json --output-format stream-json --verbose \
      --include-partial-messages --replay-user-messages
```

Expect `text_delta` events spelling `HCTL_OLLAMA_OK`, followed by a successful
terminal result with a resumable session ID.

## Full hctl run

Bootstrap the repository-local toolchain if needed, build hctl, and pass the
wrapper as the Claude executable on both `apply` and `run`:

```sh
./scripts/bootstrap-tools.sh

PATH="$PWD/.tools/go/bin:$PWD/.tools/bin:$PATH" \
  "$PWD/.tools/go/bin/go" build -o hctl ./cmd/hctl

PATH="$PWD/.tools/go/bin:$PWD/.tools/bin:$PATH" \
  ./hctl apply agents/maintainer --workspace . \
  --harness claude \
  --command "$PWD/spikes/claude-ollama/claude-ollama.sh"

printf '%s\n' '{"input_id":"ollama-1","text":"Reply with exactly: HCTL_OLLAMA_OK"}' \
  | PATH="$PWD/.tools/go/bin:$PWD/.tools/bin:$PATH" \
    ./hctl run agents/maintainer --workspace . \
      --harness claude --input jsonl --turn-timeout 3m \
      --command "$PWD/spikes/claude-ollama/claude-ollama.sh"
```

## Known boundary

`gemma3:4b` is the reliable transport test but Ollama rejects tool requests for
it. The installed `qwen3:4b` advertises tool support, but on this 8 GiB machine
it exhausted the wrapper's 1,024-token response budget while reasoning about a
simple `Read` request and never emitted a tool call. Ollama currently allocates
a 4k context here, below its 64k recommendation for coding tools. Do not treat
this spike as a working local coding agent yet.

The Qwen experiment is:

```sh
OLLAMA_CLAUDE_MODEL=qwen3:4b \
  ./spikes/claude-ollama/claude-ollama.sh \
    --tools Read
```

Keep Claude Code permission prompts enabled whenever tools are enabled.
