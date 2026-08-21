# Working status

- Updated: 2026-08-19
- Purpose: the current implementation state, its evidence, and its gaps, so a
  clean session can resume without chat history

## Resume order

1. [North Star & Tenets](../north-star.md) — what the product holds constant.
2. [Rebuild charter](rebuild.md) — the active direction: this repository is
   the prototype; the core is rebuilt fresh against the distilled spec.
3. [Vision](../vision.md), [product specification](../product-spec.md), and
   [channel specification](../channel-spec.md) — positioning, the core
   rebuild target, and the channel product's contract.
4. GitHub Issues — the live work queue.

## Implemented

One local Go CLI implements the product specification's authored-project
contract for Claude Code and Codex:

- **Apply.** Instructions, Agent Skills (root and vendored-plugin), plugin
  MCP declarations, subagents with optional effort, harness-specific files,
  standalone MCP connections, schedules, and the Discord channel policy
  compile into native harness files without overwriting hand-authored ones.
  Validation and bounds run before any workspace mutation. Plugins and
  Skills are vendored by manual directory copy only; ADR 0036 removed the
  acquisition engine.
- **Authored tools.** TypeScript, Python, and Go functions are served
  through one managed MCP server by hctl-owned language hosts; there is no
  protocol code, manifest, or second inventory.
- **Integration packages.** An operator-trusted, checksum-pinned,
  offline-verified store serves the `native-mcp` capability (the official
  GitHub server) and the `channel-adapter` capability (the external
  `hctl-discord` executable). Core contains no vendor SDKs.
- **Headless operation.** Durable JSONL turn dispatch, Markdown schedules
  with one-shot triggers and an explicit foreground UTC clock, and the
  Discord channel runtime: read-only sessions, writable worktree promotion,
  transport-neutral interactive requests with native Claude deferral and
  Codex continuation turns, and bounded capacity with hibernation.
- **Staging.** `hctl stage` prepares one canonical runnable filesystem for
  downstream OCI builds; CI builds and exercises the unpushed Codex image
  from pinned inputs.

## Evidence

`./scripts/check.sh` and the credential-free suite (fake harness processes,
no live model calls) define completion. Live records:

- [Codex live acceptance](codex-live-acceptance.md) — the native Codex
  journey proven end to end on a real CLI.
- [Discord live acceptance](discord-live-acceptance.md) — a historical
  credentialed pass predating the external-adapter cutover; the current
  external path is covered credential-free, and its authorization-gated live
  procedure is recorded there.
- [GitHub native MCP acceptance](github-native-mcp-acceptance.md) — the
  credential-free claim map; a live pass was never authorized.
- [Interactive input acceptance](interactive-input-acceptance.md) —
  credentialed Codex passed; Claude was unavailable.
- [Skill compatibility](skill-compatibility.md) — the dated vendor
  compatibility matrix behind skill generation; reverify its linked vendor
  documentation before adding or translating an extension.
- [Tool authoring](tool-authoring.md) — settled requirements and open
  questions for authored tools.

## Gaps and blocked work

- The [rebuild charter](rebuild.md)'s two human gates are satisfied: the
  shape was reviewed and merged, and the product name is Tenon. Bootstrap
  of the rebuild repository is the next step. The restructure's D5 module
  split, D6, and D7 are superseded by the charter; the channel runtime
  stays here as the working second product.
- The agent manifest and `hctl validate` with stable diagnostics are
  specified for the rebuild target and are not implemented in this
  prototype.
- Live Claude acceptance, the Claude harness image, and the live
  external-Discord pass remain blocked on explicit human authorization.
- Deliberately unresolved: the secretless-broker backend
  ([ADR 0009](../adr/0009-use-a-local-secretless-operation-broker.md)
  selects the boundary, and no secret-bearing managed operation exists yet);
  and the authored-tool follow-ups in
  [tool-authoring](tool-authoring.md#remaining-questions).
