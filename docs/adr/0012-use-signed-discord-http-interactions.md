# ADR 0012: Use signed Discord HTTP Interactions

- Status: accepted

## Decision

The first channel is a built-in Discord HTTP Interactions adapter registered by
a bounded description at `channels/discord.md`. The filename supplies the
channel name. `hctl channel discord` runs one loopback listener, selected native
harness, hctl conversation, Discord application, and allowed user. Application
identity, Ed25519 public key, allowed user, listener, and interaction tokens are
runtime values and never agent-source or generated-harness configuration.

The adapter accepts Discord PING and one application command containing exactly
one string option named `message`. It verifies Discord's signature over the
timestamp and raw body, bounds timestamp skew, validates the application, and
authorizes the configured user before it normalizes the interaction to the
existing gateway. The Discord interaction ID is the gateway input ID. The
gateway remains authoritative for durable acceptance, FIFO ordering,
deduplication, native-session continuation, and uncertain restart recovery.

After signature, identity, authorization, and input validation, the adapter
admits at most 32 pending interactions. An admitted command flushes Discord's
deferred response immediately and submits to the gateway asynchronously. It
does not wait for durable acceptance because Discord
invalidates tokens whose initial response misses three seconds. A later
queue-full or other gateway rejection edits the deferred original with stable
text. The gateway still reports acceptance before it can start a model turn and
remains authoritative for FIFO and deduplication. The adapter retains no more
than 32 pre-acceptance interactions; overflow receives an immediate ephemeral
queue-full response without token retention, outbound delivery, or a harness
turn.

The adapter keeps the interaction token only in memory, gathers bounded text
deltas, and uses fixed Discord webhook response paths to update the original
response and send at most five 2,000-rune followups. Mentions are disabled.
Discord documents a 15-minute token lifetime; hctl updates a still-pending
response after 14 minutes from its signed timestamp, then releases the token,
turn, and buffered output without interrupting the harness. HCTL-012 must
separately decide runtime-turn timeout behavior. Expiry updates bypass the
ordinary output queue, and state is released before outbound delivery. Requests
time out, do not follow redirects, bound response bodies, and are never retried.
Transport and
malformed-success failures are ambiguous and audited as uncertain; explicit
rate limits and non-success responses are classified without retaining
upstream bodies.
At most 32 ordinary terminal deliveries run concurrently; saturation releases
the detached token and output without an outbound attempt or retry and emits
only a safe classified audit. Expiry uses its existing bounded timer worker and
bypasses that ordinary-delivery limit.

## Why this does not select the credential broker

The interaction token is a short-lived continuation capability delivered by a
verified inbound request and scoped by Discord to responding to that one
interaction. Hctl does not look it up, enroll it, reuse it across interactions,
or expose it to the harness, model, gateway state, generated setup, logs, or
audit. Keeping it inside the concrete adapter until the bounded response is
therefore smaller and safer than inventing ADR 0009's future credential backend
for an inbound continuation.

A Discord bot token would be different: it is reusable account authority for
proactive operations. Gateway bots, proactive sending, command registration,
and any other secret-bearing Discord operation remain deferred and must satisfy
ADR 0009 before they ship.

## Context

Eve establishes the useful author-facing precedent: a conventional `channels/`
entry has a path-derived identity, normalizes vendor input, owns continuation,
and can defer and follow up on a turn. Eve also owns its runtime and hosted
delivery. Hctl instead extends Claude Code and Codex, so its adapter feeds the
existing local gateway and does not copy Eve's deployment, persistence, or bot
runtime.

Discord HTTP Interactions provide a concrete signed inbound transport without a
long-lived Gateway connection or bot credential. Discord requires an initial
response within three seconds, keeps interaction tokens valid for 15 minutes,
and limits a user-installed app outside a server to five followups. Immediate
defer, a 14-minute local expiry, and six total bounded messages fit those
constraints while preserving hctl's managed boundary.

## Consequences

- Agent authors add one readable Markdown file; there is no channel manifest or
  vendor configuration in source.
- Unless the operator selects an existing conversation explicitly, the runner
  derives a stable conversation from the application and allowed user.
- Apply validates and fingerprints the channel but makes no network request and
  generates no additional harness file.
- Operators must supply a public HTTPS endpoint and Discord command registration
  outside hctl. Hctl intentionally binds only plain loopback HTTP and provides
  no tunnel, TLS, deployment, or registration automation.
- Ordinary Discord messages, mentions, components, modals, typing, interruption,
  OAuth, bot tokens, and proactive delivery are not supported.
- If the process ends, the interaction token is lost. Durable gateway work may
  recover as uncertain, but hctl neither persists the token nor retries an
  ambiguous Discord delivery.
- A turn may outlive Discord's response window. The adapter reports that expiry
  to Discord and releases channel state; it does not claim to interrupt the
  native harness.

## Sources

- [Eve channels overview](https://github.com/vercel/eve/blob/84c3dfc1ff91e075444eee7c6d8e2ef55b2aaebe/docs/channels/overview.mdx)
- [Eve Discord channel](https://github.com/vercel/eve/blob/84c3dfc1ff91e075444eee7c6d8e2ef55b2aaebe/docs/channels/discord.mdx)
- [Discord receiving and responding to interactions](https://docs.discord.com/developers/interactions/receiving-and-responding)
- [ADR 0009](0009-use-a-local-secretless-operation-broker.md)
