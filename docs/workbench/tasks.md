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

### HCTL-012 — Add portable schedule source and one-shot dispatch

**Settled shape:** Follow Eve's Markdown convention at
`schedules/NESTED/NAME.md`: strict frontmatter contains only one five-field
`cron` string and the non-empty Markdown body is the scheduled prompt. The
relative path supplies the schedule name. Apply validates and fingerprints
root-agent schedules but starts no clock or model. `hctl schedule trigger`
runs one named occurrence through the existing native harness and durable
gateway using an operator-supplied stable input ID; it reports bounded lifecycle
status, discards model text, and performs no registration, daemon installation,
missed-run replay, channel delivery, network request, credential use, or live
model call in credential-free tests. TypeScript handlers, subagent schedules,
and Eve's hosted auth/delivery runtime remain unsupported.

**Decision:** Every occurrence opens a fresh native-harness task session,
matching Eve. Repeated jobs do not silently accumulate model context, source
changes need no context migration, and retry/uncertain semantics stay tied to
one occurrence. A stable input ID deduplicates that occurrence without turning
the schedule itself into a resumed conversation.

**Context:** Eve's current
[`schedules` contract](https://github.com/vercel/eve/blob/84c3dfc1ff91e075444eee7c6d8e2ef55b2aaebe/docs/schedules.mdx),
the [product specification](../product-spec.md), and current gateway/session
state.

## Ordered next

Do not start these concurrently. Shape each item using evidence from the
completed predecessor.

1. **HCTL-014 — Add per-turn gateway deadlines.** Shape an operator runtime
   deadline at the gateway/session seam after HCTL-012. It must persist timeout
   as uncertain, abort the affected native process, and state whether later
   queued input can safely reopen a session. Do not put deployment policy in
   portable agent source.
2. **HCTL-015 — Add a foreground local schedule clock.** After one-shot
   dispatch and turn deadlines are proven, assess a foreground UTC five-field
   cron runner that reuses the same trigger path, does no downtime backfill,
   and skips overlapping occurrences. Do not install or mutate launchd,
   systemd, crontab, or a hosted scheduler.

## Completed

### HCTL-011 — Add a signed Discord Interactions channel

**Outcome:** A bounded UTF-8 description at `channels/discord.md` registers one
built-in Discord channel. `hctl channel discord` runs one loopback HTTP
Interactions adapter for one selected harness and hctl conversation. It verifies
Discord's Ed25519 signature and bounded timestamp, authorizes one configured
application and user, maps the interaction ID to the existing durable gateway
input ID, flushes an admitted command's deferred acknowledgement before gateway
acceptance, then asynchronously accepts or rejects the input and delivers
bounded turn output through Discord's interaction response token.

**Boundaries:** Follow Eve's `channels/`, path-derived identity, normalized
input, continuation ownership, deferred response, and followup precedent. Use
Discord HTTP Interactions, not a Gateway bot or incoming webhook. No bot token,
OAuth, proactive sending, ordinary message/mention ingestion, command
registration, tunnel, TLS termination, public listener, deployment, component,
modal, typing, or interruption support is part of this slice. Bind loopback by
default and require explicit application ID, public key, and one allowed user
at runtime; none belong in agent source or generated harness files. Apply makes
no network request. Unless explicitly overridden, the conversation is derived
from the application and allowed user.

Refactor the gateway only enough to expose typed submission and events while
keeping JSONL as its existing adapter and persistence authoritative. One channel
process owns one conversation and one harness session; do not add concurrent
multi-conversation state mutation. Accept PING and one application-command
string option named `message`. Never pass the raw request, signature,
interaction token, or reusable Discord context to the model, state, logs, or
audit. Consume the short-lived interaction token in memory only against fixed
Discord response endpoints. The new ADR must record why that inbound
continuation capability does not select ADR 0009's future reusable-credential
backend; a bot token would.

**Evidence:** Credential-free tests with generated Ed25519 keys, fake
harnesses, and fake Discord HTTP cover discovery/fingerprinting and invalid
channel source for both harnesses; setup verification; safe listener/path and
runtime identity validation; PING; valid flushed defer before stalled gateway
acceptance; asynchronous queue rejection; bad/missing/stale signature; wrong
application; unauthorized user; bounded `message`; interaction-ID
deduplication; FIFO input while active; completion/failure/uncertain delivery;
bounded pre-acceptance admission and ordinary-delivery saturation; 14-minute
token cleanup independent of a blocked normal delivery; six total
2,000-character messages with a retained
truncation marker and disabled mentions; scoped default conversations; rate
limits; timeouts, redirects, response bounds, and ambiguous-delivery behavior;
an actual loopback runner; separated readiness diagnostics; content-free
state/log/audit; and no live Discord request, credential, registration, or
exposed listener.
`./scripts/check.sh` passes.

**Context:** Eve's current
[`channels` contract](https://github.com/vercel/eve/blob/84c3dfc1ff91e075444eee7c6d8e2ef55b2aaebe/docs/channels/overview.mdx),
[`Discord` adapter](https://github.com/vercel/eve/blob/84c3dfc1ff91e075444eee7c6d8e2ef55b2aaebe/docs/channels/discord.mdx),
Discord's official
[`Interactions` contract](https://docs.discord.com/developers/interactions/receiving-and-responding),
[product specification](../product-spec.md), and
[ADR 0009](../adr/0009-use-a-local-secretless-operation-broker.md), and
[ADR 0012](../adr/0012-use-signed-discord-http-interactions.md). The README,
product specification, glossary, status, task list, and credential-free example
record the implemented contract.

### HCTL-010 — Add an anonymous public GitHub connection

**Outcome:** A bounded UTF-8 description at `connections/github.md` registers
the path-derived `github` connection and exposes exactly
`github__get-repository`, `github__list-issues`, and `github__get-issue` through
the existing managed MCP server in both harnesses. Runtime calls make fixed,
anonymous, read-only GitHub REST requests and return selected, bounded public
repository and issue data. Apply makes no network request. Private access,
writes, credentials, generic OpenAPI loading, MCP proxying, approval UX, and
broker code remain out of scope.

**Evidence:** Project, CLI, connection, and MCP tests cover discovery for both
harnesses, fingerprinting, invalid definitions before workspace mutation, the
three exact operations, fixed method/path/headers, absent authorization,
input/output limits, truncation, redirects, timeouts, response bounds, stable
status errors, no retry, content-free audit, the unchanged connection-free
surface, and continued MCP service after a failed call. The public fixture and
[ADR 0011](../adr/0011-start-github-connections-anonymously.md) record the
anonymous-first contract. `./scripts/check.sh` passes without a live GitHub
call or credential.

### HCTL-009 — Add portable subagent effort

**Outcome:** An immediate subagent may request optional `effort: low|medium|high`
in its `instructions.md` frontmatter. Hctl emits Claude's native `effort` field
or Codex's native `model_reasoning_effort` field and omits both when the request
is absent. This is a validated request, not a guarantee that the selected
harness, model, account, or policy honors it. Root instructions and all other
subagent inheritance and isolation boundaries remain unchanged.

**Evidence:** Project and setup tests cover every supported value, unknown and
duplicate fields, non-string and unsupported values, exact native field names,
source-fingerprint changes, byte-identical description-only output, and safe
field removal on reapply. [ADR 0010](../adr/0010-allow-portable-subagent-effort.md)
amends ADR 0006 only for this optional field. `./scripts/check.sh` passes.

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

### HCTL-013 — Run live Discord Interactions acceptance

**Blocked by:** A disposable Discord application, a user-controlled public HTTPS
endpoint forwarding only `/interactions` to the loopback runner, one registered
application command, and explicit authorization to use them.

**Outcome:** Repeat the credential-free journey against Discord: endpoint PING,
one authorized `message` command, visible defer, bounded original edit and
followup, an unauthorized-user rejection, and one controlled restart/recovery
observation. Record the hctl revision, Discord application context, exact
credential-safe procedure, timings, and discrepancies without retaining
signatures, tokens, message bodies, or credentials. Any product correction also
needs a credential-free regression test.

**Context:** [Discord source example](../../examples/discord/README.md) and
[ADR 0012](../adr/0012-use-signed-discord-http-interactions.md).

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
