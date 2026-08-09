# Product specification

- Status: experimental product contract
- Working CLI name: `hctl`; product naming is deferred
- Initial runtime: local Go executable
- Initial harnesses: Claude Code and Codex CLI

## Initial installation

The initial supported platform is `darwin-arm64`. The exact `vX.Y.Z` Git tag is
the authoritative release version and names
`hctl_X.Y.Z_darwin_arm64.tar.gz`, which contains one `hctl` executable at its
archive root. The accompanying `hctl_X.Y.Z_SHA256SUMS` manifest supplies its
SHA-256 checksum. A user downloads and verifies those exact files, extracts the
executable to a stable location on `PATH`, then runs `hctl apply` with an agent
source and workspace. The generated MCP configuration records the resolved
absolute executable path: moving the binary requires reapplying the workspace;
replacing it at the same path leaves that reference valid, but the supported
upgrade journey reruns `apply` to refresh any runtime cache.

`go install` is not a supported first-release user journey. It requires a Go
toolchain and source/module resolution rather than consuming the released,
checked artifact. `hctl package` is not introduced: portable agent source and
native lockfiles remain inputs to `apply`, while generated tool hosts and
dependency environments remain disposable workspace-local caches. Another
machine installs its needed native runtimes and reruns `apply`; it does not
reuse a copied `.hctl/cache/` directory.

That remains the local installation contract. ADR 0027 separately defines a
canonical staged filesystem for downstream OCI builds; it does not make raw
workspace caches relocatable or introduce a general `hctl package` command.

## User and job

The primary user is an agent author who understands basic files and directories
and common AI concepts such as instructions, skills, and tools. They should not
need to understand registration, manifests, or harness configuration. They
define one filesystem-authored agent project, apply it to a chosen workspace,
prove it interactively in Claude Code or Codex, and may operate the same setup
headlessly through channels.

## Product principles

1. The agent project is legible, versionable, portable source and is not
   coupled to the repository that stores it.
2. Common behavior is portable; harness-specific differences are explicit.
3. Compilation and validation happen before harness files are written or a
   turn dispatcher starts.
4. Generated native files are disposable and visibly tool-owned.
5. Native harness tools remain available and explicitly unmanaged.
6. Policy applies only at managed-tool and durable-state boundaries.
7. Interactive users remain in the native harness interface.
8. Unsupported harness behavior is reported without rewriting valid authored
   source or pretending that hctl enforces it.
9. Conventional files register behavior without a second inventory.
10. Author-facing language stays concrete; runtime terminology remains internal.

## Authored project

Authoring is filesystem-forward. Where the concepts match, hctl uses Eve's
conventional vocabulary: instructions, tools, skills, channels, connections,
sandbox, subagents, and schedules. Only the subset named below is implemented
in the MVP.

The authoring API is convention-driven. An MVP project is:

```text
my-agent/
  instructions.md
  skills/
    research/
      SKILL.md
      references/
        sources.md
  plugins/
    review-pack/
      plugin.json
      skills/
        review/
          SKILL.md
  tools/
    get_weather.ts
    lookup_policy.py
    hash_text/
      tool.go
  subagents/
    researcher/
      instructions.md
  connections/
    github.md
  channels/
    discord.md
  schedules/
    billing/
      sweep.md
  harnesses/
    claude/
      .claude/
        settings.json
    codex/
      .codex/
        rules/
          default.rules
```

The directory name supplies the agent name, normalized to lowercase words with
hyphens. `instructions.md` is required and contains YAML frontmatter with one
plain `description`, an optional Boolean `friction-notes`, and a non-empty
Markdown body. Generated always-on instructions contain the body, not the
frontmatter. Friction notes are disabled when the field is absent or false;
`friction-notes: true` opts the project into the local friction inbox described
under the managed tool boundary. Changing the field changes the source
fingerprint like any other `instructions.md` edit.

Authored source remains bounded by implementation-owned safety ceilings rather
than ordinary-use quotas. Counts are intentionally high; aggregate file and
byte budgets bound the work performed by load and apply:

| Surface | Count ceiling | File and aggregate ceilings |
| --- | ---: | --- |
| Root instructions | One required file | 128 KiB |
| Root and imported skills | 256 aggregate | 1,024 files per skill; 8,192 files and 64 MiB across the skill set; `SKILL.md` 128 KiB; other resources 16 MiB each |
| Authored tools | 128 | 1,024 source and dependency files; 1 MiB each and 64 MiB aggregate |
| Immediate subagents | 128 | 128 KiB each and 16 MiB aggregate |
| Schedules | 256 | 128 KiB per source, including a 32 KiB prompt; 16 MiB aggregate |
| Vendored plugins | 128 directory entries | `plugin.json` and `mcp.json` 128 KiB each; each plugin `skills/` location has 1,024 entries |
| Accepted plugin MCP servers | 128 aggregate | Generated native MCP configuration is at most 8 MiB |
| Selected harness-specific files | 1,024 | 1 MiB each and 8 MiB aggregate |
| Standalone MCP connections | 128 | 8 KiB per source; optional Markdown context contains at most 1,024 characters |
| Channels | One implemented file | `discord.md` is an 8 KiB file whose authored policy contains 1-1,024 characters |

These ceilings are not configurable authoring API. Exceeding a root-project
or project-level directory ceiling fails before workspace mutation. Invalid or
excess optional plugin components that reach component validation remain
isolated with authored-path diagnostics. See
[ADR 0029](adr/0029-bound-authored-projects-with-aggregate-budgets.md).

The `skills/` directory is optional. Each visible immediate directory is one
skill and contains a required `SKILL.md`; its frontmatter `name` must match the
directory name. A skill follows the open
[Agent Skills specification](https://agentskills.io/specification) and may
include regular-file resources such as `scripts/`, `references/`, `assets/`,
and other nested directories. Adding or removing a skill directory updates the
compiled project without separate registration. Root and imported skills share
the aggregate ceilings above.

An [Agent Plugin v1](https://agent-plugins.org/specification) is one complete
publisher-authored directory package. Its publisher supplies `plugin.json` and
any conventional components; a consumer does not reconstruct that manifest or
manufacture a wrapper Plugin merely to use an existing package. The
specification defines the package layout and conformant client behavior. It
does not prescribe a universal distribution source, install command,
marketplace, registry, vendoring rule, or update workflow.

Hctl's current consumer contract is manual local source placement. After
obtaining and reviewing a complete Plugin directory, the consumer copies it
intact beneath `plugins/<storage-name>/` and reapplies the agent. The storage
directory name is only hctl source organization; `plugin.json` supplies Plugin
identity and need not use the same name. Hctl has no Plugin acquisition, add,
update, or remove command. Updating currently means the consumer manually
replaces the complete local directory and reapplies. Automated consumer
operations are planned under
[GitHub epic #95](https://github.com/alee792/hctl/issues/95), but their source,
pinning, provenance, drift, and replacement contracts are not yet part of this
specification.

Each visible immediate real directory beneath `plugins/` is one plugin with a
required bounded `plugin.json` targeting the exact canonical v1.0.0 schema
identifier. The directory may contain at most 128 entries, and each plugin
`skills/` location at most 1,024 entries before the merged 256-skill limit
applies. Hctl validates that schema locally without fetching it. Manifest
violations reject only that plugin; unsupported top-level fields, non-object
`extensions` values, and every unsupported extension namespace are ignored
with warnings. Namespace values are not validated. Hctl imports Agent Skills
only from immediate real directories beneath the plugin's fixed `skills/`
location. A missing `plugins/` directory, a missing plugin `skills/` directory,
and empty component locations are normal.

Root `skills/` load first. Plugin and component directories load in lexical
order. The first skill name wins; later collisions are skipped with a warning
and are not renamed. Invalid plugin skills are skipped independently while
valid sibling components continue. The merged skill set shares the aggregate
count, file, and byte bounds above. Symlinks are never followed.
Accepted plugin manifests and consumed skill resources participate in the
source fingerprint and generate through the same native Claude and Codex skill
paths as root skills.

See [ADR 0019](adr/0019-import-vendored-agent-plugin-skills.md) for the initial
vendored-skills decision.

An accepted plugin may also contain a bounded, regular UTF-8 `mcp.json` with
the exact canonical Agent Plugins v1.0.0 MCP schema identifier. Missing
`mcp.json` is normal. A malformed top-level MCP document disables only that
component; an invalid server disables only itself. Hctl supports `stdio` and
`streamable-http`; valid SSE declarations warn and are skipped. Plugin
directories and server names are processed lexically. The first exact server
name wins, `managed` is reserved for hctl, and collisions are skipped without
renaming. This Plugin-bundled MCP declaration remains inside the complete
publisher-authored package. It is distinct from a standalone connection under
`connections/`; hctl neither synthesizes a connection file from `mcp.json` nor
requires a consumer to wrap a standalone connection in `plugin.json`.

A stdio command is one bare executable name or a plugin-relative `./` path.
Plugin-relative commands and plugin-root working directories must remain within
the real plugin tree without symlinks. The optional working directory is rooted
at `./`, `${PLUGIN_ROOT}`, or `${PLUGIN_DATA}` and defaults to the plugin root.
Hctl expands the exact `${PLUGIN_ROOT}` and `${PLUGIN_DATA}` placeholders once
in arguments, environment values, and working directories, then supplies both
variables in the server environment. A plugin may not override them. Before
native setup is written, hctl creates a private persistent data directory at a
deterministic agent-and-plugin-specific path beneath workspace
`.hctl/plugin-data/`; hctl normalizes and verifies that directory as owner-only
and does not delete plugin data when configuration is removed.

Remote URLs are absolute HTTP(S), contain no user information or fragment, and
use HTTPS unless their host is `localhost` or a loopback IP literal. Header
names and values must be valid HTTP and may not collide case-insensitively.
Headers are literal package-visible values: hctl performs no expansion and they
must not contain secrets.

Accepted plugin servers are emitted as native project MCP configuration. Claude
Code receives `.mcp.json`; Codex receives `.codex/config.toml`, where plugin
servers remain optional and use prompt approval. The harness owns startup,
approval, transport, authentication, retries, and runtime behavior. Hctl does
not proxy, supervise, authorize, observe, or audit plugin MCP calls. Accepted
server values and any consumed plugin-relative command bytes and executable
intent participate in the source fingerprint. See [ADR 0020](adr/0020-map-plugin-mcp-through-native-harness-configuration.md).

Codex preserves unsupported placeholder-like text literally. Claude project
MCP configuration performs its own environment expansion in commands,
arguments, environment values, URLs, and headers. To prevent accidental ambient-secret
substitution and preserve the portable literal-value contract, hctl skips a
Claude plugin server containing such text after portable expansion and emits a
warning.

`name` and `description` are required. Names contain 1-64 lowercase ASCII
letters, digits, and single hyphens, without a leading or trailing hyphen.
Descriptions contain 1-1024 characters. The portable optional frontmatter is
string `license`, 1-500 character `compatibility`, string-to-string `metadata`,
and experimental space-separated string `allowed-tools`. Documentary fields
are preserved without claiming that a harness operationalizes them.
Harness-specific behavior is honored only when the selected harness documents
an exact representation. Recognized vendor fields and files remain intact when
applied elsewhere, with a precise warning that they may have no effect. Hctl
does not translate, strip, or enforce them. In particular, it does not pretend
that Codex honors `allowed-tools` or a Claude skill model selection. An
OpenAI-host `agents/openai.yaml` file is copied byte-for-byte to either target;
Claude apply warns because Claude does not document the file.

Apply copies supported skill resources byte-for-byte into the selected
harness's project skill directory and preserves executable intent in its
ownership and source fingerprints. The reserved `agents/openai.yaml` resource
is copied unchanged to either target and warns for Claude. All authored skill
entries must be bounded regular files and real directories with valid UTF-8
relative paths. Symlinks are rejected even when a native harness supports them,
so the portable source boundary remains deterministic and cannot escape the
agent project.

Immediate directories under `subagents/` define native harness subagents. Each
contains only an `instructions.md` file with the same description-and-body
contract plus optional string `effort`. Effort accepts exactly `low`, `medium`,
or `high`; apply emits it as Claude agent `effort` or Codex custom-agent
`model_reasoning_effort`. The field is omitted from native output when absent.
Hctl validates and requests effort, while the selected harness, model, account,
and policy determine whether it is honored. Root `instructions.md` remains
description-only. The MVP allows one level and at most 128 subagents. A subagent
inherits the selected parent's generated instructions, skills, managed MCP
tools, native tools, and permissions through native harness behavior. Child
skills, tools, dependency files, and nested subagents are rejected rather than
silently ignored. Subagent and tool names may not collide. Portable subagent
names use hyphens; generated Codex agent identifiers use underscores because
that harness requires them.

The optional `harnesses/` directory carries intentionally nonportable native
project files. `harnesses/claude/` may contain a literal `.claude/` tree and
`harnesses/codex/` may contain a literal `.codex/` tree. Apply reads only the
selected harness tree and mirrors its files at the same workspace-relative
paths. This supports native surfaces such as Claude's documented project
`.claude/settings.json` and Codex's documented project
`.codex/rules/*.rules` files without inventing an hctl schema. See the
[Claude settings documentation](https://code.claude.com/docs/en/settings) and
[Codex rules documentation](https://developers.openai.com/codex/rules).

Harness-specific files are bounded regular files beneath real directories;
paths must be normalized UTF-8 and symlinks are rejected. Contents are copied
byte-for-byte, executable intent is preserved, and the selected files join the
source fingerprint and apply ownership record. Hctl does not parse, merge, or
validate native semantics, and does not promise that a particular harness
version honors a copied file. Authors must not place credentials in these
files; hctl does not claim reliable secret detection.

Hctl-owned native destinations remain reserved. Claude authors cannot replace
`.claude/skills/` or `.claude/agents/`; Codex authors cannot replace
`.codex/config.toml` or `.codex/agents/`. Portable instructions, skills,
subagents, and managed MCP setup continue to use their existing conventions.
Case-folded aliases of these paths are also rejected before mutation so agent
source remains safe to apply to common case-insensitive workspaces.

The optional `connections/` directory contains at most 128 immediate, real,
regular UTF-8 files named `<name>.md`. Entries are not nested and symlinks are
never followed. The filename supplies the connection and native MCP server
name: 1-64 characters beginning with a lowercase ASCII letter and otherwise
containing lowercase letters, digits, underscores, or hyphens. `managed` is
reserved. Each source is at most 8 KiB and starts with a closed YAML
frontmatter mapping whose plain string `type` is exactly `mcp`.

An installed stdio connection contains exactly `type`, `package`, and
`capability`:

```md
---
type: mcp
package: github-mcp-server
capability: github
---

Use the discovered GitHub tools for repository, issue, and pull-request work.
```

The package and capability identifiers select one installed, enabled, trusted,
compatible `native-mcp` version-1 capability through the generic integration
store. The capability already owns stdio transport, executable, arguments,
working directory, non-secret environment defaults, required ambient names,
startup, native trust, and supported harness targets; authored connection
source cannot repeat or override them. Its stable native server name must equal
the filename-derived connection name.

A credential-free remote connection contains exactly `type`, `transport`, and
`url`, with transport fixed to `streamable-http`:

```md
---
type: mcp
transport: streamable-http
url: https://example.com/mcp
---

Use this connection for the public reference catalog.
```

The remote URL is absolute HTTPS with a nonempty host and no user information,
query, or fragment. The initial schema has no headers, bearer-token or
environment references, OAuth fields, timeouts, tool filters, approval grants,
provider names, frozen tool catalogs, or other transport parameters. Unknown,
duplicate, mixed-target, aliased, tagged, merged, non-string, or multidocument
frontmatter fails before workspace mutation. Hctl validates but does not open
the URL, resolve DNS, inspect TLS, follow redirects, authenticate, or prove
server compatibility. Authenticated remote MCP remains outside this delivery.

Content after frontmatter is optional trusted Markdown usage context. Hctl
trims it; whitespace-only content is absent and nonempty content contains at
most 1,024 Unicode characters. When the project has connections, generated
instructions contain one bounded `Native MCP connections` section. Connections
appear in lexical name order, every name appears once, and each nonempty body
appears once without its frontmatter. One generic boundary statement says the
selected native harness owns MCP startup, trust, approval, authentication,
discovery, calls, and effects. The body is not sent upstream and does not
replace tool descriptions, schemas, or server-returned instructions. Exact
source bytes participate in the source fingerprint.

Standalone connection names cannot collide with `managed`, another standalone
connection, or an accepted Plugin MCP server. Such a collision fails the
project before mutation; hctl never renames, shadows, or skips a standalone
connection. An installed capability whose server name differs from its
connection filename fails as a target mismatch. Claude application also rejects its
lowercase reserved names `workspace`, `claude-in-chrome`, and `computer-use`.
Existing warning-and-first-wins handling among independently optional Plugin
MCP declarations remains unchanged. Harness-owned user, administrator,
enterprise, and future built-in configuration remains subject to native
precedence and diagnostics where hctl cannot safely preflight it.

Authors need not hand-edit native Claude or Codex configuration. The portable
source commands are:

```text
hctl connection add AGENT NAME --package PACKAGE --capability CAPABILITY [--context TEXT]
hctl connection add AGENT NAME --url HTTPS_URL [--context TEXT]
hctl connection status AGENT [NAME]
hctl connection remove AGENT NAME
```

Every command requires the exact positional agent root; callers already there
use `.` explicitly. Its required `instructions.md` proves the selected
directory is an agent project. Hctl does not search ancestors, infer an
`agents/` directory, choose a workspace, or choose a harness. `add` validates
the closed source form and atomically creates `connections/NAME.md` without
overwriting any existing path. Package add also performs offline exact package
resolution and verifies the capability's server name before writing. Remote
add validates the URL without contacting it. Add does not install, enable,
trust, update, or remove a package and does not apply a workspace.

`status` lists all connections lexically or one exact selected name. For an
installed target it performs offline exact resolution and reports bounded
package/capability health and supported harnesses without execution. For a
remote target it reports `configured` and `runtime=unchecked` without a network
request. Malformed source or an unresolved installed target returns nonzero
with an authored-path diagnostic. `remove` deletes only the exact real regular
source file, even when its declared target is unhealthy; it does not remove
package state, remove the directory, or reapply workspaces. A missing or unsafe
path fails without mutation. Add and remove finish with explicit guidance to
run ordinary `hctl apply AGENT --harness ...` for each intended workspace.
There is no connection update command because the Markdown remains ordinary
versioned source and may be edited directly.

Installed targets reuse the generic offline resolver and native stdio
generation already proven by the GitHub delivery. Remote targets reuse the
existing safe Plugin HTTP renderer: Claude receives project `.mcp.json` with
`type: "http"` and the exact URL; Codex receives a project MCP table with the
exact URL, `enabled = true`, `required = false`, and prompt approval. No auth or
header field is emitted. Remote connections are startup-optional. Installed
staging copies only the selected capability closure; remote staging copies no
integration package bytes. Agents without a connection generate and stage no
corresponding server or closure. Apply and stage never contact a remote MCP
endpoint.

This source contract deliberately breaks the experimental body-only
`connections/github.md` form before publication. Hctl neither infers GitHub
from a filename nor rewrites the file. A body-only source fails at its authored
path with:

```text
connection must start with YAML frontmatter declaring "type: mcp" and one supported target; body-only connection files are no longer supported
```

Existing projects migrate by adding the installed frontmatter shown above and
retaining their Markdown body. See
[ADR 0034](adr/0034-author-generic-native-mcp-connections.md). Plugin-bundled
MCP remains solely in the publisher-authored Plugin `mcp.json`; hctl does not
synthesize a standalone connection from it or require a standalone server to
be wrapped in a Plugin.

GitHub uses the official external `github/github-mcp-server` executable through
the `native-mcp` v1 capability. Hctl reuses the package installation,
verification, cache, offline connection resolution during apply, and
selective-closure journey; it does not
import a GitHub SDK or implement an API client. The stable native server name is
`github`, its collision policy is rejection, and its command is the exact
verified installed executable with the sole argument `stdio`. Its working
directory is the exact prepared package root. The target is enabled but
startup-optional, so GitHub unavailability does not disable unrelated managed
tools or the rest of the native session.

The curated package pins official release `v1.8.0` for `darwin-arm64` and
`linux-amd64`, including immutable release URLs, archive sizes and SHA-256
checksums, exact three-file archive layout, and the extracted executable size
and SHA-256. `./integrations/github-mcp-server/install.sh` is the explicit
operator preparation and trust journey: it selects the host platform, follows
GitHub's single HTTPS release-asset redirect under a pinned destination-origin
policy, verifies every pinned identity, materializes a local package source,
and invokes the generic package installer. Hctl itself still follows no
redirect and contains no GitHub downloader, cache, SDK, or vendor switch.
Users do not select an asset, install Go, copy a binary onto `PATH`, or know the
shared cache layout. Once installed, verification, connection resolution during
apply, and staging are offline and use the exact prepared state rather than
`PATH`.

Claude receives a project `.mcp.json` stdio entry using `/usr/bin/env -C` to
enter that package root and exec the exact installed server. Codex receives a
project `[mcp_servers.github]` entry with the exact command, `args = ["stdio"]`,
the package root as `cwd`, `enabled = true`, `required = false`, and prompt-mode
native tool approval. Codex forwards the ambient variable by name through
`env_vars`; Claude's server inherits the launch environment. Neither generated
entry contains the resolved value. Exact `github` collisions with authored or
plugin native MCP servers reject apply before mutation; hctl does not rename,
override, or silently skip the installed server. Harness-owned user,
administrator, and enterprise configuration remains subject to native
precedence and diagnostics.

Authentication is deliberately unmanaged. Local shells and headless service,
container, or secret-manager configuration inject the same
`GITHUB_PERSONAL_ACCESS_TOKEN` into the Claude or Codex launch environment. The
official server reads it directly. GitHub connection discovery and package
resolution during apply remain offline and neither require nor resolve the
value. Hctl never writes it into source, package state,
generated files, apply records, caches, images, staged filesystems, logs,
diagnostics, or retained evidence.

**The harness, model-accessible shell or execution tools, plugins, and other
processes inheriting that environment may read or transmit the PAT. Hctl does
not claim otherwise.** Hctl does not proxy, supervise, filter, authorize,
confirm, retry, observe, normalize, or audit native GitHub calls. It does not
enforce repository/tool allowlists or effect classes. Fine-grained repository
scope, minimal permissions, short expiration, runtime isolation, native-harness
trust, and operator judgment are this delivery's security boundary. A
read-only workspace does not constrain GitHub effects allowed by the PAT and
official server.

Claude owns its one-time project MCP approval. Codex requires its normal
project-trust decision and owns server/tool approval. Hctl does not silently
grant either. An unattended operator must deliberately establish equivalent
native project, server, and tool trust through supported harness or deployment
configuration before launch. A missing or empty PAT produces the supported
official server's bounded `authentication required` initialization failure;
the upstream message may also name OAuth or GitHub App alternatives that hctl
does not configure in this delivery. Invalid, expired, or insufficient
authorization remains an official-server, GitHub, and native-harness runtime
failure; hctl does not intercept or reclassify it.

Direct local Claude and Codex processes inherit the shell environment present
when the operator launches them. Hctl-owned headless, concurrent, resumed, and
hibernation-replacement harness children inherit the unchanged environment of
their owning hctl service or container. Rotating external injection therefore
requires restarting that owner; merely opening a replacement child does not
refresh it. Hctl neither snapshots the value during apply nor propagates an
in-process credential update. A
missing PAT, Claude project-server approval, or Codex server/tool approval
leaves the optional GitHub server unavailable while the already-trusted native
session and unrelated managed tools remain usable. Codex project trust is
different: it is a prerequisite for launching the native project, so its
absence fails the session rather than only disabling GitHub. A missing,
disabled, untrusted, incompatible, or corrupt installed package likewise fails
offline setup verification before hctl starts a headless process, rather than
launching against stale executable configuration.

That current-package guard applies to hctl-owned process opens. A plain
`claude` or `codex` launch reads the path already embedded by the last apply and
does not resolve package state through hctl. After an update, the operator must
reapply every consuming workspace before restarting its harness or owning
service; direct and staged images must be rebuilt from the updated package.
Safe removal first runs `hctl connection remove AGENT github`, then reapplies local
consumers and rebuilds staged outputs to remove generated configuration and
closure, then removes the package and restarts or redeploys. Removing or
disabling the package first makes the still-declared connection fail reapply
while old generated configuration can remain on disk.

The PAT is the only supported hctl authentication input for this first native
delivery. The official server's OAuth and GitHub App modes remain separate
follow-up work. Native Git and `gh` authentication are separately operator-owned
and unmanaged: the MCP PAT does not promise either, and the MCP surface does not
promise exact branch publication with local Git history. See
[ADR 0031](adr/0031-use-the-official-github-server-as-native-unmanaged-mcp.md)
and its generic authored-source amendment in
[ADR 0034](adr/0034-author-generic-native-mcp-connections.md).
The [native GitHub MCP journey](github-native-mcp.md) is the canonical local,
service/container, package-lifecycle, troubleshooting, and optional live
acceptance procedure. Live acceptance is not part of the credential-free
repository gate and requires separate authorization.

The optional `channels/discord.md` file contains strict `mode: ambient`
frontmatter and a 1-1024 character UTF-8 Markdown participation policy. Its
conventional path registers the `discord` channel; any other entry
under `channels/` fails before workspace mutation. The file contains no runtime
identity, authorization ID, profile, or credential. It joins the source
fingerprint, and apply adds its policy plus the exact `HCTL_NO_REPLY` control
result to generated native instructions.

`hctl channel setup discord` resolves the one installed and enabled `discord`
channel-adapter capability and runs its exact setup mode. The external adapter
owns enrollment, non-secret profile data, legacy profile migration, bot
identity validation, and the `hctl.discord` OS credential-store entry. Status
and remove use its exact bounded modes. Deployment may inject
`HCTL_DISCORD_TOKEN`; hctl passes that opaque value only to the selected
adapter and removes it from every other child environment.

Profile selection remains `--profile`, then the existing
`HCTL_DISCORD_PROFILE`, then the persisted non-secret per-agent channel
selection, a legacy per-agent/default selector during the transition, and
finally `default`. Successful setup records only the agent, channel kind, and
opaque profile id in hctl's owner-only selection store; successful remove
clears that binding only when it still names the removed profile. Hctl reads no
Discord profile fields or credential through either lookup. The adapter alone
reads and migrates the legacy vendor profile fields. There is no in-process
Discord implementation or fallback in hctl core.

`hctl run` auto-applies a missing or stale generated harness integration, then
resolves the exact apply-time package selection and launches the external
adapter automatically. Missing, disabled, incompatible, ambiguous, or stale
selection fails with an install, enable, setup, or reapply remedy and never
falls back to in-process vendor code. The adapter serves the authorized
user in one guild channel and DM. Each surface has
independent durable dispatcher state. It is explicitly direct or shared and
core receives only its stable conversation id plus hashed owner keys; vendor
ids and payloads remain outside core. A transport-neutral channel controller
owns surface registration, pending-turn correlation, complete-response
buffering and control-result handling, typing readiness, terminal
classification, status/reset delegation, and dispatcher lifecycle. The Discord
adapter retains Gateway/REST integration, authorization, native event
filtering, reply references, rendering, mentions, commands, and delivery
semantics. Transport-owned reply targets remain
process-local and vendor payloads never enter dispatcher or durable state. One
deterministic managed-session lifecycle owns each surface's queue,
native session mapping, and resident harness process, while a shared state
owner serializes durable updates across surfaces. Other users, channels, bots,
and webhooks are ignored. Output is buffered until completion, exact
`HCTL_NO_REPLY` is suppressed, and visible replies use bounded 2,000-character
chunks with mentions disabled. `/new` resets an idle surface and `/status`
returns its redacted lifecycle and queue state. After 15 idle minutes by
default, the lifecycle closes the resident harness but retains the durable
native session mapping; `--idle-timeout` configures a positive interval up to
24 hours, and the next eligible message resumes that native conversation.
Active work is never hibernated, and queued work is never discarded. Under
resident saturation, a process with a durable backlog may close between turns
to let an older nonresident waiter run, then resume its own queue later.
Explicit `--input jsonl` selects the existing headless stream instead; one-shot
schedules retain their fresh-session semantics.

The channel runtime defaults to at most four resident harness processes and two
simultaneously active turns. `--max-resident-sessions` and
`--max-active-turns` provide bounded overrides. Accepted input remains durably
queued while it waits for active capacity, and turn grants advance in request
order across conversations so one busy surface cannot repeatedly jump ahead of
another. At resident pressure, the least-recently-idle eligible process
hibernates before a replacement opens; if all residents have backlogs, fair
rotation happens only between turns and preserves every queued input. Duplicate
delivery consumes neither a new queue entry nor capacity. `/status` includes
only aggregate active, resident, limit, and queued counts.

Channel-native human input uses a **transport-neutral interactive request**,
not unrestricted generative UI. The versioned semantic union contains exactly
`confirm`, `choose_one`, `choose_many`, `text`, `date_time`, and a modest
`form`. Every request has a bounded prompt, an optional bounded text fallback,
a relative expiry of 60 seconds through seven days, and an explicit allowed or
forbidden cancellation policy. Its fields and choices use stable semantic IDs;
choice labels, descriptions, values, selection cardinality, freeform input,
text lengths, and date/time representation are explicit and bounded. A form
has at most eight fields, each choice has at most 25 options, the whole request
has at most 64 options and 32 KiB of encoded JSON, and answers have at most
16 KiB of encoded JSON. Prompts are at most 2 KiB, fallbacks 4 KiB, labels 100
bytes, descriptions 300 bytes, option values 256 bytes, and text answers 4,000
Unicode code points. Semantic IDs contain 1-64 lowercase ASCII letters,
digits, underscores, or hyphens, begin with a letter, and are unique within
their request scope. Date input is canonical `YYYY-MM-DD`, time input is
24-hour `HH:MM`, and combined date/time input is RFC 3339 with an explicit
offset and is normalized to UTC.

Answers refer only to the request's stable field and option IDs. Trusted hctl
code independently validates them, orders fields and choices by the original
request, normalizes text line endings and date/time representations, and
rejects missing, duplicate, unknown, out-of-range, or inapplicable values.
Adapters advertise supported request kinds and concrete limits before a
lifecycle waits for input. An unsupported request deterministically uses its
specified text fallback or fails clearly when no fallback exists. Hctl assigns
interaction and callback IDs, ownership, authorization, expiry timestamps, and
continuation metadata; none are model-authored fields. The contract has no raw
vendor payloads, layout nesting, URLs, executable code, credential references,
or A2UI surface schema. A future renderer may adapt this semantic contract
without changing what the model is allowed to request.

Capabilities distinguish supported top-level request kinds from the bounded
field kinds that a native form may contain. Discord's first renderer supports
confirmations, non-freeform choices, text, and text-only forms of at most five
fields. It uses buttons or string selects for choices and an Answer button
followed by a modal for text input. Date/time, freeform choices, and mixed forms
degrade. The authored fallback is only introductory text: hctl appends and
parses one transport-neutral grammar using exact confirmations, one-based
choice ordinals, whole text replies, canonical date/time values, keyed form
lines, and exact allowed cancellation. A freeform choice is exactly
`other=TEXT`; choose-many may combine option ordinals and one freeform value as
`1,2;other=TEXT`. The freeform value counts as one selection, including an
explicit empty `other=` when the field permits zero-length text. A fallback
reply must correlate to the current bot request and enters answer acceptance
rather than ordinary input. Invalid correlated fallback replies remain pending
and receive one bounded, mention-disabled format correction. A successful
native cancellation receives an explicit cancellation acknowledgement.

The renderer command is deliberately narrower than the controller's pending
interaction snapshot. Expiry, continuation mode, and lifecycle phase support
recovery and authorization but are not exposed through the renderer seam.

Discord component and modal identifiers are bounded opaque digest handles plus
trusted positional slots. Callback decoding verifies the selected application,
authorized human, exact surface and durable owner, current pending request,
action, and request shape before mapping slots back to semantic IDs. The final
normalized answer commits before Discord acknowledgement, and continuation is
scheduled only after the acknowledgement attempt. Raw Discord payloads remain
inside the adapter. REST errors after a render attempt are ambiguous and are
not retried.

Each dispatch conversation may persist at most one nonterminal interactive
request in the same owner-only conversation record as its triggering input,
queue, native session mapping, and worktree assignment. The shared conversation
store remains the sole writer; renderers, harness continuations, MCP children,
and vendor adapters cannot save independent snapshots. The externally meaningful
lifecycle is `requested`, `rendered`, `answered`, `resuming`, then `completed`,
with explicit `cancelled` and `expired` terminal outcomes and bounded terminal
tombstones for duplicate classification.

Rendering and continuation each use a commit-before-side-effect intent. The
renderer atomically claims delivery so concurrent attempts cannot both send; a
crash before that claim remains safe for a first attempt. A crash or ambiguous
result after delivery intent becomes delivery-uncertain and is not automatically
redelivered; a subsequently valid answer may prove delivery. A crash or
ambiguous result after resume intent becomes resume-uncertain and is not
automatically resumed. Explicit recovery may adjudicate it as completed or
failed without invoking the harness again. Answers are normalized and committed
exactly once
before acknowledgement or continuation. Identical duplicates are idempotent;
conflicting, late, expired, cancelled, unauthorized, and cross-surface answers
are rejected. Acceptance requires the store-bound agent and conversation,
authorized principal and surface owner, interaction ID, original request, and
current pending record to agree; a callback or interaction ID alone is not
authority.

A terminal interaction commit also records whether a queued successor must be
woken. The next durable input consumes that wake intent when it becomes active;
runtime startup drains any intent left by a crash after completion,
cancellation, or expiry but before in-memory notification.

Waiting is parking rather than blocking: no live model turn, tool callback,
channel request, resident harness process, or active-turn grant remains held.
The pending request blocks later queued inputs for its conversation while other
conversations continue through shared capacity. A later ordinary message on
that surface is not silently discarded or added behind the parked origin: it
receives one bounded, mention-disabled busy response referencing that message.
Reset rejects a nonterminal request, worktree reconciliation treats it as busy,
and resume uncertainty also
prevents automatic worktree retirement. Status and audit expose only
`waiting_for_input` plus existing bounded aggregate queue and capacity state,
never prompts, answers, identifiers, continuation keys, paths, configuration,
or credentials.

The managed `channel.request_input` contract now exists behind a runtime
capability gate. It accepts only the semantic request above and emits a typed
harness event; the dispatcher then injects the active input, pseudonymous
owner, tool-call correlation, continuation mode, and runtime target before
calling the durable coordinator on its serialized state path. The dispatcher
recomputes native-versus-fallback resolution from trusted responder
capabilities and commits `requested` before acknowledging the harness bridge.
MCP children, renderers, and channel adapters do not write dispatch state.

Real advertisement requires a harness-owned root bridge, an available harness
continuation strategy, and responder support for native rendering or the
request's declared text fallback. A shared inherited MCP server cannot be
enabled merely by configuration or a process-wide root flag. Claude's exact
`PreToolUse` hook denies events containing its documented subagent `agent_id`;
an eligible root deferral becomes a structured harness event carrying opaque
proof produced by the harness-owned constructor. Zero or caller-assembled
events fail before persistence.
Schedules, explicit JSONL, ordinary native
sessions, unavailable responders, and unproven subagent calls do not receive
the capability. Claude and Codex independently prove root ancestry before
persistence; true subagent tool-list isolation is deferred. Generated Claude and Codex
channel instructions describe when to ask and forbid fabricated callback IDs
or vendor markup, but those instructions are not the enforcement boundary.
The selected harness strategy returns a bounded, content-free tool disposition
after the durable commit, leaving deferred-tool versus continuation-turn
semantics to ADRs 0024 and 0023. MCP does not manufacture that result. Audit
correlation uses only the MCP request identity and tool name, not semantic
request bytes. The resumed Claude tool response necessarily returns its
normalized answer to the original model turn; diagnostics and audit never
contain prompts, options, answers, fallback text, or vendor payloads.

The two durable continuation modes are intentionally different. A
**native deferred-tool continuation** later resumes the same logical tool call
using a harness-native continuation identity. A **continuation turn** later
opens another turn in the same native session with the normalized answer and
request context. Neither is a blocking request.

Claude implements `native_deferred_tool` with its documented headless
`PreToolUse` deferral protocol. Hctl commits the request, closes the process,
and retains no capacity while awaiting an answer. After answer acceptance the
generic manager restart scheduler claims the continuation exactly once,
resumes the persisted Claude session without a new user prompt, and replays the
exact tool identity and semantic input through a short-lived owner-only broker.
The initial deferred result is trusted only after consuming an exact,
single-use hook receipt recorded after successful broker delivery. A resumed
turn succeeds only after the broker atomically confirms one delivered allow
decision and one delivered exact MCP answer; attempted but disconnected broker
responses are uncertain rather than complete.
Only after lifecycle completion is durable does the manager publish the
terminal turn event. A known unavailable or lost retained session fails
deterministically; an ambiguous resumed process is never retried.

Codex implements `continuation_turn` through app-server's documented
experimental dynamic-tool and thread APIs. Only a channel-managed root with a
dispatcher handler and compatible responder registers the `channel` namespace
and `request_input` function. The adapter requires exact active root thread and
turn provenance before the dispatcher sees the semantic request. After the
durable park it returns only `continuation_turn`, lets the bounded turn end,
closes app-server, and retains no process or active-turn grant while waiting.
After answer acceptance it resumes the stored thread and starts a new turn with
a bounded controller-owned `hctl.channel_input_answer` JSON envelope containing
the exact request correlation and normalized answer. This is not
`turn/steer`, a live native input waiter, MCP elicitation, or same-tool-call
resume. Resume ambiguity follows the durable no-retry rule. Production
advertisement is enabled only for a channel-managed Discord root whose selected
adapter advertises a compatible native renderer or deterministic text fallback
codec.

New and resumed channel-managed sessions run read-only in the shared workspace:
Claude uses native plan permission mode, Codex uses a read-only sandbox with
approvals disabled, and the managed MCP boundary does not start or expose
authored tool hosts under that policy. Safe built-ins remain available. Native
MCP servers remain outside this managed boundary: a configured GitHub server
may still perform every upstream effect allowed by its PAT despite the
read-only workspace. Generated channel instructions define the exact
`HCTL_REQUEST_WRITE_ACCESS` result for requests that genuinely require a
workspace change. Like `HCTL_NO_REPLY`, it is interpreted only after the whole
trimmed response exactly matches and is never delivered as ordinary output.
This policy does not change apply, explicit JSONL, schedules, or interactive
native-harness use.

When a read-only turn returns that exact write-access result, hctl creates a
conversation-specific branch-backed Git worktree in a private sibling
directory, applies the selected agent there, resumes the same native session
with workspace-write access, and submits one internal continuation of the
original request. The control result and continuation are never Discord
messages. The owner-only dispatch state records only the validated worktree
root and branch. Later turns and restarts reuse that assignment unless the
startup retirement rules below prove it disposable; other conversations remain
read-only in the shared checkout. A non-Git workspace,
identity mismatch, unsafe assignment, or modified generated file fails without
changing the shared checkout or ambiguously retrying the turn. `/new` starts a
fresh native session while retaining an existing isolated workspace.

Writable conversations remain independent when they overlap: each keeps its
own branch, worktree, durable queue, native session, and response surface while
sharing the runtime-wide admission limits. A harness, worktree-resolution, or
turn-deadline failure retires only that conversation's current worker and
preserves the other conversations' state and execution. Failure to deliver
dispatcher events is runtime-wide and still stops admission because the
channel can no longer account for outcomes safely.

At startup, hctl validates every durable worktree assignment before admitting
turns. Active, queued, uncertain, dirty, untracked, unmerged, missing, moved,
foreign, and otherwise unverifiable assignments are preserved with local
operator diagnostics. Only an inactive worktree with verified generated setup,
no non-generated changes, and a branch already reachable from the base
checkout's `HEAD` is retired automatically. Durable retirement intent precedes
narrow cleanup of the exact worktree and branch; partial failure retains the
assignment for idempotent retry and blocks only that conversation. Discord
status remains path- and identifier-free.

The optional root `schedules/` directory contains nested Markdown task files.
The bounded, valid UTF-8 path beneath `schedules/`, without `.md`, is the
schedule name. At most 256 schedules are discovered. Each file is bounded UTF-8
Markdown whose strict YAML frontmatter
contains exactly one string field named `cron`. The value is at most 256
printable ASCII characters and must parse as a standard five-field expression.
The non-empty body is the task prompt; hctl removes only one optional blank line
after the frontmatter delimiter and otherwise preserves its Markdown bytes.
Apply validates these files and includes their original bytes in the source
fingerprint, but starts no clock, harness process, network request, or external
registration.

```sh
hctl schedule trigger AGENT NAME --workspace WORKSPACE \
  --harness claude --input-id OCCURRENCE_ID \
  --turn-timeout 90s --timeout 2m
```

One-shot dispatch requires the selected setup to be current and the operator to
supply a stable dispatch input ID. A conversation derived from the schedule name
keeps bounded durable deduplication outcomes, while every accepted input opens
a fresh native-harness task session without a resume ID. Terminal task state
clears the stored native session ID; active work recovered after restart keeps
the turn dispatcher's existing uncertain semantics and is never silently retried.
Completed duplicate input returns the prior status without opening a harness.

`--turn-timeout` configures a positive task-turn deadline up to 30 minutes and
defaults to 90 seconds. It begins only after the native process opens and the
occurrence is durably active. It is independent of the existing positive,
bounded `--timeout`, which continues to cover verification and the complete
command lifetime. Turn-deadline expiry aborts only that native process,
durably records the occurrence as `uncertain`, clears its fresh-session
continuation, persists the bounded `deadline_exceeded` reason separately from
the lifecycle status, and returns a clear command error. The lifecycle line
includes that reason. Repeating the input ID returns the retained uncertain
outcome and reason without opening a process; generic restart uncertainty has
no deadline reason, while a distinct later occurrence opens a fresh session.
If the outer command context ends first, existing uncertain restart recovery
remains authoritative.

The command writes one bounded lifecycle line containing the schedule, input
ID, status, duplicate flag, and runtime IDs when available. It never emits
model text. A non-completed outcome returns a command error after the status is
reported. This task mode performs no channel delivery, registration, daemon
installation, missed-run replay, overlap handling, credential use, or live
model call during credential-free tests. TypeScript schedule handlers,
subagent schedules, and Eve's hosted auth and delivery runtime are unsupported.

`hctl schedule run AGENT --harness <claude|codex>` is the explicit foreground
clock. It requires current generated setup, loads schedules once, verifies the
harness once, and performs no auto-apply or hot reload. Standard five-field
cron expressions are evaluated in UTC. The first occurrence is strictly after
startup; each wake admits only a matching occurrence in its current UTC minute,
without downtime or clock-jump backfill. Repeated and backward wakeups do not
duplicate an admitted scheduled minute.

One shared task runtime owns the durable store and bounds concurrent fresh
task sessions with `--max-active-turns` (default 2, maximum 64). Queued capacity
counts as in flight, so the same schedule cannot overlap. Stable occurrence IDs
are the full SHA-256 of the exact UTF-8 schedule name and canonical scheduled
UTC minute. A local lock excludes another clock for the same canonical
workspace, agent identity, and harness. SIGINT or SIGTERM stops admission and
drains admitted work through completion or its `--turn-timeout`. Lifecycle
output is bounded and contains no prompt, model text, path, or raw harness
error. See [ADR 0026](adr/0026-run-schedules-from-a-foreground-utc-clock.md).

Visible `tools/*.ts` and `tools/*.py` files each declare one tool. A visible
`tools/NAME/tool.go` directory declares one Go tool. Filenames supply tool
names, with underscores exposed as hyphens. TypeScript definitions export a
default object containing `description`, strict Zod `inputSchema` and
`outputSchema`, and `execute`. Python modules export `description`, Pydantic
`Input` and `Output` models, and `execute`. Go packages export `Description`,
`Input`, `Output`, and `Execute`. The runnable mixed-language fixture is the
canonical syntax example while the product remains experimental.

Authored source entries must be bounded regular files and real directories
without symlink traversal. Contract and code files must be UTF-8; arbitrary
skill resources may be binary. There is no authored hctl manifest, registry,
or duplicated tool inventory. TypeScript uses root `deno.json` and `deno.lock`;
Python uses `pyproject.toml` and `uv.lock`; Go uses `go.mod` and an optional
`go.sum`. These native files describe dependencies without registering tools.
Compilation produces a deterministic apply record and source fingerprint. The
bounded `echo` managed tool remains an hctl-provided default; it is not author
configuration.

## Process-isolated integration packages

Machine-installed third-party integrations use a metadata-first package
contract distinct from portable vendored `plugins/`. A bounded schema-version
1 manifest supplies a stable package id and exact semantic version,
human-readable name and description, license, source and revision provenance,
a half-open hctl compatibility range, exact platform artifacts, and one or more
closed versioned capability declarations. The SHA-256 of the exact manifest
bytes is its immutable identity.

Each `darwin` or `linux`, `arm64` or `amd64` artifact is a bounded `binary`,
`tar.gz`, or `zip` with an exact size and lowercase SHA-256. Its source is
either a normalized package-relative payload or an HTTPS URL without embedded
credentials, query, or fragment. The manifest also pins the expected
package-relative executable path, size, and SHA-256 after preparation. Hctl
validates all metadata without opening an artifact, fetching a URL, loading a
library, or running package code.

Capabilities are tagged, closed schemas. Unknown types or versions reject the
manifest without executing code. The common envelope is not an MCP runtime:
every capability has its own narrow data or process contract. Package
installation and enablement are operator-owned machine state bound to the
exact manifest and artifact identities. Portable agent source cannot choose an
install source or version, install or enable a package, grant machine trust, or
carry a credential. The install/cache/CLI journey below remains distinct from
capability-specific generation; apply does not gain a network path.

The closed installation-state schema records package id and version, manifest
SHA-256, explicit operator trust, package-level enablement, verified artifact
and executable hashes, and every declared capability's id, type, and version.
It contains no path, credential, or runtime value and validates back to the
immutable decoded manifest. Package metadata access and native-MCP selection
use defensive copies, so caller mutation cannot pair changed executable data
with the original manifest identity.

`hctl integration install SOURCE --trust operator` is the only initial trust
and installation journey. `SOURCE` is an existing real owner-controlled local
directory, zip archive, or tar.gz archive whose root contains
`integration.json`; portable agent source and `apply` never select that source
or grant trust. A package-local artifact is read beneath that source without
following symlinks. A pinned HTTPS artifact is fetched only from the exact
manifest URL, without redirects, and accepted only at its exact declared size
and SHA-256. There is no registry search, package script, dependency resolver,
git/npm/Go installer, or signature claim.

An integration's reviewed distribution tooling may materialize such a local
package source from separately pinned upstream delivery metadata. That tooling
remains outside hctl and must verify the upstream transfer, archive layout, and
post-preparation executable identity before handing the source to the generic
installer. The curated GitHub package uses this seam because GitHub's stable
release URLs redirect to signed release-asset URLs, while hctl deliberately
does not follow redirects. This does not create an apply-time network path or
a vendor-specific branch in package storage.

Installation validates the manifest, closed capability metadata, current hctl
compatibility, and the host platform before preparing an artifact. It rejects
foreign or broadly writable local source entries, symlinks, path escape,
duplicate or unsupported archive entries, unsupported platforms, ambiguous
package ids, implicit identity drift, and artifact or executable mismatch.
Binary, zip, and tar.gz platform artifacts are normalized into bounded private
regular-file closures; archive links, devices, and other special entries are
never retained. Hctl does not start an artifact during install, inspection,
verification, or setup.

Exact manifests, raw artifacts, and prepared closures live in one owner-only
OS-user integration store shared across agents and workspaces. Raw and prepared
entries are content-addressed, verified before reuse, and published atomically
under a store lock. The prepared closure includes a deterministic file receipt
so later corruption of the executable or a sibling runtime file fails closed.
Its cache identity binds the raw artifact size and SHA-256, archive format, and
expected executable path, size, and SHA-256; two transformation contracts over
the same raw bytes therefore cannot alias or replace one another. A valid
immutable prepared entry is reused rather than replaced.
Only the small installation-state record selects the current exact package;
interruption before that atomic write can leave inert unreferenced cache bytes
but cannot publish a partial installation. `hctl integration update ID SOURCE
--trust operator` is the only identity-changing path. Install of a different
manifest under an existing id fails with that remedy rather than drifting.

`integration inspect`, `verify`, `list`, `enable`, `disable`, and `remove`
operate only on that owner-controlled store. Inspect reports identity,
provenance, compatibility, platform artifacts, executable identities,
capabilities, and required ambient names/descriptions; it does not read or
print environment values. Verify is offline and rechecks the full cached
closure. Disable removes a package from future resolution without deleting its
metadata; enable first verifies it. Remove retires only the selected install
record and its non-secret consuming-agent receipts. A capability consumer may
record one receipt only after successful exact resolution; it contains agent
identity, manifest identity, and selected capability ids, never a workspace or
executable path. Inspect reports only receipts for the currently installed
manifest. Shared content-addressed bytes remain inert for exact offline reuse;
broad cache garbage collection is not part of this slice.

Apply and staging use an offline lookup that requires an enabled compatible
installation and re-verifies its exact manifest, artifact, prepared closure,
and executable. Each narrow capability consumer names its own artifact ids.
The common layer returns immutable metadata and exact local paths without
interpreting runtime behavior. Selective staging copies only those named
closures beneath `/opt/hctl/integrations/PACKAGE/MANIFEST/ARTIFACT/`; an agent
that requests no integration contributes no package artifacts. The consumer
that maps a validated authored connection or channel request into this lookup
remains with that capability's delivery, rather than becoming a generic plugin
runtime. For `native-mcp`, the offline consumer can derive a credential-free,
harness-targeted launch descriptor from the exact installed metadata and
verified paths. It does not read ambient values or write native configuration.
#97 maps generic authored connection source into generated Claude and Codex
configuration. Completed #67, #71, and #72 remain the installed GitHub runtime
and regression evidence rather than a separate provider path.

The first recognized capability is `native-mcp` version 1. It declares a
stable native server name with collision behavior fixed to rejection, the exact
artifact ids forming its selective runtime/staging closure, one matching
package-relative executable, bounded literal arguments and working directory,
literal non-secret environment defaults, required ambient environment names
and safe descriptions without values or references, and supported Claude or
Codex targets. Per-target startup is optional or required and trust remains the
native project's responsibility. Package metadata cannot modify user,
administrator, or enterprise trust.

The native harness owns process lifecycle, credentials, approvals, discovery,
calls, effects, cancellation, results, and failures. Hctl generates native
configuration in later capability-specific work; it does not proxy,
supervise, authorize, filter, confirm, retry, observe, or audit native MCP
traffic. Required ambient names are diagnostic metadata rather than a
credential channel, and resolved values never belong in generated files,
package state, staged filesystems, or retained evidence. The value is available
to the native harness-launched server and may be visible to the harness,
model-accessible execution tools, and inherited native processes; this
capability does not claim to hide it. Descriptions reject common value and
reference syntax through a closed 1-512 character prose alphabet containing
only ASCII letters, spaces, commas, periods, semicolons, parentheses,
apostrophes, and hyphens and beginning with a letter. Hctl still cannot
reliably detect an arbitrary secret disguised as allowed prose.

The second recognized capability is `channel-adapter` version 1. It declares a
stable channel kind, the exact artifacts in its selective runtime and staging
closure, one matching package-relative executable, fixed literal argument
vectors for runtime/setup/status/remove, a half-open protocol range containing
version 1, the non-secret `opaque-id-v1` profile selector, and a closed subset
of typing, replies, edits, reactions, attachments, interactive components, and
text fallback. Hctl appends only the standardized `--profile PROFILE` pair to
setup/status/remove. The runtime profile id travels in initialization. Live
feature and limit negotiation may only narrow the manifest declaration.
Every mode runs from the verified package root; no manifest field selects the
agent workspace or an ambient working directory.

Channel setup retains trusted terminal input and has a separate ten-minute
human enrollment deadline. Remove also retains trusted input, while status has
no stdin; status and remove remain bounded to 30 seconds. Interrupt or caller
cancellation kills the complete private operation process group and bounds
reaping. For a controlling terminal, the adapter process group owns the
foreground during the operation and hctl restores the original group on every
exit path. The official adapter retains default terminal-interrupt behavior
during blocking setup prompts, so Ctrl-C terminates that foreground group. The
generic per-agent/channel selection store locks the complete
read-modify-write transaction across processes before atomically replacing its
owner-only file.

The channel-adapter protocol is one bounded bidirectional JSONL stream over an
exact verified child process's stdin/stdout. The adapter opens with
hello; hctl selects one compatible version plus profile, feature, limit, and
ambient participation policy; and the adapter becomes usable only after a
correlated ready response. Stdout is protocol-only and stderr is bounded
diagnostics. Hctl owns launch, deadlines, cancellation, graceful/forced
process-tree cleanup, and the translation to the existing channel controller.
The adapter owns vendor connect/reconnect, source authorization, SDK payloads,
rendering, callback identifiers, rate limits, transport state, credentials,
and non-secret profile data.

Frames contain only closed semantic messages: opaque route/message/author
handles, normalized inbound text and attachment descriptors, authorized
status/reset requests and redacted results, activity and reply/edit/reaction
intents, the existing bounded interactive request, an
exact/ambiguous/failed render receipt, recovery-only restore, and normalized
answer,
attachment transfer authorization and bounded base64 chunks, exact/ambiguous/
failed dispositions, connection lifecycle, classified diagnostics, event
acknowledgement, and shutdown. Vendor payloads and SDK objects, credentials,
raw environment, arbitrary markup/component trees, executable code, commands,
URLs carrying authority, and filesystem/workspace paths have no protocol
representation. Hctl remains the sole writer of dispatcher, session,
interaction, worktree, capacity, and generic durable state.

Version 1 limits one frame to 256 KiB, semantic text to 64 KiB, one message to
16 attachments, one attachment transfer to 16 MiB in 64-KiB chunks, concurrent
transfers to four, outstanding correlations to 128, the in-memory protocol
queue to 64 frames and 8 MiB, retained stderr to 64 KiB, and a
setup/status/remove result to 16 KiB. Negotiation may lower those values.
Ordinary commands and deliveries have 30-second deadlines, attachments 60
seconds, and graceful shutdown five seconds before forced tree cleanup.
The narrowed frame ceiling applies to reads and writes, and the narrowed
outstanding ceiling bounds correlations and retained route/event state.
Startup recovery separately admits at most 64 frames and 8 MiB before replay,
with unique unacknowledged semantic events additionally capped by the
negotiated outstanding limit. Connection/diagnostic frames and same-id replay
remain inside the fixed queue without taking another event slot. It reserves a
negotiated maximum frame before reading and applies pipe backpressure at
capacity until recovery or its deadline, while retaining a bounded read path
for a pending recovery response. Target saturation rejects new input without
evicting an older accepted reply target.

An adapter keeps an event id and exact bytes stable until hctl acknowledges a
durable acceptance, duplicate, or rejection. Exact same-content replay is
idempotent; changed content under one id is fatal. Stable inbound source ids
enter dispatcher deduplication. Hctl never automatically resends an effect
after a complete command write without a proven pre-attempt failure. Missing
results, disconnects, deadlines, and post-write child exits are ambiguous and
use the controller's existing uncertain/no-retry behavior. Malformed,
oversized, unknown, wrong-direction, or uncorrelated protocol data terminates
only the owning adapter runtime.

At startup, the adapter advertises its bounded stable surfaces before the
controller is constructed. Hctl reattaches durable interaction state before
adapter replay: pending renders are issued once, while previously delivered
interactions are restored without posting duplicate vendor UI. Shutdown stops
admission, retires interaction UI, asks the adapter to drain, then applies
independently bounded process-tree and controller cleanup. Retained stderr is
capped by emitted sanitized bytes, credential-redacted across arbitrary write
boundaries, control-cleaned, and protocol-shaped output is suppressed before
terminating only that adapter runtime.

Setup, status, and remove use exact package modes. Secret entry and credential
storage occur inside the trusted adapter using its inherited operator terminal
or deployment environment; hctl receives one closed bounded non-secret result.
Hctl sends only a non-secret profile id. An ambient compatibility credential
may be inherited only by the exact adapter; it is never parsed or sent in a
frame and must be scrubbed from every unrelated child. Process isolation keeps
dependencies and responsibility separate, but is not an OS sandbox or a
defense against a malicious adapter or same-user peer process. See
[ADR 0032](adr/0032-use-a-bounded-semantic-channel-adapter-protocol.md).

The official `hctl-discord` integration package now implements this contract
as a separately locked and built Go module. Its executable owns DiscordGo,
Gateway/REST payloads, rendering, callbacks, application locks, credentials,
and adapter profiles. It retains the `hctl.discord` keyring service and can
atomically migrate the selected non-secret profile from the former owner-only
hctl configuration. Its reproducible builder emits exact Darwin/Linux,
amd64/arm64 package metadata that installs and selectively stages through the
shared package store. Credential-free fakes prove its four modes and runtime
protocol. See [ADR 0033](adr/0033-package-discord-as-an-external-channel-adapter.md).

The production `hctl channel setup|status|remove discord` and `hctl run` paths
select the exact installed executable and use the generic bounded process
host. Apply records the exact package/capability consumption; a later package
change requires reapply before launch. Hctl core contains no Discord transport,
credential implementation, Discord SDK, or keyring dependency.

Selective staging resolves the same exact installed capability offline, copies
only its current-platform artifact, and emits one strict non-secret descriptor
bound to the agent id, source fingerprint, manifest identity, capability, and
executable hash. The generated staged entrypoint alone selects that adjacent
descriptor. A normal direct invocation cannot use an arbitrary ambient path to
redirect adapter execution. Agents without `channels/discord.md` stage neither
the descriptor nor the adapter closure. Profiles, credentials, and durable
channel state remain runtime mounts or injected values rather than staged
content.

Core depends only on validated package data and narrow capability consumers.
Vendor packages depend inward on those contracts and run as separate
executables. No package can contribute Go interfaces, be imported into hctl,
register an in-process lifecycle, or require a vendor-named switch in core.
Existing vendored Agent Plugins and their native MCP behavior remain unchanged.
See [ADR 0030](adr/0030-use-process-isolated-integration-packages.md).

## Apply and handoff

```sh
hctl apply AGENT --workspace WORKSPACE --harness claude
hctl apply AGENT --workspace WORKSPACE --harness codex
```

`apply` validates the authored project, target harness executable, tool
definitions, locked dependencies, and protocol readiness. It invokes Deno,
`uv`, or Go only when that language is present, then materializes owned native
files in the selected workspace so the user can change into that directory and
start the selected harness normally. `--workspace` defaults to the agent
project directory, making a standalone agent the simplest case. Applying an
agent stored elsewhere is explicit:

```sh
hctl apply ~/agents/reviewer --workspace ~/Code/example --harness claude
cd ~/Code/example && claude
```

The agent project supplies instructions, skills, tools, subagents, vendored
plugins, harness-specific files, and native dependency files. The workspace
supplies harness-visible working files and is
the working directory for the harness and authored tools. Generated harness
files, apply records, plugin data, dispatch state, and runtime caches belong to
the workspace. Source discovery and dependency preparation remain rooted in
the agent project.

Claude receives `CLAUDE.md`, `.mcp.json`, `.claude/skills/`, and
`.claude/agents/`. Codex receives `AGENTS.md`, `.codex/config.toml`,
`.agents/skills/`, and `.codex/agents/`. Generated MCP configuration uses the
resolved `hctl` executable, agent-source, and workspace paths. Supported
vendored plugin servers join these native MCP files without joining hctl's
managed tool boundary. Hctl generates skills only at this project scope; it
does not modify user, administrator, enterprise, or plugin skill locations.

Codex project configuration remains subject to Codex's native repository-trust
flow. Apply does not edit the user's global Codex configuration or silently
trust a project on their behalf.

Apply refuses to overwrite hand-authored native files or any hctl-owned file
that was modified after the previous apply. Removing or changing a
harness-specific source file uses the same modified-file protection and stale
cleanup as generated portable setup. Reapplying identical source is
deterministic.

## Native harness contract

Each harness integration declares and verifies:

- its executable and compatible version signal;
- native generated-file surfaces;
- managed tool exposure;
- new-session and resume behavior;
- structured input, output, and terminal events; and
- any interruption or steering behavior that is not portable.

Claude Code uses bidirectional stream JSON. A second message received during an
active turn is queued for the next turn. Codex uses its local App Server JSONL
protocol. Active-turn steering and interruption are Codex-specific and are not
part of the portable MVP promise.

## Headless run

```sh
hctl run AGENT --harness claude
hctl run AGENT --harness codex
```

The `run` command sends bounded JSONL input through the turn dispatcher. Each
input contains a caller-provided `input_id` and `text`. The dispatcher durably
accepts and queues input while a turn is active, processes one FIFO turn per
conversation, emits ordered JSONL events,
and maps the external conversation to a resumable harness session.

A repeated input ID is deduplicated within its conversation. After a restart,
an input that was active but lacks a proven terminal result becomes uncertain;
it is not silently retried.

Dispatch state is stored owner-only at `.hctl/dispatch.json`. For migration
compatibility, if that file is absent, hctl validates an existing owner-only
`.hctl/gateway.json`, installs the validated bytes atomically at the new path,
and removes the old regular file. When both paths exist, the dispatch path is
authoritative.
Dispatch state schema 4 retains compatibility with versions 1 through 3;
existing state without outcome reasons remains valid and upgrades on its next
write. A retained task deadline may add an optional bounded reason keyed only
to its corresponding uncertain outcome; it does not introduce another
lifecycle status or execution ledger.

The local stdin adapter and transport-neutral channel controller share the turn
dispatcher's typed submission and event seam. Built-in vendor adapters use the
controller's normalized inbound-message and semantic-outcome interface; this is
not a public plugin ABI or a rich component schema. The JSONL input adapter
remains the reference for durable state and event semantics. Other vendor
channels, generic webhooks, OAuth, proactive delivery, and public listener
management remain outside the MVP.

## Managed tool boundary

The MVP exposes one bounded, read-only `echo` tool, the optional local
`record-friction` built-in, and conventionally authored TypeScript, Python, and
Go tools through one stdio MCP server in both harnesses. Inputs and outputs are
schema-validated. Audit output contains a safe request identifier, tool name,
and lifecycle outcome, never tool arguments or output.

`record-friction` is advertised only when root instructions opt in with
`friction-notes: true`. It remains available to read-only channel sessions
because it changes neither agent source nor workspace state. The tool accepts
one non-empty UTF-8 `note` of at most 1,024 bytes and returns only whether it
was recorded. Generated guidance directs the agent to finish the primary task,
record at most one note only after concrete material friction that could help a
human improve the agent project or hctl integration, and omit routine errors,
causal guesses, proposed fixes, generic advice, sensitive data, transcripts,
logs, and tool output. A store-specific failure returns `recorded: false`; the
agent must not retry it, mention it, or change the user-facing result.

Friction records are private local hctl state outside both agent source and the
selected workspace. They use one exclusive owner-only JSON file per note under
`~/Library/Application Support/hctl/state/friction/agents/AGENT_ID/` on macOS,
`${XDG_STATE_HOME:-~/.local/state}/hctl/friction/agents/AGENT_ID/` on Linux,
and the user configuration root under `hctl/state/friction/agents/AGENT_ID/`
elsewhere. The existing path-derived agent ID prevents collisions between
same-named local sources. Each record contains an identifier, UTC timestamp,
agent ID and name, exact source fingerprint, hctl version, selected native
harness, and the note. It contains no workspace identity or model-supplied
provenance. A per-agent limit of 256 records never overwrites or silently
evicts state.

This inbox is write-only to models and is not telemetry, long-term memory,
evidence, evaluation, a proposal, or a harness-improvement loop. Hctl does not
automatically read, transmit, cluster, score, convert, or apply notes. A remote
or container host remains ephemeral unless its local state is mounted or
preserved outside hctl; no remote durability is inferred or provided.

One long-lived process per authored language serves inspection and calls for
the MCP session. Tool calls are serialized in the current MVP. A call that
exceeds its deadline terminates that language host and fails clearly; graceful
per-call cancellation and automatic host restart are not claimed.

The managed boundary is additive. It does not disable, authorize, observe, or
retry harness-native tools. Secret-bearing tools require the local secretless
operation broker selected by [ADR 0009](adr/0009-use-a-local-secretless-operation-broker.md)
before they ship. The broker resolves an opaque reference only at an authorized
managed invocation and consumes the value for a constrained upstream operation;
it declares no credential or authorization input fields and never returns the
value to a tool host, harness, MCP client, or model.
No backend, credential enrollment flow, connection syntax, or unused broker
code is scaffolded in the MVP.
Codex treats the generated managed server as required and delegates its tool
approval to hctl, avoiding a second harness approval prompt after hctl records
authorization where Codex user and administrator policy permits. This setting
does not affect native or unrelated MCP tools.

## Authored tool lifecycle

Tool source and native lockfiles join the validated source fingerprint. Apply
checks TypeScript with `deno check --frozen`, prepares Python with
`uv sync --locked`, and compiles a generated Go host with native Go module
tooling. Generic TypeScript and Python hosts, their local runtime environments,
and generated Go build output live under the workspace's disposable
`.hctl/cache/tools/`; no normalized tool manifest is written.
The cache records the exact Deno and `uv` executables used during apply so a
harness can start the managed server without inheriting the same shell `PATH`.

The generated MCP command identifies its harness. At startup hctl verifies the
matching workspace apply record, selected agent identity, and source
fingerprint before loading the cached hosts. Authors write typed functions and
do not implement MCP protocol code.

## Staged agent filesystems

Publish one Codex hctl harness image and one Claude hctl harness image. Each
contains the matching hctl release, one pinned native harness, and all supported
authored-tool build and execution inputs. Users may copy in an agent, run
`apply`, and ship that larger derived image directly. That is a supported
journey, not an accidental intermediate image.

For users who want a smaller image, `hctl stage` prepares one
complete runnable filesystem tree at canonical final paths. An ordinary
two-stage OCI build copies that tree onto the harness image's documented
compatible base. Hctl owns preparation and verification of the filesystem; it
does not construct OCI manifests or layers, contact registries, publish, sign,
deploy, or operate images.

```dockerfile
FROM ghcr.io/alee792/hctl/codex:VERSION AS build
COPY . /agent
RUN hctl stage /agent --harness codex --output /out/agent

FROM DOCUMENTED_COMPATIBLE_BASE
COPY --from=build /out/agent/opt/ /opt/
COPY --from=build --chown=65532:65532 /out/agent/workspace/ /workspace/
COPY --from=build --chown=65532:65532 /out/agent/home/hctl/ /home/hctl/
USER 65532:65532
ENTRYPOINT ["/opt/hctl/bin/agent-entrypoint"]
```

The source image provides `/out` as a writable parent owned by UID/GID 65532.
The stage runs as that identity and creates a new child such as `/out/agent`;
the child itself must not already exist.

The staged tree contains hctl, the selected native harness, immutable agent
source, a generated harness integration and apply record, an empty writable
harness home, an entrypoint, and an artifact manifest. It carries only the union
of execution requirements discovered from the agent's tools: Deno for
TypeScript, Python and uv for Python, and compiled Go hosts plus required shared
libraries for Go. Tool-free agents carry none of those runtimes. Build-only
compilers, unused runtimes, module and download caches, and temporary inspection
output are excluded.

The artifact manifest records the generator and harness versions, agent and
source identity, target OS, architecture and ABI, compatible base, required
runtimes, canonical paths, and hashes, modes, and intended ownership for staged
files. Generated configuration and executable receipts name final paths rather
than build-stage paths. It also records directory modes and intended ownership.
The compatible-base contract fixes required native facilities, UID/GID 65532,
writable `/workspace` and `/home/hctl`, and ABI; a glibc payload
is not portable to Alpine without a corresponding musl-compatible harness
image.

Staging requires a new output directory outside the selected source and
workspace. It re-reads every fingerprinted source, rejects symlinks and
collisions, prepares and protocol-inspects tools in a temporary sibling, strips
build-only Deno, Python, and Go state, verifies that preparation did not mutate
authored source, and publishes with one rename only after the manifest is
complete. Repeating the operation with identical source and pinned inputs
produces the same file contents and manifest. The entrypoint verifies the exact
runtime identity, generated harness integration, and source fingerprint before
a turn and refuses to run as an identity other than UID/GID 65532.

The Python interpreter must already be installed at the canonical
`/opt/hctl/runtimes/python` prefix by the pinned harness image. Staging rejects
an arbitrary system interpreter instead of copying a binary whose loader,
standard library, or virtual environment still binds it to its old prefix.

Credentials, native harness login state, user trust decisions, channel runtime
profiles, dispatch state, sessions, logs, registry credentials, signing
material, and deployment configuration remain outside the staged tree. The
selected harness continues to own model calls, native tools, approvals, and
sandbox behavior. Publishing a harness image also requires current permission
to redistribute that harness; ADR 0027 grants no such permission.

## Deferred direction: proposals

Scripts created ad hoc by the agent remain ordinary harness-native workspace
activity unless a human promotes them into `tools/` and reapplies the project.

Generated project instructions may encourage the harness to submit reusable
discoveries through a future managed proposal tool. Instructions can influence
this behavior but cannot enforce it or observe native filesystem writes.

A proposal is a local, inert record of a candidate improvement to one existing
instruction, skill, or managed-tool source file. It does not modify active
authored source, a generated harness integration, or a running harness.
Proposal files belong to the producing workspace at `.hctl/proposals/ID/`, not
to the agent source that they name. `proposal.md` explains the suggestion and
records its target, selected source and run provenance, and the target's
SHA-256 content hash; `change.diff` is a bounded unified diff. `review.md` is
added only after a human accepts or rejects it. After publication,
`proposal.md` and `change.diff` are immutable proposal artifacts; they are not
evidence that the suggestion will help. `review.md` is the separate later
human decision record. There is no manifest or proposal registry.

A proposal can target `instructions.md`, a UTF-8 text file in an existing
skill, or an existing managed-tool source file. Binary skill resources are
outside this unified-diff flow. A proposal cannot add, remove, move, or rename
files, change a dependency file, or escape the agent source. A changed or
missing target is stale and must never be applied or rebased automatically.
The reviewer either manually makes a current change in the agent source and
reapplies it, or rejects the proposal. Both accepted and rejected records are
retained until a human removes them.

Proposals must not contain credentials, secrets, raw tool outputs, or
conversation transcripts. A future capture tool must tell callers this rule and
bound the content it accepts. It must not claim that it reliably detects or
removes secrets; owner-readable storage and human review do not make prohibited
content safe to record.

A future managed proposal tool may create this workspace-local record after
validating its bounded target, base content, and provenance. It must remain
additive: it cannot apply a diff, execute proposed code, reapply, delete a
proposal, or control native filesystem activity. Proposal capture, source
mutation, and review UX remain outside the MVP and are not scaffolded. See
[ADR 0008](adr/0008-keep-agent-proposals-workspace-local-and-inert.md).

## Failure and safety behavior

- Missing, stale, ambiguous, or edited generated harness integrations fail
  closed.
- Input, output, queue, process lifetime, state size, and protocol lines are
  bounded.
- Durable state is owner-readable only and written atomically.
- Process failure is distinct from a completed or failed model turn.
- An uncertain external effect is never described as exactly-once or retried
  without a target idempotency contract.
- Hctl-owned diagnostics do not expose credentials, private prompts, or raw
  process output. Native harness and external-server diagnostics remain outside
  that managed claim.
- A future secretless broker validates a reference, managed operation, target,
  and authorization on every call; uses private local IPC, a sensitive
  session-scoped authorization capability for one managed MCP server instance,
  and an upstream credential of its own; and returns/audits only bounded
  secret-free data. The capability is delivered only to hctl's managed
  MCP-server/broker pair, stays out of ordinary tool inputs, model-visible I/O,
  generated configuration, logs, and audit, and is rotated/removed with the
  managed MCP server process.
  Its typed operation schema declares no credential/authentication fields and
  rejects unknown fields; it cannot reliably detect a secret smuggled into an
  allowed string after the model has submitted it. It does not protect against
  native harness capabilities or any other process running as the same OS user.

## MVP acceptance

The MVP is complete when credential-free tests prove:

1. One authored project compiles deterministically for both harnesses.
2. Apply produces native, discoverable harness files and refuses conflicts.
3. Both generated harness integrations expose the same managed MCP tool
   surface.
4. Both headless drivers start and resume sessions against fake harnesses.
5. Input arriving during an active turn is durably accepted and processed
   later in FIFO order.
6. Caller-provided input IDs are deduplicated.
7. Restart recovery marks unproven active work uncertain.
8. Managed audit output remains content-free.
9. A mixed TypeScript, Python, and Go project is prepared once per apply,
   exposed identically by both generated MCP configurations, and reuses one
   host process per language across calls.
10. One agent project can be applied outside its source directory; generated
    files and execution use the selected workspace while dependencies and tool
    definitions remain rooted in agent source.
11. Immediate subagents are generated in each harness's native format, inherit
    the parent setup without duplicated child tools or skills, and map optional
    `low`, `medium`, or `high` effort to the exact native field.
12. Agent Skills directories and their regular-file resources round-trip into
    both native project skill locations, including executable intent, while
    recognized unsupported vendor metadata remains intact and produces a
    path-, field-, and harness-specific warning.
13. Harness-specific regular files round-trip only into their selected native
    project directory, join stale-source detection, and use the same collision,
    ownership, modified-file, and cleanup protections as generated setup.
14. An authorized Discord Gateway message is durably dispatched for both
    harnesses, irrelevant output can resolve to `HCTL_NO_REPLY`, visible output
    is delivered through bounded replies, and the bot token is absent from
    source, generated files, state, logs, and child environments.
15. Nested Markdown schedules validate and fingerprint identically for both
    harnesses, and a one-shot trigger deduplicates stable occurrence IDs while
    opening a fresh native session for each accepted occurrence and discarding
    model text. Its independent turn deadline aborts a stalled native process,
    durably retains uncertainty for duplicate retries, and permits a later
    occurrence to open a fresh session.
16. An exact Discord write-access result promotes only that conversation into
    a validated branch-backed Git worktree, resumes the same Claude or Codex
    session under workspace-write policy, and continues the original request
    once without exposing internal control text.
17. Runtime-wide resident-session and active-turn limits keep accepted work
    durable under saturation, hibernate eligible idle capacity, and advance
    turns fairly across conversations without a model scheduler.
18. Concurrent guild and DM mutations use distinct worktrees and native
    sessions for both Claude and Codex, survive independent hibernation and
    restart, deliver out-of-order results only to their originating surfaces,
    and contain ordinary harness or worktree failures to one conversation.
19. Startup reconciliation preserves every worktree that is busy, uncertain,
    dirty, unmerged, or unverifiable and retires only exact clean merged
    assignments through restart-safe, idempotent cleanup.
20. A transport-neutral interactive request survives restart in its owning
    dispatch conversation, accepts one authorized normalized answer exactly
    once, parks without consuming harness or active-turn capacity, and preserves
    ambiguous delivery or continuation as uncertain without automatic replay.
21. A foreground UTC schedule clock admits only current, non-overlapping
    occurrences through one bounded durable task runtime, drains admitted work
    on shutdown, and never backfills missed minutes or emits model output.
22. A credentialless fake native-MCP package generates and launches the exact
    Claude and Codex project mappings without a vendor-specific code path; a
    conspicuous fake environment value reaches only the fixture process and is
    absent from generated files, apply/package state, staging, diagnostics, and
    retained evidence.

## Explicit non-goals

- A model loop, context manager, or cross-harness chat UI
- Channels other than conversational Discord Gateway, generic webhooks, and
  proactive vendor delivery
- Claude Agent SDK or hosted OpenAI agent runtimes
- Background or distributed schedule clocks, workflows, independently
  configured nested subagents, or deployment orchestration
- Building OCI manifests or layers, publishing or signing images, deployment
  orchestration, or hosted image operation
- Governance claims over native harness tools
- Hosted secret managers and model-visible secret-bearing managed operations
- GitHub OAuth or GitHub App enrollment, a managed MCP proxy, credential
  brokering, per-call hctl authorization or confirmation, and exact Git branch
  publication through MCP
- Automatic or unreviewed promotion of agent-authored improvements
