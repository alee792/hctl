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

## Setup skill forward test

A fresh Codex session discovered and followed the portable `setup-agent`
skill, resolved an external agent source and workspace, and produced the exact
`hctl apply` command. Codex's ordinary workspace sandbox denied writes to the
generated `.agents` and `.codex` paths and hctl left no partial setup. Running
the same command directly then generated a valid Codex workspace.

The skill therefore requests native one-command permission when the harness
offers it and otherwise hands the exact command to the user's terminal. It
does not weaken sandbox settings or work around protected harness files.

## Parallel review follow-up

After the portable `code-review` skill was applied, a fresh native
`codex exec --sandbox read-only` session loaded it, ran its Standards and Spec
axes in isolated subagents, and completed with separate zero-finding reports.

The same review through `hctl gateway` became `turn.uncertain` when native
subagent collaboration began. An explicit new input resumed the same Codex
session, but a second delegated attempt ended the same way and the earlier
subagent reports were not recoverable. This is a reproducible gateway
integration limitation, not evidence against native skill execution. Do not
claim headless delegated review support until a focused Codex App Server trace
explains and fixes the missing terminal event.

## Unrelated host noise

The installed Codex version reported an available 0.146.1 update, stale model
cache warnings, five invalid user-level Neo4j skills, and an unauthenticated
user-level TwelveLabs MCP server. None prevented the hctl project setup from
loading or the acceptance checks from completing. They are outside hctl's
managed boundary.

This is intentionally a manual live check because it consumes model usage and
depends on user authentication. The default suite remains credential-free.

## Rerun procedure

Use this opt-in smoke test when Codex changes or hctl's Codex integration
changes materially. It deliberately leaves authentication and first-use
workspace trust to the user; neither belongs in hctl or the default test suite.

From the hctl repository:

```sh
./scripts/bootstrap-tools.sh
export PATH="$PWD/.tools/go/bin:$PWD/.tools/bin:$PATH"

hctl_repo="$PWD"
accept_workspace="$(mktemp -d /tmp/hctl-codex-live.XXXXXX)"
git -C "$accept_workspace" init

go build -o "$accept_workspace/hctl" ./cmd/hctl
"$accept_workspace/hctl" apply "$hctl_repo/agents/maintainer" \
  --workspace "$accept_workspace" \
  --harness codex \
  --command "$(command -v codex)"
```

Open Codex in `accept_workspace` once and accept its native repository trust
prompt. Exit without doing the test in that interactive session. Then run:

```sh
cd "$accept_workspace"

codex exec --json --sandbox read-only \
  --output-last-message codex-acceptance.md \
  'Perform the hctl live acceptance without editing the workspace. Report the first heading from the project instructions. List the hctl-provided skills and load product-review. Confirm whether the managed MCP server is enabled and required. Call echo with exactly codex-live-acceptance. Call decision-index, docs-check, and quality-check once each. Spawn docs_reviewer and ask it to state its role. Return a concise checklist including every error.' \
  | tee codex-acceptance.jsonl

codex exec resume --last --json \
  'Without calling a tool, repeat the exact value previously sent to echo.' \
  | tee codex-resume.jsonl
```

The first run passes when it produces the evidence in the table above. The
second passes when it returns `codex-live-acceptance` without another tool
call. Keep the JSONL files when diagnosing a harness change; otherwise discard
the disposable workspace and remove its Codex trust entry if Codex retains
one.

This procedure is reproducible but intentionally not a stable CI assertion:
model choice, event wording, authentication, trust, and model usage remain
Codex-owned. Every deterministic contract and every defect previously found by
this smoke test must also have a credential-free regression test in hctl.
