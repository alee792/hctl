# ADR 0023: Continue Codex channel input in a new turn

- Status: accepted

## Decision

For a channel-managed Codex root, hctl exposes the existing semantic
`channel.request_input` contract as an app-server dynamic namespace tool named
`channel.request_input`. This is a narrow Codex transport refinement to ADR
0022: the semantic request, durable dispatcher handoff, audit boundary, and
capability gate are unchanged, but Codex does not route this one tool through
the separately configured managed MCP child.

The Codex adapter opts into app-server's documented experimental API only when
the dispatcher has both a request-input handler and responder. It registers
the `channel` namespace and `request_input` function on `thread/start` with the
transport-neutral request schema. App-server sends the invocation to hctl as
an `item/tool/call` server request containing thread, turn, call, namespace,
tool, and argument provenance. Hctl accepts it only when the thread and turn
exactly match the active channel root. A call from a forked or subagent thread,
an unrelated turn, another tool, malformed arguments, or a session without the
runtime gate is rejected before dispatcher persistence.

After the dispatcher durably parks the originating input, the adapter returns
the bounded `continuation_turn` dynamic-tool result. Generated Codex
instructions tell the model to end that turn immediately. Hctl waits only for
the already bounded turn completion, then closes app-server and releases the
resident-process and active-turn grants. It does not leave a dynamic-tool
server request, native `request_user_input` waiter, MCP elicitation, or active
turn open while the human decides.

After an authorized normalized answer is durably accepted, the continuation
adapter calls `thread/resume` with the stored Codex thread id and then
`turn/start`. The sole text input is a bounded JSON envelope with type
`hctl.channel_input_answer`, schema version, interaction id, original semantic
request, and normalized answer. The envelope is constructed only from the
durable interaction; an ordinary channel message cannot become it. Generated
instructions identify it as controller-owned internal continuation data and
forbid exposing the envelope to the user.

Resume intent remains governed by ADR 0021. A conclusive completed turn
finishes the interaction. A conclusive failed or cancelled turn is failed; a
transport loss, malformed terminal result, or mismatched resumed thread is
uncertain and is never automatically repeated. The app-server process is
closed after the continuation turn. This is a later turn in the same thread,
not continuation of the original inference or tool callback.

The dynamic-tool API is explicitly experimental. Hctl uses only its currently
documented namespace registration and server-request contract and fake-tests
the generated protocol shape. It does not use `turn/steer`, experimental MCP
Tasks, raw history injection, or undocumented app-server behavior. A Codex
version that rejects the negotiated capability fails the channel session
instead of silently falling back to an unproven bridge.

## Context

Official app-server documentation makes persisted threads resumable and starts
later work with `turn/start`. It also documents dynamic tools as client-owned
server requests with exact thread and turn provenance. In contrast, a normal
configured stdio MCP child does not give its client a documented callback that
proves whether a root or inherited subagent invoked the tool. Using the dynamic
tool keeps dispatch state in hctl and closes the ancestry gap identified by ADR
0022 without inventing private IPC.

## Consequences

- Codex can honestly advertise the tool once a channel responder is wired;
  ordinary Codex, JSONL, schedule, and responder-less sessions do not opt into
  the experimental API.
- A propagated tool remains harmless in subagent threads because exact root
  thread and active-turn provenance is required before persistence.
- The managed MCP server still owns every other managed tool. The secretless
  operation broker decision is unchanged.
- Fake app-server and child-process tests provide live-style evidence for
  process exit, same-thread resume, bounded continuation input, and final
  controller delivery. Credentialed Discord end-to-end acceptance belongs to
  issue #25 after issue #24 wires rendering and answer callbacks; this slice
  does not claim that pass.
