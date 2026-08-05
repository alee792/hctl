# Clean-install proof

This credential-free proof exercises the `setup-agent` journey without using
the ignored root `./hctl`. It builds a fresh binary with the pinned
repository-local Go toolchain into a temporary install prefix, applies
`examples/minimal` to a separate empty workspace with a version-only fake Codex
CLI, and verifies the generated Codex instructions, skill resources, apply
record, and managed MCP configuration. The MCP command must name the installed
temporary binary and retain the absolute agent-source and workspace paths.

The same installed binary then applies the existing polyglot fixture with an
intentionally restricted `PATH`. The check requires the missing Deno runtime to
produce `hctl: deno is required for authored tools` before native Codex setup is
written. It never starts Codex, calls a model, reads credentials, publishes, or
modifies the repository's ignored generated setup. Temporary files are removed
on exit.

From the repository root:

```sh
./scripts/bootstrap-tools.sh
./spikes/clean-install/check.sh
```

The build uses `GOTOOLCHAIN=local`, the repository module cache, and an isolated
build cache. It cannot select or download a different Go toolchain; on a new
checkout, Go may populate the repository cache with dependencies already pinned
by `go.mod` and `go.sum`.
