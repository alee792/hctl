# Product specification

- Status: the core product's shape — the contract a clean-room implementation
  is built to meet. The current repository is the prototype reference
  implementation; see the [rebuild charter](workbench/rebuild.md).
- Scope: the core product. The conversational channel runtime is a second
  product specified in [channel-spec.md](channel-spec.md).
- Working CLI name: `hctl`; product naming is deferred and gates the rebuild
  repository.
- Initial harnesses: Claude Code and Codex CLI.

## User and job

The primary user is an agent author who understands basic files and
directories and common AI concepts such as instructions, skills, and tools.
They should not need to understand registration, manifests, or harness
configuration. There is one author and one capability ladder: the author
starts with instructions and composed skills, and may climb to typed tool
functions — written directly or drafted by their harness. Validation proves a
tool's contract, not its behavior; adopting one remains the author's
deliberate, reviewable act, like any other code.

Operating is a distinct role on the same artifact: credentials, integration
packages, schedules, and staged filesystems carry their own explicit
guardrails. The author defines one filesystem-authored agent project, applies
it to a chosen workspace, and proves it interactively in Claude Code or
Codex; the operator runs the same setup headlessly, which is where
portability is proven.

## Product principles

1. The agent project is legible, versionable, portable source and is not
   coupled to the repository that stores it.
2. Common behavior is portable; harness-specific differences are explicit.
3. Compilation and validation happen before harness files are written or a
   turn dispatcher starts.
4. Generated native files are disposable and visibly tool-owned.
5. Native harness tools remain available and explicitly unmanaged.
6. Policy applies only at managed-tool and durable-state boundaries.
7. Interactive users remain in the native harness interface.
8. Unsupported harness behavior is reported without rewriting valid authored
   source or pretending that hctl enforces it.
9. Conventional files register behavior without a second inventory.
10. Author-facing language stays concrete; runtime terminology remains
    internal.

## The authored project

Authoring is filesystem-forward and convention-driven. A project is a
directory; the directory name supplies the agent name, normalized to
lowercase hyphenated words. The full component set:

```text
my-agent/
  instructions.md          # required
  skills/                  # Agent Skills directories
  plugins/                 # complete publisher-authored Agent Plugin packages
  tools/                   # one typed function per TS/Python file or Go dir
  subagents/               # one instructions.md per immediate subagent
  connections/             # one <name>.md per standalone MCP connection
  schedules/               # nested Markdown cron tasks
  harnesses/               # literal harness-specific native files
  channels/                # channel product; see channel-spec.md
```

**Instructions.** `instructions.md` starts with YAML frontmatter carrying one
plain `description` (and an optional Boolean `friction-notes` opting into the
friction inbox below), followed by a non-empty Markdown body. Generated
always-on instructions contain the body, not the frontmatter.

**Skills.** Each immediate directory under `skills/` is one skill following
the open [Agent Skills specification](https://agentskills.io/specification):
a `SKILL.md` whose frontmatter `name` matches the directory, plus arbitrary
regular-file resources. Adding or removing a directory updates the compiled
project without registration. Resources copy byte-for-byte with executable
intent preserved. Portable fields validate to the standard's rules;
recognized vendor fields are preserved unchanged with a warning when the
selected harness does not document honoring them — hctl never translates,
strips, or enforces them. The dated per-field behavior matrix is
[skill compatibility](workbench/skill-compatibility.md).

**Plugins.** An [Agent Plugin v1](https://agent-plugins.org/specification) is
one complete publisher-authored package. A consumer vendors the reviewed
directory intact beneath `plugins/<storage-name>/`; review, pinning, and
provenance belong to the author's version control. Hctl records no dependency
lock and performs no network acquisition. Each plugin requires a bounded
`plugin.json` targeting the canonical v1.0.0 schema, validated locally
without fetching. Skills import only from the plugin's fixed `skills/`
location; root skills load first and the first skill name wins, with later
collisions skipped and warned, never renamed. Invalid components are skipped
independently with authored-path diagnostics.

An accepted plugin may carry a bounded `mcp.json` (canonical v1.0.0 MCP
schema; `stdio` and `streamable-http` supported, SSE warned and skipped).
Accepted servers are emitted as native project MCP configuration — the
harness owns startup, approval, transport, authentication, and runtime
behavior; hctl does not proxy, supervise, or audit plugin MCP calls. `managed`
is reserved; exact name collisions are skipped with a warning. Plugin-relative
commands stay inside the real plugin tree; hctl expands exactly
`${PLUGIN_ROOT}` and `${PLUGIN_DATA}` once and provides an owner-only
persistent data directory per agent and plugin. Remote URLs are absolute
HTTPS (loopback excepted), without user info or fragments; headers are
literal package-visible values and must not contain secrets.

**Tools.** Visible `tools/*.ts` and `tools/*.py` files and `tools/NAME/tool.go`
directories each declare one tool; filenames supply tool names, with
underscores exposed as hyphens. TypeScript exports a default object with
`description`, strict Zod `inputSchema` and `outputSchema`, and `execute`;
Python exports `description`, Pydantic `Input`/`Output`, and `execute`; Go
exports `Description`, `Input`, `Output`, and `Execute`. Dependencies use the
native lockfiles (`deno.json`/`deno.lock`, `pyproject.toml`/`uv.lock`,
`go.mod`/`go.sum`); there is no authored manifest, registry, or duplicated
tool inventory.

**Subagents.** Each immediate directory under `subagents/` contains only an
`instructions.md` with the same description-and-body contract plus optional
`effort: low|medium|high`, emitted to each harness's native effort field and
omitted when absent. One level, native inheritance of the parent's generated
instructions, skills, managed tools, and permissions. Child skills, tools,
dependencies, and nested subagents are rejected, not ignored. Subagent and
tool names may not collide.

**Connections.** Each `connections/<name>.md` authors one standalone native
MCP connection; the filename supplies the connection and native server name
(`managed` reserved). Closed YAML frontmatter declares `type: mcp` plus
exactly one target form: an installed stdio target (`package` +
`capability`, resolved offline through the integration store, whose stable
server name must equal the filename) or a credential-free remote target
(`transport: streamable-http` + absolute HTTPS `url`, validated without
contact). No headers, tokens, OAuth, timeouts, or tool filters in v1.
Optional trimmed Markdown after the frontmatter (at most 1,024 characters) is
model-facing usage context rendered once into generated instructions, with
one boundary statement that the native harness owns MCP startup, trust,
approval, authentication, discovery, calls, and effects. Name collisions with
`managed`, another connection, or a plugin server fail before mutation.

Authors need not hand-edit native configuration:

```text
hctl connection add AGENT NAME --package PACKAGE --capability CAPABILITY [--context TEXT]
hctl connection add AGENT NAME --url HTTPS_URL [--context TEXT]
hctl connection status AGENT [NAME]
hctl connection remove AGENT NAME
```

Commands take the exact positional agent root, never search ancestors or
choose a harness, and finish by directing the author to run `hctl apply` for
each intended workspace. There is no update command; the Markdown is ordinary
versioned source.

The GitHub connection is the canonical installed target: the official
`github/github-mcp-server` executable, installed as an integration package,
emitted into native Claude and Codex configuration with server name `github`
and rejection on collision. Authentication is deliberately unmanaged: the
operator injects `GITHUB_PERSONAL_ACCESS_TOKEN` into the harness launch
environment, the official server reads it directly, and hctl never writes it
into source, generated files, state, staging, logs, or evidence. **The
harness, model-accessible execution tools, and processes inheriting that
environment may read or transmit the PAT; hctl does not claim otherwise, and
a read-only workspace does not constrain GitHub effects.** Fine-grained
scope, short expiration, native-harness trust, and operator judgment are the
security boundary. The operator journey, lifecycle, and troubleshooting live
in [the native GitHub MCP journey](github-native-mcp.md).

**Schedules.** Nested Markdown files under `schedules/`; the relative path
without `.md` is the schedule name. Strict frontmatter carries exactly one
`cron` string (standard five-field, bounded printable ASCII); the non-empty
body is the task prompt. Apply validates and fingerprints schedules but
starts no clock. See headless operation below for execution.

**Harness-specific files.** `harnesses/claude/.claude/` and
`harnesses/codex/.codex/` carry intentionally nonportable native project
files, copied byte-for-byte to only the selected harness at the same
workspace-relative paths. Hctl does not parse, merge, or validate their
semantics. Hctl-owned destinations remain reserved (Claude `.claude/skills/`
and `.claude/agents/`; Codex `.codex/config.toml` and `.codex/agents/`),
including case-folded aliases. Authors must not place credentials in these
files; hctl does not claim reliable secret detection.

**Bounds.** Authored source is bounded by implementation-owned safety
ceilings rather than ordinary-use quotas; exceeding a ceiling fails before
workspace mutation:

| Surface | Count ceiling | File and aggregate ceilings |
| --- | ---: | --- |
| Root instructions | One required file | 128 KiB |
| Root and imported skills | 256 aggregate | 1,024 files per skill; 8,192 files and 64 MiB across the set; `SKILL.md` 128 KiB; other resources 16 MiB each |
| Authored tools | 128 | 1,024 source and dependency files; 1 MiB each and 64 MiB aggregate |
| Immediate subagents | 128 | 128 KiB each and 16 MiB aggregate |
| Schedules | 256 | 128 KiB per source, including a 32 KiB prompt; 16 MiB aggregate |
| Vendored plugins | 128 directory entries | `plugin.json` and `mcp.json` 128 KiB each; 1,024 entries per plugin `skills/` location |
| Accepted plugin MCP servers | 128 aggregate | Generated native MCP configuration at most 8 MiB |
| Selected harness-specific files | 1,024 | 1 MiB each and 8 MiB aggregate |
| Standalone MCP connections | 128 | 8 KiB per source; context at most 1,024 characters |
| Agent manifest | One optional file | 32 KiB |

Everywhere: authored entries are bounded regular files and real directories
with valid UTF-8 relative paths; symlinks are never followed and are rejected
even where a native harness supports them, so portable source cannot escape
the agent project.

## Apply and handoff

```sh
hctl apply AGENT --workspace WORKSPACE --harness <claude|codex>
```

Apply validates the authored project, the target harness, tool definitions,
and locked dependencies, then materializes owned native files in the selected
workspace so the user starts the harness normally. `--workspace` defaults to
the agent directory; the agent source and the workspace are independent.
Claude receives `CLAUDE.md`, `.mcp.json`, `.claude/skills/`, and
`.claude/agents/`; Codex receives `AGENTS.md`, `.codex/config.toml`,
`.agents/skills/`, and `.codex/agents/`. Generated files are visibly
tool-owned and disposable. Apply refuses to overwrite hand-authored native
files or any hctl-owned file modified since the previous apply, and
reapplying identical source is deterministic. All authored inputs join one
source fingerprint recorded with the apply, so stale or edited generated
setup fails closed. Codex project trust remains the user's native decision;
apply never edits global harness configuration or trusts a project on the
user's behalf.

## Agent manifest

An optional bounded `manifest.json` at the agent root pins the runtime
closure that the directory alone cannot express. It identifies and pins; it
never lists: the directory remains the sole registry of the agent's
components, and the manifest carries no component inventory.

Its closed schema records a schema version, the agent name, the expected
source fingerprint, the hctl version, and — per selected harness — the
harness executable version, a model identifier, integration package
identities (package id plus manifest SHA-256), and authored-tool runtime
versions (Deno, uv, Go) where the project uses them.

`hctl manifest write AGENT --harness ...` records the currently resolved
closure; the file is then ordinary versioned source and may be edited
directly. When the manifest is present, apply and every hctl-owned process
open verify the resolved closure against it and fail closed naming the
exact drifted pin; when absent, behavior is unchanged. The model pin is
emitted through the selected harness's documented configuration and
recorded in provenance; the harness owns model selection, and hctl does not
claim to verify which model actually served a turn.

Every apply record and dispatch lifecycle event carries the source
fingerprint and, when present, the manifest identity, so observation made
outside hctl — transcripts, evaluations, selection among revisions — can be
joined to the exact configuration that produced it. Hctl retains none of
that observation: no transcripts, no evaluations, no scores. An improvement
loop revising the agent's files is an author like any other: its revision
is validated for form before anything runs, and its merit is judged outside
hctl. The friction inbox remains a supplementary human-facing channel, not
the loop's signal path.

A pin is an axis of variation, not an editable surface: a loop may try a
different model or harness version by changing a pin, while the components
it can edit remain the authored files. Lineage and population management
belong to version control: a candidate revision is a branch or commit, its
manifest identifies it, and hctl neither records lineage nor selects among
revisions. How variants are isolated — worktrees, containers, or sandboxes
— is the operator's infrastructure choice; hctl requires only that each
variant is a directory that applies deterministically.

## Managed tool boundary

One stdio MCP server exposes the bounded built-in `echo` tool, the optional
`record-friction` built-in, and the authored tools to both harnesses. Inputs
and outputs are schema-validated; audit output carries a safe request
identifier, tool name, and lifecycle outcome — never arguments or output. One
long-lived host per authored language serves calls; authors never write
protocol code. The boundary is additive: it does not disable, authorize,
observe, or retry harness-native tools.

Codex treats the generated managed server as required and delegates its tool
approval to hctl, so an authorized managed call does not draw a second
harness prompt; every other generated MCP entry — plugin, connection, or
installed — keeps native per-call prompt approval. This exemption applies
only to the managed server and does not affect native or unrelated MCP
tools.

`record-friction` is advertised only when root instructions opt in with
`friction-notes: true`. It accepts one bounded UTF-8 note and stores it in a
private, owner-only, per-agent local inbox outside both agent source and
workspace, write-only to models, never automatically read, transmitted, or
applied. It is not telemetry, memory, or evidence. At most 256 records are
retained per agent, and the store never overwrites or silently evicts.

Secret-bearing managed operations do not exist yet. Before one ships, the
secretless operation broker boundary applies
([ADR 0009](adr/0009-use-a-local-secretless-operation-broker.md)): opaque
references resolve only at authorized managed invocations, values never reach
tool hosts, harnesses, models, generated files, or audit, and no backend or
broker code is scaffolded until a concrete operation is selected.

## Integration packages

Machine-installed third-party integrations use a metadata-first package
contract distinct from vendored `plugins/`. A bounded schema-version-1
manifest carries identity, provenance, an hctl compatibility range, exact
platform artifacts (size and SHA-256 pinned), the expected executable
identity, and closed versioned capability declarations. Hctl validates
metadata without opening artifacts, fetching URLs, or executing package code.

`hctl integration install SOURCE --trust operator` is the only trust and
installation journey: a local directory or archive containing
`integration.json`, or artifacts fetched only from exact pinned HTTPS URLs
without redirects. There is no registry, package script, dependency
resolver, or signature claim. Installed state lives in one owner-only,
content-addressed, offline-verified store shared across agents and
workspaces; `inspect`, `verify`, `list`, `enable`, `disable`, `update`, and
`remove` operate on that store, and verification is re-run before every use.
Portable agent source can never choose an install source, install or enable a
package, grant trust, or carry a credential; apply gains no network path.

Recognized capabilities are closed schemas. The core implements `native-mcp`
v1: a stable native server name, executable, bounded literal launch data,
required ambient environment names without values, and supported harness
targets — consumed by installed connections. `channel-adapter` v1 belongs to
the channel product ([channel-spec.md](channel-spec.md)): a core rebuild does
not implement its recognition and it is not acceptance-gating; it is
reintroduced only if the channel product is later ported. The native harness owns
process lifecycle, credentials, approvals, calls, and effects for everything
a package launches. Required ambient names are diagnostic metadata, not a
credential channel; resolved values never enter generated files, package
state, staging, or evidence.

## Headless operation

```sh
printf '%s\n' '{"input_id":"x-1","text":"..."}' \
  | hctl run AGENT --workspace WS --harness <claude|codex> --input jsonl
```

The turn dispatcher accepts bounded JSONL input, each line carrying a
caller-owned `input_id` and `text`. Input is durably accepted and queued
while a turn is active, processed one FIFO turn per conversation, and mapped
to a resumable native session; ordered JSONL events are emitted. A repeated
input ID deduplicates within its conversation. After restart, active work
without a proven terminal result is uncertain and never silently retried.
Dispatch state is one owner-only file per workspace.

Schedules execute two ways, both requiring current generated setup:

```sh
hctl schedule trigger AGENT NAME --workspace WS --harness codex \
  --input-id OCCURRENCE_ID --turn-timeout 90s --timeout 2m
hctl schedule run AGENT --workspace WS --harness codex
```

`trigger` dispatches one occurrence under a caller-owned stable ID: each
accepted occurrence opens a fresh native task session, duplicates return the
retained outcome without opening a harness, and the bounded turn deadline
aborts a stalled process while durably recording the occurrence as uncertain
with its reason. `run` is an explicit foreground UTC clock: standard cron
evaluated in UTC, first occurrence strictly after startup, no downtime or
clock-jump backfill, no overlap for one schedule, a local lock excluding a
second clock for the same workspace/agent/harness, and graceful drain on
signals. Lifecycle output is bounded and never contains model text. No
daemon, missed-run replay, or hosted delivery runtime.

## Staged agent filesystems

`hctl stage AGENT --harness <claude|codex> --output DIR` prepares one
complete runnable filesystem tree at canonical paths for an existing OCI
builder:

```dockerfile
FROM <hctl harness image> AS build
COPY . /agent
RUN hctl stage /agent --harness codex --output /out/agent

FROM DOCUMENTED_COMPATIBLE_BASE
COPY --from=build /out/agent/opt/ /opt/
COPY --from=build --chown=65532:65532 /out/agent/workspace/ /workspace/
COPY --from=build --chown=65532:65532 /out/agent/home/hctl/ /home/hctl/
USER 65532:65532
ENTRYPOINT ["/opt/hctl/bin/agent-entrypoint"]
```

The staged tree carries hctl, the selected harness, immutable agent source,
the generated integration and apply record, an entrypoint, an artifact
manifest, and only the execution closure the agent's tools actually need —
no build toolchains, caches, credentials, login state, trust decisions, or
conversation state. Staging is deterministic for identical pinned inputs,
verifies that preparation did not mutate authored source, and publishes with
one rename only after the manifest is complete. The entrypoint verifies
runtime identity, generated integration, and source fingerprint before a
turn. Hctl does not construct OCI layers, contact registries, publish, sign,
deploy, or operate images, and publishing a harness image requires current
permission to redistribute that harness.

## Installation and distribution

The first supported platform is `darwin-arm64`. The exact `vX.Y.Z` tag names
`hctl_X.Y.Z_darwin_arm64.tar.gz` (one executable at the archive root) and its
`SHA256SUMS` manifest; the user verifies, extracts to a stable `PATH`
location, and runs `hctl apply`. Generated MCP configuration records the
resolved absolute executable path, so moving the binary requires reapplying.
`go install` is not a supported end-user journey, and there is no `hctl
package` command: agent source and lockfiles are inputs to `apply`, while
generated hosts and dependency environments remain disposable
workspace-local caches.

## Deferred bets

Recorded once here; none is scaffolded until its trigger arrives:

- **Proposals** — inert, workspace-local, human-reviewed improvement
  records; a future capture tool must remain additive, never apply a diff,
  and never claim reliable secret detection. The prototype's ADR 0008
  records the full convention.
- **Secretless operation broker** — the boundary for the first secret-bearing
  managed operation ([ADR 0009](adr/0009-use-a-local-secretless-operation-broker.md)).
- **Post-run summaries** — deferred until a harness exposes stable runtime
  IDs; must reference native logs rather than duplicate transcripts.

## Failure and safety behavior

- Missing, stale, ambiguous, or edited generated harness integrations fail
  closed.
- Input, output, queue, process lifetime, state size, and protocol lines are
  bounded.
- Durable state is owner-readable only and written atomically.
- Process failure is distinct from a completed or failed model turn.
- An uncertain external effect is never described as exactly-once or retried
  without a target idempotency contract.
- Hctl-owned diagnostics never expose credentials, private prompts, or raw
  process output; native harness and external-server diagnostics remain
  outside that claim.
- Hctl never claims to enforce instructions, inspect native effects, sandbox
  authored code, or make model behavior safe from outside the harness.

## Acceptance

Every stated behavior in this specification binds; the list below is the
proof skeleton, not the whole contract. The core is complete when
credential-free tests (fake harness processes; no live model calls) prove:

1. One authored project compiles deterministically for both harnesses, and
   apply produces native, discoverable files while refusing conflicts and
   modified-file overwrites.
2. Both generated integrations expose the same managed MCP tool surface, and
   a mixed TypeScript/Python/Go project is prepared once per apply with one
   host process per language.
3. Agent source applies outside its own directory: generated files and
   execution use the workspace while discovery stays rooted in source.
4. Subagents generate natively with inheritance and exact effort mapping,
   and child skills, tools, dependencies, and nested subagents are rejected
   rather than ignored; skills and their resources round-trip byte-for-byte
   with executable intent, vendor metadata preserved and warned;
   harness-specific files round-trip only to their selected harness with
   full ownership protection.
5. Plugin skills and plugin MCP declarations import with deterministic
   collision handling and isolated component failure.
6. Connections generate exact native configuration for installed and remote
   targets without contacting anything; a name collision with `managed`,
   another connection, or a plugin server fails before mutation; and a
   conspicuous fake ambient value never appears in generated files, state,
   staging, or evidence.
7. Headless dispatch durably queues FIFO input, deduplicates input IDs,
   resumes sessions, and marks unproven restart work uncertain.
8. Schedules validate and fingerprint identically for both harnesses;
   triggers deduplicate stable occurrence IDs, open fresh sessions, honor
   turn deadlines with retained uncertainty, and the UTC clock admits only
   current non-overlapping occurrences and drains on shutdown.
9. Staging produces a deterministic, credential-free, minimal runnable tree
   whose entrypoint verifies identity and fingerprint before a turn;
   preparation never mutates authored source, and publication is one rename
   only after the manifest is complete.
10. Managed audit output remains content-free.
11. A present agent manifest is verified before apply and before every
    hctl-owned process open: a drifted harness version, package identity,
    or source fingerprint fails closed naming the exact pin; writing the
    manifest for an unchanged closure is byte-identical; and an absent
    manifest changes nothing.

## Explicit non-goals

- A model loop, context manager, or cross-harness chat UI
- A marketplace, automatic updater, network acquisition, or dependency lock
  for vendored components
- Claude Agent SDK or hosted OpenAI agent runtimes
- Background or distributed schedule clocks, workflows, independently
  configured nested subagents, or deployment orchestration
- Building OCI manifests or layers, publishing or signing images, or hosted
  image operation
- Governance claims over native harness tools
- Evaluations, scoring, transcript retention, or selection among agent
  revisions — hctl is an improvement loop's substrate, never the loop
- Hosted secret managers and model-visible secret-bearing managed operations
- GitHub OAuth or GitHub App enrollment, a managed MCP proxy, credential
  brokering, or per-call hctl authorization
- Automatic or unreviewed promotion of agent-authored improvements
