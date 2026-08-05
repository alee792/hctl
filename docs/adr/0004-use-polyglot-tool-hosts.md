# ADR 0004: Use native polyglot tool hosts

- Status: accepted

## Decision

Discover TypeScript and Python tool files directly under `tools/` and Go tools
as one package directory per tool. Derive their names from paths, prepare their
locked dependencies with Deno, `uv`, and Go, and expose them through one hctl
MCP server without an authored registry or generated tool manifest.

Keep one long-lived subprocess per language for an MCP session. Generic
embedded hosts load TypeScript and Python definitions. Minimal generated Go
registration glue imports authored packages and compiles a cached host. Input
and output schemas are reported and validated by the language hosts.

## Context

Authors commonly write AI integrations in TypeScript and Python, while hctl's
Go executable remains attractive for its small control-plane footprint. Making
every function an MCP server would impose protocol boilerplate and a process
start per call. Embedding either Python or JavaScript in Go would enlarge and
couple the control plane.

The executable spike proved that native long-lived processes preserve
language-native authoring, schema validation, and dependency tooling while MCP
remains the single harness-facing boundary.

## Consequence

Authored tool code is trusted local project code, not sandboxed input. Apply
requires the native executable for each used language on `PATH`, evaluates
TypeScript and Python module initialization during inspection, and stores only
disposable host artifacts under `.hctl/cache/tools/`.

Apply records the exact Deno and `uv` executables it used inside the disposable
tool cache so harness startup does not depend on a matching `PATH`. The current
MCP server serializes calls. A deadline terminates the affected language host;
graceful per-call cancellation, restart, concurrent dispatch, and relocatable
packaging require later evidence before they are promised.
