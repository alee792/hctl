# Discord source example

This credential-free agent source registers the built-in Discord channel by
directory convention. Applying it makes no Discord request and stores no
Discord identity or token:

```sh
mkdir -p /tmp/hctl-discord-workspace
hctl apply examples/discord --workspace /tmp/hctl-discord-workspace --harness codex
```

Running the channel additionally requires an already configured Discord
application's public application ID, public interaction key, and one allowed
user ID. Hctl does not register that application command, expose its loopback
listener, or supply TLS. The command must contain one string option named
`message`.
