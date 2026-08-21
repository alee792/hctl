# ADR 0034: Author generic native MCP connections

- Status: accepted
- Date: 2026-08-08
- Extends: [ADR 0001](0001-use-native-harnesses.md),
  [ADR 0029](0029-bound-authored-projects-with-aggregate-budgets.md), and
  [ADR 0030](0030-use-process-isolated-integration-packages.md)
- Amends: [ADR 0031](0031-use-the-official-github-server-as-native-unmanaged-mcp.md)
- Reuses: [ADR 0020](0020-map-plugin-mcp-through-native-harness-configuration.md)

## Plain-English summary

An author can add a standalone MCP server by creating one readable Markdown
file under `connections/` or by using `hctl connection add`. Frontmatter either
selects an exact operator-installed stdio capability or names one
credential-free HTTPS Streamable HTTP endpoint. The optional body gives the
agent extra usage context. Hctl validates and compiles either form into native
Claude Code or Codex project configuration; it does not add a provider adapter
or become the MCP runtime.

## Decision

Discover up to 128 immediate, real, regular UTF-8 files named
`connections/<name>.md`. Symlinks, directories, nested entries, other
extensions, and over-limit inventories reject the project before workspace
mutation. Each file is at most 8 KiB. The filename supplies the connection and
native server name. A name contains 1-64 characters, starts with a lowercase
ASCII letter, and otherwise contains lowercase ASCII letters, digits,
underscores, or hyphens. `managed` is reserved for hctl.

Every file starts with one closed YAML frontmatter mapping whose plain string
field `type` is exactly `mcp`. It then selects exactly one target.

An installed target has exactly these fields:

```md
---
type: mcp
package: github-mcp-server
capability: github
---

Use the discovered GitHub tools for repository, issue, and pull-request work.
```

`package` and `capability` use ADR 0030's validated identifiers and select one
installed, enabled, trusted, compatible `native-mcp` version-1 capability.
That capability already fixes stdio transport, executable, arguments, working
directory, literal non-secret environment defaults, ambient environment names,
startup, trust ownership, and supported harnesses. Authored source does not
repeat or override those values. The capability's stable `server_name` must
equal the filename-derived connection name.

A remote target has exactly these fields:

```md
---
type: mcp
transport: streamable-http
url: https://example.com/mcp
---

Use this connection for the public reference catalog.
```

The URL is an absolute HTTPS URL with a nonempty host and no user information,
query, or fragment. Hctl retains it exactly after validation. The first slice
has no headers, bearer-token or environment references, OAuth settings,
timeouts, tool filters, approval settings, provider names, frozen tool
catalogs, or other transport parameters. Hctl does not contact the endpoint,
resolve DNS, inspect TLS, follow a redirect, discover authentication, or prove
server compatibility during add, status, apply, or stage. An endpoint that
requires authentication is not a supported hctl journey in this slice, even if
a native harness can separately authenticate it.

All frontmatter fields are required for their selected form, unknown or
duplicate fields fail, and installed and remote fields cannot be mixed. YAML
aliases, tags, merge keys, non-string keys or values, and multiple documents
fail. The optional Markdown body is trimmed. Empty or whitespace-only content
means no additional context; nonempty content contains at most 1,024 Unicode
characters. Exact source bytes, including frontmatter and body, participate in
the project fingerprint.

When at least one connection exists, generated instructions contain one
bounded `Native MCP connections` section. Connections appear in lexical name
order. Each name appears once and its nonempty body appears once, without the
frontmatter. The section states once that Claude Code or Codex owns native MCP
startup, trust, approval, authentication, discovery, calls, and effects. The
body is trusted project guidance; it is not sent to the upstream server and
does not replace tool descriptions, schemas, or server-returned instructions.

### Native generation and staging

Installed targets reuse ADR 0030's offline resolver and existing generic
launch descriptor. Claude receives the existing project stdio mapping, using
`/usr/bin/env -C` for the verified working directory. Codex receives the exact
command, arguments, working directory, environment defaults and ambient names,
with the package-declared optional or required startup and prompt approval.

Remote targets reuse the existing safe native HTTP renderer. Claude's project
`.mcp.json` receives `type: "http"` and the exact URL. Codex's project
`.codex/config.toml` receives the exact `url`, `enabled = true`,
`required = false`, and prompt approval. Neither target receives an auth or
header field. Remote connections are startup-optional. Apply and stage never
open the URL.

Selective staging copies the exact installed capability closure only for an
installed target. A remote target contributes native configuration and authored
source but no integration package bytes. Agents without a connection generate
and stage no corresponding server or package closure. Hctl-owned headless
process opens re-resolve installed targets through the same generic current
state guard; remote runtime health remains native-harness state.

`managed`, another standalone connection, or a Plugin MCP server cannot own the
same generated server name. A standalone connection collision is a project
error before mutation; hctl does not rename, shadow, or skip it. A selected
installed capability whose server name differs from its connection filename is
a target mismatch and also fails before mutation. Existing warning-and-first-wins behavior among independently
optional Plugin MCP declarations remains unchanged. Claude application also
rejects the lowercase names `workspace`, `claude-in-chrome`, and
`computer-use`, which its native project surface reserves. Hctl cannot
reliably preflight user, administrator, enterprise, or future harness-owned
configuration; native precedence and diagnostics still govern those sources.

### Author commands

The first authoring journey is:

```text
hctl connection add AGENT NAME --package PACKAGE --capability CAPABILITY [--context TEXT]
hctl connection add AGENT NAME --url HTTPS_URL [--context TEXT]
hctl connection status AGENT [NAME]
hctl connection remove AGENT NAME
```

Every command requires the exact positional agent root. A caller already in
that root uses `.` explicitly. An `instructions.md`, or a supplied manifest
whose expected fingerprint matches the directory, proves the selected
directory is an agent project (amended by the instructions-optional
decision; see the product specification's Instructions section). Commands
never search ancestors, infer a parent `agents/` directory, or select a
workspace or harness.

`add` validates the root, name, selected union, optional context, directory,
and collision before atomically creating `connections/NAME.md`. It never
overwrites an existing path. Package add also performs the same offline exact
resolution used by apply, verifies that the capability server name matches
`NAME`, and writes nothing when the package is missing, disabled, incompatible,
corrupt, or unsupported on the current platform. Remote add validates the URL
without contacting it. Neither form applies a workspace or installs, enables,
trusts, updates, or removes an integration package.

`status` with no name inspects every connection in lexical order; with a name
it inspects only that exact source. It reports the declared target and bounded
context presence. Installed status performs offline exact resolution and
reports package/capability health plus supported harness targets without
executing the package. Remote status reports `configured` and
`runtime=unchecked`; it makes no network request. Any malformed selected source
or unresolved installed target yields a nonzero status while retaining bounded
authored-path diagnostics.

`remove` requires the exact root and name and deletes only that real regular
connection file. It does not require the selected package or remote endpoint to
be healthy, remove package state, remove an empty `connections/` directory, or
reapply a workspace. A missing or unsafe target fails without mutation. After
add or remove, output tells the author to run the ordinary explicit
`hctl apply AGENT --harness ...` command for each intended workspace/harness.

There is no standalone connection update command: the Markdown file is
ordinary versioned agent source and may be edited directly. Plugin-bundled MCP
remains solely in the publisher-authored Plugin `mcp.json`; connection commands
never synthesize or modify Plugin files.

### Migration and diagnostics

This is a clean pre-publication source break. A body-only connection is not
silently interpreted, rewritten, or accepted with a compatibility warning. It
fails before mutation with its authored path and this diagnostic:

```text
connection must start with YAML frontmatter declaring "type: mcp" and one supported target; body-only connection files are no longer supported
```

The maintained `connections/github.md` sources add the installed example
frontmatter above when implementation lands. That generic authored selection
replaces ADR 0031's implicit filename-to-`github-mcp-server` mapping. ADR 0031
continues to own the curated package, ambient PAT, native GitHub runtime,
operator journey, and regression evidence; no GitHub provider branch or
automatic anonymous fallback remains.

All malformed schema, bad name, package-resolution, harness-target, reserved
name, generated collision, and staging errors name the connection's authored
path and fail before workspace mutation. Diagnostics never contain body text,
credential values, environment values, remote response bodies, or resolved
redirect targets.

## Evidence contract

Implementation acceptance uses two credential-free, provider-neutral fixtures:

1. an installed fake stdio capability configured through package and
   capability fields for both Claude and Codex; and
2. an HTTPS Streamable HTTP declaration configured through the same connection
   inventory for both harnesses, without opening the endpoint.

Tests prove exact parsing, bounds, union rejection, source fingerprinting,
lexical instruction rendering, prose-once behavior, collision failure, package
resolution, harness-target failure, remote native mapping, selective staging,
remote closure omission, current-state guards, add/status/remove atomicity, and
the body-only migration diagnostic. Existing #67, #71, and #72 GitHub tests and
credential-free acceptance remain regression evidence through the generic
installed fixture. Live PAT acceptance remains separately authorized and is
not required by this decision.

## Consequences

- Standards-compatible standalone MCP servers no longer require one hctl
  adapter or provider switch each.
- Non-developers can author supported connections without editing Claude or
  Codex native configuration.
- Installed process metadata remains operator/package-owned, while portable
  source selects only an exact package capability.
- Public remote endpoints are useful without opening a credential or OAuth
  design. Header-bearing, authenticated, and hctl-managed HTTP remain deferred.
- Native harnesses continue to own runtime MCP behavior and may expose tools
  with effects beyond hctl's managed workspace boundary.
- The clean migration removes the last implicit GitHub source convention but
  deliberately requires existing experimental projects to edit `github.md`.

## Sources

- [MCP Streamable HTTP transport](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports)
- [Claude Code MCP configuration](https://code.claude.com/docs/en/mcp)
- [Codex MCP configuration](https://developers.openai.com/codex/mcp/)
