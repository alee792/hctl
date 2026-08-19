# hctl

`hctl` is a temporary, functional working name — not a product name.

hctl makes an agent something you can read: a folder of plain-language
files — instructions you review like a document, skills you compose by
dropping in a directory — validated, versioned, and applied to the capable
native harness you already trust, Claude Code or Codex, through thin vendor
adapters and without replacing their model loop or interface.

## Five-minute quickstart

Build from source (`go install` is not a supported install path — see
[Install on Apple silicon](#install-on-apple-silicon) below):

```sh
go build -o hctl ./cmd/hctl
```

An agent project is a directory with an `instructions.md` file. Optional
skills, tools, and other components are discovered by convention. A minimal
one looks like this:

```text
my-agent/
  instructions.md
  skills/
    greet/
      SKILL.md
```

`instructions.md` starts with YAML frontmatter carrying one `description`,
followed by the Markdown instructions themselves:

```md
---
description: Greet the user and keep answers short.
---

You are a concise assistant. Say hello and offer to help.
```

Each skill's `SKILL.md` names itself after its directory:

```md
---
name: greet
description: Greet people warmly in one short sentence.
---

When greeting someone, keep it to one friendly sentence.
```

Apply it to a workspace and start the native harness:

```sh
./hctl apply ./my-agent --workspace ~/code/some-repo --harness claude
cd ~/code/some-repo && claude
```

The agent source and the workspace are independent; `--workspace` defaults to
the agent directory, so a standalone agent is the simplest case. Use
`--harness codex` for Codex, which applies its own native repository-trust
flow on first launch. Interactive work always stays in the native harness.

See [examples/minimal](examples/minimal) for a working copy of this project,
including its generated output.

## The agent project

A fuller agent project uses more of the same conventions:

```text
my-agent/
  instructions.md
  skills/
    research/
      SKILL.md
  plugins/
    review-pack/              # complete publisher-authored package
      plugin.json
      skills/
        review/
          SKILL.md
  tools/
    get_weather.ts
    lookup_policy.py
    hash_text/
      tool.go
  subagents/
    researcher/
      instructions.md
  connections/
    github.md
  channels/
    discord.md
  schedules/
    billing/
      sweep.md
  harnesses/
    claude/
      .claude/
        settings.json
    codex/
      .codex/
        rules/
          default.rules
```

The directory name becomes the agent name, normalized to lowercase words with
hyphens. Each component below links to the
[product specification](docs/product-spec.md) for its exact contract and
bounds rather than repeating them here.

**Skills.** Each directory under `skills/` follows the open
[Agent Skills specification](https://agentskills.io/specification): a
`SKILL.md` plus optional resources. Adding a skill directory makes it
available on the next apply — there is no separate registration file.

**Plugins.** An [Agent Plugin v1](https://agent-plugins.org/specification) is
a complete publisher-authored package. A consumer copies the reviewed
directory beneath `plugins/`; this is manual vendoring, not an acquisition
engine — review, version pinning, and provenance belong to your own version
control.

**Tools.** A TypeScript, Python, or Go source file under `tools/` declares one
schema-validated function, exposed to the selected harness through one
hctl-managed MCP server — no protocol code required. This is the advanced rung
of the one author capability ladder: you may write the file directly or ask
your harness to draft it. Validation proves the contract, not the behavior;
an authored tool is your code, adopted like any other, and hctl does not
sandbox its behavior.

**Subagents.** Each directory under `subagents/` supplies an
`instructions.md` and inherits the parent's generated skills and tools. An
optional `effort: low|medium|high` frontmatter field requests portable
reasoning effort; the selected harness decides whether to honor it.

**Connections.** A `connections/<name>.md` file authors access to an
installed native MCP server or a credential-free HTTPS Streamable HTTP
endpoint. Author one with a command instead of hand-writing frontmatter:

```sh
hctl connection add ./my-agent github \
  --package github-mcp-server --capability github \
  --context "Use the discovered GitHub tools for repository work."
```

Claude Code or Codex owns native startup, trust, approval, authentication,
and effects; hctl only compiles the configuration.

**Harness-specific files.** Files mirrored under `harnesses/claude/.claude/`
or `harnesses/codex/.codex/` are copied literally to only the matching
harness. Hctl owns the workspace copy but does not interpret or enforce their
contents.

**Schedules.** A Markdown file under `schedules/` with a `cron` frontmatter
field and a prompt body. `apply` validates and fingerprints schedules without
starting a clock — see [Operating an agent](#operating-an-agent) below to run
one.

Across all of this, Claude Code and Codex remain the native harnesses: they
own model calls, context, native tools, approvals, and the interactive UX.
Instructions and skills influence model behavior; they do not enforce it.

## Operating an agent

Operating is a distinct, second role on the same agent project: headless
runs, schedules, channels, and staged filesystems for deployment, each with
its own explicit guardrails. This is where portability is proven — the same
folder that runs interactively also applies unchanged to a headless
dispatcher.

**Headless JSONL run.**

```sh
printf '%s\n' '{"input_id":"local-1","text":"Say hello"}' \
  | ./hctl run ./my-agent --workspace ~/code/some-repo --harness claude --input jsonl
```

**Schedules.** Trigger one occurrence with a stable caller-owned ID, or run
the validated schedules from a foreground clock:

```sh
hctl schedule trigger ~/agents/reviewer billing/sweep \
  --workspace ~/Code/example --harness codex \
  --input-id billing-sweep-2026-08-06 --turn-timeout 90s

hctl schedule run ~/agents/reviewer --workspace ~/Code/example --harness codex
```

Each accepted occurrence starts a fresh native-harness session; the command
reports lifecycle status and discards model text rather than installing a
daemon or replaying missed work.

**Discord channel.** An agent with `channels/discord.md` can run as a
conversational Discord Gateway bot. Install the adapter as a trusted
integration package once, enroll a bot, then run:

```sh
./hctl integration install PACKAGE_ROOT --trust operator
./hctl channel setup discord ~/agents/reviewer
./hctl run ~/agents/reviewer --workspace ~/Code/example --harness codex
```

Guild and DM conversations run read-only by default, with isolated Git
worktrees promoted only when a request needs a workspace write. See the
[product specification](docs/product-spec.md#authored-project) for the full
capacity, credential, and worktree contract, and
[examples/discord](examples/discord) for a complete source example.

**Staged filesystem.** `hctl stage` prepares one runnable filesystem tree at
canonical paths for an existing OCI image builder, carrying only the
execution closure the agent actually needs — no build toolchain, credentials,
or conversation state:

```sh
hctl stage ./my-agent --harness codex --output /out
```

See [Harness images](docs/harness-images.md) for the direct single-stage
image, the selective two-stage Dockerfile, the exact compatible-base digest,
and the runtime authentication boundary.

## Install on Apple silicon

The first supported distribution is `darwin-arm64` only. From the release
page for the exact `vX.Y.Z` tag, download only these two matching files:

```text
hctl_X.Y.Z_darwin_arm64.tar.gz
hctl_X.Y.Z_SHA256SUMS
```

Verify and install them with macOS-native tools (replace `X.Y.Z` with the same
version in both filenames):

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
resolved absolute path, so moving it requires `hctl apply` again. `go install`
is not a supported end-user installation path — it requires a Go toolchain
and source resolution rather than the released, checked artifact.

## Development

```sh
./scripts/bootstrap-tools.sh
export PATH="$PWD/.tools/go/bin:$PWD/.tools/bin:$PATH"
./scripts/check.sh
# With Deno and uv installed:
./spikes/polyglot-tools/check.sh
```

The bootstrap installs a pinned Go toolchain, `gopls`, `golangci-lint`,
`goimports`, and `govulncheck` under the ignored `.tools/` directory.
Applying authored tools also requires their native tooling on `PATH`: Deno
for TypeScript, `uv` for Python, and Go for Go tools. Tests use
credential-free fake harness processes; live model calls are not part of the
default suite.

## Links

- [Vision](docs/vision.md)
- [Product specification](docs/product-spec.md)
- [Glossary](docs/glossary.md)
- [Architecture map](docs/architecture.md)
- [Working status](docs/workbench/status.md)
- [Architecture decisions](docs/adr/)
