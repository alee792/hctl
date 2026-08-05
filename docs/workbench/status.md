# Working status

- Updated: 2026-08-04
- Repository: local-only `hctl` experiment; product naming remains deferred
- Purpose: let a clean session resume without depending on chat history or the
  previous Roster repository

## Resume order

1. Read the [vision](../vision.md) for the product boundary.
2. Read the [product specification](../product-spec.md) for the accepted MVP.
3. Use the [glossary](../glossary.md) for author-facing terminology.
4. Read the [tool-authoring workbench](tool-authoring.md) before designing or
   implementing authored tools.

The product specification is the current contract. Workbench documents record
settled requirements, candidate designs, implementation drift, and open spikes;
candidate APIs in them are not accepted contracts.

## Shipped prototype

The repository contains a standard-library Go CLI that:

- applies instructions and Markdown skills to native Claude Code and Codex
  files without overwriting hand-authored files;
- keeps one apply record, migrates legacy projection records, and removes the
  obsolete duplicated runtime manifest;
- exposes one bounded `echo` tool through stdio MCP;
- drives local Claude Code and Codex processes headlessly through fake-tested
  bidirectional protocols;
- maps conversations to resumable harness sessions, queues input FIFO,
  deduplicates caller input IDs, and marks ambiguous restart recovery
  uncertain; and
- passes the repository-local formatting, test, vet, lint, and vulnerability
  checks through `./scripts/check.sh`.

This proves the original MVP seam. Conventionally authored TypeScript, Python,
and Go tools are design direction, not shipped behavior.

## Settled direction

- hctl bootstraps and extends Claude Code and Codex; it does not replace their
  model loops, context management, native tools, approvals, or interactive UI.
- Interactive users run the native harness after `hctl apply`. The gateway is
  an optional headless boundary, not a cross-harness chat UI.
- The agent project is an ordinary directory. Conventional files are the
  authoring interface; there is no authored hctl manifest, registry, or second
  inventory.
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
- `apply` generates only the native harness files and one internal apply record
  needed for safe ownership and stale-source checks. Runtime caches are
  disposable and stay out of the authored project.
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

## Known implementation drift

- The MCP server still contains only the built-in `echo` tool. It does not yet
  discover `tools/` or supervise language hosts.

This drift is known prototype debt, not a competing product decision.

## Open questions

The authored-tool questions are enumerated in the
[tool-authoring workbench](tool-authoring.md#spike-questions). The main unresolved
areas are whether the spike's source shapes should become the supported API,
graceful cancellation and host restart behavior, cache ownership, MCP
integration, and packaging.

Product naming, the concrete credential-broker backend, proposal storage and
review UX, and specific vendor channel adapters are also intentionally
unresolved. Do not infer answers from the current prototype.

## Current design frontier

The first authored-tool process spike now lives in
`spikes/polyglot-tools/`. It discovers and calls one tool in each language with
locked native dependencies and persistent hosts, and proves definition,
duplicate, validation, process, and timeout failures. The next bounded step is
to promote the stable host mechanics behind hctl's existing MCP server and
exercise that server through the fake Claude and Codex harnesses. Keep vendor
channels and proposal execution out of that work.
