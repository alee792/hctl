# Remote components design notes

- Status: the outcome-level direction is accepted in `docs/vision.md`; the
  standalone MCP source and command contract is implemented through ADR 0034
  and #97, and ADR 0035's shared Plugin and Skill acquisition foundation is
  implemented through #99 while #100 and #101 still own public commands
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
- The operator-installed official GitHub MCP package now uses the generic
  `connections/<name>.md` inventory and emits native Claude and Codex
  configuration without a provider branch. Its maintainer journey and
  credential-free operator documentation and regressions are complete;
  separately authorized live PAT acceptance has not run.
- The anonymous managed GitHub client and its runtime fallback have been
  removed. The external channel-adapter host now provides a second capability
  on the generic installed-package envelope without a vendor switch in hctl
  core.
- Hctl now contains shared non-CLI acquisition, provenance, update, removal,
  status, and project-load verification primitives. It does not yet expose the
  public Agent Plugin or Agent Skill command families.

## Filesystem model

### Agent root

Use convention and explicit selection together. ADRs 0034 and 0035 apply this
rule to connection and acquired-dependency commands:

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

The GitHub connection is a fixture and consumer of the generic path, not the
template for another provider switch. Completed issues #67, #71, and #72
provide the regression baseline; completed issue #97 generalizes authored
source selection, native generation, commands, current-state checks, and
staging without weakening those journeys.

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
- Manual replacement is the only currently exposed hctl update path. ADR 0035
  defines the client-owned automated replacement contract; its shared engine
  is implemented, while the public #100 and #101 journeys remain a product gap,
  not behavior prescribed by the specification.

Issue #96 updates the README, product specification, and glossary to show the
publisher and consumer roles explicitly. Those canonical documents now state
that acquisition and update mechanics are client-owned, describe hctl's
current manual local-copy workflow, and keep the future automated consumer
contract under #95 rather than attributing it to the open specification.

## Accepted acquisition and update properties

Plugins and Skills are both directory dependencies and share one acquisition
foundation rather than duplicating download, pinning, trust, drift, and atomic
replacement logic. ADR 0035 settles the first source forms as an exact local
directory, an HTTPS Git repository plus ref and optional exact subdirectory,
or a direct digest-pinned HTTPS ZIP/TAR.GZ archive plus optional exact
subdirectory. A Git branch or tag is only a tracking input and resolves during
explicit add/update to one retained commit.

Acquisition copies the complete validated directory into portable conventional
source. Apply therefore never fetches a component, contacts its source, or
resolves a moving reference. An optional closed root
`hctl-dependencies.json` records only hctl-acquired component provenance and
deterministic installed-tree identity; it does not register components or
replace `plugin.json` or `SKILL.md`. The externally produced
`skills-lock.json` remains opaque and independently owned.

Tracked trees are immutable while acquired. Offline status reports clean,
drifted, or missing state, and apply/stage fail before workspace mutation when
tracked source and provenance disagree. Update requires a clean tree, resolves
only on explicit request, reconfirms the complete code-bearing candidate, and
atomically replaces tree plus lock. Remove refuses drift unless the operator
uses the exact destructive override. Manual untracked Plugins and Skills keep
their existing behavior.

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
- #97 completed the accepted generic filesystem-authored native MCP connection
  contract.
- #98 accepted the shared acquired-dependency contract for Agent Plugins and
  Skills in ADR 0035.
- #99 completed acquisition, provenance, drift, and replacement mechanics.
- #100 owns the Agent Plugin consumer commands and is the next implementation
  slice.
- #101 owns the Agent Skill consumer commands and follows the same landed
  foundation.

Issues #100 and #101 can now leave dependency-blocked triage after their exact
public command surfaces are reconciled with the landed shared interfaces.

## Questions settled by ADR 0035

1. First source forms are local directory, HTTPS Git plus ref, and direct
   digest-pinned HTTPS ZIP/TAR.GZ archive, each with an optional exact
   subdirectory and no recursive component search.
2. Add/update vendors complete bytes into portable source; apply never fetches.
3. `hctl-dependencies.json` records closed hctl provenance and tree identity,
   while `skills-lock.json` remains opaque.
4. Plugin manifest and Skill frontmatter names derive deterministic acquired
   destinations. Existing paths and prospective supported component
   collisions fail without replacement.
5. Acquired trees are trusted code, require terminal confirmation or `--yes`,
   remain immutable while tracked, and use explicit atomic update/removal.
6. Existing noninteractive HTTPS Git credentials may be used by Git, but hctl
   accepts and retains no credential or raw helper output.

## Implementation-ticket gate

ADR 0034 satisfied this gate for completed #97. ADR 0035 and #99 now satisfy it
for the shared dependency foundation. #100 and #101 can bind their public
component-specific commands to the landed interfaces without duplicating
source, trust, provenance, locking, drift, or recovery mechanics.

### Accepted first delivery boundary

The first dependency delivery ends at explicit source selection, review and
trust, complete directory publication, offline status, deliberate update or
removal, ordinary `hctl apply`, and normal native harness interaction. It
excludes marketplace browsing, automatic updates, managed remote MCP, OAuth,
dependency scripts, and deployment changes. Existing image staging remains a
separate deployment seam.

All dependency commands take the exact positional agent root. Plugin and Skill
destinations derive from their validated publisher names and never overwrite.
Add/update are the only source-networked phases. They vendor complete bytes and
commit exact provenance; apply and stage may still prepare unrelated locked
language dependencies on a cold cache, so the product does not describe all of
apply as offline.

The common foundation owns safe source resolution, pinning, bounds, tree
identity, trust summaries, the closed lock, drift, transaction recovery, and
atomic mutation. Component hooks own marker validation, derived name,
destination, and collision diagnostics. Public Plugin and Skill commands
remain separate so the shared layer does not become a universal package
runtime.

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

The generic implementation retains these credential-free evidence journeys:

1. Add a Skill from a temporary remote Git repository and apply it from a
   separate workspace.
2. Add a complete Plugin containing a Skill and fake MCP server, then prove
   both are consumed without reconstructing the manifest.
3. Configure two provider-neutral MCP fixtures through the same generic
   connection path, one installed stdio and one remote Streamable HTTP.
4. Prove apply never fetches, resolves, or upgrades a dependency reference and
   that add/update are the only component-networked phases.
5. Prove collision, unsafe archive or tree content, identity mismatch, and
   locally drifted update/remove fail without partial source mutation.

## Vision impact

`docs/vision.md` now records the accepted outcome-level direction: an author
can assemble a portable agent project from existing Agent Skills, Agent
Plugins, and MCP servers without writing provider-specific protocol adapters.
Acquired dependencies are explicit, pinned, and inspectable, while native
harnesses continue to own unmanaged MCP runtime behavior and hctl does not
become a marketplace, automatic updater, or replacement model loop.

The vision intentionally omits commands, source locator syntax, lock fields,
and drift behavior. Those accepted details now live in the product
specification and ADRs 0034 and 0035.
