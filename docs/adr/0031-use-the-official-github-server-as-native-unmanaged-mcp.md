# ADR 0031: Use the official GitHub server as native unmanaged MCP

- Status: accepted
- Date: 2026-08-08
- Supersedes: [ADR 0011](0011-start-github-connections-anonymously.md)
- Amends: [ADR 0015](0015-enforce-read-only-channel-sessions.md)
- Specializes: [ADR 0030](0030-use-process-isolated-integration-packages.md)
- Reuses: [ADR 0020](0020-map-plugin-mcp-through-native-harness-configuration.md)
- Authored selection amended by:
  [ADR 0034](0034-author-generic-native-mcp-connections.md)

## Plain-English summary

`connections/github.md` requests an installed copy of GitHub's official MCP
server. Claude Code or Codex starts that external server directly and gives it
the ambient `GITHUB_PERSONAL_ACCESS_TOKEN`. Hctl verifies and selects the
package and writes native configuration, but it does not resolve, consume,
store, or protect the token and does not govern the server's GitHub calls.

**The harness, model-accessible shell or execution tools, plugins, and other
processes inheriting the launch environment may read or transmit the PAT.**
Repository scope, permissions, expiration, runtime isolation, native-harness
trust, and operator judgment are the security boundary for this delivery.

## Decision

As amended by ADR 0034, `connections/github.md` explicitly selects capability
id `github` from stable integration package id `github-mcp-server` in closed
frontmatter. Its optional bounded Markdown body remains model-facing connection
guidance in generated instructions. The file contains no credential value or
reference, installed version, executable path, repository grant, tool
allowlist, or approval decision, and its body does not rewrite or freeze the
official server's tool catalog or schemas. Installation, enablement, exact
version selection, and operator trust are machine state owned by the package
journey implemented by #76.

The initial curated distribution selects official release `v1.8.0` for
`darwin-arm64` and `linux-amd64`. Its checked manifest pins archive and
executable identities; a separate declarative source lock additionally pins
the official release URLs, one allowed GitHub release-asset redirect origin,
and exact archive layout. The one-command package installer performs that
download and materialization outside hctl, then supplies the resulting local
source to #76's generic explicit-trust installer. This preserves #76's
no-redirect fetch contract and adds no GitHub downloader, cache, or vendor
switch to hctl. Direct image construction may run the same reviewed
materialization while it has network access; runtime verification is offline.

The official `github/github-mcp-server` executable supplies GitHub's MCP tool
catalog, schemas, protocol behavior, authentication, requests, results, and
failures. The first hctl delivery selects only its PAT path: the server reads
`GITHUB_PERSONAL_ACCESS_TOKEN` from its process environment. Upstream OAuth and
GitHub App modes are outside this delivery and remain follow-up #73.

Hctl owns package preparation and verification, immutable selection evidence,
selective runtime and staging closure, offline package resolution during
apply, deterministic collision checks, and native project configuration
generation. Claude Code or Codex owns process startup and lifecycle, project
trust, tool approval and discovery, calls and effects, cancellation, results,
and runtime diagnostics.
Hctl does not route this server through its managed MCP server and does not
proxy, supervise, filter, authorize, confirm, retry, observe, normalize, or
audit its calls.

### Exact native mapping

Both harnesses use the native server name `github`, collision policy `reject`,
the exact verified executable selected from the installed package, the sole
argument `stdio`, and the exact prepared package root as working directory.
The target is enabled but startup-optional: inability to initialize GitHub
does not make the rest of the native session or hctl's managed server
unavailable.

Claude's project `.mcp.json` has no working-directory field. Its `github`
stdio entry therefore uses the existing `/usr/bin/env -C` exec adapter:
`command` is `/usr/bin/env`, and `args` are `-C`, the absolute prepared package
root, `--`, the absolute verified `github-mcp-server` executable, and `stdio`.
The entry has no `env` value for the PAT; the server inherits
`GITHUB_PERSONAL_ACCESS_TOKEN` from the Claude launch environment. Claude owns
the one-time project-server approval prompted for `.mcp.json` and exposes
connection and server stderr diagnostics through its native MCP surfaces.

Codex's project `.codex/config.toml` has one `[mcp_servers.github]` stdio table.
Its `command` is the absolute verified executable, `args` is `["stdio"]`, and
`cwd` is the absolute prepared package root. It sets `enabled = true`,
`required = false`, `default_tools_approval_mode = "prompt"`, and forwards the
name `GITHUB_PERSONAL_ACCESS_TOKEN` with `env_vars`; it never emits an `env`
value for it. Codex first requires its normal project-trust decision and owns
server and per-tool approval plus native MCP diagnostics.

The same contract applies locally and headlessly. A shell, service manager,
container runtime, or external secret manager injects
`GITHUB_PERSONAL_ACCESS_TOKEN` when it launches Claude or Codex. Hctl does not
copy it from the apply environment or persist it for a later launch.
Direct local harnesses inherit their launch shell. Hctl-owned concurrent,
resumed, and hibernation-replacement harness children inherit the unchanged
environment of the owning hctl service or container; rotating external
injection requires restarting that owner, not merely opening another child.
Interactive operators establish native project and server trust through the
harness's normal approval journey. Before unattended use, the operator must
deliberately establish the equivalent native project/server trust and tool
approval in supported user, administrator, enterprise, or service launch
configuration. `apply` does not silently grant that trust, and
`connections/github.md` is not an approval grant.

Plain native launches likewise use the exact installed path embedded by the
last apply; they do not re-resolve current package state through hctl. An update
therefore requires reapplying local consumers and rebuilding direct/staged
agent images before restarting or redeploying. Safe removal removes
`connections/github.md`, reapplies local consumers and rebuilds staged outputs
to remove their native entry and closure, then removes package state and
restarts. Hctl-owned scheduled, channel, and continuation opens retain their
separate current-state guard.

### Collisions and diagnostics

Within the generated project closure, `managed` remains reserved for hctl and
an exact `github` collision with an authored/plugin native server rejects apply
before workspace mutation. Hctl never renames, shadows, or silently skips the
requested installed server. Hctl cannot reliably preflight user,
administrator, enterprise, or other harness-owned configuration; the native
harness's documented precedence and diagnostics govern collisions there.

Apply fails with bounded, credential-free diagnostics when the requested
package is absent, disabled, untrusted, incompatible, invalid for the target
platform, missing its selected verified executable, or collides in the
generated closure. Connection discovery and package resolution during apply
remain offline and do not require or resolve the ambient PAT. When the variable
is missing or empty, the supported official
server fails initialization with its bounded `authentication required`
category, which may also name upstream OAuth or GitHub App alternatives that
hctl does not configure in this delivery. Claude or Codex reports the native
connection failure. Invalid, expired, or insufficient credentials likewise
remain official-server or GitHub failures. Hctl does not intercept those
failures, copy upstream bodies, or claim a separate classification. Unrelated
managed tools remain usable because the server is optional.

### Credential and effect boundary

Hctl may retain the required variable *name* as non-secret diagnostic metadata
but never reads or writes its resolved value into agent source, package state,
generated files, apply records, caches, images, staged filesystems, logs,
diagnostics, or retained evidence. This is a 12-factor environment contract,
not a credential broker or secret-isolation guarantee.

The PAT may authorize every operation that the official server exposes and
GitHub accepts. Hctl does not enforce repository or tool allowlists or effect
classes. Operators should use fine-grained PATs limited to the needed
repositories and permissions, with short expiration, and should treat
untrusted model or channel input as having access to that authority. A
read-only workspace policy constrains local workspace effects only; it does
not constrain native GitHub effects.

Native Git and `gh` use separately operator-owned authentication and remain
unmanaged. The MCP PAT does not promise that either is authenticated, and the
official MCP surface does not promise exact Git branch publication with local
history.

## Evidence contract

Configuration-generation acceptance uses the credentialless native-MCP
fixture already validated through ADR 0030's vendor-neutral selection path.
The fixture supplies a deterministic fake stdio executable and conspicuous
fake environment marker, so Claude and Codex generation, working directory,
startup policy, trust behavior, collision rejection, discovery, and calls can
be exercised without a GitHub credential, network request, broker, or special
GitHub code path. Separate tests use a fake value to prove only the environment
name is generated and the value appears in no generated, staged, diagnostic,
or retained artifact. Live GitHub acceptance is optional and requires explicit
authorization and a temporary least-privilege credential.

The Linux/amd64 image gate also builds GitHub-bearing direct and selectively
staged agent images plus a GitHub-free counterpart. Without executing the
official server, it proves the exact pinned executable path and SHA-256,
generated Codex mapping, runtime-only fake environment inheritance, value
omission, and selective artifact omission.

The [operator journey](../github-native-mcp.md) records the literal local and
service/container paths, package lifecycle, native trust, troubleshooting, and
runtime-only secret injection. The
[acceptance record](../workbench/github-native-mcp-acceptance.md) maps each
claim to credential-free tests and keeps the separately authorized live
procedure explicitly unexecuted until its PAT, repository, permissions,
effects, and cleanup are approved.

## Context

ADR 0011 proved the authored connection path with three anonymous, managed,
read-only REST operations. That narrow implementation couples GitHub behavior
to hctl and cannot provide the official server's broader tool surface without
duplicating vendor code. ADR 0030 now provides an exact operator-installed
external executable and native-MCP capability, making the official server the
smaller long-term dependency boundary.

ADR 0009 remains accepted and unamended. It still applies before hctl ships a
secret-bearing *managed* tool or connection. This delivery is deliberately
native and unmanaged: the credential enters the harness environment and the
external server consumes it outside hctl's managed boundary. It therefore
does not satisfy, invoke, weaken, or replace the secretless broker decision.

## Consequences

- The anonymous managed GitHub tools are superseded and will be removed when
  the native delivery is wired; they are not retained as an automatic fallback.
- Agents without an installed `github-mcp-server`/`github` connection generate
  and stage no GitHub package entry or runtime artifact.
- Package installation and preparation reuse #76; this specialization adds no
  GitHub-specific installer, cache, or downloader to hctl core and no credential
  store, broker, proxy, Git client, or GitHub API client anywhere.
- The curated package's small build-time materializer is distribution tooling,
  not an hctl runtime or store path. Its downloaded official binaries are not
  vendored in this repository.
- Native tool names and catalogs are discovered from the official server and
  are not frozen into hctl's portable contract.
- Hardening follow-ups #70, #73, and #77 through #80 remain separate work.

## Sources

- [Official GitHub MCP server](https://github.com/github/github-mcp-server)
- [Claude Code MCP configuration](https://code.claude.com/docs/en/mcp)
- [Claude Code settings](https://code.claude.com/docs/en/settings)
- [Codex MCP configuration](https://developers.openai.com/codex/mcp/)
