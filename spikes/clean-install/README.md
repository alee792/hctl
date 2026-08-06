# Clean-install proof

This credential-free proof exercises the release-archive installation journey
without using the ignored root `./hctl`. It creates a disposable exact Git tag
in a temporary clone, builds the checked `darwin-arm64` archive there, verifies
its checksum, and extracts it into a temporary install prefix. It copies the
minimal agent source outside the checkout, then applies it to a separate empty
workspace with a version-only fake Codex CLI. It verifies the generated Codex
instructions, skill resources, apply record, and managed MCP configuration.
It also rejects dirty tagged source states, proves two builds of the same tag
have identical archive and checksum bytes, and successfully reapplies an
external polyglot source with Deno, uv, and Go to rebuild its workspace-local
cache without copying one from the source.
The MCP command must name the installed temporary binary and retain the
absolute agent-source and workspace paths. The proof also starts that installed
MCP server and verifies its setup before it lists the built-in `echo` tool.

The same installed binary then applies the existing polyglot fixture with an
intentionally restricted `PATH`. The check requires the missing Deno runtime to
produce `hctl: deno is required for authored tools` before native Codex setup is
written. It never starts Codex, calls a model, reads credentials, publishes, or
modifies the repository's ignored generated setup. Temporary files are removed
on exit.

From the repository root:

```sh
# Prerequisites: Deno and uv must be installed. A repository-local Deno in
# .tools/bin is selected before any system Deno by this PATH.
./scripts/bootstrap-tools.sh
export PATH="$PWD/.tools/go/bin:$PWD/.tools/bin:$PATH"
deno --version
uv --version
./spikes/clean-install/check.sh
```

The proof requires Deno and uv for its successful polyglot rerun. The build uses
`GOTOOLCHAIN=local`, the repository module cache, and an isolated build cache.
It cannot select or download a different Go toolchain; on a new checkout, Go
may populate the repository cache with dependencies already pinned by `go.mod`
and `go.sum`.
