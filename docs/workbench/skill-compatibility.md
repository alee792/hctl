# Skill compatibility work item

- Status: deferred spike
- Last reviewed: 2026-08-05

## Outcome

Replace the provisional flat `skills/*.md` convention with the open Agent
Skills directory format while preserving vendor-specific behavior when a
target harness documents it. The open format is the portable baseline; hctl
must not silently discard or pretend to enforce unsupported vendor fields.

## Baseline to prove

- Discover `skills/NAME/SKILL.md` with optional `scripts/`, `references/`, and
  `assets/` directories.
- Validate the standard `name` and `description` fields plus optional
  `license`, `compatibility`, `metadata`, and experimental `allowed-tools`.
- Preserve unknown standard-compatible resources during native setup.
- Migrate the current flat `skills/*.md` examples and maintainer agent without
  requiring an authored hctl manifest.

The baseline reference is the
[Agent Skills specification](https://agentskills.io/specification).

## Vendor compatibility questions

For Claude Code, inventory its documented skill controls such as model,
effort, invocation behavior, arguments, context, agent routing, paths, hooks,
and shell access. For Codex, inventory its documented skill metadata surfaces,
including adjacent `agents/openai.yaml` when applicable. Reverify both vendor
formats during the spike; they change independently of the open standard.

For every field, decide whether round-trip fidelity requires native
frontmatter, namespaced `metadata`, or an adjacent vendor file. Applying a
skill must emit a precise diagnostic when a requested customization cannot be
represented by the selected harness. Recommended models are hints, not
enforcement claims.

## Evidence required

Use one standard-only fixture and fixtures exercising each supported vendor
extension. Apply each to Claude and Codex, inspect the generated native files,
and prove that portable fields survive both paths while unsupported fields fail
explicitly. Keep the current flat parser isolated until this spike settles the
migration.

## Related deferred idea

A future easy button may opt a repository root into being an agent project.
Do not add configuration for it until a real journey shows why defaulting the
agent source to the workspace is insufficient.
