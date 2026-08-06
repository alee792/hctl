# ADR 0017: Bound channel runtime capacity with fair admission

- Status: accepted

## Decision

One runtime-wide capacity coordinator limits resident channel harness processes
and simultaneously active turns across all managed conversations. The defaults
are four resident sessions and two active turns. `hctl run` exposes bounded
`--max-resident-sessions` and `--max-active-turns` overrides; the active limit
may not exceed the resident limit.

Input acceptance remains a durable dispatcher operation and does not consume an
active-turn slot. When a conversation has queued work, its lifecycle requests a
turn grant. Grants follow request order across conversations, so a lifecycle
with a local backlog yields between turns to an already-waiting conversation.
Duplicate input IDs reuse their durable status and consume no new capacity.

Opening or resuming a native process reserves one resident slot. At resident
capacity, the coordinator asks the least-recently-idle eligible lifecycle to
hibernate and waits for its process to close before granting the replacement.
Active work is never selected. If every resident has a durable backlog, a
resident that just completed its turn closes before reacquiring capacity so the
older nonresident waiter cannot starve; its queued work remains intact and
resumes later. Natural idle hibernation, capacity-pressure hibernation, and
between-turn fairness rotation release the same reservation, while startup,
turn, close, cancellation, and shutdown paths release all grants
deterministically.

`/status` reports only aggregate active, resident, limit, and durable queued
counts alongside the existing surface status. It exposes no paths, channel or
session identifiers, configuration, or credentials.

## Context

ADR 0014 gave every external conversation an independent lifecycle. Without a
runtime-wide boundary, a burst across surfaces could open an unbounded number
of native processes and simultaneous model turns. Per-conversation FIFO alone
also permits a busy surface to reacquire execution before another surface gets
a turn.

## Consequences

- Capacity coordination is deterministic runtime scheduling, not a model-based
  orchestrator.
- Accepted work survives saturation in the owner-only dispatch queue and is
  not retried or dropped.
- Resident processes may hibernate earlier than their configured idle timeout
  when another conversation needs the slot.
- The current coordinator is process-local; horizontal replica coordination is
  outside the local runtime contract.
