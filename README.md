# hctl

`hctl` is a temporary, functional name for an experimental local tool. The
product name is intentionally deferred.

Define an agent project as files and use it with the capable native harness you
already trust. Apply portable instructions, skills, subagents, and managed
tools to any workspace through a generated Claude Code or Codex integration
without replacing their model loops or interfaces. For headless use, add a
session-aware turn dispatcher that connects external input and governs only
what crosses its managed boundary.

## Agent project

An agent project is an ordinary, portable directory. It needs an
`instructions.md` file with a description and Markdown body; optional skills,
tools, and inherited subagents are discovered by convention:

```text
my-agent/
  instructions.md
  skills/
    research/
      SKILL.md
  plugins/
    review-pack/              # complete publisher-authored package
      plugin.json
      mcp.json
      bin/
        review-notes
      skills/
        review/
          SKILL.md
  tools/
    repeat.ts
    add.py
    hash_text/
      tool.go
  subagents/
    researcher/
      instructions.md
  connections/
    github.md
    public-catalog.md
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

```md
---
description: Review a codebase and recommend concrete improvements.
---

Read the project guidance before changing behavior.
```

The directory name becomes the agent name, normalized to lowercase words with
hyphens. Each skill follows the open
[Agent Skills specification](https://agentskills.io/specification): a named
directory containing `SKILL.md` and optional scripts, references, assets, or
other resources. Adding a skill directory makes it available on the next
apply; there is no registration file to update.

An [Agent Plugin v1](https://agent-plugins.org/specification) is a complete
directory package authored by its publisher, including its `plugin.json` and
any conventional components. A consumer copies the reviewed complete directory
beneath `plugins/`; do not recreate its manifest or manufacture a wrapper
Plugin. Existing complete Skills are vendored the same way: copy the reviewed
directory beneath `skills/`. Review, version pinning, and provenance belong to
your own version control, where the rest of the agent source already lives.

Hctl validates each local `plugin.json`, imports skills from its fixed
`skills/` location, and maps valid `stdio` or `streamable-http` declarations
from optional `mcp.json` into native project MCP configuration. Root skills and
earlier server declarations win name collisions. Invalid plugin components
warn and remain isolated. Plugin-bundled MCP remains part of the complete
publisher-authored package; a standalone connection is authored separately and
does not require a wrapper `plugin.json`. Hctl does not operate plugin servers,
contact a marketplace, carry credentials, or interpret client extensions.
TypeScript, Python, and Go tool functions under `tools/` are exposed through
the same managed MCP server.
Immediate subagents inherit their parent's generated skills and tools through
the native harness. A subagent may optionally request portable reasoning effort
in its `instructions.md` frontmatter:

```md
---
description: Review the implementation against the specification.
effort: high
---

Report only actionable discrepancies.
```

Effort accepts `low`, `medium`, or `high`; hctl passes the request to the
selected harness but cannot guarantee that its model, account, or policy honors
it. Apply preserves recognized target-specific metadata and warns when the
selected harness does not document honoring it. Native files
that are intentionally not portable can be mirrored under
`harnesses/claude/.claude/` or `harnesses/codex/.codex/`; only the selected
harness receives them. Hctl copies those files literally and owns their
workspace copies, but does not merge, validate, or promise that the harness
honors their contents. Generated skills, subagents, and MCP configuration stay
reserved for hctl. Do not put credentials in authored harness files.

A standalone native MCP connection is one bounded
`connections/<name>.md` file. Frontmatter selects either an exact
operator-installed `native-mcp` package capability or one credential-free
HTTPS Streamable HTTP endpoint; optional Markdown gives the agent additional
usage context. For example:

```md
---
type: mcp
package: github-mcp-server
capability: github
---

Use the discovered GitHub tools for repository and pull-request work.
```

Non-developers can author the same source without editing YAML or native
harness configuration:

```sh
hctl connection add ./my-agent github \
  --package github-mcp-server --capability github \
  --context "Use the discovered GitHub tools for repository work."
hctl connection add ./my-agent public-catalog \
  --url https://example.com/mcp
hctl connection status ./my-agent
```

These commands do not install packages, contact remote endpoints, choose a
harness, or implicitly apply a workspace. Apply the agent explicitly, and use
`hctl connection remove ./my-agent NAME` before reapplying to remove a native
entry. Remote v1 has no header, credential, or OAuth fields. Claude Code or
Codex owns native startup, trust, approval, authentication, discovery, calls,
and effects.

The GitHub example selects the operator-installed official server through this
generic path. Its ambient `GITHUB_PERSONAL_ACCESS_TOKEN` remains native and
unmanaged: hctl does not read or persist the value and does not filter,
confirm, authorize, or audit native GitHub effects. Native Git and `gh`
authentication remain separately operator-owned. See the
[complete native GitHub MCP journey](docs/github-native-mcp.md),
[minimal example](examples/minimal), [plugin example](examples/plugins),
[public GitHub example](examples/github),
[Discord source example](examples/discord), and the
[mixed-language example](spikes/polyglot-tools/fixture).

A root agent may also define Markdown task schedules. The relative path is the
schedule name, frontmatter contains one five-field `cron` string, and the body
is the prompt:

```md
---
cron: "0 9 * * 1-5"
---

Sweep stale billing work.
```

Apply validates and fingerprints schedules but starts no clock. Trigger one
occurrence explicitly with a stable caller-owned ID:

```sh
hctl schedule trigger ~/agents/reviewer billing/sweep \
  --workspace ~/Code/example --harness codex \
  --input-id billing-sweep-2026-08-06 --turn-timeout 90s

# Or keep the validated schedules running from a foreground UTC clock.
hctl schedule run ~/agents/reviewer \
  --workspace ~/Code/example --harness codex
```

Each accepted occurrence starts a fresh native-harness session. Retrying the
same input ID is deduplicated through the durable turn dispatcher. The command reports
lifecycle status and discards model text; it does not register a cron job,
install a daemon, replay missed work, or deliver output to a channel.
The task turn deadline is independent of the command's overall `--timeout`;
expiry aborts that harness process and retains the occurrence as uncertain so
it cannot be silently rerun. Both the first result and later duplicates report
the durable `deadline_exceeded` reason.

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

For explicit headless JSONL input:

```sh
printf '%s\n' '{"input_id":"local-1","text":"Say hello"}' \
  | ./hctl run agents/maintainer --workspace . --harness claude --input jsonl
```

Use `--harness codex` for Codex. Interactive work remains in the native
harness. Codex loads the generated project configuration after the user trusts
the repository on first launch. The turn dispatcher exists for headless sessions and
future input adapters.

## Optional staged filesystem

`hctl stage` prepares one complete runnable filesystem tree for an existing
image builder. The output directory must not already exist:

```sh
hctl stage ./my-agent --harness codex --output /out
```

Staging validates the harness and agent, prepares and inspects locked authored
tools, then publishes `/out` atomically. The tree uses canonical runtime paths
under `/opt/hctl`, `/workspace`, and `/home/hctl`; generated configuration never
refers to the build directory. `opt/hctl/artifact.json` records the harness and
agent identity, target OS/architecture/ABI, selected runtimes, final paths, and
the hash, mode, and intended owner of every other file.

The command carries only the execution closure selected by the agent: Deno for
TypeScript, Python and uv for Python, and the compiled host rather than the Go
toolchain for Go. A tool-free agent carries none of those runtimes. Build caches,
native harness login state, provider credentials, channel tokens, conversations,
and deployment configuration are excluded. Staging targets the current machine;
it does not cross-compile or make a glibc payload compatible with Alpine.

The intended optional two-stage pattern is:

```dockerfile
FROM HCTL_CODEX_IMAGE AS build
COPY . /agent
RUN hctl stage /agent --harness codex --output /out/agent

FROM DOCUMENTED_COMPATIBLE_BASE
COPY --from=build /out/agent/opt/ /opt/
COPY --from=build --chown=65532:65532 /out/agent/workspace/ /workspace/
COPY --from=build --chown=65532:65532 /out/agent/home/hctl/ /home/hctl/
USER 65532:65532
ENTRYPOINT ["/opt/hctl/bin/agent-entrypoint"]
```

The hctl harness image reserves `/out` as a writable directory for its
non-root user. Each staged output is a new child such as `/out/agent`.

See [Harness images](docs/harness-images.md) for complete direct and two-stage
Codex Dockerfiles, the exact compatible-base digest, and the runtime
authentication boundary.

The final base must match the artifact ABI and provide the documented harness
libraries, certificates, `/bin/sh`, `id`, and UID/GID 65532. The entrypoint
fails instead of silently running as another identity. Keep `/workspace` and
`/home/hctl` writable and durable when channel or harness state must survive a
restart; do not mount an empty volume over the prepared workspace. The Codex
image is currently built but not published; the Claude image and exact-tag
publication are separate release work. Until a harness image is published,
`--command PATH` is useful only for a
self-contained harness executable; a harness installed under
`/opt/hctl/harness` is copied with that full runtime tree. Authored Python
staging likewise requires the relocatable interpreter supplied at
`/opt/hctl/runtimes/python`; hctl refuses to present an arbitrary system Python
installation as a portable closure.

An agent with `channels/discord.md` can run as a conversational Discord Gateway
bot without a public listener or tunnel. Build an exact package for the current
platform, explicitly trust its local source, enroll an existing bot once, then
run the agent:

```sh
package_root="$(mktemp -d)/hctl-discord"
./discordadapter/build-package.sh \
  --version 0.1.0 \
  --revision "$(git rev-parse HEAD)" \
  --target "$(go env GOOS)-$(go env GOARCH)" \
  --output "$package_root"
./hctl integration install "$package_root" --trust operator
./hctl channel setup discord ~/agents/reviewer
./hctl channel status discord ~/agents/reviewer
./hctl run ~/agents/reviewer --workspace ~/Code/example --harness codex
# When retiring the enrollment:
./hctl channel remove discord ~/agents/reviewer
```

Setup stores non-secret profile metadata in the OS-standard hctl configuration
directory and the token in the OS credential store. Run validates that token
against the configured application and bot IDs, auto-applies stale generated
setup, opens an outbound Discord Gateway connection, and serves the authorized
user in one configured guild channel plus DM. The bot reads eligible ambient
messages, applies the participation policy in `channels/discord.md`, replies in
the same channel, and stays silent when the agent returns exactly
`HCTL_NO_REPLY`. `/new` resets an idle conversation and `/status` reports safe
runtime status. Deployment may mount the same non-secret TOML configuration and
inject `HCTL_DISCORD_TOKEN`. Hctl passes that opaque value only to the exact
selected Discord adapter for setup, status, remove, and runtime; harnesses, MCP
servers, authored tools, Git, schedules, and unrelated children receive a
scrubbed environment. The adapter is trusted to consume the value, and another
process running as the same OS user may still inspect that process environment:
process separation is dependency and ownership isolation, not a secret broker
or OS sandbox.
An idle channel session releases its resident harness after 15 minutes by
default while retaining its native conversation; use `--idle-timeout` to choose
a different interval up to 24 hours. The next message resumes that conversation.
The runtime admits at most four resident sessions and two active turns by
default; use `--max-resident-sessions` and `--max-active-turns` to set bounded
overrides. Saturated work remains durably queued and advances fairly across
conversations, while resident pressure hibernates an eligible idle process.
Channel-managed Claude and Codex sessions run read-only in the shared checkout.
When a request genuinely requires a workspace change, the agent returns an
internal write-access result that hctl withholds from Discord. For a Git
workspace, hctl creates a private branch-backed worktree for that conversation,
resumes the same native session with workspace-write access, and continues the
request once. Later messages and restarts reuse that isolated checkout while
other conversations remain read-only. Guild and DM mutations may run
concurrently: each keeps its own worktree, queue, native session, and response
surface under the shared capacity limits, and an ordinary failure in one does
not stop the other. Interactive, JSONL, and scheduled use retain their existing
native policy. On restart, hctl validates every saved worktree and preserves
anything active, queued, uncertain, dirty, unmerged, or unverifiable. It removes
only an inactive, clean worktree whose branch is already merged into the base,
using durable cleanup intent so an interrupted retirement can be retried safely.

## Product boundary

Claude Code and Codex are the native harnesses and own model calls, context
management, planning, native tools, approvals, and interactive UX. `hctl` owns
only the filesystem compilation, generated harness files, session mapping, and
tools routed through its managed boundary.

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
implementation otherwise favors the Go standard library and uses maintained
YAML and cron-parser dependencies for standards-compliant authored files;
language-specific schema libraries remain inside tool hosts. Tests use
credential-free fake harness processes; live model calls are not part of the
default suite.

For an optional local-model experiment, the
[Claude Code through Ollama spike](spikes/claude-ollama) provides a wrapper and
records the current model and tool-use limitations.
