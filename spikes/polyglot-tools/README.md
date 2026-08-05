# Polyglot tool-host proof

This executable proof tests the shipped path from conventionally authored tool
functions to one MCP-shaped catalog through generated Claude and Codex setup.

The fixture contains one tool in each supported language:

```text
fixture/tools/
  repeat.ts
  add.py
  hash_text/tool.go
```

There is no hctl manifest or registry. The probe copies the portable agent
source separately from its workspace, applies both harnesses, launches each
generated MCP command, and calls every tool twice. The second call must report
invocation number two, proving that a language runtime is not started per call.
It also validates native subagent files and safely switches the workspace to a
second agent.

Run it from the repository root:

```sh
./spikes/polyglot-tools/check.sh
```

The check expects repository-local Go and Deno binaries plus `uv` on `PATH`.
Python dependencies are prepared with `uv sync --locked`; runtime environments
and the generated Go host are stored under the disposable workspace
`.hctl/cache/tools/`.

This is trusted-code execution. The subprocesses provide protocol separation,
bounded messages, validation, and crash detection. They are not sandboxes.
