# Channel extraction seam audit

- Status: phase 1 of the [restructure plan](restructure.md) decision D5
- Updated: 2026-08-18
- Scope: analysis only. No Go code changed.

## Purpose

D5 extracts the conversational channel runtime from the root `hctl` module. This
document enumerates, at field and function granularity, which parts of
`internal/dispatch`, `internal/dispatchstate`, `internal/interaction`,
`internal/harness`, `internal/mcp`, and `internal/cli` exist only for channels;
defines the narrow interfaces the extracted module would need from core; and
recommends the two structural decisions the split cannot avoid — where durable
state lives afterwards, and whether the channel runtime ships as its own
executable.

Every load-bearing claim cites `file:line` in the tree at commit `914d4b2`.
Where this audit contradicts D5's planned scope, the code is treated as
authoritative and the discrepancy is called out under
[Corrections to D5](#corrections-to-d5).

Classification vocabulary used in the tables:

- **CORE** — reachable from `hctl run --input jsonl`, `hctl schedule trigger`,
  `hctl schedule run`, `hctl apply`, `hctl stage`, or `hctl mcp serve` without
  any channel construct.
- **CHANNEL** — reachable only from `internal/channel/controller`,
  `internal/channel/adapterhost`, or the `hctl channel` CLI.
- **SHARED** — reachable from both. These are the seams the split must cut or
  duplicate.

## Corrections to D5

Five points where the code disagrees with the planned scope in
[restructure.md](restructure.md) lines 115–132.

1. **`internal/interaction` cannot move whole.** Core's Codex driver builds its
   dynamic tool schema from `interaction.RequestJSONSchema()`
   (`internal/harness/codex/codex.go:121`) and decodes with
   `interaction.DecodeRequest` (`internal/harness/codex/codex.go:367`); core's
   managed MCP server does the same (`internal/mcp/request_input.go:14`,
   `internal/mcp/server.go:221`, `internal/mcp/server.go:225`). Only the
   *lifecycle* half (`coordinator.go`, `lifecycle.go`) is channel-only. See
   [1.3](#13-internalinteraction).
2. **The request-input surface of `internal/harness` cannot move whole.**
   `harness.Event.RequestInput` is emitted from inside both drivers' ordinary
   turn loops (`internal/harness/claude/claude.go:212`,
   `internal/harness/codex/codex.go:375`), interleaved with plain turn driving.
   Moving `RequestInputEvent` would require either duplicating each vendor's
   stream protocol or introducing a driver extension point. See
   [1.4](#14-internalharness).
3. **`hctl hook claude-deferred-input` is core-side, not channel-side.**
   `claude.RunDeferredHook` (`internal/harness/claude/deferred.go:235`) is a
   6-line proxy to the broker socket created by `Driver.Open`
   (`internal/harness/claude/claude.go:83`). The broker lives inside the core
   driver process, so the hook must be served by whatever binary opened the
   Claude session. No packaging change is required. See
   [3](#binary-and-cli-shape-decision).
4. **`internal/dispatch.Manager` has no core consumer at all.** Its only
   non-test caller is `controller.New` (`internal/channel/controller/controller.go:210`).
   The channel-only fraction of `internal/dispatch` is therefore much larger
   than "capacity coordination, hibernation, interaction continuation
   scheduling, `RequestInputHandler`" — it is all of `manager.go` plus most of
   `state_store.go`.
5. **`internal/interaction/text.go` is production-dead in the root module.**
   `TextInstructions`, `TextFallback`, and `ParseTextAnswer` have no non-test
   caller anywhere under `internal/` or `cmd/`. The vendor adapter renders its
   own fallback from the protocol field `fallback_text`
   (`channeladapter/types.go:372`, `discordadapter/rendering.go:175`). 213 lines
   should be deleted, not moved.

## Inventory

### 1.1 `internal/dispatch`

`internal/dispatch` is 3,217 source lines across six files plus 4,927 test
lines. Two entirely separate runtimes live in it: the stateless
`Run`/`RunSubmissions`/`RunTask` path used by JSONL and schedules, and the
long-lived `Manager` used only by channels. They share exactly one loop
(`runSubmissions`) and one store type (`conversationStore`).

#### `dispatch.go` (728 lines)

| Symbol | Line | Class | Note |
| --- | --- | --- | --- |
| `Submission`, `SubmissionResult` | 36, 45 | SHARED | `Reply chan<-` (39) is used only by `Manager.Submit`; JSONL and task paths leave it nil or set it locally |
| `Event`, `Event.Terminal` | 73, 89 | SHARED | Event type set is identical for both paths except `interaction.parked` (525) |
| `eventSink` | 98 | SHARED | |
| `Run` | 118 | CORE | `hctl run --input jsonl` only (`internal/cli/cli.go:1171`) |
| `RunSubmissions` | 128 | CORE | opens its own store, `policy: PolicyDefault`, no capacity |
| `RunTask`, `runTask` | 142, 146 | CORE | schedules (`internal/schedule/schedule.go:92`) |
| `runOptions.freshSessions` | 161 | CORE | tasks only |
| `runOptions.turnTimeout` | 162 | SHARED | |
| `runOptions.idleTimeout` | 163 | CHANNEL | zero on both core paths; gates `driver.process_opened` (286) and idle hibernation (304, 345) |
| `runOptions.timers` | 164 | SHARED | task deadline (146) and channel hibernation both need it |
| `runOptions.policy` | 165 | SHARED | core always `PolicyDefault`; channel uses `PolicyReadOnly`/`PolicyWorkspaceWrite` (`manager.go:1111`, `1118`) |
| `runOptions.store` | 166 | SHARED | the state-ownership seam |
| `runOptions.capacity` | 167 | SHARED | `TaskRuntime` passes one (`task_runtime.go:112`), but with `residentLimit == activeLimit` |
| `runOptions.forceHibernate` | 168 | SHARED-in-name, CHANNEL-in-effect | `TaskRuntime` wires it (`task_runtime.go:113`) but tasks always pass `needsResident=true` (241), so `errCapacityHibernation` (`capacity.go:92`) is unreachable for tasks |
| `runOptions.wake` | 169 | CHANNEL | only `Manager` supplies it (`manager.go:1148`); drives `case <-wake` (387) |
| `runOptions.requestInputs` | 170 | CHANNEL | only `Manager` supplies it (`manager.go:1131`) |
| `runOptions.taskDeadline` | 171 | CORE | |
| `runSubmissions` | 174 | SHARED | the one genuinely shared loop |
| — hibernation branches | 242–267, 345–364, 365–386 | CHANNEL | all gated on `capacity != nil && idleTimeout > 0` or on `forceHibernate` |
| — `handleRequestInput` call | 439–449 | CHANNEL | |
| — `waiting_for_input` parking | 490–528 | CHANNEL | emits `driver.process_hibernated` + `interaction.parked` |
| `projectSessionDriver`, `openProjectSession` | 574, 578 | SHARED | the CLI's apply-before-open decorator seam |
| `closeHarness` | 585 | SHARED | |
| `readInput`, `validateInput` | 619, 650 | CORE | JSONL transport |
| `runTurn`, `runHarnessTurn` | 666, 703 | SHARED | |
| `ValidateConversation`, `ValidateInputID` | 716, 723 | SHARED | used by CLI (1151) and schedules (`internal/schedule/schedule.go:44`) |

#### `manager.go` (1,227 lines) — CHANNEL in full

Every exported symbol in this file is reached only through
`internal/channel/controller`. The `Manager` interface the controller declares
(`internal/channel/controller/controller.go:102-111`) plus its
`interactionManager` interface (67-74) together enumerate the whole
channel-facing surface.

| Symbol | Line | Class | Note |
| --- | --- | --- | --- |
| `ErrManagerClosed`, `ErrConversationBusy`, `ErrWaitingForInput` | 17-19 | CHANNEL | `ErrWaitingForInput` is returned from `state_store.go:393` and never inspected outside dispatch |
| `DefaultIdleTimeout` | 22 | CHANNEL | CLI default for `--idle-timeout` (`internal/cli/cli.go:1139`), a channels-only flag |
| `Lifecycle` + six constants | 26-35 | CHANNEL | `LifecycleWaiting` leaks into `dispatch.go:426`/`490` as a status string |
| `ConversationStatus` | 37 | CHANNEL | |
| `Manager` struct fields | 45-75 | CHANNEL | `capacity` (51), `workspaces` (52), `diagnostics`/`diagnosticSink` (70-71), `requestInputFactory` (72), `interactionReady` (73), `expiryStops` (74) are all channel concepts |
| `ConfigureRequestInput` | 79 | CHANNEL | pre-admission binding, rejects post-admission calls (85) |
| `ConfigureInteractionReady` | 92 | CHANNEL | |
| `ConfigureDiagnosticSink` | 107 | CHANNEL | the only consumer is `controller.New` (`controller.go:182`) writing to the audit stream |
| `WorkspaceProvider` | 120 | CHANNEL | implemented only by `worktree.Manager` |
| `WorkspaceReconciler` | 126 | CHANNEL | |
| `Config` (seven-through-one constructor) | 146-154 | CHANNEL | `Emit func(string, Event) error` is per-conversation, unlike core's `func(Event) error`; `Workspaces` and `Configure` are channel-only |
| `NewManager` | 156 | CHANNEL | sole caller `controller.go:210` |
| `newManager`, `newManagerConfigured` | 169, 173 | CHANNEL | `newManager` is test-only (`manager_test.go:660` and 8 more) |
| startup recovery: interaction expiry (219-240), runnable workers (241-249), continuation recovery (250-263) | | CHANNEL | |
| `Elevate` | 267 | CHANNEL | write-access promotion |
| `Submit` multi-lock admission | 352-492 | CHANNEL | four `m.mu` acquisitions plus `worker.submitMu`; the rework target named in D5 phase 2 |
| `Status` | 494 | CHANNEL | |
| `InteractionContinuation` | 533 | CHANNEL | type-asserts `harness.ContinuationTurnDriver` / `NativeDeferredToolDriver` (537-538) |
| `NewInteractionCoordinator` | 548 | CHANNEL | |
| `ScheduleInteractionExpiry` / `CancelInteractionExpiry` / `ScheduleInteractionResume` | 556, 612, 623 | CHANNEL | |
| `managerContinuation` + `projectContinuationTurnDriver` / `projectDeferredToolDriver` | 675-789 | CHANNEL | 100 lines that re-implement capacity acquisition, workspace resolve, and event emission outside the worker loop |
| `Capacity` | 801 | CHANNEL | |
| `wakeInteraction` | 816 | CHANNEL | |
| `Diagnostics`, `recordDiagnostic` | 838, 844 | CHANNEL | bounded ring of 32 (`24`) |
| `reconcileWorkspaces` | 859 | CHANNEL | worktree retirement on startup |
| `stopIdleWorker`, `Reset` | 942, 989 | CHANNEL | |
| `run` (worker body) | 1109 | CHANNEL | constructs the channel `runOptions` at 1145-1149 |
| `failDispatchEventDelivery` | 1168 | CHANNEL | runtime-wide fatal boundary |

#### `capacity.go` (269 lines)

| Symbol | Line | Class | Note |
| --- | --- | --- | --- |
| `DefaultMaxResidentSessions`, `DefaultMaxActiveTurns` | 12-13 | SHARED | CLI uses both for `run` (`cli.go:1140-1141`) and `DefaultMaxActiveTurns` for `schedule run` (`cli.go:551`) |
| `CapacityStatus` | 16 | CHANNEL | only surfaced through `Manager.Capacity` → `controller.Status` → `channeladapter.RuntimeStatus` (`adapterhost/host.go:607`) |
| `capacityCoordinator`, `register`, `acquireTurn`, `releaseTurn`, `releaseResident`, `unregister`, `shutdown` | 24-187 | SHARED | `TaskRuntime` uses admission fairness (`task_runtime.go:57`, `91`, `104`, `133`) |
| `errCapacityHibernation`, `capacityState.hibernating`, `hibernate` channel | 9, 41, 43 | CHANNEL | tasks never reach the hibernation return (see `dispatch.go:241` — tasks always request residency) |
| `scheduleLocked` eviction, `oldestIdleLocked`, `oldestQueuedResidentLocked`, `waitingForResidentLocked` | 189-269 | CHANNEL-in-effect | `TaskRuntime` constructs the coordinator with `residentLimit == activeLimit` (`task_runtime.go:57`), so the eviction branch (200-213) is unreachable for tasks |

The honest split is: **admission counting is SHARED, eviction/hibernation is
CHANNEL.** A task runtime needs roughly 60 of the 269 lines.

#### `state_store.go` (756 lines)

| Symbol | Line | Class | Note |
| --- | --- | --- | --- |
| `conversationStore`, `conversationRef`, `openConversationStore` | 23, 30, 58 | SHARED | three independent instances exist in a process (`dispatch.go:132`, `task_runtime.go:53`, `manager.go:190`) |
| `conversationSnapshot` | 37 | SHARED | fields `workspace`, `branch`, `retiring` (44-46) are CHANNEL; `waitingForInput` (41) is CHANNEL |
| `workspaceRecord` | 49 | CHANNEL | |
| `recover` | 68 | CORE | |
| `recoverTask`, `terminalizeTask` | 88, 103 | CORE | |
| `snapshot` | 127 | SHARED | |
| `queued` | 145 | CHANNEL | only `Manager.Capacity` (`manager.go:802`) |
| `runnable`, `interactionConversations`, `recoverInteractionContinuations` | 163, 177, 193 | CHANNEL | |
| `workspaceRecords`, `markWorkspaceRetiring`, `clearRetiredWorkspace` | 218, 253, 266 | CHANNEL | |
| `inputStatus` | 284 | CHANNEL | only `Manager.Submit` (`manager.go:375`) |
| `outcomeReason` | 302 | CORE | JSONL duplicate reporting (`dispatch.go:418`) |
| `assignWorkspaceAndAccept` | 312 | CHANNEL | |
| `lookup`, `accept`, `startNext`, `setSessionID`, `complete`, `completeWithReason` | 344, 359, 383, 404, 420, 424 | SHARED | `startNext` carries channel logic at 392-398 (`ErrWaitingForInput`, `InteractionWakePending`) |
| `reset` | 449 | CHANNEL | |
| `loadInteraction`, `openInteraction`, `updateInteraction`, `finishInteraction` | 469, 479, 514, 547 | CHANNEL | `finishInteraction` alone is 88 lines of tombstone and phase-transition logic |
| `boundInteractionStore` + `interactionStore` / `interactionStoreWithWake` | 636-670 | CHANNEL | this is the `interaction.Store` implementation |
| `persistMutation`, `persistMutationIfChanged`, `cloneSessionState` | 672, 688, 730 | SHARED | copy-on-write via JSON round trip |
| `conversationAdmissionStatus` | 720 | SHARED | returns `LifecycleWaiting` (725) — a channel concept in a shared helper |

Roughly 55% of `state_store.go` by line count is interaction or worktree
bookkeeping.

#### `task_runtime.go` (134 lines) — CORE in full

`TaskRuntime` (16), `Recover` (31), `NewTaskRuntime` (39), `Run` (69), `Start`
(80), `StopAdmission` (122), `Wait` (128), `Close` (130). Used by
`internal/schedule` (`schedule.go:85`) and `hctl schedule run`
(`internal/cli/cli.go:589`). Its only channel coupling is the shared
`capacityCoordinator` and `conversationStore`.

#### `request_input.go` (103 lines) — CHANNEL in full

`ErrRequestInputUnavailable` (13), `RequestInputContext` (15),
`RequestInputHandler` (23), `interactionRequester` (27),
`CoordinatorRequestInputHandler` (33), `handleRequestInput` (69).
`ErrRequestInputUnavailable` leaks two packages outward — into
`adapterhost/host.go:1077` and `controller.go:306`.

### 1.2 `internal/dispatchstate`

One durable file, `.hctl/dispatch.json`, owner-only mode `0600`, schema version
4, 1 MiB cap (`dispatchstate.go:18-25`, `99-101`, `243-253`). No package outside
`internal/dispatch` imports it in production.

| Field / method | Line | Class |
| --- | --- | --- |
| `State.SchemaVersion`, `State.Conversations` | 28-29 | SHARED |
| `Conversation.ID/AgentID/Harness/SourceFingerprint/SessionID` | 33-38 | SHARED |
| `Conversation.LegacyManifestFingerprint` | 37 | SHARED (migration) |
| `Conversation.WorkspaceRoot`, `WorktreeBranch`, `WorktreeRetiring` | 39-41 | CHANNEL |
| `Conversation.Queue`, `Outcomes`, `OutcomeOrder`, `OutcomeReasons` | 42-45 | SHARED |
| `Conversation.Interaction`, `InteractionTombstones`, `InteractionWakePending` | 46-48 | CHANNEL |
| `Input.Status == "parked"` | 60 | CHANNEL |
| `Load`, `Save`, `decode`, `rejectDuplicateJSONKeys` | 67, 243, 98, 190 | SHARED |
| decode validation for interaction state | 133-135, 139-141, 162-171 | CHANNEL |
| decode validation for worktree assignment | 145-147 | CHANNEL |
| `GetOrCreate`, `Reset`, `conversationKey` | 255, 285, 300 | SHARED |
| `ResetLifecycle` | 291 | CHANNEL (only reached when `WorkspaceRoot != ""`, `state_store.go:460`) |
| `Accept`, `StartNext`, `Complete`, `CompleteWithReason`, `remember`, `OutcomeReason` | 304, 320, 331, 335, 413, 409 | SHARED |
| `Park`, `CompleteParked` | 349, 359 | CHANNEL |
| `RecoverUncertain` | 368 | SHARED |
| `RecoverTaskUncertain` | 386 | CORE (and it *rejects* parked input at 389-391) |

Schema versions carry the channel history: version 3 gates interaction records
(`dispatchstate.go:133`), version 4 gates outcome reasons (136). Both survived
the split of `internal/session` into `internal/dispatchstate` under D4 without
changing on-disk bytes.

### 1.3 `internal/interaction`

The package has two halves with no cyclic dependency between them.

| File | Lines | Class | Note |
| --- | --- | --- | --- |
| `interaction.go` | 716 | SHARED | `Request`/`Answer`/`Field`/`Option` schema, `DecodeRequest` (192), `ValidateRequest` (284), `NormalizeAnswer` (407), `Capabilities` (136), `Resolve` (558). Used by core's Codex driver, core's Claude deferred encoder, and core's MCP server |
| `schema.go` | 183 | SHARED | `RequestJSONSchema` (8) is the tool input schema advertised by `internal/mcp/request_input.go:14` and `internal/harness/codex/codex.go:121` |
| `text.go` | 213 | DEAD | no production caller (see [Corrections to D5](#corrections-to-d5) item 5) |
| `lifecycle.go` | 274 | CHANNEL | `Lifecycle` (77), `Tombstone` (96), `DurableState` (104), `FinishRequest` (111), `Digest` (236) |
| `coordinator.go` | 435 | CHANNEL | `Store` (27), `Coordinator` (116), `Renderer` (57), `Continuation` (77), `RenderIntent`/`ContinuationIntent`/`ContinuationResult` (34, 61, 70), `PendingInteraction` (45) |

`ContinuationIntent` and `ContinuationResult` are referenced from core's
`internal/harness/harness.go:122` and `:128` only because the two continuation
driver *interfaces* live there; they move together.

### 1.4 `internal/harness`

| Symbol | Line | Class | Note |
| --- | --- | --- | --- |
| `Input`, `TurnResult`, `Session`, `Driver`, `ExecutionPolicy`, `ResolveExecutable` | 13, 81, 131, 112, 87, 138 | CORE | the polymorphic seam D4 accepted |
| `Event` | 18 | CORE | except field `RequestInput` (27) |
| `Event.RequestInput` / `RequestInputEvent` | 27, 30 | SHARED (structurally CORE) | emitted from inside both drivers' turn loops; see below |
| `RequestInputAcknowledgement`, `RequestInputToolResult`, `RequestInputDisposition` | 38, 47, 51 | SHARED | `RequestInputContinuationTurn` is compared in `controller.go:550` and `codex.go:378` |
| `NewRootRequestInputEvent`, `NewDeferredRootRequestInputEvent`, `ProvenRoot` | 67, 73, 79 | SHARED | the root-ancestry proof consumed at `dispatch/request_input.go:76` |
| `OpenRequest.ManagedRequestInput` | 99 | SHARED | set true only by `dispatch.go:275` when a channel handler exists |
| `OpenRequest.Deferred`, `DeferredToolResume` | 100, 105 | CHANNEL | set only by `claude/continuation.go:24` |
| `ContinuationTurnDriver` | 121 | CHANNEL | |
| `NativeDeferredToolDriver` | 127 | CHANNEL | |
| `process.go` (`StartProcessWithPolicy*`) | — | CORE | |

**Why the request-input surface cannot leave core.** The Claude driver starts
the deferred broker inside `Open` (`claude.go:82-88`), consults it inside
`RunTurn` (`claude.go:204`, `249-255`), and emits the request-input event from
the middle of the stream-JSON result branch (`claude.go:200-222`). The Codex
driver registers its dynamic tool inside `Open` (`codex.go:77-78`, `114-124`)
and emits from `handleDynamicTool` (`codex.go:375`), which is dispatched from
the ordinary event loop (`codex.go:311`). There is no cut line that does not
either duplicate the vendor stream protocols or add a driver extension point —
and an extension point registry is exactly the generic layer CLAUDE.md forbids.

**What *can* move from the drivers.** Both continuation strategies are thin and
use only exported driver API:

| Unit | File | Lines | Uses only |
| --- | --- | --- | --- |
| Claude deferred-tool resume | `harness/claude/continuation.go` | 56 | `Driver.Open`, `BuildDeferredUpdatedInput`, `ManagedRequestInputTool`, `Err*` sentinels |
| Codex continuation turn | `harness/codex/continuation.go` | 98 | `Driver.Open`, `Session.RunTurn/Close/Abort`, `interaction.*` |

Both can be re-expressed in the channel module as wrapper types over the
core driver (`type claudeDeferred struct{ *claude.Driver }`), leaving core's
drivers untouched. `broker.go` (270) and `deferred.go` (289) must stay with
`claude.go` — the broker is created and consumed by `Open`/`RunTurn`, and
`RequestDigest`/`BuildDeferredUpdatedInput` are used by both sides.

**The `hctl hook claude-deferred-input` entrypoint** (`internal/cli/cli.go:443-449`)
calls `claude.RunDeferredHook` (`deferred.go:235`), which dials the Unix socket
named by `HCTL_CLAUDE_DEFERRED_BROKER` (`deferred.go:18`, `broker.go:237`). The
socket is created by the process that opened the Claude session
(`claude.go:83-87`). Generated hook config bakes an absolute path to the running
hctl binary (`internal/setup/setup.go:408`, `415`) and is written only when
`p.DiscordChannel != nil` (`setup.go:407`). Because the broker lives in the core
driver, the hook stays in the core binary and the generated config keeps
pointing at `hctl`. **No binary packaging change is implied by the hook.**

### 1.5 `internal/mcp`

The request-input gating in the managed MCP server is smaller than it looks and
is entirely Claude-broker-shaped.

| Symbol | Line | Class | Note |
| --- | --- | --- | --- |
| `requestInputToolName` + `requestInputDefinition` | `request_input.go:8`, `:10` | CHANNEL | advertises `channel.request_input` |
| `requestInputRuntime` interface | `server.go:80` | TEST-ONLY | production callers pass `nil` (`server.go:74`); only `request_input_test.go` supplies one |
| `requestInputAvailable` | `server.go:87` | SHARED | in production it reduces to `claude.DeferredBrokerAvailable(os.Getenv(claude.DeferredBrokerEnv))` (89) |
| `tools/list` gating | `server.go:143-145` | CHANNEL | |
| broker result branch in `tools/call` | `server.go:202-217` | CHANNEL | delegates to `claude.RequestDeferredBrokerResult` |
| bridge branch in `tools/call` | `server.go:218-244` | TEST-ONLY | unreachable in production since `requests` is always nil |
| audit correlation carve-out | `server.go:188-194` | CHANNEL | request bytes must not influence the audit id |
| read-only policy gate | `server.go:62` | SHARED | `HCTL_EXECUTION_POLICY=read-only` disables the authored-tool runtime — set only by the channel path via `harness.PolicyReadOnly` |

**Recommended seam.** Delete the dead `requestInputRuntime` bridge (interface,
both branches, `serveRequestsWithInput`, `callManagedWithInput`) and keep a
single concrete predicate: the managed server advertises `channel.request_input`
if and only if a Claude deferred broker answers on
`HCTL_CLAUDE_DEFERRED_BROKER`. That leaves core's MCP server with ~35 lines of
channel-aware code, no interface, and no knowledge of the channel module. The
tool schema stays in core because `interaction.RequestJSONSchema` does.

### 1.6 `internal/cli`

| Unit | Line | Class |
| --- | --- | --- |
| `run` flags `--input`, `--profile`, `--config`, `--idle-timeout`, `--max-resident-sessions`, `--max-active-turns` | 1133-1141 | CHANNEL (except `--max-active-turns`, shared with `schedule run` at 551) |
| `--input jsonl` branch | 1166-1171 | CORE |
| "no configured channels" error | 1172-1173 | CHANNEL |
| channel adapter resolve → `adapterhost.New` → `Run` | 1174-1204 | CHANNEL |
| `runChannel` / `runChannelContext` | 701, 707 | CHANNEL |
| `selectedAdapterProfile` | 787 | CHANNEL |
| `discordAdapterRemedy` | 835 | CHANNEL |
| `resolveChannelAdapter` | 839 | SHARED — also called from `runApply` (960) |
| `recordChannelConsumption` | 858 | SHARED — called from `runApply` (993) and `ensureAppliedForPolicyContext` (1253) |
| `verifyChannelConsumption` | 872 | CHANNEL |
| `stagedChannelAdapterPath` | 891 | SHARED — used by `resolveChannelAdapter` |
| `currentSetupDriver` | 614 | CORE — also wraps the schedule-clock driver (588) |
| `currentContinuationDriver`, `currentDeferredDriver`, `currentContinuationDeferredDriver` | 622, 635, 653 | CHANNEL — only `Manager.managerContinuation` type-asserts them (`manager.go:697`, `709`) |
| `newCurrentSetupDriver` switch | 669-684 | SHARED shape, CHANNEL payload |
| `runHook` | 443 | CORE (see 1.4) |
| `ensureAppliedForPolicyContext` writable branch | 1226-1246 | CHANNEL — `PolicyWorkspaceWrite` is only ever passed from the continuation decorators |

`internal/cli/cli.go` currently imports `channel/adapterhost`, `channelconfig`,
`channelselection`, `dispatch`, and `interaction` (17-24). After the split it
should import none of the first three.

### 1.7 Other couplings

**`internal/channelconfig` (85 lines).** Two unrelated things in one package:

- The three control sentinels `NoReplyResult`, `RequestWriteAccessResult`,
  `WriteContinuationPrompt` (`config.go:15-17`). Consumed by
  `internal/channel/controller/controller.go` (637, 654, 659, 670, 701-704, 728)
  — and by `internal/setup/setup.go:370` and `:378`, which bake the literal
  strings into generated `CLAUDE.md`/`AGENTS.md`.
- `ProfileSelection` / `SelectedPath` / `LoadProfileSelection` (25-48), legacy
  TOML profile selection, consumed only by `internal/cli/cli.go:814-818`.

**The setup coupling is the sharpest seam in the whole audit.** Generated
instructions are produced by `filesForPolicyWithNativeMCP`
(`setup.go:345`) during core's `hctl apply`, and the entire Discord section
(`setup.go:369-381`) is channel semantics: participation policy, the two control
sentinels, `channel.request_input` usage guidance, the Codex
`hctl.channel_input_answer` envelope contract, and the read-only/writable
distinction. Core cannot delegate that text to the channel module without
either (a) importing it, or (b) making generated-instruction content
pluggable — both forbidden. See [decision D-6](#decision-list).

**`internal/channelselection` (176 lines).** Agent→channel→profile mapping in
`$XDG_CONFIG_HOME/hctl/channel-selections.json`. Consumed only by
`internal/cli/cli.go` (745, 751, 755, 800, 804). CHANNEL in full.

**`internal/secureenv`.** SHARED infrastructure — used by worktree (415, 425),
both harness drivers, `internal/stage` (120, 706), `internal/tool` (51, 251),
and `adapterhost/process.go:61`. Stays in core; the channel module imports it.

**`internal/integration` channel-adapter capability resolution.**
`ChannelAdapterMode` (`resolved.go:51`), `ChannelAdapterLaunchDescriptor` (60),
`ResolveChannelAdapter` (159), `LaunchDescriptor` (228), the manifest types
(`manifest.go:213-267`), `staged_channel_adapter.go` in full, and
`ValidateChannelProfileID` (`manifest.go:98`). This is metadata-only package
resolution with no channel runtime behavior; it is used by `hctl apply` (960),
`hctl stage` (`stage.go:169`), and consumption recording (858). **Recommend it
stays in core** — moving it would force `apply` and `stage` to shell out.

**`internal/worktree` (435 lines).** Depends on `setup` (116, 158, 170, 182,
190, 307, 310, 318), `project` (329), `tool` (315), `integration` (300), and
`secureenv`. Imported only by `dispatch/manager.go`, `dispatch/state_store.go`,
and `controller.go`. CHANNEL in full, but it is the package that most demands a
stable core seam: it calls five distinct `setup` entry points, including three
(`WritableChannelFiles`, `WritableChannelRetirementFiles`,
`RemoveWritableChannel`) that exist *only* for it.

**`internal/stage`.** `finalChannelAdapterDescriptor` (32), channel resolution
(142, 169), descriptor emission (296, 300), and the launcher export of
`HCTL_CHANNEL_ADAPTER_DESCRIPTOR` (569). Staging a channel-capable agent stays a
core concern because staging composes the whole closure.

**`internal/project`.** `DiscordChannel` type (161), field (212), load (338),
`loadDiscordChannel` (573), `parseDiscordChannel` (606), and the source record
contribution (380-382). Core keeps this: `channels/discord.md` participates in
the project source fingerprint, and `hctl apply` and `hctl stage` both branch on
it (`cli.go:957`, `cli.go:1097`).

## State ownership decision

### The constraint

`.hctl/dispatch.json` holds core and channel state in one document with one
schema version. Its sole-writer discipline is enforced only *within* a process:
`conversationStore` serializes on `s.mu` and rewrites the whole file through
`rootfs.WriteAtomic` (`state_store.go:66`, `dispatchstate.go:252`). Three
independent stores can already exist per process
(`dispatch.go:132`, `task_runtime.go:53`, `manager.go:190`); today they never
run concurrently on the same workspace because each CLI verb constructs exactly
one. There is no file lock on the dispatch state. The only cross-process lock in
the repository is `schedule.AcquireRuntimeLock` (`internal/schedule/lock.go:17`),
an advisory `flock` keyed by workspace+agent+harness.

Any split that puts the channel runtime in a second process therefore breaks
whole-file rewriting: a channel process writing an interaction commit would
clobber a concurrent `hctl schedule run` task outcome, and vice versa.

### Options

**(A) One file, channel module writes through a core-owned store.** Core exports
a store handle; the channel module calls it. Preserves the current durable
schema and the single atomic commit that ties `CompleteParked` to interaction
finish (`state_store.go:617-631`) — a real crash-correctness property. Requires
either same-process operation, or a cross-process lock plus reload-before-write.

**(B) Two files.** `.hctl/dispatch.json` for queue/outcomes/session,
`.hctl/channel.json` for interaction lifecycle, tombstones, and worktree
assignment. Restores sole-writer per file, but destroys the atomicity of
`finishInteraction`: completing a parked input and clearing the interaction
would become two writes across two files, exactly the crash window ADR 0021
exists to close. It also splits the parked-queue invariant
(`dispatchstate.go:162-171`) across files.

**(C) Channel module owns the file, core depends on it.** Inverts the dependency
D5 requires. Rejected outright.

### Recommendation: (A), one file, core-owned, with a cross-process advisory lock

Keep `.hctl/dispatch.json` and `internal/dispatchstate` in core, including the
`Interaction*` and `Worktree*` fields and the schema-4 validation for them. Core
exposes one narrow store seam (see
[`dispatchstate.Store`](#seam-3-durable-dispatch-state)); the channel module
implements `interaction.Store` on top of it rather than owning the file.

Rationale:

1. The single-transaction guarantee at `state_store.go:617-631` (complete the
   parked input, append the tombstone, clear the interaction, set the wake flag —
   or none of them) is the most valuable correctness property in the channel
   stack. Options B and C both weaken it.
2. The durable schema is *data*, not behavior. A core package that validates a
   schema containing channel fields costs core ~90 lines of validation
   (`dispatchstate.go:133-171`) and no runtime coupling. The 1,200-line
   `Manager` is what actually taxes core, and it moves regardless.
3. Migration risk is zero: on-disk bytes are unchanged, matching how D4 renamed
   `internal/session` without touching the format.
4. The lock has precedent. Generalize `schedule.AcquireRuntimeLock`
   (`internal/schedule/lock.go:17`) into a dispatch-state lock keyed by the same
   workspace+agent+harness digest, acquired by any process that opens a
   `conversationStore` for writing. Combined with reload-on-acquire this makes
   two-process operation safe without changing the file format.

Accepted cost: core's durable schema still names channel concepts. That is
recorded honestly rather than hidden behind a generic "extensions" map, which
would be a plugin layer in JSON.

## Binary and CLI shape decision

### What the channel module actually needs from core

Unlike `discordadapter` — which depends only on `hctl/channeladapter`
(`discordadapter/go.mod:11`) and never on `hctl` — the channel runtime needs
project loading, setup verify/apply (including the writable-channel variants),
harness driver construction and `Open`, the dispatch loop, and durable state. It
is a *consumer of core as a library*, not a protocol peer. The `discordadapter`
precedent therefore applies to the module boundary and the check-script shape,
but not to the dependency mechanism.

Note a Go mechanic that this makes load-bearing: a module published at import
path `hctl/channel` may import `hctl/internal/...`, because Go's internal rule
is import-path-prefix based and both share the `hctl/` prefix. This is legal but
it means `internal/` stops bounding core once a sibling module exists.

### Options

**(a) Separate executable, operator-facing.** `channel/cmd/hctl-channel`; the
operator runs `hctl-channel run AGENT --harness codex`. `hctl run --input
channels` and `hctl channel …` are removed from the root binary with a fixed
remedy message.

**(b) Package imported by the root binary.** Reverses "core never imports
channel". Buys package tidiness and nothing else; the boundary guard test
(`internal/integration/dependency_boundary_test.go:18`) could not be extended to
cover it. Rejected.

**(c) Thin stubs in the root binary that exec a channel binary.** Preserves the
documented CLI, at the cost of a sibling-binary discovery mechanism (a
`hctl-channel` next to `resolvedSelf`, mirroring `stagedChannelAdapterPath`
at `cli.go:891`), plus stdio/exit-code/signal forwarding for an interactive
`hctl channel setup discord` flow that already manipulates the controlling
terminal (`adapterhost/operation.go:51`, `prepareForegroundOperation`).

### Recommendation: (a), with (c) available as a later compatibility shim

Ship `hctl-channel` as the operator-facing command for both `run` and
`channel <setup|status|remove>`. Reasons:

1. **`hctl channel setup discord` is interactive.** It hands the child process
   the real terminal and restores it afterwards
   (`adapterhost/operation.go:48-60`). Proxying that through a second exec layer
   is a genuine source of terminal-state bugs for no user benefit.
2. **The guard test extends cleanly.** Add `hctl/channel` to
   `forbiddenRootDependencies` (`dependency_boundary_test.go:11-16`) and the
   existing four-way assertion (go.mod, production imports, test imports, built
   binary metadata) proves the boundary for free.
3. **`./scripts/check.sh` extends cleanly** — one more parenthesized block
   alongside `channeladapter` and `discordadapter` (`scripts/check.sh:43-57`),
   and core's ordinary `go test ./...` stops compiling the channel stack.
4. **One binary is not actually preserved by (c).** Under (c) the operator still
   has to install two binaries; the only thing preserved is the command name.
   Two named commands is more legible than one command that silently execs
   another.

Keep in the root binary: `hctl run --input jsonl` (with `--input` retained and
`channels` rejected with a remedy naming `hctl-channel`), `hctl hook
claude-deferred-input`, `hctl apply`, `hctl stage`, `hctl schedule`, `hctl mcp
serve`, and the whole `integration`/`connection` surface. The `hctl channel`
verb is removed.

Two consequences to accept explicitly:

- The channel binary must be built and installed alongside `hctl`; the release
  archive (ADR 0007) and the staged launcher (`stage.go:569`) both need a second
  artifact.
- Generated `.claude/hctl-settings.json` continues to name the **root** hctl
  binary (`setup.go:408`), because the deferred hook talks to a broker created
  inside the core Claude driver. This holds whether the driver was opened by
  `hctl` or by `hctl-channel`, since the channel binary links the same core
  driver and passes the same executable path into `setup.Apply`.

## Seam interface definitions

Five directions of dependency. All five are owned by core, defined as concrete
types or single-method interfaces, and none is a registry or plugin table.

### Seam 1: project load

Already narrow. `project.Load(source, harness, workspace...)`
(`internal/project/project.go`) and `project.LoadRelocated`
(used at `worktree.go:329`) are exported functions returning `*project.Project`.
**No new interface.** The channel module calls them directly.

### Seam 2: setup verify and writable apply

Today the channel side reaches five separate `setup` functions
(`VerifyWritableChannel`, `ApplyWritableChannelWithNativeMCP`,
`WritableChannelFiles`, `WritableChannelRetirementFiles`,
`RemoveWritableChannel`) plus `ValidateNativeMCP`. Collapse to one core-owned
type:

```go
// package setup
type WritableChannel struct{ Project *project.Project }

func (WritableChannel) Verify() error
func (WritableChannel) Apply(executable string, servers []integration.NativeMCPLaunchDescriptor) (Result, error)
func (WritableChannel) OwnedFiles() ([]string, error)          // was WritableChannelFiles
func (WritableChannel) RetirementFiles() ([]string, error)     // was WritableChannelRetirementFiles
func (WritableChannel) Remove(retained map[string]bool) error  // was RemoveWritableChannel
```

Owning side: core. This is a rename-and-group, not new abstraction; it makes the
writable-channel apply record (`setup.go:285-306`, field `meta.ChannelWritable`)
one reviewable unit instead of five exported functions scattered through an
857-line file.

### Seam 3: durable dispatch state

The channel module needs exactly the interaction subset. Core defines:

```go
// package dispatch
type ConversationStore struct{ /* unexported */ }

func OpenStore(root string) (*ConversationStore, error)
func (*ConversationStore) Close() error   // releases the advisory lock

// Bound to one conversation; implements interaction.Store from the channel module's side.
type ConversationHandle struct{ /* unexported */ }

func (*ConversationStore) Conversation(ref Reference) *ConversationHandle
func (*ConversationHandle) Snapshot() (Snapshot, error)
func (*ConversationHandle) LoadInteraction() (InteractionState, error)
func (*ConversationHandle) OpenInteraction(InteractionRecord) error
func (*ConversationHandle) UpdateInteraction(id string, mutate func(*InteractionRecord) error) error
func (*ConversationHandle) FinishInteraction(FinishRequest) error
func (*ConversationHandle) AssignWorkspace(root, branch string) error
func (*ConversationHandle) ...
```

Owning side: core. `interaction.Store` (`coordinator.go:27`) moves to the
channel module and is *implemented* there against `ConversationHandle`, which
inverts today's arrangement where core's `boundInteractionStore`
(`state_store.go:636`) implements a channel interface. That inversion is the
point: core stops naming a channel abstraction.

The types `InteractionRecord`, `FinishRequest`, and the phase/delivery/resume
enums are durable-schema data and stay in `internal/dispatchstate`
(they are currently in `internal/interaction/lifecycle.go`, imported by
`dispatchstate.go:13`). Moving them *into* `dispatchstate` removes core's
dependency on `internal/interaction`'s lifecycle half entirely.

### Seam 4: driver open and continuation

Core keeps `harness.Driver`, `harness.Session`, `harness.OpenRequest`,
`harness.Event` (including `RequestInput`), and both concrete drivers. The
channel module defines its own two interfaces, moved verbatim from
`harness.go:121` and `:127`:

```go
// package channel/harnessext
type ContinuationTurnDriver interface { ContinueTurn(...) ContinuationResult }
type NativeDeferredToolDriver interface { ResumeDeferredTool(...) ContinuationResult }

func WrapClaude(*claude.Driver) NativeDeferredToolDriver
func WrapCodex(*codex.Driver) ContinuationTurnDriver
```

Owning side: channel. `WrapClaude`/`WrapCodex` are the relocated
`continuation.go` files, expressed as wrappers over exported driver API. This
also removes the `projectContinuationTurnDriver` / `projectDeferredToolDriver`
duck-typing pair (`manager.go:680-686`) and the three CLI decorator types
(`cli.go:622-667`) — the channel module composes apply-before-open itself.

### Seam 5: dispatch submission and event stream

`runSubmissions` stays in core, but its channel-only options must not stay in a
core struct. Split `runOptions` (`dispatch.go:160`) by exporting the loop with
an explicit options type whose channel fields the channel module supplies:

```go
// package dispatch
type LoopOptions struct {
    TurnTimeout  time.Duration
    IdleTimeout  time.Duration   // 0 disables hibernation
    Policy       harness.ExecutionPolicy
    Store        *ConversationHandle
    Capacity     *Capacity        // nil disables admission control
    Hibernate    <-chan struct{}  // nil disables forced hibernation
    Wake         <-chan struct{}  // nil disables interaction wake
    RequestInput RequestInputHandler // nil disables managed request-input
    FreshSessions bool
    TaskDeadline  bool
}

func RunLoop(ctx context.Context, p *project.Project, d harness.Driver, conversation string,
    submissions <-chan Submission, emit func(Event) error, options LoopOptions) error
```

Owning side: core. `RequestInputHandler` (`request_input.go:23`) — one method,
`Handle(context.Context, RequestInputContext) error` — is the only interface
core must keep for the channel's benefit, and it is already minimal.
`CoordinatorRequestInputHandler` (`request_input.go:33`) moves to the channel
module, since it is a coordinator adapter.

`Manager` and the whole actor model move to the channel module and take
`Capacity`'s eviction half with them.

**No plugin layer needed.** The one place a registry would be tempting is
generated-instruction content for channels (`setup.go:369-381`). The concrete
alternative is described under decision D-6.

## Sequencing

Eight PR-sized steps. Each is independently green under `./scripts/check.sh`.
Steps 1–4 change no module boundaries and can land in any order relative to each
other; steps 5–8 are ordered.

**Step 1 — delete dead code.** Remove `internal/interaction/text.go` (213 lines,
no production caller) and the test-only request-input bridge in `internal/mcp`
(`requestInputRuntime`, `serveRequestsWithInput`, `callManagedWithInput`, and
`server.go:218-244`), replacing the latter with a direct broker predicate.
Carries D4's deferred "handler map replacing the managed-tool if-chain" if the
if-chain is touched anyway. Smallest, highest-confidence step.

**Step 2 — move interaction lifecycle types into `dispatchstate`.** Relocate
`Lifecycle`, `Tombstone`, `DurableState`, `FinishRequest`, `Digest`, and the
phase/delivery/resume enums from `internal/interaction/lifecycle.go` into
`internal/dispatchstate`. On-disk JSON is unchanged (field tags move verbatim).
After this, `internal/dispatchstate` no longer imports `internal/interaction`
(`dispatchstate.go:13`), and `internal/interaction` is purely the semantic
request/answer schema. Lands D4's deferred "request-input types in
`internal/harness`" question by settling it: the types stay, the lifecycle
leaves.

**Step 3 — group the writable-channel setup surface (Seam 2).** Introduce
`setup.WritableChannel` and reduce five exported functions to one type. Pure
refactor inside core; `internal/worktree` is the only caller.

**Step 4 — split `internal/channelconfig`.** Move `ProfileSelection` and
friends into `internal/channelselection` (the package that already owns
selection state), leaving `channelconfig` as three constants. Then decide D-6
(below) and apply it. Carries D4's deferred "splitting the channel control
sentinels out of `internal/channelconfig`".

**Step 5 — introduce the store seam (Seam 3) and the advisory lock.** Export
`dispatch.ConversationStore`/`ConversationHandle`, move `interaction.Store`
implementation to the caller side, and generalize
`schedule.AcquireRuntimeLock` into a dispatch-state lock acquired by every
writing store. Still one module; `Manager` now goes through the exported seam.
This is the step that makes a second process safe, so it must precede the split.

**Step 6 — the dispatch actor-mailbox rework.** Replace `Manager.Submit`'s
multi-lock admission protocol (`manager.go:352-492`) with a single mailbox
goroutine per manager, and export `dispatch.RunLoop`/`LoopOptions` (Seam 5).
Do this **before** the module move, not after as D5 phase 2 suggests: the rework
churns `manager_test.go` (3,158 lines, 50 tests), and doing it while the file is
still in its current package keeps the diff reviewable as one concern. Carries
D7's "race/parallel stress for the dispatch concurrency core" and the
`manager_test.go` split.

**Step 7 — the module move.** Create `channel/` with `go.mod` `module
hctl/channel`, `require hctl v0.0.0`, `replace hctl => ..`. Move
`internal/channel/controller`, `internal/channel/adapterhost`,
`internal/interaction/coordinator.go`, `internal/worktree`,
`internal/channelselection`, `dispatch/manager.go`, `dispatch/request_input.go`'s
`CoordinatorRequestInputHandler`, the capacity eviction half, the two
continuation drivers (as wrappers, Seam 4), and the `run`/`channel` CLI surface
into `channel/cmd/hctl-channel`. Add `hctl/channel` to
`forbiddenRootDependencies` (`dependency_boundary_test.go:11`) and a `(cd
channel && …)` block to `scripts/check.sh`. Make `hctl run --input channels`
return a fixed remedy naming `hctl-channel`.

**Step 8 — release and staging plumbing.** Second binary in the release archive
(ADR 0007) and in `internal/stage`'s closure and launcher
(`stage.go:296-300`, `:569`). Write the ADR recording the split and the state
ownership decision, then D5 phase 3 (docs split).

D4 deferred items and their landing steps:

| D4 deferred item | Step |
| --- | --- |
| Split channel control sentinels out of `internal/channelconfig` | 4 |
| Request-input types in `internal/harness` | 2 (settled: they stay in core) |
| `Submission`/`dispatchstate.Input`/`harness.Input` triple | 6 (repackaging survives; the mailbox rework removes one hop) |
| Handler map for `internal/mcp` if-chain | 1 |
| Bounded operator-facing diagnostic detail | 6 (`Manager.recordDiagnostic`, `manager.go:844`, moves with the rework) |
| Splitting `project.go` / `cli.go` | 7 (cli.go loses ~450 lines in the move) |

## Decision list

Each item states the ambiguity, the options, and a recommendation.

**D-1. Durable state ownership.** *Recommendation: one file, core-owned, with a
cross-process advisory lock (option A).* Preserves the parked-input/interaction
single-commit invariant; accepts that core's schema names channel fields.
Alternative B (two files) is only defensible if a future ADR retires that
invariant.

**D-2. Binary shape.** *Recommendation: separate operator-facing `hctl-channel`
(option a).* `hctl channel setup discord` manipulates the controlling terminal,
which a stub-exec layer would have to proxy. Accept two installed binaries.
Revisit with a compatibility shim (option c) only if operator feedback demands
the old command names.

**D-3. `internal/interaction` split.** *Recommendation: split, do not move.* Core
keeps `interaction.go` + `schema.go` (needed by the Codex driver and MCP);
channel takes `coordinator.go`; `lifecycle.go` migrates into
`internal/dispatchstate`; `text.go` is deleted. Requires renaming the remaining
core package — `internal/interactionschema` is the honest name, but the rename
is cosmetic and can be deferred.

**D-4. Request-input surface in `internal/harness`.** *Recommendation: it stays
in core.* Contradicts D5's stated scope. The alternative — a driver extension
point so the channel module supplies deferred-tool behavior — is a plugin layer
and is forbidden. Record this as an accepted deviation in the ADR for step 7.

**D-5. `hctl hook claude-deferred-input`.** *Recommendation: stays in the root
binary; generated hook config keeps naming `hctl`.* Follows from D-4. No
packaging change.

**D-6. Generated channel instructions.** Core's `apply` writes the Discord
participation section, the two control sentinels, and the Codex continuation
envelope contract into `CLAUDE.md`/`AGENTS.md` (`setup.go:369-381`). Options:
(i) keep the text in core and keep the sentinels in a tiny core package — core
owns a channel-shaped string it does not interpret; (ii) let `hctl-channel`
write a second generated instruction fragment that core's apply record does not
own — breaks the "generated files are visibly tool-owned, never overwritten"
invariant and creates two apply records; (iii) make instruction sections
pluggable — forbidden.
*Recommendation: (i).* Keep `internal/channelconfig` as exactly three exported
string constants plus a doc comment stating that core emits them verbatim and
the channel runtime interprets them. This is ~15 lines of core owning a
vocabulary, which is materially cheaper than two apply records. It does mean
core's generated instructions still mention Discord.

**D-7. `internal/integration` channel-adapter resolution.** *Recommendation:
stays in core.* `hctl apply` (`cli.go:957-962`) and `hctl stage`
(`stage.go:169`) both resolve the channel adapter, and the staged descriptor is
part of the agent closure. Moving it would force those core verbs to shell out.
The channel module imports `integration` read-only.

**D-8. Capacity coordinator split.** *Recommendation: core keeps admission
counting (`register`/`acquireTurn`/`releaseTurn`/`releaseResident`/`unregister`,
~60 lines) for `TaskRuntime`; the channel module takes eviction
(`scheduleLocked`'s victim branch, `oldestIdleLocked`,
`oldestQueuedResidentLocked`, `waitingForResidentLocked`, `hibernating`,
`errCapacityHibernation`).* Verify first that no task-runtime configuration can
reach the eviction branch — today it cannot, because
`newTaskRuntime` sets `residentLimit == activeLimit` (`task_runtime.go:57`) —
and add a test asserting that, so the split is provably behavior-preserving.

**D-9. `internal/` no longer bounds core.** Once `hctl/channel` exists it may
import `hctl/internal/...` by import-path prefix. Options: (i) accept it and
rely on review; (ii) promote the five seams to non-internal packages
(`hctl/dispatchapi`, `hctl/setupapi`, …) and add a guard test asserting the
channel module imports no `hctl/internal/` path.
*Recommendation: (ii) for the seams named above only.* A guard test in the
channel module (`go list -deps` filtered for `/internal/`) is cheap and makes
the boundary mechanical rather than social — the same technique
`dependency_boundary_test.go` already uses in the other direction.

**D-10. Ordering of the mailbox rework.** D5 phase 2 places the rework with the
module move. *Recommendation: land it first (step 6).* The rework touches
`manager_test.go` (3,158 lines); combining it with a module move produces a diff
where no reviewer can separate relocation from behavior change.

**D-11. `hctl run --input channels` compatibility.** Options: silently exec,
hard error with remedy, or keep the flag accepting only `jsonl`.
*Recommendation: hard error with a fixed remedy naming `hctl-channel`,* keeping
`--input` so `--input jsonl` scripts are unaffected. Consistent with D4's
acceptance of breaking changes and with `discordAdapterRemedy`'s existing style
(`cli.go:835`).
