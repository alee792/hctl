# ADR 0032: Use a bounded semantic protocol for external channel adapters

- Status: accepted
- Implementation: external process host wired on 2026-08-08
- Extends: [ADR 0030](0030-use-process-isolated-integration-packages.md)
- Specialized by: [ADR 0033](0033-package-discord-as-an-external-channel-adapter.md)
- Prepares the migration from: [ADR 0028](0028-use-a-conversational-discord-gateway-channel.md)
- Preserves: [ADR 0014](0014-manage-channel-session-lifecycles.md),
  [ADR 0015](0015-enforce-read-only-channel-sessions.md),
  [ADR 0016](0016-isolate-writable-channel-conversations.md),
  [ADR 0017](0017-bound-channel-runtime-capacity.md),
  [ADR 0021](0021-persist-durable-interactive-input-lifecycles.md), and
  [ADR 0025](0025-render-discord-input-with-bounded-native-components.md)

## Plain-English summary

Channel vendors run in exact installed executables rather than hctl's Go
process. Hctl and one selected adapter exchange only bounded, versioned channel
semantics over the child's standard input and output. Hctl continues to own
agent execution and durable conversation state; the adapter owns its vendor
connection, credentials, payloads, rendering, and rate limits. This separates
dependencies and responsibility. It is not an operating-system sandbox and it
does not hide a credential from a trusted adapter or another process running as
the same user.

This record defines the contract used by the Discord extraction. The generic
host now implements it and production channel routing selects the installed
external adapter. The old in-process implementation remains only as rollback
code until the separate dependency-removal delivery.

## Package capability

Schema 1 of the common integration envelope recognizes `channel-adapter`
capability version 1. It declares:

- one stable channel kind;
- the exact artifact ids forming its selective runtime and staged closure;
- one package-relative executable that matches each artifact's verified
  executable identity;
- fixed non-empty literal non-secret argument vectors for `runtime`, `setup`,
  `status`, and `remove` modes;
- a half-open adapter protocol version range that includes version 1;
- the one non-secret `opaque-id-v1` profile-selector contract; and
- a closed subset of `typing`, `replies`, `edits`, `reactions`, `attachments`,
  `interactive-components`, and `text-fallback` transport features.

Manifest validation reads no artifact and starts no process. Hctl appends only
the standardized `--profile PROFILE` pair to setup, status, and remove mode.
The runtime profile travels in the initialization frame instead. Profile ids
contain 1-64 lowercase letters, digits, and single hyphens, begin with a
letter, and are selectors rather than secrets, paths, environment-variable
names, commands, or credential references.

The live handshake may only narrow the manifest's feature declaration. It
cannot add a feature or widen a limit. Package installation and staging use
the capability's referenced artifacts, not every artifact in the package.
Every mode uses the verified package root as its fixed working directory; the
manifest cannot select a workspace or ambient directory.

## Process topology and modes

For one configured channel runtime, hctl resolves one installed, enabled,
compatible capability and starts its exact verified executable directly. It
does not search a shell path, invoke a shell, load a library, open a TCP
listener, create a workspace socket, use a Go plugin, or accept in-process
registration. There is one adapter process and one protocol connection for
that owning runtime.

Runtime stdin and stdout carry newline-delimited protocol frames only. Any
other stdout is a fatal protocol violation. Stderr is a bounded diagnostic
surface, not a second protocol or evidence stream. Hctl owns launch,
supervision, deadlines, cancellation, graceful shutdown, forced shutdown, and
complete child-process-tree cleanup. The adapter owns vendor connect,
reconnect, heartbeat, transport retry where safe, rate limits, and ephemeral
vendor state.

Setup, status, and remove use the exact declared mode arguments. Setup and
remove may use the inherited trusted terminal directly for prompts and hidden
secret entry; secrets do not use protocol stdin/stdout. Each mode writes at
most one closed, 16-KiB, non-secret operation-result JSON object to stdout and
then exits. The result contains only operation, profile id, a stable status,
and bounded safe identity/message text. Status is non-interactive. The adapter
owns enrollment, validation, lookup, replacement, rotation, and removal.
Setup keeps trusted stdin and stderr attached and has a separate ten-minute
human enrollment deadline. Remove also retains trusted terminal input; status
receives no stdin. Status and remove retain the ordinary 30-second bound.
For a controlling terminal, hctl gives the adapter's private process group
foreground ownership before exec and restores the original foreground group on
every exit path. Caller cancellation or an interrupt kills the complete
private process group and waits only through the forced-reap bound. The
official adapter leaves terminal interrupt signals at their default disposition
during blocking setup prompts, so Ctrl-C terminates that foreground group.

## Runtime handshake and semantic messages

The adapter first sends `hello` with its channel kind, supported half-open
protocol range, declared features, and limits. Within five seconds hctl checks
those values against the installed manifest and sends one correlated
`initialize` selecting version 1, a non-secret profile id, accepted features,
negotiated limits, and transport-neutral ambient participation ceilings. The
adapter sends one correlated `ready` that can narrow those features and limits.
No vendor connection is admitted to the controller before this exchange
completes.

`ready` may also declare the adapter's bounded startup surfaces so hctl can
reattach durable work before accepting replay. Every surface carries an opaque
route, a stable transport-neutral conversation id, an explicit `direct` or
`shared` participation kind, and two lowercase SHA-256 owner keys. Later
messages and controls repeat that semantic identity; changing it or reusing one
conversation id for another route is fatal. Vendor ids and authorization
material remain adapter-owned.

Every frame has protocol version, a bounded stable frame id, a closed kind, an
optional kind-governed correlation id, and exactly one strictly decoded
payload. The version-1 union contains:

Adapter to hctl:

- normalized inbound message with a stable source id, opaque route, message,
  and author handles, semantic plain text, and bounded attachment descriptors;
- an authorized semantic `status` or `reset` control request so vendor command
  decoding remains adapter-owned while hctl retains lifecycle ownership;
- normalized interactive answer or cancellation tied to an hctl-issued
  request and stable semantic field/option ids;
- an exact, ambiguous, or failed interaction-render receipt correlated to the
  stable hctl interaction id;
- exact, ambiguous, or failed delivery/attachment disposition;
- connection states `connecting`, `ready`, `reconnecting`, `degraded`, and
  `closed`;
- bounded base64 attachment chunks authorized by an hctl fetch; and
- classified configuration, authentication, connection, rate-limit, protocol,
  or internal diagnostic plus shutdown completion.

Hctl to adapter:

- initialization and durable adapter-event acknowledgement;
- a correlated status/reset result containing only bounded agent/harness,
  lifecycle, queue, and capacity fields or a stable busy/failure disposition;
- typing, active, or idle activity intent tied to an opaque route;
- semantic reply/delivery intent tied to an opaque route and optional message
  reference;
- the existing bounded confirm, choose-one, choose-many, text, date/time, or
  form interaction, a recovery-only restore intent, and a separate
  cancellation;
- bounded attachment fetch or in-band delivery chunks with an explicit
  transfer id and maximum; and
- shutdown.

Handles use a bounded opaque alphabet and have no meaning to hctl. Adapter
callback ids and render positions never leave the adapter. Vendor payloads,
SDK objects, tokens, raw environment, arbitrary component or markup trees,
executable code, commands, URLs carrying authority, and filesystem or
workspace paths have no protocol representation. Text remains semantic user or
agent content; the adapter owns vendor escaping, chunking, mentions, reply
references, buttons, selects, modals, reactions, attachment transport, and
vendor-native command decoding. Hctl still owns status and reset behavior.

The interaction payload retains ADR 0021's tighter 32-KiB request, 16-KiB
answer, prompt, fallback, field, option, and text ceilings. Hctl remains the
authority that validates and normalizes answers against the durable original
request. The adapter cannot change cancellation, expiry, continuation,
authorization, or ownership state.
An exact render receipt marks the durable interaction delivered. An ambiguous
or missing receipt is never retried. On restart, hctl publishes startup
surfaces to the controller first, then restores already-delivered interactions
without creating another vendor message and renders only work still durably
pending; adapter input replay begins afterward.

## Credentials and authority

Hctl supplies only the profile id. It does not send a token, secret reference,
path, environment-variable name, or raw environment in a frame. The adapter is
the trusted credential consumer and must never emit the credential through an
operation result, runtime frame, stderr, diagnostic, audit, or retained test
evidence.

For migration compatibility, a channel-specific ambient value such as
`HCTL_DISCORD_TOKEN` may already be present in the hctl launcher environment.
The process host may pass that opaque value only to the exact selected
adapter and must remove it from every harness, MCP server, authored tool host,
Git process, schedule, setup for another adapter, and unrelated child. Hctl
does not parse the value or place it in an argument or frame. The adapter owns
runtime consumption and identity validation.

Process separation removes vendor dependencies and limits accidental
ownership, but a malicious adapter or same-user peer process may read or misuse
resources available to that user. This contract is not a credential broker,
authorization proxy, container boundary, or OS sandbox.

## Bounds and backpressure

Version 1 fixes implementation ceilings; handshake values may only lower
them:

| Surface | Ceiling |
| --- | ---: |
| One JSONL frame | 256 KiB |
| Semantic text in one message/delivery | 64 KiB |
| Classified diagnostic payload | 4 KiB |
| Attachments on one message/delivery | 16 |
| One attachment transfer | 16 MiB |
| One decoded attachment chunk | 64 KiB |
| Concurrent attachment transfers | 4 |
| Outstanding correlations or unacknowledged events | 128 |
| In-memory protocol queue | 64 frames and 8 MiB |
| Retained stderr per process | 64 KiB |
| Setup/status/remove result | 16 KiB |

Ordinary commands have a 30-second response deadline, deliveries 30 seconds,
attachment transfers 60 seconds, and graceful shutdown five seconds followed
by at most two seconds for forced tree cleanup. The process host may impose a
smaller operation-specific deadline.

The negotiated frame ceiling is installed on both decoder and encoder. The
negotiated semantic text and attachment ceilings apply in their respective
directions, while `max_outstanding` bounds live correlations, retained event
receipts, remembered reply targets, startup surfaces, and newly admitted
surfaces. Reply-target saturation rejects new input before controller admission
and never evicts an older accepted target. Startup replay is independently
bounded to 64 frames and 8 MiB before durable recovery opens admission, while
unique unacknowledged semantic events are also bounded to the negotiated
outstanding limit. Connection/diagnostic frames and same-id event replay remain
inside the fixed queue bounds without consuming another event slot. The host
reserves room for one negotiated maximum-size frame before starting each read.
At capacity it stops reading so the adapter pipe applies backpressure until
recovery completes or its bounded deadline expires. A bounded response-read
path remains open for a pending recovery correlation, so connection state or
event replay cannot deadlock the required receipt.
Admission waits only to its operation deadline and never creates an unbounded
goroutine or map.

Neither side may silently drop, reorder, merge, or overwrite a semantic frame.
When its bounded queue is full it stops admission and applies pipe
backpressure. Failure to make progress before the applicable deadline is fatal
to that adapter runtime. It does not consume unrelated channel, schedule, or
interactive native-harness capacity.

## Replay, ambiguity, and failure

An adapter keeps an event frame id and exact bytes stable until hctl returns an
event acknowledgement. Hctl acknowledges normalized inbound input only after
the dispatcher durably accepts it or durably classifies a duplicate/rejection.
An exact duplicate id with the same canonical content is idempotent and may be
acknowledged again; the same id with different content is fatal. The stable
source id also enters the existing durable dispatcher deduplication boundary,
so reconnect or process restart cannot create another accepted input.

Connection reconnect is adapter-owned and does not authorize replay of an hctl
effect. Hctl never automatically resends a delivery, interaction render,
cancellation acknowledgement, or outbound attachment after the complete
command reached the child but no exact pre-attempt failure was returned. A
missing result, partial response, disconnect, deadline, or child exit after
that boundary is ambiguous. It is committed as uncertain through the existing
controller contract and is not retried. Exact failure means the adapter proves
the vendor effect was not attempted. An adapter may deduplicate a repeated
command id defensively, but hctl does not rely on process-local memory for
exactly-once claims.

Malformed JSON, duplicate keys, an oversized or unterminated line, unknown
field/kind/enum, wrong direction, correlation mismatch, protocol text on
stderr, or non-protocol stdout terminates only the owning adapter runtime.
Version negotiation has no permissive fallback: no common supported version
fails before initialization. Unknown later fields and versions require a new
closed protocol version.

A child exit before `ready` is startup failure. After readiness, non-effecting
pending commands fail; a fully written pending effect is ambiguous as above.
Hctl stops new admission, cancels adapter-owned interaction UI, sends shutdown,
and then kills the complete process tree after the five-second deadline. Reap
after a forced kill is independently bounded by two seconds, and controller
cleanup has its own bounded drain so a stuck controller cannot retain the
adapter process. Adapter stderr is capped at 64 KiB, strips unsafe controls,
redacts inherited credential values across arbitrary stream-write boundaries,
and suppresses protocol-shaped content instead of retaining it before
terminating that adapter runtime. The 64-KiB ceiling counts emitted sanitized
bytes, including diagnostic prefixes, rather than untrusted raw input. Adapter
reconnect does not write dispatcher, session, worktree, capacity, or interaction state.
Hctl's existing durable store remains the only writer.

## Ownership and dependency direction

Hctl core retains portable channel policy, surface-to-conversation mapping,
the channel controller, dispatcher, harness sessions, execution policy,
worktrees, capacity and hibernation, generic durable state, interaction
normalization, and delivery uncertainty. The adapter retains vendor
credentials and profiles, SDK and payload decoding, source authorization,
Gateway or equivalent transport, rendering, callback validation, rate limits,
mentions, replies, reactions, edits, attachments, and vendor diagnostics.

The dependency-free `hctl/channeladapter` module contains only the wire schema,
validators, codec, constants, and credential-free fixtures. Hctl's process
host depends on that semantic contract through small
responsibility-specific seams for package selection, process construction,
transport, clock/deadlines, controller handoff, and diagnostics. An external
Discord module may depend on the same contract but never on hctl's internal
controller, dispatcher, harness, project, session, worktree, or credential
packages. Hctl never imports the Discord module.

There is no vendor-named core interface, service container, locator, reflection
registry, capability grab bag, generic plugin lifecycle, or switch on a vendor
name. Capability lookup selects a closed `channel-adapter` declaration by
channel kind; the process host implements the transport-neutral controller
seam.

## Consequences

- A deterministic fake proves handshake, inbound input, reply, interaction
  answer, cancellation, status/reset delegation, reconnect, exact and ambiguous
  delivery, delivered-interaction restore, concurrent conversation
  hibernation/resume, attachments, child failure, bounded shutdown, and
  malformed/oversized failure without credentials or a vendor SDK.
- A second no-op fixture uses the same protocol without Discord imports,
  showing that the seam is capability-shaped rather than vendor-shaped.
- Discord extraction can preserve the literal
  `channels/discord.md` setup/status/remove/run journey while moving SDK and
  credential ownership out of hctl's root module.
- Production does not fall back to the retained in-process Discord adapter.
  Its code remains temporarily for rollback and is removed by the separate
  dependency-cutover delivery.
- TCP transports, public sockets, dynamic libraries, arbitrary plugins,
  brokered credentials, hosted adapters, remote channel protocols, and a
  universal integration runtime remain out of scope.
