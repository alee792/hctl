# ADR 0006: Use native inherited subagents

- Status: accepted

## Decision

Discover one level of subagent directories under an agent project. Each child
contains only a descriptive `instructions.md`. Generate Claude subagents under
`.claude/agents/` and Codex subagents under `.codex/agents/`, relying on each
harness's native parent inheritance for instructions, skills, managed MCP
tools, native tools, and permissions.

Reject child tools, skills, dependency files, nested subagents, and names that
collide with parent tools. A child that needs an independently configured
runtime should instead be a separately applied agent project.

Portable source names use hyphens. Codex generated agent names replace those
hyphens with underscores to satisfy its native collaboration identifier rules;
Claude keeps the portable name.

## Context

The first design treated every subagent as a complete isolated project. That
would require duplicated configuration, child-specific MCP servers, and an
hctl-owned delegation model. Both target harnesses already provide native
subagent routing and parent context, which is sufficient for the current user
journey.

## Consequence

Hctl owns discovery, validation, and native child files, but not delegation or
inheritance semantics inside the harness. The generated child configuration
contains routing metadata and instructions only. This keeps the feature
additive and makes unsupported isolation claims impossible.
