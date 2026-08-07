# Working status

- Updated: 2026-08-07
- Repository: GitHub-backed `hctl` experiment; publication remains pending and
  product naming remains deferred
- Purpose: let a clean session resume without depending on chat history or the
  previous Roster repository

## Resume order

1. Read the [vision](../vision.md) for the product boundary.
2. Read the [product specification](../product-spec.md) for the accepted MVP.
3. Use the [glossary](../glossary.md) for author-facing terminology.
4. Use the [maintainer task list](tasks.md) for executable next work.
5. Read the [tool-authoring workbench](tool-authoring.md) before designing or
   implementing authored tools.

The product specification and accepted ADRs are the current contract.
Workbench documents record settled requirements, implementation evidence, and
open follow-up questions.

## Shipped prototype

The repository contains a small Go CLI that:

- applies portable agent instructions, root and vendored Agent Plugins v1
  skills with bundled resources, native unmanaged plugin MCP declarations,
  immediate inherited subagents with an optional
  portable
  reasoning-effort request, and selected
  harness-specific native files to an
  independently selected workspace as native Claude Code and Codex files
  without overwriting hand-authored files;
- keeps one apply record, migrates legacy projection records, and removes the
  obsolete duplicated runtime manifest;
- discovers, prepares, validates, and exposes TypeScript, Python, and Go tool
  functions beside the bounded built-in `echo` tool through stdio MCP;
- discovers a bounded `connections/github.md` description and exposes three
  fixed anonymous public GitHub repository and issue operations through the
  same managed MCP server without contacting GitHub during apply;
- discovers a bounded `channels/discord.md` participation policy and runs one
  outbound conversational Discord Gateway adapter over the durable turn
  dispatcher, with independent managed session lifecycles for the authorized
  guild channel and DM;
- discovers nested Markdown schedules, validates and fingerprints their
  five-field cron metadata, and explicitly triggers fresh-session task
  occurrences through the durable turn dispatcher without emitting model text;
- drives local Claude Code and Codex processes headlessly through fake-tested
  bidirectional protocols;
- maps conversations to resumable harness sessions, queues input FIFO,
  deduplicates caller input IDs, and marks ambiguous restart recovery
  uncertain; and
- passes the repository-local formatting, test, vet, lint, and vulnerability
  checks through `./scripts/check.sh`.

The credential-free polyglot check applies one mixed project for fake Claude
and Codex harnesses, launches the MCP command from each generated setup, and
proves persistent hosts plus bounded failure behavior.

The proof keeps mixed-language agent source separate from its workspace,
validates generated subagent files for both harnesses, and switches the
workspace safely to a second agent.

The maintainer agent includes Matt Pocock's MIT-licensed `code-review` skill
as a pinned, attributed portable import. Its independent Standards and Spec
reviews layer beside the existing hctl-specific review skills.

The transport-neutral interactive request contract is implemented as a strict
in-process module with conformance fixtures for confirmations, choices, text,
date/time input, forms, normalized answers, and deterministic text fallback.
Its interaction coordinator now stores one pending request in the owning
dispatch conversation through the existing serialized state owner. It commits
delivery and resume intent before external effects, preserves ambiguous effects
as uncertain without automatic replay, validates exactly-once authorized
answers, retains bounded terminal tombstones, blocks later input in that
conversation, and makes reset and worktree reconciliation preserve nonterminal
work. The coordinator distinguishes native deferred-tool continuation from a
later continuation turn. Codex now implements the latter through a documented
experimental app-server dynamic namespace tool with exact root thread/turn
provenance, followed after answer acceptance by `thread/resume` and a new
`turn/start` carrying a bounded controller-owned answer envelope. The original
tool call is not suspended, and neither a process nor active-turn grant is held
while waiting. Its model-facing schema is discriminated by semantic request
kind and requires the same kind-specific constraints enforced by runtime
validation; malformed semantic calls receive a safe retryable `invalid` result
distinct from session or root-provenance unavailability. Claude implements the
former through an exact root-only
`PreToolUse` matcher, a short-lived owner-only broker, and the same generic
restart/capacity scheduler; known session loss fails deterministically and
ambiguous execution is not retried. Exact single-use defer receipts prove the
initial root event, and successful resume requires delivered hook and MCP
responses rather than process success alone. The strict
`channel.request_input` MCP schema, typed harness event, and dispatcher-owned
durable handoff are now implemented behind a disabled-by-default capability
bridge. The dispatcher recomputes responder fallback, rejects unproven root or
subagent calls before persistence, and acknowledges only after `requested`
commits; generated channel instructions are conditional and model-visible
results and audit stay content-free. Root provenance is an opaque harness-event
proof rather than a process-wide flag; Codex derives that proof from exact
app-server thread and turn provenance, so a propagated subagent call fails
before persistence. Harness strategies own the bounded result disposition, and
audit correlation excludes semantic request bytes. Harnesses opt into their
native strategy only when a handler exists. The live Discord runtime now binds
that handler to its renderer and deterministic text fallback codec.
Credential-free stitched acceptance drives fake Discord I/O through the real
controller, dispatcher, and Claude/Codex process adapters: both harness
processes exit while parked with zero resident or active capacity, then an
authorized native callback resumes the exact Claude deferred call or a new
turn in the same Codex thread and produces one final referenced response. That
pass exposed and fixed a canonical JSON ordering defect in Claude's hook-to-MCP
replay. Fixture-driven tests also prove equivalent normalized native and text
fallback answers, including deterministic date/time and mixed-form degradation.
Credentialed interactive acceptance is recorded separately and is never
inferred from these fake processes.

The [credential-free clean-install check](../../spikes/clean-install/README.md)
creates a disposable exact-tagged `darwin-arm64` release archive, verifies its
checksum, extracts it into an isolated install prefix, copies a minimal agent
source outside the checkout, applies it to a fresh workspace with fake Codex,
verifies that the native MCP setup names that installed binary, starts that MCP
server, and proves a missing authored-tool runtime fails clearly before native
setup is written. It rejects dirty tagged source states, proves archive and
checksum bytes are reproducible across two builds, and successfully rebuilds a
fresh external polyglot workspace cache from source and its lockfiles. It does
not publish or deploy anything.

A [live Codex acceptance pass](codex-live-acceptance.md) on CLI 0.144.1 also
proved native instruction and skill discovery, all three authored-tool hosts,
a generated custom subagent, and session resume in a fresh external workspace.

## Settled direction

- hctl bootstraps and extends Claude Code and Codex; it does not replace their
  model loops, context management, native tools, approvals, or interactive UI.
- Interactive users run the native harness after `hctl apply`. The turn dispatcher is
  an optional headless boundary, not a cross-harness chat UI.
- The agent project is portable source and is not coupled to the repository
  that stores it. Conventional files are the authoring interface; there is no
  authored hctl manifest, registry, or second inventory.
- A workspace is selected independently and defaults to the agent project for
  the standalone case. Harness files and hctl runtime state are written to the
  workspace; source discovery remains rooted in the agent project.
- Author-facing language uses concrete Eve-style terms such as instructions,
  skills, tools, channels, and connections. Avoid using `capability` as a
  synonym for `tool` and `projection` for generated harness files.
- hctl governs and observes only managed tools and durable state crossing its
  boundary. Harness-native tools remain available and explicitly unmanaged.
- TypeScript, Python, and Go are first-class authored-tool languages. Authors
  write typed functions; hctl-owned language hosts expose them through MCP.
- TypeScript and Python source should be loaded by generic long-lived hosts.
  Go requires minimal generated build glue and a compiled host cached by source
  fingerprint. No normalized tool manifest is persisted.
- The project-visible harness setup contains only native harness files and one
  internal apply record needed for safe ownership and stale-source checks.
  Runtime caches are disposable and stay outside authored source.
- Agent-proposed improvements are inert, workspace-local, human-reviewed
  proposal directories. They bind one existing source file to its exact base
  content, never mutate active authored files automatically, and retain
  immutable proposal evidence plus a later human decision. They must not retain
  credentials, secrets, raw tool outputs, or conversation transcripts.
- [ADR 0009](../adr/0009-use-a-local-secretless-operation-broker.md) selects a
  local secretless operation broker before the first secret-bearing managed
  tool or connection ships. It resolves opaque references only at authorized
  managed invocations and consumes values itself, keeping them out of authored
  files, generated harness files, the harness environment, model-visible I/O,
  diagnostics, and audit. Backend selection and connection syntax remain
  deferred; native harness capabilities and same-user peer processes remain
  outside this additive boundary.
- Authored tools are trusted project code in the local MVP. Process boundaries,
  validation, cancellation, and limits provide reliability; hctl does not claim
  to contain malicious tool code or provide an OS sandbox.
- `apply` prepares locked tool dependencies through native Deno, Python, and Go
  tooling. hctl does not invent a second package manager or dependency file.
- Immediate subagents use native Claude and Codex configuration, inherit their
  parent's skills and tools, and may optionally request `low`, `medium`, or
  `high` reasoning effort. Hctl maps that request to each harness's native
  field without claiming it will be honored. Children may not define tools,
  skills, dependencies, or further subagents in the current slice.
- Skills use the open `skills/NAME/SKILL.md` layout. Portable documentary
  metadata and regular-file resources survive both native project harness
  setups. Recognized vendor metadata is copied unchanged to either target and
  warns when that harness does not document honoring it.
- Agent Plugins v1 directories may be vendored beneath `plugins/`. Hctl validates
  each local manifest without a schema fetch, imports its fixed Agent Skills
  component, and maps supported optional MCP declarations into native harness
  configuration. Root skills and earlier plugin/server sources win deterministic
  name collisions; invalid components warn without suppressing independent
  valid components. Plugin MCP remains native and unmanaged by hctl.
- Intentionally nonportable native files use a literal
  `harnesses/claude/.claude/` or `harnesses/codex/.codex/` tree. Only the
  selected tree is copied, its files share the existing source-fingerprint and
  apply-ownership protections, and hctl neither merges nor interprets them.
- The first connection slice follows Eve's `connections/`, path-derived name,
  model-facing description, and qualified-tool conventions. A bounded
  `connections/github.md` definition enables only anonymous, public, read-only
  repository and issue operations through hctl's existing managed MCP server.
  It deliberately adds no credential, broker, generic OpenAPI runtime, or
  remote MCP proxy.
- The Discord channel uses an outbound Gateway connection and a strict
  `channels/discord.md` participation policy. Runtime profiles pin the bot
  application and identity to one authorized user, guild channel, and DM;
  credentials remain in the OS credential store. `hctl run` auto-applies stale
  setup, buffers responses for exact no-reply suppression, and exposes `/new`
  and `/status` without requiring a public listener or tunnel. A
  transport-neutral channel controller now owns reusable surface registration,
  pending-turn correlation, buffering, exact controls, one-time elevation,
  typing readiness, failure classification, and lifecycle delegation. Discord
  retains its Gateway/REST adapter, native rendering, commands, and delivery
  semantics. A deterministic session manager owns one dispatcher worker and at
  most one resident harness process per conversation, and serializes durable
  state updates across surfaces.
- Root-agent Markdown schedules follow Eve's nested `schedules/` convention:
  strict frontmatter contains one valid standard five-field cron string and the
  body is the task prompt. Apply starts no clock. `hctl schedule trigger`
  accepts a caller-owned occurrence ID, reuses bounded turn dispatcher deduplication,
  opens a fresh native session for each accepted occurrence, reports lifecycle
  status, and discards model text. Its independent operator-selected turn
  deadline aborts a stalled task process, retains the occurrence as uncertain,
  persists `deadline_exceeded` as a separate bounded reason for duplicate
  reporting, and leaves later occurrences free to open fresh sessions.
  `hctl schedule run` adds an explicit foreground UTC clock over one shared
  durable task runtime. It admits current minutes only, skips overlap including
  capacity waiters, excludes a second matching clock with a local lock, and
  drains admitted work on signal shutdown without installing a daemon.

## Retained product horizon

These are later product promises, not MVP implementation work:

- Future channel adapters feed the same session-aware turn dispatcher exercised by
  local input. Network adapters verify their source before acceptance.
- An external conversation maps to one native-harness session. Accepted input
  is durably queued FIFO; a stable source delivery ID is deduplicated when the
  channel supplies one.
- New input does not implicitly interrupt an active turn. Explicit interruption
  or steering is exposed only when the harness integration supports it.
- Outbound delivery is a managed action. Ambiguous delivery is recorded as
  uncertain and is not automatically repeated; retained payload content is
  bounded.
- Channels, connections, sandboxes, subagents, schedules, and harness-specific
  files should use understandable filesystem conventions before configuration
  is introduced.
- Slack, generic webhooks, OAuth, public-listener management, proactive vendor
  delivery, scheduling, deployment integrations, and a hosted SDK remain
  outside the current MVP.
- ADR 0027 defines the later deployment seam: a Codex- or Claude-specific hctl
  harness image may be used directly, or may selectively stage one agent's
  runnable filesystem for an existing OCI builder. Hctl does not own image
  construction, publication, signing, deployment, or operation.
- A future post-run summary may include the parent outcome and child activity
  when a harness exposes stable runtime IDs. It should reference native harness
  logs rather than duplicate transcripts, and remain optional observability
  rather than part of portable completion semantics.

## Open questions

The authored-tool questions are enumerated in the
[tool-authoring workbench](tool-authoring.md#remaining-questions). The main unresolved
areas are whether a future helper package should wrap the intentionally small
structural source APIs, graceful per-call cancellation and host restart,
concurrent calls, richer local imports, and cache cleanup. HCTL-001 selected
and HCTL-002 implemented a versioned `darwin-arm64` release archive for first
installation; it deliberately leaves a relocatable package command out until a
concrete need demonstrates one. ADR 0027 answers the concrete OCI distribution
need with a bounded staged-filesystem contract, while retaining no general
package command and no portability claim for raw workspace caches.

Product naming, the concrete secretless-broker backend, and proposal review UX
are also intentionally unresolved.

## Current design frontier

Portable source, independent workspaces, authored tools, native inherited
subagents with optional effort, Agent Skills compatibility, and harness-specific
authored files are implemented and exercised through generated Claude and Codex
configurations.
The Codex native journey has live evidence, and the `darwin-arm64`
clean-machine release archive journey is credential-free tested. Equivalent
Claude acceptance remains. The credential-broker execution boundary is settled
in ADR 0009; its backend and credential-owner decision remain deferred because
the first GitHub slice is anonymous. HCTL-010 and HCTL-011 are complete.
HCTL-012's schedule source and one-shot dispatch are complete. Each occurrence
uses a fresh native-harness task session, while its stable input ID deduplicates
retries of that occurrence. HCTL-014 now gives the native task turn a separate
bounded deadline: expiry aborts the affected process, durably retains an
uncertain result with a distinct deadline reason, and does not prevent a later
occurrence from opening a fresh session. HCTL-015 now adds the foreground UTC
clock with shared state ownership, bounded concurrency, no backfill, stable
occurrence identity, overlap skipping, runtime locking, and graceful drain.
ADR 0027 now settles the image/runtime boundary: `hctl stage` atomically
prepares canonical agent filesystems, selects only discovered tool execution
closures, records deterministic file and ownership evidence, and rejects
build-path or credential-state leakage. Existing build and deployment systems
still own OCI output and operation. The
[artifact pipeline gap analysis](artifact-pipeline-gap-analysis.md) now defines
a CI-first path from checked source through deterministic target binaries,
unpushed image acceptance, and separately authorized exact-tag publication.
The first foundation runs repository checks and reproducible Linux and Darwin
binary construction in hosted CI. Binary builds now require and expose an exact
version, exact-tag archives inject their tag, and one validated input manifest
pins the Ubuntu Linux/amd64 base plus every harness and authored-tool runtime
artifact by source, size, and checksum. The planned images are thin hctl-owned
Ubuntu images rather than layers on vendor development environments. Codex is
the first unpushed vertical slice; Claude publication remains blocked pending
explicit permission, and an unpushed Claude CI build requires a separate
authorization decision. Real container
acceptance and harness image publication remain follow-up work. Portable
sandbox authoring is still deferred because the
native harnesses do not expose equivalent
sandbox contracts. Do not implement proposal capture, image deployment,
sandbox source, or broker code merely to exercise future seams.

The channel runtime now owns explicit independent managed session lifecycles
for its configured Discord guild and DM surfaces. Idle lifecycles release their
resident harness after a bounded interval and resume the retained native
session on the next message. New and resumed channel harnesses are enforced
read-only in the shared checkout and can return an exact internal write-access
result. That result now promotes the requesting conversation into a validated,
durably assigned branch-backed Git worktree, resumes the same native session
under workspace-write policy, and continues the request once. Unassigned
conversations remain read-only. Global resident-session and active-turn limits
now bound the runtime with documented defaults, durable saturation queues,
fair cross-conversation turn admission, and idle hibernation under resident
pressure. Guild and DM mutations can now overlap in distinct writable
worktrees and native sessions, complete out of order without crossing response
surfaces, hibernate and resume independently, and restore both assignments
after restart. Ordinary worktree, harness, and deadline failures retire only
the affected conversation worker; loss of dispatcher-event delivery remains a
runtime-wide failure. Credential-free fake acceptance covers Claude and Codex.
Startup now reconciles those durable worktrees against exact Git and generated
ownership evidence. It preserves busy, uncertain, dirty, unmerged, missing,
moved, foreign, and unverifiable assignments; only inactive clean branches
already merged into the base are retired. A durable retirement marker makes
partial cleanup retryable without broad or forced deletion, and local
diagnostics explain preservation or recovery while Discord status stays
redacted.

The durable interactive-input foundation extends that same conversation record
and sole-writer state path rather than introducing a second state file. The
accepted runtime integration exposes only `waiting_for_input` and must park
without retaining model-turn or harness capacity. The dispatch state already
rejects reset while an interaction is nonterminal and treats that work as busy;
ambiguous continuation also prevents automatic worktree retirement. The
generic managed request-input schema and dispatcher handoff are advertised only
for the live Discord root after its compatible responder is bound.
Codex has a fake-tested continuation-turn strategy and Claude has a fake-tested
native deferred-tool strategy, both with root provenance and manager-owned
capacity. The Discord adapter now renders bounded native confirmations,
non-freeform choices, text, and text-only forms through stable DiscordGo v0.29
components, with deterministic degradation to an hctl-owned text grammar.
Opaque reconstructable callback handles, exact application/user/surface
authorization, durable answer-before-ack ordering, post-ack continuation, and
ambiguous-delivery no-retry behavior are fake-tested. Fake child processes
provide live-style process-exit, same-session resume, and final controller
delivery evidence. Credentialed Codex interactive-input acceptance passed;
credentialed Claude remained unavailable because the installed CLI had no
authenticated account. The
[interactive-input acceptance record](interactive-input-acceptance.md) keeps
those claims separate from the broader credential-free evidence.

A [live Discord acceptance pass](discord-live-acceptance.md) with an enrolled
user-controlled bot exercised independent guild and DM write promotion,
graceful restart, preservation and reuse of both dirty managed worktrees, and
redacted `/status` output. The broader automated evidence remains
credential-free; the runtime connects outbound through Discord's Gateway and
needs no public HTTPS handoff.

The [maintainer task list](tasks.md) turns that frontier into a prioritized,
permission-aware queue. It is the source for agent assignments; this section
remains the broader state summary.
