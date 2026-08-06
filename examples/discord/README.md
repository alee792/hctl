# Discord source example

This credential-free agent source registers the built-in Discord channel by
directory convention. Applying it makes no Discord request and stores no
Discord identity or token.

## Local preparation

Create a workspace, apply the source, then start the loopback runner with the
public identity values from a Discord application:

```sh
mkdir -p /tmp/hctl-discord-workspace
hctl apply examples/discord --workspace /tmp/hctl-discord-workspace --harness codex

hctl channel discord examples/discord \
  --workspace /tmp/hctl-discord-workspace \
  --harness codex \
  --application-id "$DISCORD_APPLICATION_ID" \
  --public-key "$DISCORD_PUBLIC_KEY" \
  --allowed-user "$DISCORD_USER_ID"
```

The local endpoint is `http://127.0.0.1:8787/interactions`. The default durable
conversation is scoped to the application and allowed user. Supply
`--conversation ID` only when intentionally selecting another existing context.

## External handoff

Discord requires a public HTTPS Interactions Endpoint URL. Route one URL such
as `https://agent.example/interactions` through infrastructure you control to
the exact loopback endpoint above. Keep every other local path private. Hctl
does not provide the domain, TLS certificate, reverse proxy, tunnel, firewall,
deployment, or availability layer.

In the Discord Developer Portal, set that public URL as the application's
Interactions Endpoint URL. Discord will send a signed PING and expects the
runner's PONG. Using Discord's application-command registration API, register
one command with this shape; the command name itself is operator-selected:

```json
{
  "name": "ask",
  "description": "Ask the local agent",
  "type": 1,
  "options": [
    {
      "name": "message",
      "description": "Message for the agent",
      "type": 3,
      "required": true
    }
  ]
}
```

These portal, command-registration, public-network, and real-message steps are
live external actions. They were not performed by the repository tests and
require an application owner, appropriate credentials, and explicit human
authorization.

## Visible result and recovery

When the allowed user's command is admitted, Discord should show a loading
state immediately. After gateway acceptance and the native harness turn, hctl
edits that original response and may add up to five followups. If the local
queue is already full before admission, Discord shows an immediate private busy
response. If the durable gateway rejects after the defer, the loading response
becomes a stable bounded error. If work is still pending after 14 minutes from
Discord's signed timestamp, hctl replaces the loading response with an expiry
notice and releases the in-memory response token; this does not interrupt the
harness.

At most 32 completed responses are delivered concurrently. If that separate
limit is saturated, hctl drops the detached response state without retrying and
records `delivery=saturated` in its content-free audit.

Gateway input and session state remain durable, but the interaction token does
not. After a process restart, Discord can receive a known terminal status only
if it redelivers the same signed interaction while its token is valid. Hctl
does not retry ambiguous outbound delivery or reconstruct old response tokens.
The separately tracked live-acceptance task must verify this journey before it
is claimed against a real Discord application.
