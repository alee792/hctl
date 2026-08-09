# Remote components design notes

- Status: the outcome-level direction is accepted in `docs/vision.md`; the
  standalone MCP source and command contract is accepted in ADR 0034 for #97,
  while Plugin and Skill acquisition mechanics remain ideation under #98
- Started: 2026-08-08
- Purpose: retain the product-model questions and proposed journeys behind
  the filed GitHub issues until their contracts are accepted in the product
  specification and ADRs

## Why this note exists

The desired product should let a non-developer add an MCP connection, Agent
Plugin, or Agent Skill that already exists elsewhere, apply the agent, and
interact with it through the selected native harness. The current prototype
can consume local source, but its acquisition and update journeys are either
absent or provider-shaped. Broad discussion can obscure the distinct package,
runtime, credential, and ownership questions, so this note keeps them separate.

## Current contract and implementation

- An agent project is one explicitly selected directory. `instructions.md` is
  its required conventional root file; there is no authored hctl manifest.
- Commands currently receive that directory as the positional `AGENT`. The
  workspace is selected independently and defaults to the agent directory.
  Directory placement never implicitly selects a parent repository or an
  `agents/` directory.
- Root Agent Skills are already consumed from local `skills/<name>/`
  directories.
- Agent Plugins are already consumed from local `plugins/<storage-name>/`
  directories. Hctl validates the publisher-authored `plugin.json`, imports
  skills, and maps supported `mcp.json` servers into native harness
  configuration.
- The operator-installed official GitHub MCP package now emits native Claude
  and Codex configuration. Its maintainer journey and credential-free operator
  documentation and regressions are complete; separately authorized live PAT
  acceptance has not run. Installed-package resolution and native server
  rendering are generic once given a resolved capability, but authored project
  loading, selection and resolution wiring, validation, staging, and generated
  model guidance remain GitHub-specific.
- The anonymous managed GitHub client and its runtime fallback have been
  removed. The external channel-adapter host now provides a second capability
  on the generic installed-package envelope without a vendor switch in hctl
  core.
- Hctl does not currently acquire or update Agent Plugins or Agent Skills.

## Filesystem model

### Agent root

Use convention and explicit selection together. ADR 0034 accepts this rule for
connection commands; #98 still owns applying it to acquired dependencies:

- The destination agent root is the exact `AGENT` directory supplied to the
  command, consistent with `hctl apply AGENT`.
- The required `instructions.md` proves that the selected directory is an
  agent project.
- Callers already in the root pass `.` explicitly. Hctl does not default,
  search ancestors, or infer a parent `agents/` directory.

### Imported component root

Resolve a source to one exact component directory:

- `plugin.json` identifies an Agent Plugin root.
- `SKILL.md` identifies an Agent Skill root.
- A source that points at a monorepo needs an explicit subdirectory. Hctl
  should not recursively search a remote tree and guess which package the user
  intended.
- Acquisition copies the complete selected component directory after bounded
  validation. It must not reconstruct a plugin from selected manifest fields
  or flatten a skill into a second inventory.

### MCP connection source

ADR 0034 accepts `connections/<name>.md` as the authoring surface:

- The filename supplies the stable connection name.
- Frontmatter supplies machine-readable transport and target parameters.
- The optional Markdown body supplies trusted, model-facing usage context. It
  is not sent to the upstream MCP server and is not a credential channel.
- Credential values never appear in the file. The initial generic native path
  covers credential-free remote MCP and operator-installed stdio MCP.
  Authenticated managed remote MCP remains dependent on the accepted gateway,
  credential-isolation, and OAuth work.

Installed source contains exactly `type: mcp`, `package`, and `capability`.
Remote source contains exactly `type: mcp`, `transport: streamable-http`, and
one credential-free HTTPS `url`. The first remote slice has no headers, auth,
timeouts, filters, or approval fields. The accepted examples, bounds,
diagnostics, rendering, commands, and clean body-only migration are recorded in
[ADR 0034](../adr/0034-author-generic-native-mcp-connections.md).

## Genericity boundary

Standards-compatible MCP should not require one hctl adapter per vendor. Hctl
can validate a generic connection declaration and compile it to the selected
harness's native MCP configuration. The native harness then owns MCP process
lifecycle, authentication, approval, calls, and effects.

A vendor adapter is still appropriate when the upstream does not expose MCP or
when hctl deliberately owns a managed runtime contract, such as a channel
transport. That is a different product boundary from consuming a native MCP
server.

The GitHub connection should become a fixture or consumer of the generic path,
not the template for another provider switch. Completed issues #67, #71, and
#72 provide the regression baseline; issue #97 owns generalizing the remaining
authored source selection and staging without weakening those journeys.

## Agent Plugin publisher and consumer distinction

The Agent Plugins v1 specification defines a directory package, its
publisher-authored `plugin.json`, and conventional component locations. It
does not define a universal install command, marketplace, registry,
distribution protocol, or update workflow.

Therefore:

- A publisher creates `plugin.json` and the complete plugin directory.
- A consumer acquires that complete directory; they should not write a new
  `plugin.json` merely to consume the plugin.
- `plugins/` vendoring is hctl's current dependency decision, not a requirement
  of the Agent Plugins specification.
- Manual replacement is the only current hctl update path. That is a product
  gap, not prescribed behavior from the specification.

Issue #96 updates the README, product specification, and glossary to show the
publisher and consumer roles explicitly. Those canonical documents now state
that acquisition and update mechanics are client-owned, describe hctl's
current manual local-copy workflow, and keep the future automated consumer
contract under #95 rather than attributing it to the open specification.

## Proposed acquisition and update properties

Plugins and Skills are both directory dependencies and should share one small
acquisition primitive rather than duplicate download, pinning, drift, and
replacement logic.

Candidate supported sources are:

- an exact local directory;
- a Git repository plus exact commit and optional component subdirectory; and
- a pinned HTTPS archive plus expected digest and optional component
  subdirectory.

Unresolved questions include whether friendly release tags are accepted only
as inputs that immediately resolve to immutable commits or digests, and
whether marketplace-specific locators belong in the first slice.

The workflow should eventually provide explicit add, status, update, and
remove operations. Provenance belongs in hctl-owned dependency metadata or a
lock record, not in `plugin.json` or `SKILL.md`. It should retain source,
component path, immutable revision or digest, and installed content identity.
Updates should be deliberate and reviewable and must never resolve a moving
reference implicitly during `apply`.

Whether `apply` may fetch an already locked immutable component into a local
cache remains open. The current command is not globally offline: authored-tool
preparation runs frozen or locked Deno, uv, and Go dependency operations that
may use the network on a cold cache. The accepted offline property is narrower:
plugin schema validation, installed integration lookup, and connection
discovery do not acquire or update remote packages.

The repository currently contains a `skills-lock.json` written by an external
skill-installation workflow. Its origin/path/hash shape is useful evidence but
is not yet an hctl contract and should not be adopted silently.

## Existing related issues

- #67, #71, and #72 are completed evidence for the operator-installed GitHub
  MCP package, native Claude and Codex rendering, the maintainer journey, and
  credential-free operator documentation and regressions. Live PAT acceptance
  remains separately authorized and unexecuted.
- #74, #75, and #76 are completed evidence for the generic installed-package
  foundation, package management, and harness-neutral native MCP resolution.
- #84 and #85 are completed evidence that the same package envelope can host
  an external channel adapter while hctl core remains vendor-agnostic.
- #77: managed MCP gateway.
- #78: credential isolation.
- #80: brokered remote HTTP MCP and OAuth.

## Filed issue graph

- #95 is the outcome epic for remote connections and reusable dependencies.
- #96 is the completed documentation clarification for Agent Plugin publisher
  and consumer journeys.
- #97 owns implementation of the accepted generic filesystem-authored native
  MCP connection contract.
- #98 owns the shared acquired-dependency contract for Agent Plugins and Agent
  Skills.
- #99 owns acquisition, provenance, drift, and replacement mechanics and is
  blocked on #98.
- #100 owns the Agent Plugin consumer commands and is blocked on #99.
- #101 owns the Agent Skill consumer commands and is blocked on #99.

Issue #97 is ready for implementation after its accepted specification lands.
Issues #98 through #101 remain `needs-triage` until their source locator,
pinning, provenance, drift, trust, and credential decisions below are accepted.
Filing the graph records that later product work; it does not settle those
contracts.

## Questions to settle next

1. Which immutable remote locator forms are required for the first Plugin and
   Skill acquisition slice?
2. Where should source provenance and local content identity be recorded so it
   is portable enough for another machine but remains separate from upstream
   manifests?

ADR 0034 settles connection root selection, schema, Markdown rendering, command
semantics, and migration for #97. Issue #98 owns the remaining shared
dependency decisions before #99 through #101 proceed.

## Implementation-ticket gate

ADR 0034 satisfies this gate for #97. The remaining decisions materially change
Plugin or Skill source, commands, or safety behavior and must be accepted before
#99 through #101 are marked `ready-for-agent`.

### First delivery boundary

Decide whether the first delivery ends at explicit dependency selection,
generic native MCP configuration, `hctl apply`, and normal native harness
interaction. The recommended boundary excludes marketplace browsing,
automatic updates, managed remote MCP, OAuth, and deployment changes. Existing
image staging remains the separate deployment seam.

Do not describe all of `apply` as offline. Decide instead whether Plugin and
Skill contents are committed beneath the agent root before apply, or whether
apply may fetch an already locked immutable dependency into a verified cache.
In either model, apply must not select a newer version, resolve an unpinned
moving reference, grant machine-wide integration trust, or contact a runtime
MCP endpoint.

### Command targets and names

Connection add, status, and remove now require an exact positional `AGENT`;
callers in the root use `.` explicitly. #98 must apply or deliberately amend
that accepted precedent for dependency add, status, update, and remove and
decide how each dependency destination name is chosen:

- a connection needs an explicit name because a remote MCP endpoint may expose
  no stable package identity;
- a Skill can use its validated Agent Skills `name`; and
- a Plugin can derive a storage-safe name from its manifest or require an
  explicit destination name because its current directory name is only a
  storage identity.

No operation should silently replace an existing destination.

### Generic connection schema and rendering — accepted

ADR 0034 accepts exact `connections/<name>.md` frontmatter discriminating
`type: mcp` and exactly one of:

- an installed package and `native-mcp` capability; or
- a credential-free remote Streamable HTTP URL.

Installed process arguments and ambient environment names remain package-owned.
The remote first slice excludes literal headers. Credential values, provider
names, tool catalogs, tool filters, and approval grants remain absent.

The optional Markdown body reaches both harnesses through one bounded generated
instruction section naming the connection and including its nonempty prose
once. It is not sent upstream and does not rewrite MCP tool descriptions,
schemas, or server-returned instructions.

### Remote source locator

Select the initial source forms. A small generic first delivery could support
an existing local directory and an HTTPS Git repository plus a required ref
and optional component subdirectory. Add resolves a branch or tag to an exact
commit; the lock records both the requested tracking ref and resolved commit.
Pinned HTTPS archives and marketplace-specific locators can remain later
additions unless a required real package is not available through Git.

Choose one materialization model:

- `add` copies the full component into portable source, so apply needs no
  component network access; or
- `add` records an exact lock and apply fetches only missing immutable content
  into a verified cache, similar to existing locked language dependency
  preparation.

The second model gives a better clean-clone journey and avoids committing
third-party bytes, but requires cache, fetch, trust, and staging contracts. A
later apply may work offline from cache, but cold apply is network-dependent.

Decide whether private Git sources may use the operator's existing Git
credential setup in the first delivery. No credential value or helper output
may enter agent source, diagnostics, or retained state.

### Portable provenance record

Choose one root-level, committed lock format shared by acquired Plugins and
Skills. A candidate is `hctl.lock.json`, containing only dependency kind,
destination, original source, requested ref, resolved immutable revision,
component subdirectory, and installed tree identity. It must not become a
second component inventory or be required for manually copied local
dependencies.

Decide whether the existing externally produced `skills-lock.json` is migrated,
left independent, or deliberately unsupported. Do not silently claim its
schema.

### Trust and executable content

Treat remote acquisition as code installation. Skills may contain scripts and
Plugins may contain MCP executables or other runnable resources. Decide the
operator confirmation contract for an interactive terminal and the explicit
noninteractive equivalent. The confirmation should show the exact source,
resolved revision, component path, identity, and detected executable-capable
contents. Acquisition must retain existing path, symlink, size, and file-count
bounds and publish the destination atomically.

### Local edits, update, and removal

Choose whether acquired directories are immutable managed dependencies or
ordinary editable vendored source. The recommended filesystem-forward behavior
is:

- apply uses the current local files;
- status reports drift from the acquired tree identity;
- update and remove refuse to overwrite or delete drifted contents unless the
  operator uses an explicit destructive override; and
- update is always requested explicitly and atomically replaces the directory
  only after validation.

Also decide whether changing the tracked ref is part of `update` or a separate
operation. No background or apply-time update is allowed.

### Migration and overlap

ADR 0034 accepts a clean pre-publication break for body-only
`connections/github.md`, with an exact generic migration diagnostic and no
compatibility inference or automatic rewrite. The generic implementation
supersedes its provider-specific parser and generator. Completed issues #67,
#71, and #72 remain the installed GitHub package fixture and regression baseline
rather than a separate connection architecture. There is no anonymous managed
GitHub implementation or runtime fallback left to remove.

Keep Plugin-bundled MCP separate: adding a Plugin preserves and consumes its
publisher-authored `mcp.json`; it does not synthesize a second connection file.
Adding one standalone MCP endpoint or installed server creates a connection.

### Acceptance journeys

Define credential-free evidence before implementation:

1. Add a Skill from a temporary remote Git repository and apply it from a
   separate workspace.
2. Add a complete Plugin containing a Skill and fake MCP server, then prove
   both are consumed without reconstructing the manifest.
3. Configure two provider-neutral MCP fixtures through the same generic
   connection path, one installed stdio and one remote Streamable HTTP.
4. Prove apply never resolves or upgrades a moving dependency reference. If
   cold locked fetch is accepted, prove exact identity verification, cache
   reuse, and an explicit offline failure mode; otherwise prove add/update are
   the only component-networked phases.
5. Prove collision, unsafe archive or tree content, identity mismatch, and
   locally drifted update/remove fail without partial source mutation.

## Vision impact

`docs/vision.md` now records the accepted outcome-level direction: an author
can assemble a portable agent project from existing Agent Skills, Agent
Plugins, and MCP servers without writing provider-specific protocol adapters.
Acquired dependencies are explicit, pinned, and inspectable, while native
harnesses continue to own unmanaged MCP runtime behavior and hctl does not
become a marketplace, automatic updater, or replacement model loop.

The vision should not specify commands, frontmatter, source locator syntax,
lockfile fields, or drift behavior. Those details belong in the product
specification and accepted ADRs after the choices above are made.
