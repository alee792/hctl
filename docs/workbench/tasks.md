# Maintainer task list

This is the executable queue for the hctl maintainer agent. It is deliberately
smaller than the product horizon in [Working status](status.md).

## How to use this list

- Take the first unblocked task in `Ready` unless the user names another task.
- Work on one task at a time. Do not silently expand it into a later task.
- Read the linked product and workbench context before changing code.
- Treat research tasks as evidence and a recommendation, not authorization to
  publish, deploy, add credentials, or contact external systems.
- Before handoff, review the change with the maintainer's `code-review` skill
  and run `./scripts/check.sh` when code or generated behavior changed.
- Move or rewrite an item when evidence changes; do not leave completed work in
  `Ready`.

## Ready

### HCTL-009 — Richer subagents

**Outcome:** Allow an immediate subagent's `instructions.md` frontmatter to
request one optional portable `effort: low|medium|high`. Emit the value as
Claude's native `effort` agent field and Codex's native
`model_reasoning_effort` custom-agent field. Omit both when unspecified so
existing description-only children remain compatible.

**Boundaries:** Hctl validates and requests effort; the selected harness,
model, account, and policy determine whether it is honored. Keep root
instructions description-only. Do not add model selection, tool or skill
allowlists, child-owned tools, skills, connections or MCP servers, permissions,
sandbox/worktree settings, hooks, turn limits, nested children, delegation
policy, lifecycle management, harness-version detection, or runtime
observability. Those do not have one honest portable native contract today.

**Acceptance:** Accept exactly `low`, `medium`, or `high`; reject unknown or
duplicate fields, non-string effort, and unsupported values clearly. Prove
description-only output remains unchanged, both native outputs use their exact
field names, effort changes the source fingerprint, and removing it safely
removes the native field on reapply. Update README, product specification,
status, and ADR 0006's accepted boundary through a new short ADR. Run
`./scripts/check.sh` and the maintainer's independent reviews.

## Ordered after HCTL-009

Do not start these concurrently. Shape each item using evidence from the
completed predecessor.

1. **HCTL-010 — GitHub connection.** Inspect Eve's then-current connection
   filesystem convention and interface, then implement the smallest useful
   hctl connection consistent with ADR 0009. No live credential or GitHub
   action is authorized by this queue entry.
2. **HCTL-011 — Discord channel.** Inspect Eve's then-current channel
   filesystem convention and interface, then connect Discord to hctl's
   session-aware gateway with authenticated, deduplicated input and bounded
   outbound delivery. No live bot, webhook, listener, or Discord action is
   authorized by this queue entry.
3. **HCTL-012 — Schedules and sandbox/runtime conventions.** Reassess these
   after a real connection and channel expose the runtime and deployment needs;
   do not treat them as one implementation project unless the evidence supports
   that shape.

## Completed

### HCTL-008 — Add harness-specific authored files

**Outcome:** An agent project may carry bounded native files under a literal
`harnesses/claude/.claude/` or `harnesses/codex/.codex/` tree. Apply selects
only the matching harness, copies regular-file contents unchanged, preserves
executable intent, and tracks the files in the existing source fingerprint and
apply ownership record. It rejects symlinks, invalid paths, collisions, and
hctl-owned skill, subagent, and MCP/configuration destinations. Hctl does not
merge or interpret native files or promise harness enforcement. Credentials
are prohibited in authored harness files, but hctl does not claim to detect
them reliably.

**Evidence:** Credential-free project and setup tests cover harness isolation,
fingerprint changes, literal content, executable mode, bounded and reserved
paths, preflight collisions, and refusal to overwrite or remove a modified
owned workspace file. The convention and its current official
[Claude project settings](https://code.claude.com/docs/en/settings) and
[Codex project rules](https://developers.openai.com/codex/rules) precedents are
documented in the product specification. `./scripts/check.sh` passes.

### HCTL-006 — Select a credential-broker boundary

**Outcome:** Select a future hctl-owned local secretless operation broker, not
a vault or a storage backend. It resolves an opaque credential reference only
at an authorized managed invocation, performs a constrained upstream operation
itself, and returns/audits only bounded secret-free data. Environment/file
injection, credential-helper stdout, and direct SDK retrieval all disclose the
value to a child or authored tool and therefore do not satisfy the boundary.
No backend, author-facing connection syntax, credentials, or code was added.

**Evidence:** [ADR 0009](../adr/0009-use-a-local-secretless-operation-broker.md)
records the primary-source research, alternatives, minimum reference,
authorization, IPC, lifecycle, failure, redaction, and audit contract, and
the explicit same-OS-user/native-harness limit.

### HCTL-005 — Spike human-reviewed improvement proposals

**Outcome:** Keep proposals as inert, workspace-local directories at
`.hctl/proposals/ID/`, separate from the portable agent source they suggest
changing. Each record holds a human-readable explanation and provenance, an
exact base-content hash, and one bounded diff for an existing instruction,
skill, or managed-tool source file. A human manually accepts by changing source
and reapplying, or rejects it in a retained review record; stale proposals are
never automatically rebased or applied. Proposal evidence is immutable and may
not contain credentials, secrets, raw tool outputs, or conversation transcripts.

**Evidence:** [ADR 0008](../adr/0008-keep-agent-proposals-workspace-local-and-inert.md)
settles storage across the source/workspace boundary, target scope, provenance,
base binding, conflict and stale handling, explicit review, safe failure,
retention, and the additive human-in-the-loop boundary for a future managed
proposal tool. No mutation path, proposal command, or tool was implemented.

### HCTL-004 — Spike an optional post-run summary

**Outcome:** Do not implement a summary. Hctl can durably report the parent
input outcome and observed parent runtime IDs; the current Codex App Server
schema and prior live trace show child runtime IDs, but hctl has no stable
native-log locator contract. A summary now would either invent one, copy
transcript-derived content, or imply portable child semantics.

**Evidence:** The credential-safe
[Codex post-run summary spike](codex-post-run-summary-spike.md) records the
reproducible schema procedure, current gateway/session/event boundaries, the
prior child-stream observation, raw-observation limits, and additive failure
behavior. Its regression source proves a child completion before the parent
does not complete the parent gateway turn. No model turn, credential material,
native-log content, or transcript was captured for this spike.

**Follow-up:** No implementation is queued. Reopen only when optional
Codex-specific observability is prioritized and an explicitly authorized
model-backed capture can establish a documented native-log reference. Child
visibility must remain outside completion semantics.

### HCTL-002 — Implement the accepted install journey

**Outcome:** Implement the accepted release-archive contract without
publication. The exact checked-out `vX.Y.Z` Git tag is the only version source.
Build only `darwin-arm64`, producing `hctl_X.Y.Z_darwin_arm64.tar.gz` with one
root-level `hctl` executable and `hctl_X.Y.Z_SHA256SUMS` containing its SHA-256
checksum. Document the exact download, verification, extraction, `PATH`, and
`apply` journey. Do not add `hctl package`, bundle agent source, tool runtimes,
workspace cache output, or another platform.

**Evidence:** A credential-free check builds the exact-tagged `darwin-arm64`
release archive, verifies its checksum, extracts it into an isolated prefix,
then uses only that extracted executable to apply an agent source and workspace
outside the checkout and start its generated managed MCP server. It also proves
the generated configuration records the resolved installed executable, required
language runtimes fail before native setup is written, and source plus lockfiles
are retained while a successful fresh apply rebuilds `.hctl/cache/`. The builder
rejects dirty tagged source and emits reproducible archive and checksum bytes.
No publication or deployment occurred.

**Acceptance:** A credential-free check builds and extracts the release-shaped
archive into an isolated prefix, uses only that extracted executable to apply
an agent source and workspace outside the checkout, and starts its generated
managed MCP server. It proves the generated configuration records the resolved
installed executable, required language runtimes fail before native setup is
written, and a fresh machine keeps source plus lockfiles and reruns `apply`
rather than copying `.hctl/cache/`. Run the full quality gate. Do not publish
without explicit human authorization.

**Context:** [ADR 0007](../adr/0007-first-install-release-archive.md),
[product specification](../product-spec.md#initial-installation), and the
[clean-install proof](../../spikes/clean-install/README.md).

### HCTL-001 — Choose the first install and packaging contract

**Decision:** Start with the `darwin-arm64` platform exercised by the
clean-install proof. The exact `vX.Y.Z` Git tag names
`hctl_X.Y.Z_darwin_arm64.tar.gz`, containing a root-level `hctl` executable,
and `hctl_X.Y.Z_SHA256SUMS`. The generated MCP configuration stores the
resolved absolute path: moving the executable requires `apply`; replacing it
in place leaves the path valid, but the supported upgrade journey reruns
`apply`. `go install` is rejected as the first user journey because it requires
a Go toolchain and source/module resolution instead of the released artifact.
Do not introduce `hctl package`: agent source and lockfiles travel to another
machine, while generated hosts, prepared native dependency environments, and
`.hctl/cache/` stay disposable and are rebuilt by `apply`.

**Evidence:** The credential-free
[clean-install proof](../../spikes/clean-install/README.md) copies the minimal
agent source outside the checkout, applies it with an installed binary to a
separate workspace, and starts the managed MCP server to list `echo`. The
runtime behavior and rejected alternatives are recorded in
[ADR 0007](../adr/0007-first-install-release-archive.md).

## Waiting on access

### HCTL-003 — Run live Claude Code acceptance

**Blocked by:** A working Claude Code account and explicit authorization to use
it.

**Outcome:** Repeat the established native acceptance journey for Claude Code:
instructions, a portable skill, TypeScript/Python/Go managed tools, an
instructions-only subagent, and session continuation in an external workspace.

**Evidence:** Record the Claude CLI version, exact credential-safe procedure,
observed result, warnings, and any discrepancy from the fake-tested contract.
Any product fix discovered live also needs a credential-free regression test.

**Context:** [Codex live acceptance](codex-live-acceptance.md).

## Start only with human direction

### HCTL-007 — Revisit authored-tool host hardening

Use observed failures or author feedback to choose among graceful per-call
cancellation, host restart, concurrency, log routing, richer local imports,
cache cleanup, or helper packages. Do not implement these speculatively as one
platform project; open one bounded task for the demonstrated need.

## Not queued

Slack/webhooks, hosted SDKs, image deployment, root-as-agent shorthand, and
product naming remain outside the ordered queue. Their presence in design
notes is not authorization for the maintainer to start them.
