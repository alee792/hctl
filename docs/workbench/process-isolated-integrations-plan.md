# Process-isolated third-party integrations execution plan

This is the durable execution contract for completing the process-isolated
third-party integration initiative in `alee792/hctl`. GitHub Issues remain the
authoritative specifications and tracker. This plan records sequencing and
cross-issue boundaries so execution can resume safely after context compaction.

## Scope

Complete GitHub issues
[#75](https://github.com/alee792/hctl/issues/75),
[#76](https://github.com/alee792/hctl/issues/76),
[#65](https://github.com/alee792/hctl/issues/65),
[#66](https://github.com/alee792/hctl/issues/66),
[#67](https://github.com/alee792/hctl/issues/67),
[#71](https://github.com/alee792/hctl/issues/71),
[#72](https://github.com/alee792/hctl/issues/72),
[#82](https://github.com/alee792/hctl/issues/82),
[#83](https://github.com/alee792/hctl/issues/83),
[#84](https://github.com/alee792/hctl/issues/84), and
[#85](https://github.com/alee792/hctl/issues/85) in native dependency order.
Finally, verify the completion criteria and status of epics
[#74](https://github.com/alee792/hctl/issues/74),
[#64](https://github.com/alee792/hctl/issues/64), and
[#81](https://github.com/alee792/hctl/issues/81).

Treat `AGENTS.md`, `docs/vision.md`, `docs/product-spec.md`,
`docs/glossary.md`, relevant ADRs, and `docs/workbench/status.md` as
authoritative. Read each issue completely before acting. Preserve unrelated
user work.

## Dependency graph

1. Complete #75.
2. After #75 closes, #76, #65, and #82 are independently eligible.
3. Complete #66 after #65 and #76; complete #83 after #76 and #82.
4. Complete #67 after #65 and #66; complete #84 after #82 and #83.
5. Complete #71 after #67; complete #85 after #84.
6. Complete #72 after #71.
7. Verify epics #74, #64, and #81 after every implementation issue closes.

Parallelize only dependency-independent issues and only with isolated
worktrees or explicitly owned sub-agent scopes. Never implement dependent
issues against incompatible unmerged assumptions. Adapt successors to the
merged predecessor instead of duplicating its contracts.

## Per-issue protocol

For every issue:

1. Confirm it is open, unassigned, labeled `ready-for-agent`, and has no open
   blockers.
2. Make assignment to the acting maintainer the first tracker write.
3. Use one focused branch and pull request unless repository instructions say
   otherwise.
4. Implement the literal acceptance criteria without pulling successors
   forward.
5. Keep vendor dependencies out of hctl's root module.
6. Use narrow behavior-oriented interfaces and process boundaries. Do not add
   service containers, arbitrary plugin loading, or vendor switches in core.
7. Run the narrowest proving tests, then the appropriate repository or module
   quality gates.
8. Update canonical documentation and ADRs when required. Never hand-edit
   generated OpenWiki files.
9. Preserve complete safe failure output and credential-free automated
   evidence.
10. Open a pull request linked to the issue that states tests, evidence,
    limitations, and remaining dependent work.
11. Continue only after the implementation is merged and the issue is closed,
    or the tracker otherwise shows its blocker resolved. A pull request alone
    is not completion.
12. While waiting for review or merge, monitor that work instead of claiming a
    blocked successor.

## Product boundaries

### Package foundation

- #74-#76 define a metadata-first package envelope, explicit install trust,
  immutable platform artifacts, capability versioning, a shared cache, offline
  apply, and selective staging.
- Integration code runs in separate executables. Hctl never imports vendor SDKs
  or integration modules.
- The package envelope is shared, while every capability has its own narrow
  contract. Do not invent one generic runtime API for MCP, channels, providers,
  and arbitrary extensions.

### GitHub and native MCP

- GitHub uses the `native-mcp` capability and the official external
  `github-mcp-server` executable.
- Authentication uses the deliberately simple unmanaged contract: ambient
  `GITHUB_PERSONAL_ACCESS_TOKEN`.
- The harness, model, and native processes may access that environment value.
- Hctl never persists the resolved value in source, package state, generated
  files, images, staging, logs, diagnostics, or retained evidence.
- Hctl does not broker, filter, authorize, confirm, proxy, observe, or audit
  native GitHub MCP calls in this delivery.
- Fine-grained PAT repository scope, minimal permissions, expiration, runtime
  isolation, native harness trust, and operator judgment form the security
  boundary.
- Native Git and `gh` authentication are separately operator-owned. Do not
  promise exact branch publication through MCP.
- Do not implement #70, #73, or #77-#80 under this goal.

### Discord and channel extraction

- Discord uses a separate `channel-adapter` capability and external adapter
  process. Do not route inbound Discord through MCP.
- Hctl core retains the transport-neutral channel controller, dispatcher,
  harness sessions, execution policy, worktrees, capacity, and durable generic
  state.
- The Discord module owns `discordgo`, Gateway transport, credentials/keyring,
  payload decoding, rendering, interactions, rate limits, and Discord
  diagnostics.
- After #85, the root hctl module neither imports the Discord module nor retains
  Discord-only dependencies.
- Core and adapter communicate only through #82's versioned, bounded semantic
  protocol. Vendor payloads, SDK objects, tokens, arbitrary markup, executable
  code, and workspace paths never cross it.
- Preserve `channels/discord.md` semantics and the literal
  setup/status/remove/run journey.
- Preserve credential enrollment when safely possible. Ambient
  `HCTL_DISCORD_TOKEN` compatibility may exist in the parent launcher
  environment, but hctl treats it as opaque, passes it only to the exact
  adapter, and scrubs it from every unrelated child.
- Document honestly that process separation provides dependency and ownership
  isolation, not an OS sandbox.

## Safety

- Use fake credentials, fake upstreams, and deterministic adapters by default.
- Perform live GitHub or Discord acceptance only with explicit authorization
  and temporary least-privilege credentials.
- Never merge, publish releases, delete branches, rotate real credentials, or
  make unrelated external changes without explicit authority.
- If canonical documents, merged predecessors, and an issue contradict one
  another, surface the conflict instead of silently choosing a new
  architecture.
- Give every parallel worker explicit issue/file ownership, require it to
  preserve others' work, and prevent overlapping dependent implementations.

## Completion criteria

The goal is complete only when:

- #75, #76, #65, #66, #67, #71, #72, #82, #83, #84, and #85 are implemented
  and closed with merged evidence.
- The official GitHub MCP server installs and launches through generated native
  Claude and Codex configuration without rebuilding hctl.
- Discord runs through the installed external channel adapter with existing
  product behavior preserved.
- Root hctl builds and tests without GitHub or Discord SDK dependencies.
- GitHub-free and Discord-free agents omit their respective artifacts from
  generated and staged closures.
- Package resolution during apply remains offline.
- Credential boundaries and unmanaged limitations are documented plainly.
- Relevant root and separate-module quality gates pass.
- Epics #74, #64, and #81 accurately reflect completion and implementation
  evidence.

## Progress reporting

Each progress update states the active issue, completed evidence, pull request
link, current blockers, and next eligible frontier. Persist until the complete
goal is achieved or external review or authorization makes further progress
impossible.
