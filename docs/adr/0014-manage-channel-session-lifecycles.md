# ADR 0014: Manage channel session lifecycles deterministically

- Status: accepted

## Decision

A long-lived channel runtime owns one managed session lifecycle for each
external conversation. The lifecycle owns that conversation's dispatcher
worker, durable queue, native harness session mapping, and at most one resident
harness process. Channel adapters submit input, observe bounded events, query a
redacted lifecycle status, and reset only idle conversations; they do not own
dispatcher goroutines or native processes themselves.

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

The planned idle hibernation, global admission limits, and isolated writable
worktrees also need one transport-independent place to observe and control
conversation lifecycle without introducing a supervisor agent.

## Consequences

- The Discord adapter retains only authorization, message classification,
  response buffering, and delivery concerns.
- Safe status can distinguish inactive, idle, queued, and active lifecycle
  states without paths or runtime identifiers.
- One conversation failure still terminates the channel runtime consistently;
  recovery never silently retries ambiguous work.
- Idle hibernation, capacity limits, read-only execution, and writable
  worktrees remain separate changes built through this lifecycle interface.
