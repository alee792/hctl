# Discord source example

The portable `channels/discord.md` declares ambient behavior and a participation
policy. It contains no Discord application, user, guild, channel, or credential
identity.

## Local setup

Build or download the official adapter package for the current platform, then
install it with explicit operator trust and verify the cached closure. A local
source build looks like:

```sh
./discordadapter/build-package.sh \
  --version 0.1.0 --revision "$(git rev-parse HEAD)" \
  --target "$(go env GOOS)-$(go env GOARCH)" \
  --output /tmp/hctl-discord-package
hctl integration install /tmp/hctl-discord-package --trust operator
hctl integration verify hctl-discord
```

Create a Discord application and bot in the Developer Portal, enable Message
Content Intent, and copy its bot token. Then run the literal installed-adapter
journey:

```sh
hctl channel setup discord examples/discord
hctl channel status discord examples/discord
hctl run examples/discord --workspace /tmp/hctl-discord-workspace --harness codex
hctl channel remove discord examples/discord
```

The external adapter's setup wizard validates the bot, records one authorized
user and guild channel, stores the token under the existing `hctl.discord`
keyring identity, and prints the bot install URL. Hctl records only the opaque
profile selection. `run` automatically prepares and applies stale native setup
before starting the exact installed adapter and its outbound Gateway
connection. No public endpoint, TLS configuration, or tunnel is needed.

Eligible messages in the configured channel and the authorized user's DM become
separate durable conversations. The agent buffers its response, posts bounded
chunks with mentions disabled, or emits exactly `HCTL_NO_REPLY` to stay silent.
Use `/new` to reset an idle conversation and `/status` for redacted runtime
status.

For deployment, mount the adapter's owner-only profile state and inject the
matching bot token as `HCTL_DISCORD_TOKEN`. Hctl treats that value as opaque,
passes it only to the exact selected adapter, and removes it from every Claude,
Codex, MCP, tool-host, and unrelated adapter child environment. Process
separation keeps dependencies and ownership clean; it is not an OS sandbox or
credential broker.

An agent staged with `hctl stage` carries the selected current-platform adapter
closure and an agent-bound non-secret descriptor. An agent without
`channels/discord.md` carries neither. Runtime profiles, tokens, conversation
state, and harness login state are never staged.
