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

### HCTL-001 — Choose the first install and packaging contract

**Outcome:** Recommend the smallest supported way to install a pinned hctl
version and use it from an unrelated agent project and workspace. Decide what,
if anything, a future `hctl package` command must add beyond `hctl apply`.

**Scope:** Compare only realistic first-release paths such as `go install` and
a release archive. Include how the installed executable is referenced by
generated MCP configuration, which tool-host artifacts remain local caches,
and what must be rebuilt on another machine. Do not publish a package or build
a general deployment system.

**Evidence:** Extend the existing
[clean-install proof](../../spikes/clean-install/README.md) where a concrete
experiment resolves uncertainty. Record the recommendation and rejected
alternatives in an ADR if it changes the product contract. The result must
identify the follow-up implementation task precisely enough for a clean
session to execute it.

**Context:** [Product specification](../product-spec.md),
[tool-authoring workbench](tool-authoring.md), and
[Working status](status.md#current-design-frontier).

### HCTL-002 — Implement the accepted install journey

**Blocked by:** HCTL-001 and human acceptance of its product-facing choice.

**Outcome:** A user can install a pinned hctl build outside this repository,
apply the minimal agent to a fresh unrelated workspace, and start a supported
native harness with the generated managed tools available.

**Evidence:** A credential-free clean-install check covers the documented
commands, executable discovery, a workspace outside the source checkout,
required language-runtime detection, and actionable failure before partial
native setup. Run the full quality gate. Do not publish without explicit human
authorization.

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
