# ADR 0029: Bound authored projects with aggregate budgets

- Status: accepted
- Supersedes count ceilings in: [ADR 0013](0013-run-schedules-as-fresh-dispatch-tasks.md), [ADR 0019](0019-import-vendored-agent-plugin-skills.md)
- Extends: [ADR 0020](0020-map-plugin-mcp-through-native-harness-configuration.md)

## Plain-English summary

Hctl keeps hard safety ceilings for authored projects, but ordinary agents
should not encounter them. Skills, tools, subagents, schedules, plugins, plugin
MCP servers, and harness-specific files receive high count ceilings. Aggregate
file and byte budgets, bounded generated configuration, and separate tool
catalog and call limits control actual resource use. The ceilings remain
internal product constants rather than author configuration.

## Decision

Raise the authored-project cardinality ceilings to 256 aggregate root and
plugin skills, 128 tools, 128 immediate subagents, 256 schedules, 128 plugin
directory entries, 1,024 entries in each plugin `skills/` location, 128 accepted
plugin MCP servers, and 1,024 selected harness-specific files. A skill and the
tool-source inventory may each contain at most 1,024 files.

Root and imported skills share one 8,192-file and 64 MiB budget. Each skill may
contain at most 64 MiB, `SKILL.md` remains limited to 128 KiB, and another
resource may contain at most 16 MiB. Tool source and native dependency files
share a 64 MiB budget. Subagent sources share 16 MiB, schedule sources share
16 MiB, and selected harness-specific files retain their existing 8 MiB
aggregate budget.

The authored-tool language-host protocol uses separate response ceilings. Tool
calls, results, and individual schemas retain their 64 KiB bound; the complete
inspection catalog may contain up to 8 MiB. Raising discovery therefore does
not weaken the managed invocation boundary.

Generated Claude `.mcp.json` and Codex `.codex/config.toml` files may contain at
most 8 MiB, and verification applies that same ceiling. Other generated-file
verification accepts the largest supported 16 MiB skill resource, so apply and
later verification use compatible bounds.

These limits are not configurable through files, environment variables, or CLI
flags. Root and project-level directory violations fail before workspace
mutation. Optional plugin components keep their existing isolation behavior:
invalid or excess skills and servers warn and skip at their authored paths when
the containing plugin remains independently valid.

## Context

The initial bootstrap accepted between one and eight portable skills. Later
filesystem, Agent Skills, and plugin changes described eight only as the
existing limit and carried it forward. No accepted decision explains the
number, and it is not part of the portable Agent Skills contract or hctl's
native harness integration.

Removing bounds would be unsafe because load and apply read, validate,
fingerprint, retain, and copy authored bytes. Raising only item counts would
also multiply worst-case memory and generated-state work: 256 independently
maximal 8 MiB skills would admit 2 GiB before generated copies. Aggregate
budgets express the resource boundary directly while allowing many small,
composed skills.

The former single 64 KiB authored-tool host line served both catalog inspection
and individual calls. That made a larger tool count ineffective even when each
tool was valid. Generated plugin MCP input likewise had no explicit aggregate
output check even though later setup verification read generated files with a
smaller ceiling. Both downstream limits must move with discovery.

## Consequences

- Normal projects and composed plugin skill sets can grow well beyond the old
  MVP counts without adding configuration.
- A project can trade a few large skill resources for many small skills while
  remaining inside the same aggregate skill-content ceiling.
- A larger tool catalog does not permit larger tool arguments, schemas, or
  results.
- Large generated MCP configurations fail before workspace mutation and remain
  verifiable when accepted.
- Apply records may contain more owned paths, but remain within their existing
  8 MiB metadata ceiling under the authored file-count limits.
- ADR 0019 remains authoritative for plugin discovery, precedence, collision,
  and component isolation; its eight-skill, 32-plugin, and 128-entry numbers are
  historical and replaced by this decision.
