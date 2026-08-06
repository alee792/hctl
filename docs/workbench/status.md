# Working status

- Updated: 2026-08-06
- Repository: local-only `hctl` experiment; product naming remains deferred
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

The repository contains a small Go CLI, with one maintained YAML dependency,
that:

- applies portable agent instructions, open Agent Skills directories with
  bundled resources, immediate inherited subagents with an optional portable
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
  and `/status` without requiring a public listener or tunnel. A deterministic
  session manager owns one dispatcher worker and at most one resident harness
  process per conversation, and serializes durable state updates across
  surfaces.
- Root-agent Markdown schedules follow Eve's nested `schedules/` convention:
  strict frontmatter contains one valid standard five-field cron string and the
  body is the task prompt. Apply starts no clock. `hctl schedule trigger`
  accepts a caller-owned occurrence ID, reuses bounded turn dispatcher deduplication,
  opens a fresh native session for each accepted occurrence, reports lifecycle
  status, and discards model text.

## Retained product horizon

These are later product promises, not MVP implementation work:

- Advance the remaining schedule runtime as separate bounded slices: per-turn
  dispatch deadlines, then a foreground local clock. Completing that sequence
  is not permission to add live credentials, contact GitHub or Discord,
  publish, deploy, or replace native harness behavior.
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
- A later deployment path may compose a harness, an agent project, and hctl in
  an image. This is a packaging and operations layer over the same portable
  source contract, not a reason to couple source to its storage repository.
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
concrete need demonstrates one.

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
retries of that occurrence. Per-turn dispatch deadlines and a foreground local
clock are separate follow-ups.
Portable sandbox and image/runtime authoring are deferred: the native harnesses
do not expose equivalent sandbox contracts, native lockfiles already cover
authored-tool runtimes, and hctl does not own deployment. Do not implement
proposal capture, image deployment, sandbox source, or broker code merely to
exercise future seams.

The channel runtime now owns explicit independent managed session lifecycles
for its configured Discord guild and DM surfaces. Idle process hibernation,
global resident-session and active-turn limits, read-only admission, and
conversation-specific writable worktrees remain the ordered follow-up slices
tracked by the concurrent-session epic.

Live Discord acceptance remains pending enrollment of a user-controlled bot and
authorized guild/channel. The automated evidence uses credential-free tests;
the runtime itself connects outbound through Discord's Gateway and needs no
public HTTPS handoff.

The [maintainer task list](tasks.md) turns that frontier into a prioritized,
permission-aware queue. It is the source for agent assignments; this section
remains the broader state summary.
