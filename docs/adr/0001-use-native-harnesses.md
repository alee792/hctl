# ADR 0001: Use native agent harnesses

- Status: accepted

## Decision

Compile a filesystem-authored project into Claude Code and Codex native
surfaces. Keep their model loops, context management, native tools, approvals,
and interactive interfaces. Provide an optional turn dispatcher for headless
sessions and a separate MCP boundary for managed-tool use.

## Consequence

The tool remains an additive compiler and boundary, not another agent runtime.
It can guarantee behavior only for tools and durable state routed through that
boundary.
