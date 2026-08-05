# Polyglot tool-host spike

This spike tests the smallest credible path from conventionally authored tool
functions to one MCP-shaped catalog. It is intentionally outside the shipped
CLI while the source contracts and process behavior are still being learned.

The fixture contains one tool in each supported language:

```text
fixture/tools/
  repeat.ts
  add.py
  hash_text/tool.go
```

There is no hctl manifest or registry. The probe discovers files, asks native
tooling to prepare locked dependencies, starts one long-lived host per
language, combines their catalogs, and calls every tool twice. The second call
must report invocation number two, which proves that a language runtime is not
started per call.

Run it from the repository root:

```sh
./spikes/polyglot-tools/check.sh
```

The check expects the repository-local Go and Deno binaries plus `uv` on
`PATH`. Deno 2.8.1 is installed in `.tools/bin` for this working copy; Python
dependencies are prepared with `uv sync --locked`, and the generated Go host
is compiled from a disposable module under `.tools/cache/`.

This is trusted-code execution. The subprocesses provide protocol separation,
bounded messages, validation, and crash detection. They are not sandboxes.
