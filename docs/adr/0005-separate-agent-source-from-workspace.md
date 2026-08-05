# ADR 0005: Separate agent source from workspace

- Status: accepted

## Decision

Treat an agent project as portable authored source that can be applied to any
independently selected workspace. The agent source contains instructions,
skills, tools, subagents, and native dependency files. The workspace contains
generated harness files, apply records, gateway state, runtime caches, and the
files on which the harness and authored tools operate.

`--workspace` defaults to the agent source directory, preserving a simple
standalone-agent journey. Using an agent stored in another project or shared
location requires an explicit workspace. Directory placement, including being
a child of an `agents/` directory, never implicitly selects the parent
repository as the workspace.

## Context

An agent definition may live beside other agent definitions, inside the
repository it maintains, or in a separate reusable project. Inferring its
operating workspace from its storage layout couples portable behavior to one
repository convention and makes native `CLAUDE.md`, `AGENTS.md`, and MCP
inheritance surprising.

Keeping source and workspace explicit also supplies a stable composition seam
for a future deployable image containing a harness, an agent project, and
hctl. Image construction and deployment remain outside the MVP.

## Consequence

Apply and gateway commands must carry both source and workspace identity.
Generated MCP commands must preserve both paths so runtime verification loads
the selected source against the correct workspace record. Authored language
dependencies are resolved from agent source, while tool calls use the workspace
as their process working directory.

This boundary controls native configuration discovery and default working
context; it is not a security sandbox. Hctl does not claim that Claude Code or
Codex native tools cannot access paths outside the workspace.
