# hctl

`hctl` is a temporary, functional name for an experimental local tool. The
product name is intentionally deferred.

Define an agent project as files and use it with the capable harness you
already trust. Apply portable instructions, skills, subagents, and managed
tools to any workspace as native Claude Code or Codex setup without replacing
their model loops or interfaces. For headless use, add a session-aware gateway
that connects external input and governs only what crosses its managed
boundary.

## Agent project

An agent is an ordinary, portable directory. It needs an `instructions.md`
file with a description and Markdown body; optional skills, tools, and
instructions-only subagents are discovered by convention:

```text
my-agent/
  instructions.md
  skills/
    research/
      SKILL.md
  tools/
    repeat.ts
    add.py
    hash_text/
      tool.go
  subagents/
    researcher/
      instructions.md
  harnesses/
    claude/
      .claude/
        settings.json
    codex/
      .codex/
        rules/
          default.rules
```

```md
---
description: Review a codebase and recommend concrete improvements.
---

Read the project guidance before changing behavior.
```

The directory name becomes the agent name, normalized to lowercase words with
hyphens. Each skill follows the open Agent Skills layout: a named directory
containing `SKILL.md` and optional scripts, references, assets, or other
resources. Adding a skill directory makes it available on the next apply;
there is no registration file to update. TypeScript, Python, and Go tool
functions under `tools/` are exposed through the same managed MCP server.
Immediate subagents inherit their parent's generated skills and tools through
the native harness. Apply preserves recognized target-specific metadata and
warns when the selected harness does not document honoring it. Native files
that are intentionally not portable can be mirrored under
`harnesses/claude/.claude/` or `harnesses/codex/.codex/`; only the selected
harness receives them. Hctl copies those files literally and owns their
workspace copies, but does not merge, validate, or promise that the harness
honors their contents. Generated skills, subagents, and MCP configuration stay
reserved for hctl. Do not put credentials in authored harness files. See the
[minimal example](examples/minimal) and the
[mixed-language example](spikes/polyglot-tools/fixture).

## Current journey

```sh
go build -o hctl ./cmd/hctl
./hctl apply agents/maintainer --workspace . --harness claude
claude
```

The agent source and workspace are independent. `--workspace` defaults to the
agent directory for a standalone agent; selecting another directory installs
the generated native harness files there:

```sh
hctl apply ~/agents/reviewer --workspace ~/Code/example --harness codex
cd ~/Code/example && codex
```

For headless use, apply the agent first and then submit JSONL input:

```sh
printf '%s\n' '{"input_id":"local-1","text":"Say hello"}' \
  | ./hctl gateway agents/maintainer --workspace . --harness claude
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

## Install on Apple silicon

The first supported distribution is `darwin-arm64` only. From the release page
for the exact `vX.Y.Z` tag, download only these two matching files:

```text
hctl_X.Y.Z_darwin_arm64.tar.gz
hctl_X.Y.Z_SHA256SUMS
```

Verify and install them with the macOS-native tools (replace `X.Y.Z` with the
same version in both filenames):

```sh
cd ~/Downloads
shasum -a 256 -c hctl_X.Y.Z_SHA256SUMS
mkdir -p "$HOME/.local/bin"
tar -xzf hctl_X.Y.Z_darwin_arm64.tar.gz -C "$HOME/.local/bin"
export PATH="$HOME/.local/bin:$PATH"
hctl apply ~/agents/reviewer --workspace ~/Code/example --harness codex
cd ~/Code/example && codex
```

Keep the executable at that stable path: generated MCP setup records its
resolved absolute path, so moving it requires `hctl apply` again. Replacing the
binary in place keeps the reference valid, but the supported upgrade journey
reruns `apply` to refresh any runtime cache. Keep the agent source and its
native lockfiles on each machine; install its required native runtimes and rerun
`apply` rather than copying `.hctl/cache/`.

`go install` is not a supported end-user installation path in the first
release: it requires a Go toolchain and source/module resolution rather than
using the released, checked artifact. A `hctl package` command is also not part
of this contract.

## Development

```sh
./scripts/bootstrap-tools.sh
export PATH="$PWD/.tools/go/bin:$PWD/.tools/bin:$PATH"
./scripts/check.sh
# With Deno and uv installed:
./spikes/polyglot-tools/check.sh
```

The bootstrap installs a pinned Go toolchain, `gopls`, `golangci-lint`,
`goimports`, and `govulncheck` under the ignored `.tools/` directory. Launch
your editor from the configured shell to make the repository-local `gopls`
available. Applying authored tools also requires their native tooling on
`PATH`: Deno for TypeScript, `uv` for Python, and Go for Go tools. The
implementation otherwise favors the Go standard library and uses one maintained
YAML dependency for standards-compliant skill frontmatter; language-specific
schema libraries remain inside tool hosts. Tests use credential-free fake
harness processes; live model calls are not part of the default suite.
