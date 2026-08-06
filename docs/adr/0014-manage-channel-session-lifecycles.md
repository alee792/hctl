# ADR 0014: Manage channel session lifecycles deterministically

- Status: accepted

## Decision

A long-lived channel runtime owns one managed session lifecycle for each
external conversation. The lifecycle owns that conversation's dispatcher
worker, durable queue, native harness session mapping, and at most one resident
harness process. Channel adapters submit input, observe bounded events, query a
redacted lifecycle status, and reset only idle conversations; they do not own
dispatcher goroutines or native processes themselves.

When a managed conversation has no active or queued work, its resident harness
process is closed after a configurable idle interval (15 minutes by default).
The durable conversation and native session ID remain authoritative, so the
next accepted message reopens the harness with that ID. Hibernation never
retries work. A close failure is a classified lifecycle failure and leaves the
durable conversation intact for explicit recovery.

All managed conversations in one runtime share one serialized owner for the
workspace's durable dispatch state. Conversation turns may run concurrently,
but accepting, starting, completing, recovering, and updating native session
identity are persisted without allowing one conversation's state snapshot to
overwrite another's progress.

This manager is deterministic runtime coordination. It does not choose goals,
route work through a model, replace the native harness loop, or reinterpret the
turn dispatcher's FIFO, deduplication, timeout, and uncertain-recovery rules.
Explicit JSONL dispatch keeps its existing single-conversation interface, and
scheduled occurrences continue to open fresh native sessions.

## Context

The first conversational Discord implementation let each surface create and
own a long-lived dispatcher goroutine. That kept guild and DM histories
separate, but spread lifecycle ownership into the transport adapter and loaded
an independent in-memory copy of the same durable state file for each surface.
Concurrent saves could therefore discard another surface's progress.

Idle hibernation, global admission limits, and isolated writable worktrees also
need one transport-independent place to observe and control conversation
lifecycle without introducing a supervisor agent. ADR 0017 defines the global
capacity coordinator built at this seam.

## Consequences

- The Discord adapter retains only authorization, message classification,
  response buffering, and delivery concerns.
- Safe status can distinguish inactive, idle, hibernated, queued, and active lifecycle
  states without paths or runtime identifiers.
- Ordinary harness, worktree, close, and deadline failures retire only the
  affected conversation worker while preserving its durable recovery state
  and peer lifecycles. Failure to deliver dispatcher events still terminates
  the channel runtime because outcomes can no longer be accounted for safely;
  recovery never silently retries ambiguous work.
- Capacity limits are defined by ADR 0017 and writable worktrees by ADR 0016.
  Read-only channel execution is defined by ADR 0015.
