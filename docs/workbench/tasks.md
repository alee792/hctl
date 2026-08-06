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

### HCTL-002 — Implement the accepted install journey

**Outcome:** Implement the accepted release-archive contract without
publication. The exact checked-out `vX.Y.Z` Git tag is the only version source.
Build only `darwin-arm64`, producing `hctl_X.Y.Z_darwin_arm64.tar.gz` with one
root-level `hctl` executable and `hctl_X.Y.Z_SHA256SUMS` containing its SHA-256
checksum. Document the exact download, verification, extraction, `PATH`, and
`apply` journey. Do not add `hctl package`, bundle agent source, tool runtimes,
workspace cache output, or another platform.

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

## Completed

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

### HCTL-004 — Spike an optional post-run summary

Determine whether the gateway can report a bounded parent outcome and child
activity using stable harness IDs while pointing to native logs rather than
copying transcripts. Start with Codex; do not invent cross-harness semantics or
make child visibility part of task completion.

### HCTL-005 — Spike human-reviewed improvement proposals

Design the smallest inert proposal flow through which an agent can suggest a
change to instructions, a skill, or a managed tool. Resolve storage, review,
conflicts, provenance, and rejection before implementing mutation. Applying a
proposal must remain an explicit human action.

### HCTL-006 — Select a credential-broker boundary

Before the first secret-bearing managed tool or connection, evaluate existing
local credential brokers and define the minimum execution contract that keeps
secrets out of agent source, generated harness files, the harness environment,
and model-visible I/O. Do not build a vault or accept real credentials during
the spike.

### HCTL-007 — Revisit authored-tool host hardening

Use observed failures or author feedback to choose among graceful per-call
cancellation, host restart, concurrency, log routing, richer local imports,
cache cleanup, or helper packages. Do not implement these speculatively as one
platform project; open one bounded task for the demonstrated need.

## Not queued

Channels, Slack/webhooks, schedules, hosted SDKs, image deployment,
root-as-agent shorthand, stronger sandboxing, and product naming remain in the
product horizon. Their presence in design notes is not authorization for the
maintainer to start them.
