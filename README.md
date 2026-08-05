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
the native harness. Apply warns when it safely omits target-specific
presentation metadata and fails before writing files when losing behavior
would be material. See the
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
