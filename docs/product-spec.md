# Product specification

- Status: experimental product contract
- Working CLI name: `hctl`; product naming is deferred
- Initial runtime: local Go executable
- Initial harnesses: Claude Code and Codex CLI

## User and job

The primary user is an agent author who understands basic files and directories
and common AI concepts such as instructions, skills, and tools. They should not
need to understand registration, manifests, or harness configuration. They
define one filesystem-authored agent project, apply it to a chosen workspace,
prove it interactively in Claude Code or Codex, and may operate the same setup
headlessly through channels.

## Product principles

1. The agent project is legible, versionable, portable source and is not
   coupled to the repository that stores it.
2. Common behavior is portable; harness-specific differences are explicit.
3. Compilation and validation happen before harness files are written or a
   gateway starts.
4. Generated native files are disposable and visibly tool-owned.
5. Native harness tools remain available and explicitly unmanaged.
6. Policy applies only at managed-tool and durable-state boundaries.
7. Interactive users remain in the native harness interface.
8. Unsupported behavior fails honestly instead of being silently emulated.
9. Conventional files register behavior without a second inventory.
10. Author-facing language stays concrete; runtime terminology remains internal.

## Authored project

Authoring is filesystem-forward. Where the concepts match, hctl uses Eve's
conventional vocabulary: instructions, tools, skills, channels, connections,
sandbox, subagents, and schedules. Only the subset named below is implemented
in the MVP.

The authoring API is convention-driven. An MVP project is:

```text
my-agent/
  instructions.md
  skills/
    research.md
  tools/
    get_weather.ts
    lookup_policy.py
    hash_text/
      tool.go
  subagents/
    researcher/
      instructions.md
```

The directory name supplies the agent name, normalized to lowercase words with
hyphens. `instructions.md` is required and contains YAML frontmatter with one
plain `description` plus a non-empty Markdown body. Generated always-on
instructions contain the body, not the frontmatter. The `skills/` directory is optional;
each visible Markdown file in it is one skill and its frontmatter name must
match its filename. Adding or removing a skill file updates the compiled
project without separate registration.

Immediate directories under `subagents/` define native harness subagents. Each
contains only an `instructions.md` file with the same description-and-body
contract. The MVP allows one level and at most eight subagents. A subagent
inherits the selected parent's generated instructions, skills, managed MCP
tools, native tools, and permissions through native harness behavior. Child
skills, tools, dependency files, and nested subagents are rejected rather than
silently ignored. Subagent and tool names may not collide.

Visible `tools/*.ts` and `tools/*.py` files each declare one tool. A visible
`tools/NAME/tool.go` directory declares one Go tool. Filenames supply tool
names, with underscores exposed as hyphens. TypeScript definitions export a
default object containing `description`, strict Zod `inputSchema` and
`outputSchema`, and `execute`. Python modules export `description`, Pydantic
`Input` and `Output` models, and `execute`. Go packages export `Description`,
`Input`, `Output`, and `Execute`. The runnable mixed-language fixture is the
canonical syntax example while the product remains experimental.

Authored source files must be regular, bounded UTF-8 files without symlink
traversal. There is no authored hctl manifest, registry, or duplicated tool
inventory. TypeScript uses root `deno.json` and `deno.lock`; Python uses
`pyproject.toml` and `uv.lock`; Go uses `go.mod` and an optional `go.sum`.
These native files describe dependencies without registering tools.
Compilation produces a deterministic apply record and source fingerprint. The
bounded `echo` managed tool remains an hctl-provided default; it is not author
configuration.

## Apply and handoff

```sh
hctl apply AGENT --workspace WORKSPACE --harness claude
hctl apply AGENT --workspace WORKSPACE --harness codex
```

`apply` validates the authored project, target harness executable, tool
definitions, locked dependencies, and protocol readiness. It invokes Deno,
`uv`, or Go only when that language is present, then materializes owned native
files in the selected workspace so the user can change into that directory and
start the selected harness normally. `--workspace` defaults to the agent
project directory, making a standalone agent the simplest case. Applying an
agent stored elsewhere is explicit:

```sh
hctl apply ~/agents/reviewer --workspace ~/Code/example --harness claude
cd ~/Code/example && claude
```

The agent project supplies instructions, skills, tools, subagents, and native
dependency files. The workspace supplies harness-visible working files and is
the working directory for the harness and authored tools. Generated harness
files, apply records, gateway state, and runtime caches belong to the
workspace. Source discovery and dependency preparation remain rooted in the
agent project.

Claude receives `CLAUDE.md`, `.mcp.json`, `.claude/skills/`, and
`.claude/agents/`. Codex receives `AGENTS.md`, `.codex/config.toml`,
`.agents/skills/`, and `.codex/agents/`. Generated MCP configuration uses the
resolved `hctl` executable, agent-source, and workspace paths.

Codex project configuration remains subject to Codex's native repository-trust
flow. Apply does not edit the user's global Codex configuration or silently
trust a project on their behalf.

Apply refuses to overwrite hand-authored native files or generated files that
were modified after the previous apply. Reapplying identical source is
deterministic.

## Harness contract

Each harness integration declares and verifies:

- its executable and compatible version signal;
- native generated-file surfaces;
- managed tool exposure;
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

## Managed tool boundary

The MVP exposes one bounded, read-only `echo` tool plus conventionally authored
TypeScript, Python, and Go tools through one stdio MCP server in both harnesses.
Inputs and outputs are schema-validated. Audit output contains a safe request
identifier, tool name, and lifecycle outcome, never tool arguments or output.

One long-lived process per authored language serves inspection and calls for
the MCP session. Tool calls are serialized in the current MVP. A call that
exceeds its deadline terminates that language host and fails clearly; graceful
per-call cancellation and automatic host restart are not claimed.

The managed boundary is additive. It does not disable, authorize, observe, or
retry harness-native tools. Secret-bearing tools require a credential
broker before they ship; no unused broker backend is scaffolded in the MVP.

## Authored tool lifecycle

Tool source and native lockfiles join the validated source fingerprint. Apply
checks TypeScript with `deno check --frozen`, prepares Python with
`uv sync --locked`, and compiles a generated Go host with native Go module
tooling. Generic TypeScript and Python hosts, their local runtime environments,
and generated Go build output live under the workspace's disposable
`.hctl/cache/tools/`; no normalized tool manifest is written.

The generated MCP command identifies its harness. At startup hctl verifies the
matching workspace apply record, selected agent identity, and source
fingerprint before loading the cached hosts. Authors write typed functions and
do not implement MCP protocol code.

## Deferred direction: proposals

Scripts created ad hoc by the agent remain ordinary harness-native workspace
activity unless a human promotes them into `tools/` and reapplies the project.

Generated project instructions may encourage the harness to submit reusable
discoveries through a future managed proposal tool. Instructions can influence
this behavior but cannot enforce it or observe native filesystem writes.

A proposal records a candidate improvement to instructions, a skill, a tool,
or other agent feedback. It does not modify active authored source or a running
harness setup. Human review and explicit acceptance are required before the
change joins the agent project and is reapplied.

Proposal schema, storage, review UX, conflict handling, sensitive-content
policy, acceptance workflow, and tool-execution isolation require a dedicated
product and security spike. They are outside the MVP and are not scaffolded.

## Failure and safety behavior

- Missing, stale, ambiguous, or edited harness setups fail closed.
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
2. Apply produces native, discoverable harness files and refuses conflicts.
3. Both generated harness setups expose the same managed MCP tool surface.
4. Both headless drivers start and resume sessions against fake harnesses.
5. Input arriving during an active turn is durably accepted and processed
   later in FIFO order.
6. Caller-provided input IDs are deduplicated.
7. Restart recovery marks unproven active work uncertain.
8. Managed audit output remains content-free.
9. A mixed TypeScript, Python, and Go project is prepared once per apply,
   exposed identically by both generated MCP configurations, and reuses one
   host process per language across calls.
10. One agent project can be applied outside its source directory; generated
    files and execution use the selected workspace while dependencies and tool
    definitions remain rooted in agent source.
11. Immediate instructions-only subagents are generated in each harness's
    native format and inherit the parent setup without duplicated child tools
    or skills.

## Explicit non-goals

- A model loop, context manager, or cross-harness chat UI
- Vendor channels or webhook delivery
- Claude Agent SDK or hosted OpenAI agent runtimes
- Scheduling, workflows, independently configured nested subagents, or
  deployment orchestration
- Building or deploying packaged agent images
- Governance claims over native harness tools
- Credential storage before a secret-bearing tool exists
- Automatic or unreviewed promotion of agent-authored improvements
