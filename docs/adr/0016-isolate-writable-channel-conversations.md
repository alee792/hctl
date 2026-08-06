# ADR 0016: Isolate writable channel conversations in Git worktrees

- Status: accepted

## Decision

When a completed read-only Discord turn returns exactly
`HCTL_REQUEST_WRITE_ACCESS`, hctl suppresses that control result, assigns the
external conversation a dedicated branch-backed Git worktree, prepares the
selected agent project there, and resumes the same native harness session with
workspace-write access. Hctl then submits one fixed internal continuation so
the harness continues the original request once. Neither control text is sent
to Discord.

The shared workspace must be the canonical root of a Git checkout. Worktrees
live in an owner-only sibling directory and use a deterministic, opaque
conversation hash for both their directory and `hctl/<agent>/<hash>` branch.
Hctl rejects an existing path or branch that lacks the matching durable
assignment. The relocated agent must have the selected logical name and exact
source fingerprint before hctl preserves its logical agent identity.

The conversation's absolute worktree root and branch are stored in the
owner-only dispatch state. They contain no credential. Every later process for
that conversation revalidates the deterministic assignment, canonical path,
checked-out branch, selected source, and generated setup before opening. Other
conversations remain read-only in the shared checkout. `/new` clears the native
session and turn history but retains an existing workspace assignment.

Claude uses native `acceptEdits` mode for a writable conversation. Codex uses
the `workspace-write` sandbox with approvals disabled and must confirm those
effective settings. The existing execution-policy marker makes authored
managed tools available only after this promotion. Tokens remain filtered from
all Git, setup, harness, MCP, and tool-host children.

## Context

ADR 0015 established read-only channel sessions and an exact request for
elevation. Mutating the shared checkout would couple independent external
conversations, while asking the model to construct its own isolation boundary
would make a prompt responsible for runtime security and recovery.

## Consequences

- A mutating conversation has a stable checkout and native session across
  later messages, idle hibernation, and hctl restarts.
- Multiple mutating conversations may run concurrently, but each retains its
  own checkout, queue, native session, lifecycle, and originating response
  surface. Ordinary per-conversation failures do not cancel peer lifecycles.
- Creation and setup happen before writable harness execution. A mismatch or
  preparation failure leaves the shared checkout unchanged and produces a
  classified channel failure without retrying the user turn.
- Generated-file ownership and stale-source protections apply inside the
  isolated checkout exactly as they do during ordinary apply.
- Worktree lifecycle cleanup after a conversation is permanently retired is
  deferred; resetting a surface deliberately retains its assignment.
- Global resident-process and active-turn capacity remains a separate runtime
  concern.
