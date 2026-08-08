# ADR 0028: Use a conversational Discord Gateway channel

- Status: accepted
- Date: 2026-08-06
- Supersedes: [ADR 0012](0012-use-signed-discord-http-interactions.md)
- Amends: [ADR 0009](0009-use-a-local-secretless-operation-broker.md)
- Adapter ownership amended by: [ADR 0033](0033-package-discord-as-an-external-channel-adapter.md)
- Extended by: [ADR 0014](0014-manage-channel-session-lifecycles.md),
  [ADR 0015](0015-enforce-read-only-channel-sessions.md),
  [ADR 0016](0016-isolate-writable-channel-conversations.md),
  [ADR 0017](0017-bound-channel-runtime-capacity.md),
  [ADR 0018](0018-reconcile-and-retire-managed-worktrees.md), and
  [ADR 0025](0025-render-discord-input-with-bounded-native-components.md)

## Decision

The Discord channel uses Discord's outbound Gateway WebSocket and bot
REST API. It accepts ambient messages from one authorized user in one configured
guild channel and that user's DM, maps each surface to a durable turn-dispatch
conversation, buffers native-harness output, and replies in the same channel.
It requires no public listener, tunnel, TLS endpoint, or HTTP interaction key.

Portable `channels/discord.md` contains strict `mode: ambient` frontmatter and a
bounded participation policy. Apply fingerprints the original source and adds
the policy plus an exact `HCTL_NO_REPLY` control result to generated harness
instructions. Discord application identity, authorization IDs, profile choice,
and credentials are runtime configuration and never enter portable source.

Local enrollment through the external `hctl-discord` adapter stores non-secret
profile metadata in its OS-standard configuration directory and the token in
the OS credential store. Deployment mounts the adapter profile state and may
inject `HCTL_DISCORD_TOKEN`. Before connecting, the adapter queries Discord and
requires the token's application and bot IDs to match the selected profile. An
adapter-owned application-ID lock prevents two local runtimes from using one
identity concurrently.

The separately installed adapter is the trusted credential-consuming operation
boundary for Discord transport. Hctl passes an ambient compatibility value only
to that exact process and scrubs it from the model harness, MCP servers, authored
tool hosts, generated files, workspace state, diagnostics, and audit. The root
module imports no Discord SDK or credential implementation. ADR 0009 continues
to govern model-invocable secret-bearing managed tools and connections.

`hctl run` serves declared channels by default and auto-applies missing or stale
native setup without overwriting modified generated files. Explicit
`--input jsonl` preserves the headless stream interface. `/new` resets only an
idle surface; `/status` returns redacted runtime state without a model turn.

## Context

This decision originally replaced ADR 0012 in commit `3f47d3d`. Restoring the
superseded Interactions ADR under its original number preserves the decision
history; the later number records the already-shipped Gateway decision without
changing its effective date.

## Consequences

- Discord application creation and privileged-intent enablement remain manual.
- One profile supports one user, one guild channel, and that user's DM.
- Other users, channels, bots, and webhooks are discarded before dispatch.
- Replies are bounded, disable mentions, and are never retried after ambiguous
  delivery.
- Direct multi-agent routing, multiple channels/users, HTTP mode, proactive
  schedule delivery, hosted secret managers, and horizontal replicas are
  deferred.
