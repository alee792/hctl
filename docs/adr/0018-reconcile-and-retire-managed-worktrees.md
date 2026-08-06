# ADR 0018: Reconcile and retire managed worktrees conservatively

- Status: accepted

## Decision

Before a Discord runtime admits turns, it reconciles every durable worktree
assignment for the selected agent and harness against the exact deterministic
path, branch, repository ownership, generated setup evidence, Git status, and
merge ancestry. Invalid, missing, moved, foreign, or otherwise unverifiable
assignments remain in durable state and produce local recovery diagnostics;
they are not repaired or replaced automatically.

Active or queued conversations, conversations with an uncertain outcome,
worktrees with tracked or untracked changes, and branches with commits not
already reachable from the base checkout are always preserved. An inactive
worktree is automatically disposable only when its non-generated Git status is
clean and its branch is an ancestor of the base checkout's current `HEAD`.
Hctl-owned generated writable setup is ignored for cleanliness only after its
apply record, bytes, and modes are revalidated.

Before cleanup, hctl durably marks the exact assignment as retiring. It then
removes only the verified generated setup, exact registered worktree, and exact
managed branch. The assignment is cleared only after both Git targets are
gone. A partial failure retains the marker, root, and branch for an idempotent
retry on the next startup. While that marker remains, the affected conversation
rejects new work and directs the operator to local recovery; peer conversations
remain available.

Local startup diagnostics may name the worktree path and explain whether it was
preserved, retired, or needs repair. Discord `/status` remains redacted and
does not expose paths, branches, native session IDs, message contents,
configuration, or credentials.

## Context

ADR 0016 introduced durable conversation worktrees and deliberately deferred
their retirement. Idle alone is not disposal evidence: an idle conversation is
resumable, and a clean branch can still contain committed work that has not
reached the base checkout. Cleanup also spans a filesystem checkout, Git's
worktree registry, a branch, and durable dispatch state, so interruption must
not erase the ownership evidence needed to finish safely.

## Consequences

- A valid dirty or unmerged assignment resumes in place without creating a
  replacement checkout.
- Clean worktrees whose branches contain no unique work may be reclaimed on
  startup; a later mutation for that conversation receives a new deterministic
  worktree from the then-current base.
- Missing evidence causes preservation and a local diagnostic, never a broad
  or forced cleanup guess.
- Automatic merging, committing, pushing, pull-request creation, and deletion
  of unresolved work remain outside hctl.
