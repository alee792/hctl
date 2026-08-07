# ADR 0019: Import vendored Agent Plugin skills

- Status: accepted

## Plain-English summary

An agent project may vendor Agent Plugins v1 directories under `plugins/` and
use the skills they contain. Hctl validates each local `plugin.json` without a
network request, then imports valid immediate directories under that plugin's
`skills/` directory into the existing native Claude Code or Codex skill setup.
Root agent skills take precedence. Invalid plugins, invalid plugin skills, and
name collisions produce warnings while independent valid components continue.
MCP servers, installation, registries, updates, and client extensions are not
part of this slice.

## Decision

The agent project remains hctl's only authored project boundary. An optional
real `plugins/` directory contains immediate real plugin directories. Each
plugin must have a bounded, regular UTF-8 `plugin.json` that
targets the exact canonical Agent Plugins v1.0.0 schema identifier. Hctl
implements that small schema locally and performs no schema fetch during load
or apply. Plugin discovery accepts at most 32 entries, and each plugin's
`skills/` location accepts at most 128 entries before the merged eight-skill
limit applies.

Manifest violations reject only that plugin. Unknown top-level manifest fields,
non-object `extensions` values, and every unsupported extension namespace are
ignored with precise warnings because hctl cannot operationalize them. Hctl
does not validate unsupported namespace values. A valid plugin contributes skills only
from immediate real directories under its fixed `skills/` location. Missing
`plugins/`, missing plugin `skills/`, and an empty plugin `skills/` directory are
normal. Other component entries, invalid Agent Skills, and symlinks are skipped
with warnings rather than escaping the source boundary or suppressing valid
sibling components.

Root `skills/` load first. Plugin directories and their skill directories load
in lexical order. The first skill with a given name wins; later collisions are
skipped with a warning and are never renamed. The merged set retains hctl's
eight-skill aggregate limit and existing per-skill file and byte bounds. Valid
plugin manifests and consumed plugin skill resources join the normal source
fingerprint. Apply then uses the existing skill generator unchanged apart from
preserving the original source path in diagnostics.

## Context

Agent Plugins v1 packages skills and MCP server declarations behind one
`plugin.json`. Its component locations are fixed, component failures are
isolated, and clients may support a subset of components. Hctl already has a
portable agent project, Agent Skills validation, native harness generation,
source fingerprinting, and safe ownership. Treating plugins as vendored
dependencies reuses those boundaries without introducing a second agent
manifest, downloading executable content during apply, or redefining hctl's
project model.

Skills are the smallest interoperable slice and require no credentials or
runtime process supervision. Native plugin MCP mapping has different trust,
configuration, lifecycle, and collision questions, so GitHub issue #27 owns it
as a separately shaped phase.

## Consequences

- Plugin directory names are storage identities only; the manifest supplies
  the plugin name and need not match its vendored directory name.
- Hctl does not discover skills outside the fixed plugin `skills/` location.
- `mcp.json` and all other plugin files are untouched and do not affect native
  setup in this phase.
- Hctl does not download, install, update, publish, convert, or resolve plugin
  dependencies.
- Extension data is never interpreted or copied into another hctl concept.
- Plugin authors can use one standards-shaped local package across compatible
  clients while hctl continues to compile one explicit agent project.

## Sources

- [Agent Plugins specification v1.0.0](https://agent-plugins.org/specification)
- [Canonical plugin manifest schema](https://agent-plugins.org/schemas/1.0.0/plugin.schema.json)
- [Agent Skills specification](https://agentskills.io/specification)
- [Product specification](../product-spec.md)
