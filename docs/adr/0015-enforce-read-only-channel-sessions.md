# ADR 0015: Enforce read-only channel sessions before elevation

- Status: accepted

## Decision

Every new or resumed channel-managed harness process starts with the strongest
supported non-mutating native policy. Claude runs in `plan` permission mode.
Codex starts or resumes its app-server thread with the `read-only` sandbox and
`never` approval policy. A reserved, non-secret execution-policy marker is
inherited by hctl's managed MCP server; while it is read-only, the server does
not start or advertise authored tool hosts. Built-in echo and anonymous GitHub
read operations remain available.

Before opening a managed Claude process, hctl verifies that the selected binary
accepts and documents plan permission mode. Codex must echo an effective
`readOnly` sandbox and `never` approval policy in its thread start or resume
response. Missing or different confirmation fails the open before any turn is
dequeued.

This enforcement applies only to managed channel lifecycles. Explicit JSONL,
one-shot schedules, apply, verification, and interactive native-harness use
retain their existing default policy.

Generated channel instructions define one exact control result,
`HCTL_REQUEST_WRITE_ACCESS`. An agent returns it only when completing the user
request genuinely requires workspace mutation. Discord buffers the complete
turn and suppresses the result only when the trimmed output equals that value;
prefixes, incidental mentions, code formatting, and surrounding prose are
ordinary visible output. The existing `HCTL_NO_REPLY` result follows the same
complete-output rule.

## Context

Conversational sessions can inspect a shared checkout concurrently, but letting
them mutate it would couple unrelated conversations and create races. Prompt
instructions are not an enforcement boundary. ADR 0016 defines how the exact
write-access result promotes one conversation into an isolated Git worktree.

## Consequences

- Channel sessions cannot silently obtain native write approval in the shared
  checkout.
- Hctl-managed authored tools are unavailable to read-only sessions because
  their effects are not yet declared or sandboxed; safe built-ins remain.
- Unsupported policy setup fails before a turn starts and is reported through
  the existing credential-free startup-failure classification.
- The execution-policy marker contains no credential, path, or runtime identity.
- Writable continuation and durable worktree assignment are defined by ADR
  0016; eventual retirement cleanup remains separate.
