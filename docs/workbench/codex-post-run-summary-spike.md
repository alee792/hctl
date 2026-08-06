# Codex post-run summary spike

- Date: 2026-08-05
- Codex CLI observed: 0.144.1 on macOS arm64
- Scope: Codex App Server only; this is not a portable harness contract

## Result

Do not implement a post-run summary yet. The gateway can reliably report the
parent input outcome it already owns, and Codex exposes child runtime IDs in its
current App Server protocol. It does not, however, expose a stable native-log
locator through the gateway contract. Adding a summary now would either invent
that locator, retain transcript-derived content, or make a Codex-specific
observation look portable.

## What hctl can observe now

| Fact | Evidence and boundary |
| --- | --- |
| Parent outcome | The gateway durably maps its caller `input_id` to `completed`, `failed`, `cancelled`, or `uncertain`; the Codex driver emits the matching parent `threadId` and `turn.id`. This is hctl-owned state, not a model-authored result. |
| Parent runtime IDs | The Codex driver opens or resumes one thread and starts one parent turn. It filters a completion whose `threadId` or `turn.id` differs from that active parent, so a normally identified child cannot complete the parent. |
| Child runtime activity | The existing live gateway trace recorded child `turn/completed` notifications on the parent stream with a different `threadId` and `turn.id`. The current Codex v2 schema also gives `item/agentMessage/delta` `threadId`, `turnId`, and `itemId`; collaboration items carry `agentThreadId`, and a subagent thread has `parentThreadId`. These are Codex runtime identifiers, not hctl IDs and not a cross-harness promise. |
| Native log content | Nothing in the gateway reads or copies it. Codex keeps rollout JSONL locally and its current schema describes resuming by `thread_id` and repairing metadata from rollout JSONL, but the driver receives no documented log path or log-reference token. A thread ID alone is only a lookup hint. |

The live child observation is recorded in
[Codex live acceptance](codex-live-acceptance.md#parallel-review-follow-up).
The credential-free regression in `internal/harness/codex/codex_test.go` keeps
the essential ordering case: a child output and completion arrive before the
parent completion, and only the parent completes the gateway turn.

## Reproducible, credential-safe procedure

This procedure uses the installed Codex binary's locally generated protocol
schema. It sends no `turn/start` request, does not invoke a model, does not
read a transcript, and does not print or retain credential material.

```sh
./scripts/bootstrap-tools.sh
codex --version
schema_dir="$(mktemp -d /tmp/hctl-codex-schema.XXXXXX)"
codex app-server generate-json-schema --out "$schema_dir"
rg -n -C 3 'threadId|turnId|agentThreadId|parentThreadId|collabAgentToolCall' \
  "$schema_dir/v2/TurnCompletedNotification.json" \
  "$schema_dir/v2/AgentMessageDeltaNotification.json" \
  "$schema_dir/v2/ItemStartedNotification.json" \
  "$schema_dir/v2/ItemCompletedNotification.json" \
  "$schema_dir/v2/ThreadStartResponse.json"
.tools/go/bin/go test ./internal/harness/codex
rm -rf "$schema_dir"
```

For a future, explicitly authorized model-backed rerun, use a disposable
workspace and save the App Server wire trace outside the repository. Ask the
parent to delegate exactly one no-file, no-tool child action. Verify that the
trace contains the parent and child IDs, any collaboration item that links
them, and the native rollout reference returned by a documented Codex surface.
Do not commit the trace or copy transcript text into hctl.

## Raw-observation boundaries

The schema confirms fields, not lifecycle guarantees or log retention. The
earlier live trace confirms the same-stream child completion behavior, but its
raw JSONL is deliberately not retained here because it can contain model
output and workspace details. This spike did not start a model turn or a new
child agent; the prior live child run was sufficient to validate the currently
implemented correlation guard without handling credentials or transcripts.

In particular, the following remain unobserved and must not be inferred:

- a stable public path or resolver for a child rollout log;
- whether every child delegation emits a collaboration item with a usable
  parent/child link;
- retention, access control, or portability of Codex's local rollout logs; and
- equivalent IDs, child events, or log semantics for Claude or another
  harness.

## Failure behavior and recommendation

Any future Codex-only optional observation must be best-effort and additive:

- Parent completion remains the sole gateway terminal condition. Missing,
  malformed, reordered, or unlinked child events must be omitted from the
  observation rather than changing the parent outcome.
- Record only bounded identifiers and an explicit observation state such as
  `observed`, `unlinked`, or `unavailable`; never record deltas, item payloads,
  paths, or transcripts.
- Do not synthesize a log path. A future reference may name the harness and
  the observed thread ID only after Codex supplies a documented resolver or
  explicit rollout reference. If it cannot, omit the reference.
- A changed or unsupported Codex protocol must disable child observation and
  leave the existing parent-only gateway behavior intact.

The smallest safe next step is therefore **no implementation**. If optional
Codex observability becomes a product priority, first run the authorized
capture described above and add one Codex-specific, bounded event test based on
that capture. Keep the event outside the portable gateway completion contract.

## Follow-up task state

HCTL-004 is complete as a research task. No implementation or new queued task
is justified by the present evidence. Reopen this question only with a product
decision to prioritize optional Codex observability and authorization for the
model-backed capture; do not block gateway completion on child visibility.
