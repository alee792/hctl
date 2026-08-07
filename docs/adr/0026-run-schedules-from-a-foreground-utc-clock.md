# ADR 0026: Run schedules from a foreground UTC clock

- Status: accepted

## Plain-English summary

An operator can run `hctl schedule run AGENT` as one foreground process. It
evaluates the agent's already-applied five-field schedules in UTC and submits
current occurrences through one shared, bounded task runtime. It installs no
daemon and does not replay missed work.

## Decision

The runner loads and compiles source, verifies the selected harness and current
generated setup, and acquires a lock for the canonical workspace, agent
identity, and harness once. It does not auto-apply or hot reload.

One task-runtime coordinator owns the durable dispatch store and active-turn
capacity for every schedule. Different schedule conversations may execute
concurrently up to `--max-active-turns`. Capacity-queued and active occurrences
both count as in flight; a later due minute for that schedule is skipped. Every
occurrence retains ADR 0013's fresh native session and per-turn deadline.

The clock evaluates UTC only. Its first candidate is strictly after startup.
On each wake it admits at most the matching occurrence in the current UTC
minute, never an older stored candidate. A process-local scheduled-minute
watermark prevents duplicate admission after repeated or backward clock
movement. Occurrence IDs hash the complete exact schedule name and canonical
scheduled UTC minute with SHA-256.

SIGINT and SIGTERM stop admission before shutdown. Already admitted work,
including capacity waiters, completes or reaches its turn deadline before the
runtime store and lock close. Durable-state, coordinator, or diagnostic-output
failure also stops admission; an individual terminal occurrence does not stop
unrelated schedules. Startup classifies queued or active task state retained
from an interrupted prior runtime as uncertain and never executes it.

## Consequences

- Hctl provides a foreground clock, not daemon supervision or hosted scheduling.
- Sleep, downtime, and forward jumps do not create catch-up bursts.
- Model output and prompts never enter lifecycle diagnostics.
- The lock is local; distributed scheduling remains unsupported.

## Related decisions

- [ADR 0013](0013-run-schedules-as-fresh-dispatch-tasks.md)
- [Product specification](../product-spec.md)
