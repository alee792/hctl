# Channel product specification

- Status: the conversational channel runtime's contract, as implemented by
  the prototype. The channel runtime is a second product built on the core
  ([product-spec.md](product-spec.md)); it stays in the prototype repository
  and is not part of the core rebuild target. See the
  [rebuild charter](workbench/rebuild.md).
- Implemented channel: Discord, through the external `hctl-discord` adapter.

## Job

An operator runs an already-proven agent as a conversational presence: one
authorized human converses with the agent through a chat surface, the same
agent folder unchanged. The native harness still owns intelligence; the
channel runtime is deterministic coordination between a chat transport and
the core's turn dispatcher — never another chat UI, model loop, or agent
orchestrator.

## Authoring and setup

The optional `channels/discord.md` file carries strict `mode: ambient`
frontmatter and a 1–1,024 character Markdown participation policy. Its
conventional path registers the channel; it contains no identity, profile, or
credential, joins the source fingerprint, and its policy plus the exact
`HCTL_NO_REPLY` control result join generated native instructions.

The adapter is an integration package (`channel-adapter` v1 capability):
installed and trusted by the operator through the core's package journey,
launched as a separate executable, never imported into core. The adapter owns
vendor transport, SDK payloads, rendering, callbacks, rate limits, bot
identity, enrollment, and the OS credential store; core receives only stable
conversation ids, hashed owner keys, and normalized semantics. `hctl channel
setup|status|remove discord` runs the adapter's exact bounded modes;
deployment may inject `HCTL_DISCORD_TOKEN`, passed only to the selected
adapter and scrubbed from every other child.

```sh
hctl integration install PACKAGE_ROOT --trust operator
hctl channel setup discord AGENT
hctl run AGENT --workspace WS --harness <claude|codex>
```

`hctl run` auto-applies stale generated setup, resolves the exact installed
adapter, and never falls back to in-process vendor code.

## Conversational semantics

The adapter serves one authorized user in one guild channel and that user's
DM. Each surface maps to an independent durable dispatcher conversation with
its own queue, native session, and response surface; other users, channels,
bots, and webhooks are ignored. Output is buffered until turn completion,
exact `HCTL_NO_REPLY` is suppressed, and visible replies use bounded chunks
with mentions disabled. `/new` resets an idle surface; `/status` returns
redacted lifecycle and aggregate queue state — never prompts, answers,
identifiers, paths, or credentials. Idle residents hibernate after a bounded
interval, retaining the durable native session mapping; the next eligible
message resumes that conversation. Active work is never hibernated; queued
work is never discarded.

## Capacity

One runtime-wide coordinator bounds resident harness processes (default 4)
and simultaneously active turns (default 2), with bounded operator overrides.
Accepted input queues durably under saturation, turn grants advance in
request order across conversations, and at resident pressure the
least-recently-idle eligible process hibernates; fair rotation between turns
preserves every queued input. Duplicate delivery consumes neither a queue
entry nor capacity.

## Read-only sessions and write promotion

Channel-managed sessions run read-only in the shared workspace under the
strongest supported native policy, with the managed boundary withholding
authored tool hosts; safe built-ins remain, and native MCP servers (for
example a configured GitHub server) remain outside this policy. When a
read-only turn returns exactly `HCTL_REQUEST_WRITE_ACCESS`, the runtime
creates a conversation-specific branch-backed Git worktree in a private
sibling directory, applies the agent there, resumes the same native session
with write access, and continues the original request once — control results
never appear as channel messages. Writable conversations stay independent:
each keeps its own branch, worktree, queue, session, and surface. At startup
every durable worktree assignment is validated; only an inactive worktree
with verified generated setup, no non-generated changes, and a branch already
reachable from the base checkout is retired automatically, through durable,
idempotent cleanup. Anything busy, dirty, uncertain, or unverifiable is
preserved with operator diagnostics.

## Interactive input

Channel-native human input uses a transport-neutral semantic request — never
generative UI. The versioned union is exactly `confirm`, `choose_one`,
`choose_many`, `text`, `date_time`, and a bounded `form`; every request has a
bounded prompt, optional text fallback, a relative expiry, an explicit
cancellation policy, and stable semantic IDs for fields and options, all with
fixed size ceilings. Answers reference only those IDs; trusted code validates,
normalizes, and rejects unknown or out-of-range values. The contract carries
no vendor payloads, layout, URLs, executable code, or credential references.

The managed `channel.request_input` tool exposes exactly that contract behind
a runtime capability gate: only a proven channel-managed root session with a
compatible responder receives it; the dispatcher independently validates root
provenance, injects ownership and correlation, and commits the request
durably before acknowledging the harness. Adapters advertise supported kinds
and limits; unsupported requests degrade deterministically to an hctl-owned
text-fallback grammar or fail clearly.

Waiting is parking, not blocking: no model turn, resident process, or
capacity grant is held. Each conversation persists at most one nonterminal
request in its own durable record; later input on that surface receives one
bounded busy response. The two continuation modes are intentionally
different: Claude resumes the same logical tool call through its documented
native deferral protocol; Codex opens a continuation turn in the same thread
carrying the normalized answer envelope. Ambiguous delivery or resumption is
retained as uncertain and never automatically replayed; explicit recovery may
adjudicate it. Diagnostics and audit never contain prompts, options, answers,
or vendor payloads.

## Adapter protocol

The channel-adapter protocol is one bounded, versioned, bidirectional JSONL
stream over the verified child's stdin/stdout: hello/ready negotiation that
may only narrow declared features and limits, closed semantic frames (opaque
handles, normalized text and attachments, status/reset, activity and reply
intents, the interactive request and its receipts and answers, dispositions,
lifecycle, classified diagnostics, shutdown), and fixed size, concurrency,
deadline, and queue ceilings. Vendor payloads, credentials, raw environment,
markup, executable code, and filesystem paths have no protocol
representation. Core is the sole writer of dispatcher, interaction, worktree,
and capacity state. Event ids are stable until acknowledged; identical replay
is idempotent, changed content under one id is fatal, and ambiguous effects
follow the core's uncertain/no-retry rule. Process isolation separates
dependencies and responsibility; it is not an OS sandbox.

## Acceptance

The channel product is complete when credential-free tests (fake adapter and
harness processes) prove:

1. An authorized message is durably dispatched for both harnesses, irrelevant
   output resolves to `HCTL_NO_REPLY`, visible output arrives as bounded
   replies, and the bot token is absent from source, generated files, state,
   logs, and child environments.
2. An exact write-access result promotes only its conversation into a
   validated branch-backed worktree, resumes the same session with write
   access, and continues the original request once without exposing control
   text.
3. Runtime-wide capacity limits keep accepted work durable under saturation,
   hibernate eligible idle capacity, and advance turns fairly without a model
   scheduler.
4. Concurrent guild and DM mutations use distinct worktrees and sessions,
   survive independent hibernation and restart, deliver out-of-order results
   only to their originating surfaces, and contain ordinary failures to one
   conversation.
5. Startup reconciliation preserves every busy, uncertain, dirty, unmerged,
   or unverifiable worktree and retires only clean merged assignments through
   restart-safe cleanup.
6. A transport-neutral interactive request survives restart in its owning
   conversation, accepts one authorized normalized answer exactly once, parks
   without consuming capacity, and preserves ambiguous delivery or
   continuation as uncertain without automatic replay.

Live credentialed acceptance is separate, authorization-gated, and recorded
in [the Discord acceptance record](workbench/discord-live-acceptance.md).

## Decision records

The channel domain's mechanics are recorded in ADRs 0014–0018, 0021–0025,
0028, 0032, and 0033. They document the prototype implementation and move
with this product; the core rebuild does not depend on them.
