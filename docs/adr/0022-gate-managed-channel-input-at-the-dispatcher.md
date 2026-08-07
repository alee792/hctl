# ADR 0022: Gate managed channel input at the dispatcher

- Status: accepted

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
production MCP child receives no bridge yet, so this capability is disabled
until the Claude or Codex continuation and Discord responder slices provide it.

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
and missing harness strategies do not advertise the tool. Native subagents
currently inherit the managed MCP configuration and neither supported harness
exposes trustworthy caller ancestry to the MCP child. Therefore any call whose
channel-root origin is not independently proven is rejected before coordinator
persistence. Literal tool-list isolation for subagents remains deferred until
harness adapters expose trustworthy ancestry.

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

The current adapters cannot yet prove subagent ancestry or park and resume a
real tool call. Landing the generic seam disabled avoids advertising a
capability that cannot complete while making later harness and responder work
testable against one contract.

## Consequences

- Vendor-specific rendering and real resume behavior remain outside this
  decision.
- Parallel and repeated requests fail through the coordinator's one-pending
  invariant after the first durable commit.
- Capability negotiation is performed again at the trusted dispatcher seam;
  an MCP-side preflight result is never authority.
- A strategy owns its bounded tool disposition; generic MCP code does not
  choose deferred-tool versus continuation-turn semantics.
- Adding another channel responder does not change the model-authored schema or
  durable lifecycle.
