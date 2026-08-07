# ADR 0025: Render Discord input with bounded native components

- Status: accepted
- Date: 2026-08-07
- Integrates: [ADR 0021](0021-persist-durable-interactive-input-lifecycles.md),
  [ADR 0022](0022-gate-managed-channel-input-at-the-dispatcher.md),
  [ADR 0023](0023-continue-codex-input-in-a-new-turn.md), and
  [ADR 0024](0024-resume-claude-channel-input-with-native-tool-deferral.md)

## Decision

The built-in Discord adapter implements the transport-neutral
`interaction.Renderer` seam. It advertises top-level request kinds separately
from field kinds that can appear inside a native form. The first adapter renders
confirmations, non-freeform choices, text, and text-only forms of at most five
fields. Confirmations and small single choices use buttons, larger or multiple
choices use string selects, and text or text-only forms use an Answer button
followed by a modal. Date/time, freeform choices, and mixed forms degrade to the
request's declared text fallback.

The renderer receives a narrow `RenderIntent` containing only correlation,
ownership, request, and resolved rendering fields. Controller recovery and
callback authorization use a separate `PendingInteraction` snapshot; expiry,
continuation mode, and lifecycle phase do not cross the renderer interface.

Hctl, rather than the model or adapter, defines the fallback answer grammar and
parser. It uses exact `yes` or `no`, one-based option ordinals, the whole text
reply, canonical date/time syntax, keyed form lines, and exact `cancel` only
when allowed. A freeform choice uses exactly `other=TEXT`; choose-many may use
`1,2;other=TEXT` to combine ordinals with one freeform selection. The freeform
value counts as one selection, and explicit empty `other=` remains a selection
when zero-length text is allowed. The authored `fallback_text` is only the
introduction. A fallback reply must reference a bot message containing the
current request's opaque marker and enters durable answer acceptance instead
of the ordinary message queue.

Discord payloads remain adapter-local. Component IDs use a bounded versioned
digest handle and trusted positional opcode; they contain no semantic IDs,
prompt, answer, authorization value, channel, session, path, or credential. A
callback is accepted only when application, authorized human, configured
surface, reconstructed conversation, durable owner, current interaction,
request shape, handle, action, and values all agree. Positional values are
mapped back through the durable semantic request and normalized independently.

The coordinator commits delivery intent before Discord REST. Once a send is
attempted, an error is ambiguous and is never retried automatically. A valid
callback can prove an uncertain delivery. Native callback answers are
normalized and committed before the acknowledgement attempt; continuation is
scheduled only after that attempt. An acknowledgement failure does not roll
back or repeat the answer. The Answer button only opens a modal and is not a
final answer.

DiscordGo remains at v0.29 and this implementation uses its stable action row,
button, string select, modal, and text input structures. Raw Discord callback
payloads and tokens never enter dispatcher state, model input, or audit.

## Consequences

- Another channel can implement the same renderer and answer interface without
  importing Discord types or reproducing the durable lifecycle.
- The semantic contract stays intentionally smaller than Block Kit, A2UI, or
  arbitrary model-authored layouts.
- Delivery-pending guild interactions are reconstructable on restart. DM
  delivery is reconstructed when that authorized DM surface next appears.
  Delivery-intended and delivery-uncertain state is never automatically sent.
- Credential-free stitched acceptance covers Discord, Claude, and Codex.
  Credentialed Codex passed; credentialed Claude was unavailable because the
  installed CLI had no authenticated account. The
  [acceptance record](../workbench/interactive-input-acceptance.md) preserves
  the exact evidence and limits.
