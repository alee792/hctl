# Product specification

- Status: experimental product contract
- Working CLI name: `hctl`; product naming is deferred
- Initial runtime: local Go executable
- Initial harnesses: Claude Code and Codex CLI

## User and job

The primary user is a product engineer building an agent that may eventually
serve end users through channels. They define one filesystem-authored project,
prove it interactively in Claude Code or Codex, and operate the same setup
headlessly without maintaining another model loop.

## Product principles

1. The authored directory is the legible, versionable source of truth.
2. Common behavior is portable; harness-specific differences are explicit.
3. Compilation and validation happen before a projection or gateway starts.
4. Generated native files are disposable and visibly tool-owned.
5. Native harness capabilities remain available and explicitly unmanaged.
6. Policy applies only at managed capability and durable-state boundaries.
7. Interactive users remain in the native harness interface.
8. Unsupported behavior fails honestly instead of being silently emulated.

## Authored project

Authoring is filesystem-forward. Where the concepts match, hctl uses Eve's
conventional vocabulary: instructions, tools, skills, channels, connections,
sandbox, subagents, and schedules. Only the subset named below is implemented
in the MVP.

The intended authoring API is convention-driven: adding an artifact to its
conventional directory should register it without duplicating that inventory
in configuration. Configuration should hold only settings that the filesystem
layout cannot express. The MVP's explicit `agent.json` source paths are a
bootstrap format, not a commitment to a registry-first product.

The MVP project contains:

- a bounded `agent.json` configuration;
- one instruction document;
- one or more portable `SKILL.md` files; and
- the bounded `echo` managed capability used to prove the shared boundary.

Source paths must be normalized relative paths inside the project. Source
files must be regular UTF-8 files without symlink traversal. Compilation
produces a deterministic manifest and source fingerprint.

## Apply and handoff

```sh
hctl apply AGENT --harness claude
hctl apply AGENT --harness codex
```

`apply` validates the authored project, target harness executable, and
protocol readiness. It materializes owned native files directly in the agent
project so the user can change into that directory and start the selected
harness normally.

Claude receives `CLAUDE.md`, `.mcp.json`, and `.claude/skills/`. Codex receives
`AGENTS.md`, `.codex/config.toml`, and `.agents/skills/`. Generated MCP
configuration uses the resolved `hctl` executable path.

Codex project configuration remains subject to Codex's native repository-trust
flow. Apply does not edit the user's global Codex configuration or silently
trust a project on their behalf.

Apply refuses to overwrite hand-authored native files or generated files that
were modified after the previous apply. Reapplying identical source is
deterministic.

## Harness contract

Each harness integration declares and verifies:

- its executable and compatible version signal;
- native projection surfaces;
- managed capability exposure;
- new-session and resume behavior;
- structured input, output, and terminal events; and
- any interruption or steering behavior that is not portable.

Claude Code uses bidirectional stream JSON. A second message received during an
active turn is queued for the next turn. Codex uses its local App Server JSONL
protocol. Active-turn steering and interruption are Codex-specific and are not
part of the portable MVP promise.

## Gateway

```sh
hctl gateway AGENT --harness claude
hctl gateway AGENT --harness codex
```

The gateway accepts bounded JSONL input containing a caller-provided
`input_id` and `text`. It durably accepts and queues input while a turn is
active, processes one FIFO turn per conversation, emits ordered JSONL events,
and maps the external conversation to a resumable harness session.

A repeated input ID is deduplicated within its conversation. After a restart,
an input that was active but lacks a proven terminal result becomes uncertain;
it is not silently retried.

The local stdin adapter exercises the future channel seam. Slack, webhooks,
OAuth, network listeners, and vendor delivery are outside the MVP.

## Managed capability boundary

The MVP exposes one bounded, read-only `echo` tool through stdio MCP in both
harnesses. Inputs and outputs are schema-validated. Audit output contains a
safe request identifier and lifecycle outcome, never the echoed content.

The managed boundary is additive. It does not disable, authorize, observe, or
retry harness-native tools. Secret-bearing capabilities require a credential
broker before they ship; no unused broker backend is scaffolded in the MVP.

## Deferred direction: authored tools and proposals

Managed tool implementations may be authored as part of the agent project and
included in its validated source fingerprint. Scripts created ad hoc by the
agent remain ordinary harness-native workspace activity unless they cross a
managed boundary.

Generated project instructions may encourage the harness to submit reusable
discoveries through a future managed proposal tool. Instructions can influence
this behavior but cannot enforce it or observe native filesystem writes.

A proposal records a candidate improvement to instructions, a skill, a tool,
or other agent feedback. It does not modify active authored source or a running
projection. Human review and explicit acceptance are required before the
change joins the agent project and is reapplied.

Proposal schema, storage, review UX, conflict handling, sensitive-content
policy, acceptance workflow, and tool-execution isolation require a dedicated
product and security spike. They are outside the MVP and are not scaffolded.

## Failure and safety behavior

- Missing, stale, ambiguous, or edited projections fail closed.
- Input, output, queue, process lifetime, state size, and protocol lines are
  bounded.
- Durable state is owner-readable only and written atomically.
- Process failure is distinct from a completed or failed model turn.
- An uncertain external effect is never described as exactly-once or retried
  without a target idempotency contract.
- Diagnostics do not expose credentials, private prompts, or raw process
  output.

## MVP acceptance

The MVP is complete when credential-free tests prove:

1. One authored project compiles deterministically for both harnesses.
2. Apply produces native, discoverable projections and refuses conflicts.
3. Both generated projections expose the same managed MCP capability.
4. Both headless drivers start and resume sessions against fake harnesses.
5. Input arriving during an active turn is durably accepted and processed
   later in FIFO order.
6. Caller-provided input IDs are deduplicated.
7. Restart recovery marks unproven active work uncertain.
8. Managed audit output remains content-free.

## Explicit non-goals

- A model loop, context manager, or cross-harness chat UI
- Vendor channels or webhook delivery
- Claude Agent SDK or hosted OpenAI agent runtimes
- Scheduling, workflows, subagents, or deployment orchestration
- Governance claims over native harness tools
- Credential storage before a secret-bearing capability exists
- Automatic or unreviewed promotion of agent-authored improvements
