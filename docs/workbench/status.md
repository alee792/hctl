# Working status

- Updated: 2026-08-05
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
  bundled resources, and immediate instructions-only subagents to an
  independently selected workspace as native Claude Code and Codex files
  without overwriting hand-authored files;
- keeps one apply record, migrates legacy projection records, and removes the
  obsolete duplicated runtime manifest;
- discovers, prepares, validates, and exposes TypeScript, Python, and Go tool
  functions beside the bounded built-in `echo` tool through stdio MCP;
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
- Interactive users run the native harness after `hctl apply`. The gateway is
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
  parent's skills and tools, and may not define child tools, skills,
  dependencies, or further subagents in the current slice.
- Skills use the open `skills/NAME/SKILL.md` layout. Portable documentary
  metadata and regular-file resources survive both native project harness
  setups. Recognized vendor metadata is copied unchanged to either target and
  warns when that harness does not document honoring it.

## Retained product horizon

These are later product promises, not MVP implementation work:

- Advance the remaining filesystem conventions in this order: harness-specific
  authored files, richer subagents, a GitHub connection, a Discord channel,
  then schedules and sandbox/runtime conventions. Each item must remain a
  bounded product slice; completing the sequence is not permission to add live
  credentials, contact GitHub or Discord, publish, deploy, or replace native
  harness behavior.
- Before defining the GitHub connection or Discord channel contract, inspect
  Eve's then-current filesystem convention and interface. Reuse its plain
  author-facing concepts where they fit hctl's native-harness boundary; record
  deliberate differences rather than copying an incompatible runtime model.
- Future channel adapters feed the same session-aware gateway exercised by
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
- Slack, webhooks, OAuth, network listeners, vendor delivery, scheduling,
  deployment integrations, and a hosted SDK remain outside the current MVP.
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

Product naming, the concrete secretless-broker backend, proposal review UX,
and the exact GitHub and Discord authoring interfaces are also intentionally
unresolved. Do not infer answers from the current prototype or define those
interfaces before their ordered work item reaches the frontier.

## Current design frontier

Portable source, independent workspaces, authored tools, native inherited
subagents, and the Agent Skills compatibility contract are implemented and
exercised through generated Claude and Codex configurations. The Codex native
journey has live evidence, and the `darwin-arm64` clean-machine release archive
journey is credential-free tested. Equivalent Claude acceptance remains. The
credential-broker execution boundary is settled in ADR 0009; backend selection
waits for the GitHub connection slice or another concrete secret-bearing
managed tool. Harness-specific authored files are the next product slice; the
remaining ordered conventions stay behind it. Do not implement proposal
capture, image deployment, or broker code merely to exercise future seams.

The [maintainer task list](tasks.md) turns that frontier into a prioritized,
permission-aware queue. It is the source for agent assignments; this section
remains the broader state summary.
