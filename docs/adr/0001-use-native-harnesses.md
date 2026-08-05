# ADR 0001: Use native agent harnesses

- Status: accepted

## Decision

Compile a filesystem-authored project into Claude Code and Codex native
surfaces. Keep their model loops, context management, native tools, approvals,
and interactive interfaces. Provide an optional gateway only for headless
sessions and managed-tool use.

## Consequence

The tool remains an additive compiler and boundary, not another agent runtime.
It can guarantee behavior only for tools and durable state routed through that
boundary.
