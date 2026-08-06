# ADR 0010: Allow portable subagent effort requests

- Status: accepted

## Plain-English summary

Subagent authors need one portable way to request how much reasoning a child
uses without choosing a model or creating a separate runtime. An immediate
subagent may now set `effort` to `low`, `medium`, or `high`; hctl validates it
and writes the matching native Claude or Codex field. The harness still decides
whether the request is honored. This does not add child-owned tools, skills,
permissions, sandboxing, nesting, lifecycle management, or runtime observation.

## Decision

Amend [ADR 0006](0006-use-native-inherited-subagents.md) so an immediate
subagent's `instructions.md` frontmatter may contain optional string `effort`
beside required string `description`. Accept exactly `low`, `medium`, or `high`.

Emit the value as `effort` in Claude's generated agent frontmatter and
`model_reasoning_effort` in Codex's generated custom-agent TOML. Omit the native
field when source effort is absent, preserving existing description-only output.
Keep root instructions description-only.

## Consequence

The source fingerprint and generated child file change with the effort request,
so ordinary apply ownership and stale-source checks cover additions, changes,
and removal. Hctl owns validation and native mapping only; model availability,
account settings, harness version, and policy may ignore or constrain the
request without changing hctl's additive boundary.
