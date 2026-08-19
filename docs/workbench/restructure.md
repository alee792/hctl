# Restructure plan (2026-08)

- Status: active restructure charter
- Updated: 2026-08-18
- Purpose: persist the refresh decisions and their execution plan so any
  session (human or agent) can resume without chat history. This document
  supersedes the pre-restructure narrative in [status.md](status.md) where the
  two disagree.

## Why

An August 2026 review of the whole repository reached these conclusions:

1. **The effort distribution was inverted.** The differentiated product is the
   author/apply core: portable agent projects compiled into native Claude Code
   and Codex configuration, with typed TypeScript/Python/Go tool functions
   served through one managed MCP server. That core is a small fraction of the
   code. The majority — the Discord channel runtime, durable interactive
   input with per-harness continuation strategies, worktree-isolated writable
   conversations, capacity coordination, and a transactional dependency
   acquisition engine — is production-grade machinery for the speculative
   tail.
2. **The strongest engineering was misallocated.** The dispatcher and
   interaction lifecycle have real crash-correctness arguments, while
   `internal/tool` (a headline feature) and `channeladapter`'s protocol
   validator (a trust boundary) were the thinnest-tested code in the repo.
3. **The documentation served resuming agents, not the stated user.** The
   product spec is closed-schema contract prose; there is no human quickstart
   or contributor architecture map.

## Decisions

### D1 — The core is author/apply/tools/connections plus the headless dispatcher

Kept in the root module and treated as the product:

- Agent project discovery and validation (`internal/project`)
- Native harness generation and apply records (`internal/setup`)
- Authored polyglot tools and the managed MCP server (`internal/tool`,
  `internal/mcp`)
- Standalone native MCP connections and integration packages
  (`internal/integration`)
- Headless JSONL dispatch and durable conversation state
  (`internal/dispatch`, `internal/dispatchstate`)
- Markdown schedules (`internal/schedule`)
- Staged filesystems (`internal/stage`)

### D2 — The vendoring acquisition engine is removed (done)

[ADR 0036](../adr/0036-remove-the-acquisition-engine.md) removed
`internal/acquisition`, the `hctl plugin` and `hctl skill` commands, and
`hctl-dependencies.json`. Manual vendoring — copying a reviewed complete
directory beneath `plugins/` or `skills/` — is the only journey; provenance
belongs to the author's version control. Roughly 4,000 lines of engine,
hooks, CLI, and tests were deleted, and project loading no longer takes a
per-root operation lock.

### D3 — The vision is rewritten around plain-language authorship (done)

`docs/vision.md` was rewritten after a positioning review. The settled
positioning:

- **Category:** the toolchain for open, file-based agent formats (Agent
  Skills, Agent Plugins), with vendor adapters kept thin. Not agents-as-code:
  the differentiator is that an agent is a legible plain-language document,
  not another code artifact.
- **One author, one capability ladder** — no author/developer persona split.
  Composing skills and writing instructions is the first-class journey; typed
  tools are an advanced rung the author may climb directly or by asking the
  harness to draft the file. Validation proves the contract, not the
  behavior; the trust boundary stays with the author, as with any code they
  adopt.
- **Operator is a distinct role, not a persona split.** Credentials,
  integration packages, schedules, channels, and staging are operator
  concerns with their own guardrails. The operator journey is where
  portability is proven: the same folder runs interactively, headless, and
  staged.
- **Deferred deliberately:** social presence and standard stewardship come
  after the five-minute author journey is good. But conventions minted now
  (layout, frontmatter keys, the product name) are the future standard's
  vocabulary — treat naming and format decisions as expensive to reverse,
  and give the deferred product name a deadline.

### D4 — Core contracts are simplified with breaking changes

Accepted as good contracts after review: `harness.Driver`/`harness.Session`
(the only genuinely polymorphic seam), `internal/rootfs`, the validation and
bounding discipline, and the three-module split (`hctl`, `hctl/channeladapter`,
`hctl/discordadapter`).

Changes executed in this pass:

- `dispatch.NewManager(ctx, project, driver, dispatch.Config)` replaces the
  seven-constructor family; zero-value Config fields select documented
  defaults.
- Dead exports `RunSubmissionsWithTurnTimeout` and `RunTaskWithTurnTimeout`
  are deleted.
- `internal/session` is renamed `internal/dispatchstate`: it is the durable
  dispatch-state schema, and the old name collided with harness sessions and
  resident sessions. On-disk formats are unchanged.

Deliberately deferred to the channel extraction (D5), because the affected
code moves with it: splitting the channel control sentinels out of
`internal/channelconfig`; the request-input types in `internal/harness`; the
`Submission`/`dispatchstate.Input`/`harness.Input` triple (repackaging at
durable and protocol boundaries is accepted as honest layering for now).

Deferred as follow-up issues rather than blocking the restructure: a handler
map replacing the managed-tool if-chain in `internal/mcp`; recovering
diagnosability lost to fixed-sentence error opacity (attach bounded detail to
operator-facing surfaces without leaking it to model-visible output);
splitting `project.go` (2,200 lines) and `cli.go` (1,300+ lines) by
responsibility.

### D5 — The channel runtime leaves core

**Phase 1 (seam audit) is complete:**
[channel-seam-audit.md](channel-seam-audit.md) is now the authoritative scope
document and makes five corrections to the planned move list below — most notably: the
request/answer schema half of `internal/interaction` stays in core (both
drivers and the MCP server consume it), the request-input surface of
`internal/harness` stays in core (it is emitted inside both drivers' ordinary
turn loops; only the continuation wrappers move), and the
`hook claude-deferred-input` entrypoint stays in the root binary. The audit
also recommends one core-owned state file with an advisory lock, a separate
operator-facing channel binary that depends on core as a library (with a
non-`internal/` seam surface and an import guard), and landing the dispatch
actor-mailbox rework *before* the module move. Where this section and the
audit disagree, the audit wins.

The conversational channel stack is a coherent second product and will be
extracted following the `discordadapter` precedent (separate module, wire
contract inward, no imports from core into it). Planned scope to move:

- `internal/channel/controller` and `internal/channel/adapterhost`
- `internal/interaction` (transport-neutral interactive requests)
- `internal/worktree` (writable-conversation promotion is a channel feature)
- The channel-only parts of `internal/dispatch`: capacity coordination,
  hibernation, interaction continuation scheduling, `RequestInputHandler`
- The request-input surface of `internal/harness` (`RequestInputEvent`,
  deferred-tool and continuation-turn driver interfaces) and the
  per-harness continuation strategies in `harness/claude` and `harness/codex`
- The interaction lifecycle records of `internal/dispatchstate`
- `hctl run --input channels`, `hctl channel ...` CLI surface
- The `channeladapter` and `discordadapter` modules (already separate)

Extraction phases:

1. **Seam audit.** Enumerate exactly which dispatch/manager and dispatchstate
   fields exist only for channels; define the narrow interfaces the extracted
   module needs from core (project load, setup verify, driver open).
2. **Module split.** Move the packages into a `channel/`-rooted module wired
   the same way `discordadapter` is; core keeps JSONL dispatch, schedules, and
   fresh-session tasks only. The dispatch actor-mailbox rework (replacing the
   `Manager.Submit` multi-lock admission protocol) lands here, where the
   complexity lives.
3. **Docs split.** Channel, interaction, worktree, and capacity sections move
   from the product spec into a channel-runtime spec; core spec shrinks
   accordingly.

Non-goal: deleting the channel runtime. Its acceptance evidence (live Discord
pass, interactive-input records) stays valid; it simply stops taxing core.

### D6 — Documentation is rewritten for the author first

- README: plain-language pitch plus a five-minute quickstart
  (`apply` → `claude`/`codex`) written for the author persona; the tool,
  connection, and operator journeys follow clearly second.
- Author-grade diagnostics are a tracked workstream, not polish: validation
  errors are read by plain-language authors and by harnesses self-correcting
  AI-drafted files, so they must stay exact, bounded, and legible to both.
- A one-page contributor architecture map (the package layering is clean but
  undocumented).
- The product spec stays the precise contract, but shrinks with D5's split.

### D7 — Tests are rebalanced toward the core and trust boundaries

Priority order: `internal/tool` (0.30 test:source; venv-relocation staging
especially), `channeladapter/validate.go` (protocol trust boundary at 0.19
package ratio), race/parallel stress for the dispatch concurrency core (moves
with D5 phase 2), splitting the 3,100-line `manager_test.go`.

### D8 — The issue board restarts from this plan

All pre-restructure open issues are closed with a pointer here. The live board
is: one restructure epic, one issue per phase/deferred item above, plus the
carried-over Claude acceptance-parity work (live Claude acceptance and the
Claude harness image both remain blocked on human authorization and are core
concerns, not channel concerns).

## Sequencing

1. ✅ Vendoring excision (D2), vision fix (D3), first contract pass (D4),
   board reset (D8).
2. Channel extraction seam audit → module split → docs split (D5), carrying
   the dispatch rework and deferred D4 items that move with it.
3. Documentation rewrite (D6) once the core surface is stable after D5.
4. Test rebalance (D7) alongside 2–3; `internal/tool` coverage need not wait.

## Invariants that do not change

- The native harness owns the model loop, context, native tools, approvals,
  and interactive UX. Hctl compiles and validates; it does not enforce model
  behavior.
- Generated files are disposable, visibly tool-owned, and never overwrite
  hand-authored files.
- Authored source is bounded and validated before workspace mutation.
- Credential-free tests define completion; live/credentialed actions require
  explicit human authorization.
