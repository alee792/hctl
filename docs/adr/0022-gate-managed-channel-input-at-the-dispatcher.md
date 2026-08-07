# ADR 0022: Gate managed channel input at the dispatcher

- Status: accepted
- Integrated by: [ADR 0023](0023-continue-codex-input-in-a-new-turn.md),
  [ADR 0024](0024-resume-claude-channel-input-with-native-tool-deferral.md),
  and [ADR 0025](0025-render-discord-input-with-bounded-native-components.md)

## Decision

Hctl defines one built-in managed MCP tool named `channel.request_input`. Its
arguments are exactly the transport-neutral semantic request contract. The
model cannot supply interaction, callback, ownership, authorization,
continuation, channel, or vendor-rendering fields.

The tool is advertised only by a trusted bridge established for the root agent
of a channel-managed session, and only when the selected harness has a
continuation strategy and the channel responder can render the request natively
or use its declared text fallback. A shared inherited MCP server cannot enable
the capability through configuration or a process-wide root flag. The
Claude's MCP child receives a short-lived, root-owned broker only when a
per-conversation responder bridge exists; ADR 0024 defines that native
continuation. Codex uses its client-owned dynamic tool. Production exposure
became available when ADR 0025 supplied the Discord responder bridge.

An accepted tool call crosses a structured internal harness event. The harness
event carries semantic request data and tool-call correlation. Only the
harness-owned root-event constructor can attach opaque channel-root proof; a
zero or caller-assembled event has no proof and is rejected before persistence.
The event does not carry a rendering decision. The dispatcher-bound handler recomputes capability negotiation from
its responder capabilities, injects the active dispatch input, pseudonymous
owner, continuation mode, and runtime correlation, then calls the durable
interaction coordinator synchronously on the serialized dispatcher loop. The
coordinator commits `requested` before the harness bridge is acknowledged.
The MCP child, renderer, adapter, and harness process never write dispatch
state independently.

Generated Claude and Codex instructions mention the tool only conditionally
for authored channel projects. They tell the root agent to ask only when a
missing choice materially changes the work, otherwise proceed, and never
invent callback IDs or vendor markup. Instructions are guidance, not the
capability boundary.

Schedules, explicit JSONL runs, ordinary native sessions, missing responders,
and missing harness strategies do not advertise the tool. Claude subagents may
inherit the managed MCP listing, but their documented hook `agent_id` is denied
before deferral or persistence. Codex proves the exact root thread and turn.
Any call whose channel-root origin is not independently proven is rejected.
Literal tool-list isolation for subagents remains deferred.

The selected harness strategy returns a bounded, content-free disposition only
after the durable commit. MCP merely encodes that typed disposition, leaving
Claude deferred-tool and Codex continuation-turn result semantics to their
respective adapters. Audit correlation for this tool derives from the MCP
request identity and tool name, never semantic request bytes. Diagnostics and
audit never contain prompts, options, answers, fallback text, or vendor payloads. The
existing secretless operation broker remains the required boundary for
secret-bearing managed connections and is unchanged by this tool.

## Context

Prose can teach participation behavior but cannot create a correlated,
validated, durable human-input lifecycle. Conversely, letting an MCP child or
vendor adapter save request state would introduce a second writer and violate
the dispatch conversation's atomic queue and interaction invariants. A typed
harness event preserves each harness's ownership of tool continuation while
keeping persistence in the dispatcher.

ADR 0023 supplies Codex continuation turns. ADR 0024 proves Claude root
ancestry and parks and resumes the same native tool call. Both remain behind
the generic capability seam until a compatible responder is bound.

## Consequences

- Vendor-specific rendering and real resume behavior are specified separately
  by ADRs 0023 through 0025.
- Parallel and repeated requests fail through the coordinator's one-pending
  invariant after the first durable commit.
- Capability negotiation is performed again at the trusted dispatcher seam;
  an MCP-side preflight result is never authority.
- A strategy owns its bounded tool disposition; generic MCP code does not
  choose deferred-tool versus continuation-turn semantics.
- Adding another channel responder does not change the model-authored schema or
  durable lifecycle.
