# ADR 0033: Package Discord as an external channel adapter

- Status: accepted
- Specializes: [ADR 0032](0032-use-a-bounded-semantic-channel-adapter-protocol.md)
- Extracts transport ownership from: [ADR 0028](0028-use-a-conversational-discord-gateway-channel.md)
- Preserves interaction behavior from: [ADR 0025](0025-render-discord-input-with-bounded-native-components.md)

## Plain-English summary

The official Discord integration is now built as `hctl-discord`, a separate Go
module and executable. It owns DiscordGo, Gateway and REST payloads, Discord
rendering, application locks, credentials, and non-secret profiles. Hctl does
not import the module. The two processes will communicate only through the
bounded `hctl/channeladapter` version-1 protocol once the generic process host
is added.

## Decision

The integration package id is `hctl-discord`; its one capability has id and
channel kind `discord`, type `channel-adapter`, and version 1. The exact fixed
modes are `run --stdio`, `setup`, `status`, and `remove`. Setup, status, and
remove receive only hctl's standardized non-secret `--profile` selector. The
manifest advertises the protocol range `[1,2)` and the seven closed Discord
features: typing, replies, edits, reactions, attachments, interactive
components, and text fallback.

The adapter module depends only on the dependency-free `hctl/channeladapter`
wire module and its own vendor dependencies. It does not import hctl root
internals, controller, dispatcher, harness, interaction store, project,
session, worktree, or credential packages. Narrow seams for Discord transport,
credential storage, profile storage, application locking, and time make
credential-free acceptance deterministic without creating an hctl-wide
dependency-injection framework.

The adapter owns:

- bot identity and configured guild/channel/user scope validation;
- the outbound Gateway connection, reconnect lifecycle, slash-command and
  callback decoding, rate-limit behavior supplied by DiscordGo, and bounded
  vendor diagnostics;
- authorization and normalization of inbound messages and attachments;
- mention-disabled replies, edits, reactions, attachment transport, native
  components and modals, and bounded text fallback; and
- exact/ambiguous effect classification, with no automatic retry after an
  uncertain Discord write.

Hctl will continue to own portable participation policy, controller and
dispatcher state, model execution, sessions, worktrees, capacity, hibernation,
and durable generic interaction state. This issue deliberately does not wire
the external process into those owners or remove the in-process adapter; those
are the process-host and final-cutover deliveries.

## Credentials and profiles

The keyring service remains exactly `hctl.discord`, and the non-secret profile
id remains the keyring account, so existing credentials are not stranded.
Adapter-owned profiles use an owner-only file beneath the OS user configuration
directory. When that file lacks a selected profile, the adapter can read the
former owner-only hctl `config.toml` Discord profile shape, validate it, and
atomically migrate only that selected non-secret profile.

Setup reads a token through the inherited trusted terminal (hidden when it is
a terminal), validates bot identity and authorization scope, and uses a
compensating rollback for credential replacement when profile publication
fails. Remove likewise attempts to restore the profile if keyring deletion
fails. Either operation reports an explicit actionable error if its rollback
also fails instead of claiming that the prior state was restored.
`HCTL_DISCORD_TOKEN` remains an explicit
deployment compatibility input for setup, status, and runtime. It is consumed
only inside the selected adapter and never appears in a command argument,
profile, operation result, protocol frame, diagnostic, log, package artifact,
or test evidence.

The operation result contains only operation, profile id, stable status,
bounded bot label, and a safe message. Runtime receives only the profile id in
the initialization frame. Process separation is dependency and ownership
isolation; it is not a secret broker, authorization proxy, OS sandbox, or
defense from a malicious same-user process.

## Artifact and evidence

The separate module has its own `go.mod`, `go.sum`, and third-party license
record. A deterministic builder supports Darwin and Linux on amd64 and arm64,
uses trimmed build paths and no build id, and emits one package-local binary
plus an exact schema-1 manifest containing its measured size and SHA-256. A
credential-free root test builds that external module, installs it through the
shared operator-trusted store, resolves the exact `discord` capability offline,
and selectively stages its executable through the same #76 boundary used by
other integration packages.

Fake Discord, credential, profile, lock, and protocol counterparts prove
setup/status/remove, migration, identity, Gateway lifecycle, authorized inbound
messages, replies, interactions, reconnect, cancellation, delivery ambiguity,
shutdown, and strict malformed input without a real token or upstream. The
shared protocol module retains the exhaustive malformed and oversized frame
evidence.

## Consequences

- DiscordGo, WebSocket, keyring, and Discord transport tests now have an
  independently buildable dependency home, while the old root imports remain
  temporarily until cutover.
- Installing this package does not activate Discord. The later process host
  must select the verified capability and preserve the existing literal hctl
  channel journey before the old adapter can be removed.
- Release automation can build exact platform packages without rebuilding the
  hctl executable. A multi-platform release is a set of exact platform package
  manifests; apply and capability resolution remain offline.
- Live Discord acceptance still requires explicit authorization and a
  temporary least-privilege bot credential. It is not required for automated
  evidence.
