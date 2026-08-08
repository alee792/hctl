# ADR 0020: Map plugin MCP through native harness configuration

- Status: accepted
- Extended by: [ADR 0029](0029-bound-authored-projects-with-aggregate-budgets.md)

## Plain-English summary

Valid MCP servers declared by a vendored Agent Plugins v1 dependency are added
to the selected harness's native project configuration. Claude Code and Codex
remain responsible for starting, approving, authenticating, and operating
those servers. Hctl validates and translates the package declaration; it does
not proxy or manage plugin MCP traffic.

## Decision

An accepted plugin may contain an optional bounded `mcp.json` targeting the
exact Agent Plugins v1.0.0 MCP schema identifier. Hctl implements the supported
schema locally without fetching it. A malformed top-level document disables
only that plugin's MCP component. Invalid individual servers, unsupported SSE
servers, duplicate names, and the reserved `managed` name warn and are skipped
without suppressing valid sibling servers or skills.

Hctl supports `stdio` and `streamable-http`. Stdio commands are either a bare
executable name or a plugin-relative `./` path. Plugin-relative commands and
plugin-root working directories must be bounded real paths without symlinks.
`${PLUGIN_ROOT}` and `${PLUGIN_DATA}` are expanded once in arguments,
environment values, and working directories only. Hctl supplies both variables
to every stdio server and creates one private, persistent workspace-local data
directory per agent-and-plugin identity before writing native configuration.
Existing data-root permissions are normalized to owner-only and verified on
later use.

Remote URLs must be absolute HTTP(S) URLs without credentials or fragments.
Non-loopback endpoints require HTTPS. Headers must have valid HTTP names and
values, may not collide case-insensitively, are copied literally, and therefore
must not contain secrets.

Plugin directories and server names are considered in lexical order. The first
exact server name wins and names are never rewritten. Accepted server values,
plugin-relative command content, and executable intent join the source
fingerprint. Claude receives project `.mcp.json` entries and uses its native
project-server approval. Because Claude's project MCP format has no working
directory field, its stdio entry uses the system `/usr/bin/env -C` exec adapter
to preserve the portable working directory before immediately replacing itself
with the declared command. Codex receives project `.codex/config.toml` entries;
plugin servers are optional and use prompt approval. Hctl's own `managed` server
retains its existing required and approved policy.

Claude performs a second environment-expansion pass over project MCP values.
When unsupported placeholder-like text would survive portable expansion, hctl
skips that server for Claude with a warning rather than risk substituting an
ambient secret or changing a value that the portable specification treats as
literal. Codex preserves that text unchanged.

## Consequences

- Hctl does not start, proxy, supervise, authorize, observe, retry, or audit
  plugin MCP server calls.
- Plugin data survives server or plugin removal; hctl never treats it as an
  owned generated file.
- Package headers are visible source configuration, not a credential channel.
- SSE, OAuth, portable credentials, client extensions, installation, updates,
  downloads, and marketplaces remain unsupported.
- Native clients may still reject a valid portable declaration if their own
  capabilities or policies differ.

## Sources

- [Agent Plugins specification v1.0.0](https://agent-plugins.org/specification)
- [Canonical MCP schema](https://agent-plugins.org/schemas/1.0.0/mcp.schema.json)
- [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp)
- [Codex MCP documentation](https://developers.openai.com/codex/mcp/)
- [Product specification](../product-spec.md)
