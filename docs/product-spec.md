# Product specification

- Status: experimental product contract
- Working CLI name: `hctl`; product naming is deferred
- Initial runtime: local Go executable
- Initial harnesses: Claude Code and Codex CLI

## Initial installation

The initial supported platform is `darwin-arm64`. The exact `vX.Y.Z` Git tag is
the authoritative release version and names
`hctl_X.Y.Z_darwin_arm64.tar.gz`, which contains one `hctl` executable at its
archive root. The accompanying `hctl_X.Y.Z_SHA256SUMS` manifest supplies its
SHA-256 checksum. A user downloads and verifies those exact files, extracts the
executable to a stable location on `PATH`, then runs `hctl apply` with an agent
source and workspace. The generated MCP configuration records the resolved
absolute executable path: moving the binary requires reapplying the workspace;
replacing it at the same path leaves that reference valid, but the supported
upgrade journey reruns `apply` to refresh any runtime cache.

`go install` is not a supported first-release user journey. It requires a Go
toolchain and source/module resolution rather than consuming the released,
checked artifact. `hctl package` is not introduced: portable agent source and
native lockfiles remain inputs to `apply`, while generated tool hosts and
dependency environments remain disposable workspace-local caches. Another
machine installs its needed native runtimes and reruns `apply`; it does not
reuse a copied `.hctl/cache/` directory.

## User and job

The primary user is an agent author who understands basic files and directories
and common AI concepts such as instructions, skills, and tools. They should not
need to understand registration, manifests, or harness configuration. They
define one filesystem-authored agent project, apply it to a chosen workspace,
prove it interactively in Claude Code or Codex, and may operate the same setup
headlessly through channels.

## Product principles

1. The agent project is legible, versionable, portable source and is not
   coupled to the repository that stores it.
2. Common behavior is portable; harness-specific differences are explicit.
3. Compilation and validation happen before harness files are written or a
   gateway starts.
4. Generated native files are disposable and visibly tool-owned.
5. Native harness tools remain available and explicitly unmanaged.
6. Policy applies only at managed-tool and durable-state boundaries.
7. Interactive users remain in the native harness interface.
8. Unsupported harness behavior is reported without rewriting valid authored
   source or pretending that hctl enforces it.
9. Conventional files register behavior without a second inventory.
10. Author-facing language stays concrete; runtime terminology remains internal.

## Authored project

Authoring is filesystem-forward. Where the concepts match, hctl uses Eve's
conventional vocabulary: instructions, tools, skills, channels, connections,
sandbox, subagents, and schedules. Only the subset named below is implemented
in the MVP.

The authoring API is convention-driven. An MVP project is:

```text
my-agent/
  instructions.md
  skills/
    research/
      SKILL.md
      references/
        sources.md
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
  harnesses/
    claude/
      .claude/
        settings.json
    codex/
      .codex/
        rules/
          default.rules
```

The directory name supplies the agent name, normalized to lowercase words with
hyphens. `instructions.md` is required and contains YAML frontmatter with one
plain `description` plus a non-empty Markdown body. Generated always-on
instructions contain the body, not the frontmatter.

The `skills/` directory is optional. Each visible immediate directory is one
skill and contains a required `SKILL.md`; its frontmatter `name` must match the
directory name. A skill follows the open Agent Skills format and may include
regular-file resources such as `scripts/`, `references/`, `assets/`, and other
nested directories. Adding or removing a skill directory updates the compiled
project without separate registration. Hctl keeps the existing eight-skill
limit.

`name` and `description` are required. Names contain 1-64 lowercase ASCII
letters, digits, and single hyphens, without a leading or trailing hyphen.
Descriptions contain 1-1024 characters. The portable optional frontmatter is
string `license`, 1-500 character `compatibility`, string-to-string `metadata`,
and experimental space-separated string `allowed-tools`. Documentary fields
are preserved without claiming that a harness operationalizes them.
Harness-specific behavior is honored only when the selected harness documents
an exact representation. Recognized vendor fields and files remain intact when
applied elsewhere, with a precise warning that they may have no effect. Hctl
does not translate, strip, or enforce them. In particular, it does not pretend
that Codex honors `allowed-tools` or a Claude skill model selection. An
OpenAI-host `agents/openai.yaml` file is copied byte-for-byte to either target;
Claude apply warns because Claude does not document the file.

Apply copies supported skill resources byte-for-byte into the selected
harness's project skill directory and preserves executable intent in its
ownership and source fingerprints. The reserved `agents/openai.yaml` resource
is copied unchanged to either target and warns for Claude. All authored skill
entries must be bounded regular files and real directories with valid UTF-8
relative paths. Symlinks are rejected even when a native harness supports them,
so the portable source boundary remains deterministic and cannot escape the
agent project.

Immediate directories under `subagents/` define native harness subagents. Each
contains only an `instructions.md` file with the same description-and-body
contract plus optional string `effort`. Effort accepts exactly `low`, `medium`,
or `high`; apply emits it as Claude agent `effort` or Codex custom-agent
`model_reasoning_effort`. The field is omitted from native output when absent.
Hctl validates and requests effort, while the selected harness, model, account,
and policy determine whether it is honored. Root `instructions.md` remains
description-only. The MVP allows one level and at most eight subagents. A subagent
inherits the selected parent's generated instructions, skills, managed MCP
tools, native tools, and permissions through native harness behavior. Child
skills, tools, dependency files, and nested subagents are rejected rather than
silently ignored. Subagent and tool names may not collide. Portable subagent
names use hyphens; generated Codex agent identifiers use underscores because
that harness requires them.

The optional `harnesses/` directory carries intentionally nonportable native
project files. `harnesses/claude/` may contain a literal `.claude/` tree and
`harnesses/codex/` may contain a literal `.codex/` tree. Apply reads only the
selected harness tree and mirrors its files at the same workspace-relative
paths. This supports native surfaces such as Claude's documented project
`.claude/settings.json` and Codex's documented project
`.codex/rules/*.rules` files without inventing an hctl schema. See the
[Claude settings documentation](https://code.claude.com/docs/en/settings) and
[Codex rules documentation](https://developers.openai.com/codex/rules).

Harness-specific files are bounded regular files beneath real directories;
paths must be normalized UTF-8 and symlinks are rejected. Contents are copied
byte-for-byte, executable intent is preserved, and the selected files join the
source fingerprint and apply ownership record. Hctl does not parse, merge, or
validate native semantics, and does not promise that a particular harness
version honors a copied file. Authors must not place credentials in these
files; hctl does not claim reliable secret detection.

Hctl-owned native destinations remain reserved. Claude authors cannot replace
`.claude/skills/` or `.claude/agents/`; Codex authors cannot replace
`.codex/config.toml` or `.codex/agents/`. Portable instructions, skills,
subagents, and managed MCP setup continue to use their existing conventions.
Case-folded aliases of these paths are also rejected before mutation so agent
source remains safe to apply to common case-insensitive workspaces.

The optional `connections/github.md` file contains a 1-1024 character UTF-8
Markdown description. Its conventional path registers a connection named
`github`; there is no connection manifest or name field. It exposes exactly
`github__get-repository`, `github__list-issues`, and `github__get-issue` through
the existing managed MCP server for both harnesses. The description is included
in each tool's model-visible description. Any other entry under `connections/`
fails before workspace mutation.

This first connection is anonymous, public, read-only GitHub REST access.
Repository inputs are bounded `owner` and `repo` strings. Issue listing accepts
optional `state` (`open`, `closed`, or `all`) and `limit` from 1-20, defaulting
to `open` and 10; GitHub's issues endpoint may include pull requests. Single
issue lookup requires a positive issue number. Hctl sends fixed GET requests
to `https://api.github.com` with GitHub's JSON accept and current
`2026-03-10` API-version headers,
no authorization header, a five-second client timeout, no redirects, no retry,
and a one-MiB response limit. Returned repository and issue fields are selected
and bounded rather than forwarding raw upstream bodies; a `truncated` field
reports when returned text or labels were shortened. Errors are stable
categories for invalid input, missing resources, rate limits, authorization,
service availability, timeouts, oversized or invalid responses, and other
request failures; they do not include upstream bodies or arbitrary diagnostics.
Apply performs no network request.

Private repository access, credentials, writes, a generic OpenAPI engine,
dynamic MCP proxying, approval UX, and credential-broker code are deferred. Any
secret-bearing extension must first satisfy
[ADR 0009](adr/0009-use-a-local-secretless-operation-broker.md).

The optional `channels/discord.md` file contains a 1-1024 character UTF-8
Markdown description. Its conventional path registers the built-in `discord`
channel; any other entry under `channels/` fails before workspace mutation.
The file contains no application identity, user identity, public key, token,
listener address, or vendor configuration and joins the source fingerprint for
both harnesses. Apply performs no network request and generates no extra native
harness file for the channel.

`hctl channel discord` requires the selected project to have been applied and
accepts one application ID, Ed25519 public key, allowed user ID, harness, and
optional conversation override at runtime. Without an override, its bounded,
deterministic conversation ID includes both the configured application and
allowed user so changing either cannot inherit the prior native session. It
binds a numeric loopback address, serves one clean
Interactions path, and accepts Discord PING plus application commands with
exactly one string option named `message`. It verifies the signature over the
timestamp and raw body, rejects timestamps outside five minutes, validates the
interaction and application IDs, and authorizes the configured user before
submitting only the interaction ID and message text to the typed gateway seam.
The interaction ID is the durable input ID, so the existing FIFO queue,
deduplication, session mapping, and uncertain-recovery behavior remain
authoritative.

Admitted commands receive a flushed Discord deferred acknowledgement immediately
after transport authentication, authorization, and bounded input validation;
the HTTP handler does not wait for durable gateway acceptance. Submission then
runs asynchronously. Queue-full or other gateway rejection edits the deferred
original with stable bounded text, while accepted input keeps the gateway's
existing durable FIFO and deduplication semantics. A model turn cannot start
before the gateway reports acceptance. Before that acceptance, the adapter
retains at most 32 pending interactions, matching the durable gateway queue;
additional valid commands receive an immediate ephemeral queue-full response
without retaining a token, making an outbound request, or starting a harness
turn.

The adapter retains the short-lived interaction token only in process memory,
aggregates bounded text deltas, and updates the fixed original-response
endpoint. The original plus at most five 2,000-rune followups fit the limit for
user-installed apps; every payload disables mentions and the final bounded
chunk retains any truncation marker. Discord documents a three-second initial
response deadline and a 15-minute interaction-token lifetime. Hctl defers
immediately and, after 14 minutes from the signed timestamp, updates a still
pending response with stable expiry text and releases its token, output, and
turn memory. Expiry delivery does not wait behind ordinary output delivery, and
state release does not wait on any outbound HTTP request. This cleanup does not
interrupt or claim to stop the harness. HCTL-012 must revisit the separate
runtime-turn timeout implied by this limit.

Completed, failed, and recovered-uncertain outcomes have bounded fallback text.
At most 32 ordinary terminal deliveries run concurrently. If that delivery
limit is full, hctl releases the detached token and output, classifies the
delivery as a no-retry `delivery=saturated` failure, and performs no outbound
request. Discord receives no terminal update and the detached response is no
longer eligible for pending-turn expiry. The deadline-sensitive expiry path
uses its already bounded pending-turn worker instead of this ordinary-delivery
limit. Delivery has a five-second timeout,
follows no redirect, reads at most 64 KiB of response, and never retries. A
transport failure or invalid successful response is recorded as uncertain; an
explicit rate limit or other non-success response is classified as rate-limited
or failed without its response body.
Audit contains only channel, input ID, status class, and classified delivery
outcome. Listener readiness is a separate operator diagnostic, not an audit
event.

This slice follows Eve's `channels/`, path-derived identity, normalized input,
continuation, deferred-response, and followup concepts while leaving the native
harness responsible for the turn. It does not add a Discord Gateway bot,
incoming webhook, bot token, OAuth, proactive sending, ordinary message or
mention ingestion, command registration, tunnel, TLS termination, public
listener, deployment, component, modal, typing, or interruption support.

Visible `tools/*.ts` and `tools/*.py` files each declare one tool. A visible
`tools/NAME/tool.go` directory declares one Go tool. Filenames supply tool
names, with underscores exposed as hyphens. TypeScript definitions export a
default object containing `description`, strict Zod `inputSchema` and
`outputSchema`, and `execute`. Python modules export `description`, Pydantic
`Input` and `Output` models, and `execute`. Go packages export `Description`,
`Input`, `Output`, and `Execute`. The runnable mixed-language fixture is the
canonical syntax example while the product remains experimental.

Authored source entries must be bounded regular files and real directories
without symlink traversal. Contract and code files must be UTF-8; arbitrary
skill resources may be binary. There is no authored hctl manifest, registry,
or duplicated tool inventory. TypeScript uses root `deno.json` and `deno.lock`;
Python uses `pyproject.toml` and `uv.lock`; Go uses `go.mod` and an optional
`go.sum`. These native files describe dependencies without registering tools.
Compilation produces a deterministic apply record and source fingerprint. The
bounded `echo` managed tool remains an hctl-provided default; it is not author
configuration.

## Apply and handoff

```sh
hctl apply AGENT --workspace WORKSPACE --harness claude
hctl apply AGENT --workspace WORKSPACE --harness codex
```

`apply` validates the authored project, target harness executable, tool
definitions, locked dependencies, and protocol readiness. It invokes Deno,
`uv`, or Go only when that language is present, then materializes owned native
files in the selected workspace so the user can change into that directory and
start the selected harness normally. `--workspace` defaults to the agent
project directory, making a standalone agent the simplest case. Applying an
agent stored elsewhere is explicit:

```sh
hctl apply ~/agents/reviewer --workspace ~/Code/example --harness claude
cd ~/Code/example && claude
```

The agent project supplies instructions, skills, tools, subagents,
harness-specific files, and native dependency files. The workspace supplies
harness-visible working files and is
the working directory for the harness and authored tools. Generated harness
files, apply records, gateway state, and runtime caches belong to the
workspace. Source discovery and dependency preparation remain rooted in the
agent project.

Claude receives `CLAUDE.md`, `.mcp.json`, `.claude/skills/`, and
`.claude/agents/`. Codex receives `AGENTS.md`, `.codex/config.toml`,
`.agents/skills/`, and `.codex/agents/`. Generated MCP configuration uses the
resolved `hctl` executable, agent-source, and workspace paths. Hctl generates
skills only at this project scope; it does not modify user, administrator,
enterprise, or plugin skill locations.

Codex project configuration remains subject to Codex's native repository-trust
flow. Apply does not edit the user's global Codex configuration or silently
trust a project on their behalf.

Apply refuses to overwrite hand-authored native files or any hctl-owned file
that was modified after the previous apply. Removing or changing a
harness-specific source file uses the same modified-file protection and stale
cleanup as generated portable setup. Reapplying identical source is
deterministic.

## Harness contract

Each harness integration declares and verifies:

- its executable and compatible version signal;
- native generated-file surfaces;
- managed tool exposure;
- new-session and resume behavior;
- structured input, output, and terminal events; and
- any interruption or steering behavior that is not portable.

Claude Code uses bidirectional stream JSON. A second message received during an
active turn is queued for the next turn. Codex uses its local App Server JSONL
protocol. Active-turn steering and interruption are Codex-specific and are not
part of the portable MVP promise.

## Gateway

```sh
hctl gateway AGENT --harness claude
hctl gateway AGENT --harness codex
```

The gateway accepts bounded JSONL input containing a caller-provided
`input_id` and `text`. It durably accepts and queues input while a turn is
active, processes one FIFO turn per conversation, emits ordered JSONL events,
and maps the external conversation to a resumable harness session.

A repeated input ID is deduplicated within its conversation. After a restart,
an input that was active but lacks a proven terminal result becomes uncertain;
it is not silently retried.

The local stdin adapter and signed Discord Interactions adapter share the same
typed submission and event seam. The JSONL gateway remains the reference for
durable state and event semantics. Other vendor channels, generic webhooks,
OAuth, proactive delivery, and public listener management remain outside the
MVP.

## Managed tool boundary

The MVP exposes one bounded, read-only `echo` tool, the optional anonymous
GitHub connection's three read-only tools, and conventionally authored
TypeScript, Python, and Go tools through one stdio MCP server in both harnesses.
Inputs and outputs are schema-validated. Audit output contains a safe request
identifier, tool name, and lifecycle outcome, never tool arguments or output.

One long-lived process per authored language serves inspection and calls for
the MCP session. Tool calls are serialized in the current MVP. A call that
exceeds its deadline terminates that language host and fails clearly; graceful
per-call cancellation and automatic host restart are not claimed.

The managed boundary is additive. It does not disable, authorize, observe, or
retry harness-native tools. Secret-bearing tools require the local secretless
operation broker selected by [ADR 0009](adr/0009-use-a-local-secretless-operation-broker.md)
before they ship. The broker resolves an opaque reference only at an authorized
managed invocation and consumes the value for a constrained upstream operation;
it declares no credential or authorization input fields and never returns the
value to a tool host, harness, MCP client, or model.
No backend, credential enrollment flow, connection syntax, or unused broker
code is scaffolded in the MVP.
Codex treats the generated managed server as required and delegates its tool
approval to hctl, avoiding a second harness approval prompt after hctl records
authorization where Codex user and administrator policy permits. This setting
does not affect native or unrelated MCP tools.

## Authored tool lifecycle

Tool source and native lockfiles join the validated source fingerprint. Apply
checks TypeScript with `deno check --frozen`, prepares Python with
`uv sync --locked`, and compiles a generated Go host with native Go module
tooling. Generic TypeScript and Python hosts, their local runtime environments,
and generated Go build output live under the workspace's disposable
`.hctl/cache/tools/`; no normalized tool manifest is written.
The cache records the exact Deno and `uv` executables used during apply so a
harness can start the managed server without inheriting the same shell `PATH`.

The generated MCP command identifies its harness. At startup hctl verifies the
matching workspace apply record, selected agent identity, and source
fingerprint before loading the cached hosts. Authors write typed functions and
do not implement MCP protocol code.

## Deferred direction: proposals

Scripts created ad hoc by the agent remain ordinary harness-native workspace
activity unless a human promotes them into `tools/` and reapplies the project.

Generated project instructions may encourage the harness to submit reusable
discoveries through a future managed proposal tool. Instructions can influence
this behavior but cannot enforce it or observe native filesystem writes.

A proposal is a local, inert record of a candidate improvement to one existing
instruction, skill, or managed-tool source file. It does not modify active
authored source, generated harness setup, or a running harness. Proposal files
belong to the producing workspace at `.hctl/proposals/ID/`, not to the agent
source that they name. `proposal.md` explains the suggestion and records its
target, selected source and run provenance, and the target's SHA-256 content
hash; `change.diff` is a bounded unified diff. `review.md` is added only after
a human accepts or rejects it. After publication, `proposal.md` and
`change.diff` are immutable evidence; `review.md` is the separate later human
decision record. There is no manifest or proposal registry.

A proposal can target `instructions.md`, a UTF-8 text file in an existing
skill, or an existing managed-tool source file. Binary skill resources are
outside this unified-diff flow. A proposal cannot add, remove, move, or rename
files, change a dependency file, or escape the agent source. A changed or
missing target is stale and must never be applied or rebased automatically.
The reviewer either manually makes a current change in the agent source and
reapplies it, or rejects the proposal. Both accepted and rejected records are
retained until a human removes them.

Proposals must not contain credentials, secrets, raw tool outputs, or
conversation transcripts. A future capture tool must tell callers this rule and
bound the content it accepts. It must not claim that it reliably detects or
removes secrets; owner-readable storage and human review do not make prohibited
content safe to record.

A future managed proposal tool may create this workspace-local record after
validating its bounded target, base content, and provenance. It must remain
additive: it cannot apply a diff, execute proposed code, reapply, delete a
proposal, or control native filesystem activity. Proposal capture, source
mutation, and review UX remain outside the MVP and are not scaffolded. See
[ADR 0008](adr/0008-keep-agent-proposals-workspace-local-and-inert.md).

## Failure and safety behavior

- Missing, stale, ambiguous, or edited harness setups fail closed.
- Input, output, queue, process lifetime, state size, and protocol lines are
  bounded.
- Durable state is owner-readable only and written atomically.
- Process failure is distinct from a completed or failed model turn.
- An uncertain external effect is never described as exactly-once or retried
  without a target idempotency contract.
- Diagnostics do not expose credentials, private prompts, or raw process
  output.
- A future secretless broker validates a reference, managed operation, target,
  and authorization on every call; uses private local IPC, a sensitive
  session-scoped authorization capability for one managed MCP server instance,
  and an upstream credential of its own; and returns/audits only bounded
  secret-free data. The capability is delivered only to hctl's managed
  MCP-server/broker pair, stays out of ordinary tool inputs, model-visible I/O,
  generated configuration, logs, and audit, and is rotated/removed with the
  managed MCP server process.
  Its typed operation schema declares no credential/authentication fields and
  rejects unknown fields; it cannot reliably detect a secret smuggled into an
  allowed string after the model has submitted it. It does not protect against
  native harness capabilities or any other process running as the same OS user.

## MVP acceptance

The MVP is complete when credential-free tests prove:

1. One authored project compiles deterministically for both harnesses.
2. Apply produces native, discoverable harness files and refuses conflicts.
3. Both generated harness setups expose the same managed MCP tool surface.
4. Both headless drivers start and resume sessions against fake harnesses.
5. Input arriving during an active turn is durably accepted and processed
   later in FIFO order.
6. Caller-provided input IDs are deduplicated.
7. Restart recovery marks unproven active work uncertain.
8. Managed audit output remains content-free.
9. A mixed TypeScript, Python, and Go project is prepared once per apply,
   exposed identically by both generated MCP configurations, and reuses one
   host process per language across calls.
10. One agent project can be applied outside its source directory; generated
    files and execution use the selected workspace while dependencies and tool
    definitions remain rooted in agent source.
11. Immediate subagents are generated in each harness's native format, inherit
    the parent setup without duplicated child tools or skills, and map optional
    `low`, `medium`, or `high` effort to the exact native field.
12. Agent Skills directories and their regular-file resources round-trip into
    both native project skill locations, including executable intent, while
    recognized unsupported vendor metadata remains intact and produces a
    path-, field-, and harness-specific warning.
13. Harness-specific regular files round-trip only into their selected native
    project directory, join stale-source detection, and use the same collision,
    ownership, modified-file, and cleanup protections as generated setup.
14. A signed Discord interaction is authorized and immediately deferred, then
    asynchronously accepted or rejected by the durable gateway. Accepted input
    is deduplicated and delivered through bounded responses for both harnesses
    without persisting its token or exposing a non-loopback listener.

## Explicit non-goals

- A model loop, context manager, or cross-harness chat UI
- Channels other than signed Discord Interactions, generic webhooks, and
  proactive vendor delivery
- Claude Agent SDK or hosted OpenAI agent runtimes
- Scheduling, workflows, independently configured nested subagents, or
  deployment orchestration
- Building or deploying packaged agent images
- Governance claims over native harness tools
- Credential storage, enrollment, or backend selection before a
  secret-bearing tool exists
- Automatic or unreviewed promotion of agent-authored improvements
