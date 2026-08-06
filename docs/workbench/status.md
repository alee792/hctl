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
builds a fresh hctl binary with the pinned repository Go toolchain into a
disposable install prefix, applies the minimal example to a fresh workspace
with fake Codex, verifies that the native MCP setup names that installed
binary, and proves a missing authored-tool runtime fails clearly before native
setup is written.

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
- Agent-proposed improvements are human-reviewed proposals. They never mutate
  active authored files automatically.
- A credential broker is required before the first secret-bearing managed tool
  or connection ships. Secrets must remain out of authored files, generated
  harness files, the harness environment, and model-visible input or output.
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
concurrent calls, richer local imports, cache cleanup, and packaging.

Product naming, the concrete credential-broker backend, proposal storage and
review UX, and specific vendor channel adapters are also intentionally
unresolved. Do not infer answers from the current prototype.

## Current design frontier

Portable source, independent workspaces, authored tools, native inherited
subagents, and the Agent Skills compatibility contract are implemented and
exercised through generated Claude and Codex configurations. The Codex native
journey has live evidence; equivalent Claude acceptance and clean-machine
packaging remain. The next product decision is packaging, proposal capture,
credential brokering, or another vision item. Do not add channels or image
deployment merely to exercise those future seams.

The [maintainer task list](tasks.md) turns that frontier into a prioritized,
permission-aware queue. It is the source for agent assignments; this section
remains the broader state summary.
