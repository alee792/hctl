# Discord source example

The portable `channels/discord.md` declares ambient behavior and a participation
policy. It contains no Discord application, user, guild, channel, or credential
identity.

## Local setup

Create a Discord application and bot in the Developer Portal, enable Message
Content Intent, and copy its bot token. Then run:

```sh
hctl channel setup discord examples/discord
hctl channel status discord examples/discord
hctl run examples/discord --workspace /tmp/hctl-discord-workspace --harness codex
```

The setup wizard validates the bot, records one authorized user and guild
channel, stores the token in the OS credential store, and prints the bot install
URL. `run` automatically prepares and applies stale native setup before opening
an outbound Gateway connection. No public endpoint, TLS configuration, or tunnel
is needed.

Eligible messages in the configured channel and the authorized user's DM become
separate durable conversations. The agent buffers its response, posts bounded
chunks with mentions disabled, or emits exactly `HCTL_NO_REPLY` to stay silent.
Use `/new` to reset an idle conversation and `/status` for redacted runtime
status.

For deployment, mount an owner-only config file selected by `HCTL_CONFIG` and
inject the matching bot token as `HCTL_DISCORD_TOKEN`. The token is consumed by
hctl and removed from every Claude, Codex, MCP, and tool-host child environment.
