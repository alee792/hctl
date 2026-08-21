---
name: setup-agent
description: Apply a portable hctl agent project to a Claude Code or Codex workspace. Use when a user asks to set up, install, apply, switch, or reapply an agent in a workspace or prepare that workspace for its native harness.
---

Resolve the agent source, target workspace, and harness from the request and
current environment. Ask one focused question only when one of those choices
is materially ambiguous. The workspace defaults to the agent source when the
user wants a standalone agent.

Find `hctl` on `PATH`; in an hctl source checkout, an existing executable
`./hctl` is also acceptable. Do not download software or build hctl from source
unless the user asks. Verify the chosen harness executable is on `PATH`, the
agent directory is a valid agent project — proven by `instructions.md`, a
supplied manifest matching its fingerprint, or both — and the workspace
exists or the user has authorized creating it.

Before changing files, show the resolved source, workspace, harness, and exact
command. If the user already requested setup with those choices, proceed
without another confirmation:

```sh
hctl apply AGENT --workspace WORKSPACE --harness claude
hctl apply AGENT --workspace WORKSPACE --harness codex
```

Use absolute paths for `AGENT` and `WORKSPACE`. Let `hctl apply` perform source,
dependency, harness, ownership, and stale-state validation. Preserve its stdout
and warnings in the result; do not reproduce that validation in shell code.
Codex may protect `.agents` and `.codex` from ordinary sandboxed writes. Use
the harness's native one-command approval mechanism when it is available. If
it is unavailable, stop and give the user the exact command to run in their
terminal; never weaken global sandbox settings or bypass approvals.

On success, report the generated files and the native next command:

```sh
cd WORKSPACE && claude
cd WORKSPACE && codex
```

Explain that the current harness session may not load newly generated
instructions, skills, subagents, or MCP configuration; start a fresh session
for the applied agent. Codex may also require its native repository-trust step.

If apply refuses an ownership conflict, modified generated file, missing
runtime, or invalid source, stop and report its remediation. Never delete or
rewrite conflicting native harness files, pre-create its output tree, or work
around harness protections to force setup through.
