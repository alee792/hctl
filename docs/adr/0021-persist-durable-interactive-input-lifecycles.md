# ADR 0021: Persist durable interactive-input lifecycles

- Status: accepted

## Decision

Hctl persists at most one nonterminal interactive-input lifecycle in the same
owner-only dispatch conversation record that owns the triggering input, queue,
native session mapping, and worktree assignment. The shared conversation store
is the sole writer for this state. The interaction coordinator mutates it
through that store; renderers, harness continuations, MCP children, and vendor
adapters cannot load and save independent conversation snapshots.

The portable request is the transport-neutral semantic contract defined by the
product specification. Hctl adds bounded runtime correlation, pseudonymous
surface and principal ownership, expiry, normalized answer state, and one of
two continuation modes:

- a **native deferred-tool continuation** resumes the same logical tool call
  through a harness-native continuation identity; and
- a **continuation turn** starts a later turn in the same native session with
  the normalized answer and request context.

Neither mode is a **blocking request**: hctl does not keep a live model turn,
tool callback, channel request, or resident harness process open while a human
is deciding. A pending request parks the conversation, releases its active-turn
grant and resident-process capacity, and blocks later queued inputs for that
conversation. Other conversations continue through the capacity coordinator.

The externally meaningful lifecycle is `requested -> rendered -> answered ->
resuming -> completed`, with explicit `cancelled` and `expired` terminal
outcomes. Delivery and continuation have additional internal intent and
uncertainty state because both cross process or network seams:

1. The request is committed before rendering. A renderer then atomically claims
   one delivery intent; only the claim winner may perform the side effect. A
   crash before the claim leaves the first attempt available, while a crash
   after the claim is treated as ambiguous.
2. A successful render is committed as rendered. A crash or ambiguous result
   after delivery intent becomes delivery-uncertain and is never automatically
   redelivered. A valid answer may prove that the request reached the user.
3. A normalized answer is committed exactly once before acknowledgement or
   continuation. Identical duplicate answers are idempotent; conflicting,
   late, expired, cancelled, unauthorized, or cross-surface answers fail.
4. Resume intent is committed before invoking the harness continuation. A
   crash or ambiguous result after that intent becomes resume-uncertain and is
   never automatically resumed again. A bounded explicit recovery action may
   adjudicate a known failure or uncertain outcome as completed or failed; it
   never repeats model work.
5. Completion, cancellation, and expiry replace the pending request with a
   bounded terminal tombstone before another request may open. If queued work
   remains, the same commit records a wake intent; starting the next durable
   input consumes it, so a crash between terminal state and notification is
   recovered on the next runtime start.

Answer acceptance requires the store-bound agent and conversation, authorized
principal and surface owner, interaction ID, original request, and current
pending record to agree. A callback or interaction ID alone is never authority.

A nonterminal lifecycle makes the conversation busy. Reset rejects it rather
than discarding the request or answer. Worktree reconciliation preserves its
assigned worktree, and a resume-uncertain lifecycle also supplies uncertainty
evidence that prevents automatic retirement. Shutdown stops new admission and
leaves committed state for deterministic recovery.

Status and audit surfaces expose only `waiting_for_input` and existing bounded
aggregate queue or capacity state. They do not expose prompts, answers,
interaction or callback IDs, channel IDs, native session identities,
continuation keys, paths, configuration, or credentials. Durable state contains
no raw vendor payload or credential.

This decision establishes the transport-neutral coordinator, lifecycle, and
dispatch-store contract only. GitHub issues #21 through #24 separately own the
managed `channel.request_input` tool, Claude native deferred-tool continuation,
Codex continuation turns, and Discord rendering. Until those slices are wired,
the live Discord and harness adapters do not offer interactive input.

## Context

The semantic request vocabulary can describe bounded confirmations, choices,
text, date/time input, and modest forms, but a request is not useful if its
ownership and continuation exist only in memory. Human response time is
unbounded relative to a model turn, process lifetime, or channel connection.
Holding one of those resources open would consume scarce capacity and still
lose correlation on restart.

The dispatcher already provides owner-only, strict, atomically replaced state
and one serialized writer across conversations. A second interaction state file
would introduce competing ownership, partial snapshots, and unclear reset and
worktree rules. Extending the conversation record keeps persistence and
recovery decisions at the existing deep seam.

## Consequences

- One conversation can wait for one human answer without occupying a harness
  process or active-turn slot; multiple pending requests per conversation are
  deferred.
- Adapters render semantic requests and return normalized answers, but do not
  control durable lifecycle transitions or continuation policy.
- Recovery prefers a visible uncertain state and operator action over duplicate
  UI delivery or duplicate harness execution.
- Terminal tombstones retain only bounded correlation digests needed to
  classify duplicate and late answers, not prompt or answer content.
- Adding Slack or another adapter does not change the coordinator or persisted
  lifecycle, provided the adapter implements the same renderer and answer
  normalization seams.
