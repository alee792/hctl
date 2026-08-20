# Architecture map

A compact map of the codebase for contributors, not a spec. For the product
contract, see [the product specification](product-spec.md); for the settled
positioning, see [the vision](vision.md); for in-flight structural work, see
[the restructure plan](workbench/restructure.md).

## Modules

The repository is three Go modules, split by trust and dependency weight
rather than by feature:

- **`hctl` (root, this module's `go.mod`).** The CLI, agent-project
  compiler, dispatcher, and everything an author or operator runs directly.
  Its production dependency graph is asserted (by
  `internal/integration/dependency_boundary_test.go`) to exclude
  `hctl/discordadapter` and vendor packages such as `discordgo` and
  `go-keyring` — the root binary never links Discord's SDK or credential
  code.
- **`hctl/channeladapter`.** A zero-dependency wire-contract module: the
  closed, versioned JSONL protocol types shared between hctl and any
  external channel adapter. Both the root module and `discordadapter` import
  it; it imports nothing of its own beyond the standard library, so it
  cannot pull vendor code into the root binary.
- **`hctl/discordadapter`.** The separately built `hctl-discord` executable:
  DiscordGo, Gateway/REST transport, and the `hctl.discord` credential
  identity. The root module reaches it only as a subprocess, launched from a
  package artifact installed through `internal/integration` — never as a Go
  import. This is why the dependency-boundary test above matters: it is the
  enforcement mechanism for that separation.

## Package layering (root module)

Packages are grouped by dependency order, lower groups depending only on
groups above them. None of them are named `core`, `common`, `util`, or
`service` — each package is one concrete responsibility.

**Foundations** — depended on widely, depend on little:

- `internal/rootfs` — filesystem safety primitives: atomic owner-only
  writes, path bounding, symlink rejection.
- `internal/interaction` — transport-neutral interactive-request types
  (confirmation, choice, text, date/time, modest forms) shared by dispatch
  and channels.
- `internal/secureenv` — builds the scrubbed child-process environment that
  withholds channel credentials from harnesses, MCP servers, and tools.
- `internal/version` — validates and exposes the hctl build version string.

**Component contracts** — one type of harness component or dependency each:

- `internal/tool` — authored TypeScript/Python/Go tool discovery, native
  toolchain preparation (`deno check`, `uv sync`, Go build), and the
  per-language host runtime.
- `internal/harness` — the `Driver`/`Session` seam: the one genuinely
  polymorphic abstraction over Claude Code and Codex process protocols.
- `internal/integration` — metadata-only manifests, content-addressed
  package storage, and installation state for process-isolated integration
  packages (e.g. the Discord adapter).
- `internal/dispatchstate` — the durable JSONL-dispatch state schema (fresh
  sessions, conversation mapping); renamed from `internal/session` to avoid
  colliding with harness sessions.

**Domain model** — the hub the rest of the CLI is organized around:

- `internal/project` — loads and validates a complete portable agent
  project: instructions, skills, plugins, tools, subagents, connections,
  channels, schedules, and harness-specific files, all bounded and
  fingerprinted.

**Compilation and execution surfaces** — consume a loaded project:

- `internal/setup` — materializes a validated project as native Claude or
  Codex files in a workspace and writes/reads the apply record.
- `internal/stage` — assembles a complete OCI-neutral agent filesystem tree
  for downstream image builders.
- `internal/worktree` — private branch-backed Git worktrees promoted for a
  channel conversation that needs a workspace write.
- `internal/friction` — stores bounded, model-authored friction notes in
  private local state (opt-in via `friction-notes: true`).

**Runtime coordination** — turn-level and protocol-level machinery:

- `internal/dispatch` — the turn dispatcher: durable JSONL admission, FIFO
  per-conversation processing, capacity coordination, and hibernation.
- `internal/mcp` — the managed MCP server hctl runs for authored and
  built-in tools (`echo`, `record-friction`, tool-host functions).
- `internal/schedule` — Markdown cron-schedule parsing, validation, trigger,
  and the foreground UTC clock.

**Channel runtime** — the conversational (Discord-shaped) stack, largest and
most speculative part of the tree, marked for extraction per
[restructure.md D5](workbench/restructure.md#d5--the-channel-runtime-leaves-core):

- `internal/channel/controller` — transport-neutral correlation of
  normalized channel input with dispatcher events and semantic outcomes.
- `internal/channel/adapterhost` — process lifecycle for one external
  channel adapter subprocess (start, protocol I/O, shutdown).
- `internal/channelconfig`, `internal/channelselection` — channel control
  sentinels and non-secret profile/selection storage.

**Presentation:**

- `internal/cli` — flag parsing and command dispatch for every `hctl`
  subcommand.
- `cmd/hctl` — the `main` package; thin entry point into `internal/cli`.

## Where state lives

All of it is workspace- or user-scoped, never inside agent source:

- `.hctl/dispatch.json` (workspace) — durable turn-dispatcher state. If
  absent, an existing owner-only `.hctl/gateway.json` is migrated in place;
  when both exist, `dispatch.json` is authoritative.
- `.hctl/apply/` (workspace) — one apply record per harness (`claude.json`,
  `codex.json`), used to detect stale or hand-edited generated files.
- `.hctl/cache/tools/` (workspace) — disposable authored-tool language
  hosts, local runtime environments, and generated Go build output.
- `.hctl/plugin-data/` (workspace) — private persistent data directories for
  Plugin-declared stdio MCP servers, keyed per agent-and-plugin identity.
- User-level integration package store (OS-standard app-data directory,
  owner-only, shared across agents and workspaces) — exact manifests, raw
  platform artifacts, and verified prepared closures for installed
  integration packages such as the Discord adapter.
- User-level friction-note store (OS-standard state directory, owner-only,
  per agent ID) — opt-in friction records, capped at 256 per agent.

## Invariants

These hold across the whole tree and are worth defending in review:

- Every write to durable or generated state goes through `internal/rootfs`:
  atomic, owner-only, path-bounded. No package hand-rolls its own file
  writes to shared state.
- Filesystem, process, protocol, and model-visible inputs are validated and
  bounded before they cause a mutation (see the aggregate ceilings in
  [product-spec.md](product-spec.md#the-authored-project)).
- No generic `core`, `services`, `adapters`, or `utils` package. A package
  name says what it concretely owns.
- Generated harness files are disposable and visibly tool-owned; `apply`
  never overwrites a hand-authored file or one modified since the last
  apply.
- The native harness owns the model loop, context, native tools, approvals,
  and interactive UX. Hctl compiles and validates; it does not enforce
  model behavior, and instructions/skills influence rather than constrain
  it.
- Tests and the literal CLI journey define completion, not this document.
  Credential-free fakes exercise harness and channel-adapter protocols; live
  model calls and live Discord runs are excluded from the default suite.

## In-flight work

The channel runtime (`internal/channel/*`, `internal/worktree`, the
channel-only parts of `internal/dispatch`, and the harness continuation
wrappers) is planned for extraction into a separate module, following the
precedent already set by `discordadapter` itself; no code has moved yet. The
authoritative scope is the
[channel seam audit](workbench/channel-seam-audit.md) — notably, the
request/answer schema half of `internal/interaction` and the request-input
surface of `internal/harness` stay in core. See
[docs/workbench/restructure.md](workbench/restructure.md) (decisions D1 and
D5) for the phases and rationale.
