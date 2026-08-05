# hctl

`hctl` is a temporary, functional name for an experimental local tool. The
product name is intentionally deferred.

Define an agent project as files and use it with the capable harness you
already trust. Compile portable instructions, skills, and managed tools
into native Claude Code and Codex setups without replacing their model loops or
interfaces. For headless use, add a session-aware gateway that connects
external input and governs only what crosses its managed boundary.

## Agent project

An agent is an ordinary directory. It needs only an `instructions.md` file;
optional skills are Markdown files discovered from `skills/`:

```text
my-agent/
  instructions.md
  skills/
    research.md
```

The directory name becomes the agent name, normalized to lowercase words with
hyphens. Adding a skill file makes it available on the next `hctl apply`; there
is no registration file to update. See the [minimal example](examples/minimal).

## Current journey

```sh
go build -o hctl ./cmd/hctl
./hctl apply ./examples/minimal --harness claude
cd examples/minimal && claude
```

For headless use, apply the agent first and then submit JSONL input:

```sh
printf '%s\n' '{"input_id":"local-1","text":"Say hello"}' \
  | ./hctl gateway ./examples/minimal --harness claude
```

Use `--harness codex` for Codex. Interactive work remains in the native
harness. Codex loads the generated project configuration after the user trusts
the repository on first launch. The gateway exists for headless sessions and
future input adapters.

## Product boundary

Claude Code and Codex own model calls, context management, planning, native
tools, approvals, and interactive UX. `hctl` owns only the filesystem
compilation, generated harness files, session mapping, and tools routed
through its managed boundary.

Native harness tools remain available and unmanaged. Instructions and
skills influence model behavior; they do not provide enforcement.

See [the vision](docs/vision.md), [product specification](docs/product-spec.md),
[glossary](docs/glossary.md), [current working status](docs/workbench/status.md),
and [architecture decisions](docs/adr/).

## Development

```sh
./scripts/bootstrap-tools.sh
export PATH="$PWD/.tools/go/bin:$PWD/.tools/bin:$PATH"
./scripts/check.sh
```

The bootstrap installs a pinned Go toolchain, `gopls`, `golangci-lint`,
`goimports`, and `govulncheck` under the ignored `.tools/` directory. Launch
your editor from the configured shell to make the repository-local `gopls`
available. The implementation uses the Go standard library. Tests use
credential-free fake harness processes; live model calls are not part of the
default suite.
