# Codex live acceptance

- Date: 2026-08-05
- Codex CLI: 0.144.1 on macOS arm64, authenticated with ChatGPT
- Hctl generator: 0.6.0-dev
- Source agent: `agents/maintainer`
- Workspace: fresh trusted Git directory outside the agent project

## Result

The native Codex journey works end to end:

| Surface | Live evidence |
| --- | --- |
| Instructions | Codex reported the generated `# maintainer` heading. |
| Skills | Codex discovered `product-review`, `simplicity-pass`, and `tool-authoring`, then loaded `product-review`. |
| Managed MCP | Codex loaded the generated project server as enabled and required. |
| Built-in tool | `echo` returned `codex-live-acceptance`. |
| Python tool | `decision-index` returned a valid empty index for the empty workspace. |
| TypeScript tool | `docs-check` returned a valid structured missing-documents result. |
| Go tool | `quality-check` returned a valid structured missing-script result. |
| Subagent | Codex spawned the generated `docs_reviewer` child and the child responded from its developer instructions. |
| Resume | `codex exec resume` retained the same thread ID and recalled the prior echo value without another tool call. |

The failed document and quality assertions were expected because the disposable
workspace intentionally contained no project files beyond generated harness
setup. The calls themselves completed normally and returned their declared
output schemas.

## Defects found by the live run

1. Managed startup depended on Codex inheriting the same shell `PATH` used by
   apply. Apply now records and reuses the exact Deno and `uv` executables.
2. Codex sends the standard optional MCP `_meta` field in `tools/call`. Hctl now
   accepts that envelope field while keeping tool arguments strict.
3. Codex applied its own approval prompt to authored managed tools. The
   generated managed server is now required and delegates approval to hctl.
4. Portable hyphenated subagent names are invalid collaboration identifiers in
   Codex. Generated Codex agent names now use underscores.

The credential-free polyglot proof now clears `PATH` before starting each
generated MCP command, preserving an automated regression check for the first
defect. Unit and setup tests cover the other generated contracts.

## Unrelated host noise

The installed Codex version reported an available 0.146.1 update, stale model
cache warnings, five invalid user-level Neo4j skills, and an unauthenticated
user-level TwelveLabs MCP server. None prevented the hctl project setup from
loading or the acceptance checks from completing. They are outside hctl's
managed boundary.

This is intentionally a manual live check because it consumes model usage and
depends on user authentication. The default suite remains credential-free.
