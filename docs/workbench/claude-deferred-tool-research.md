# Claude Code deferred-tool protocol research

- Verified: 2026-08-06
- Scope: the `native_deferred_tool` strategy for `channel.request_input`

Claude Code documents deferred tool calls as a headless integration protocol
for `claude -p`. A `PreToolUse` hook may return
`hookSpecificOutput.permissionDecision: "defer"`; Claude then exits
successfully with `stop_reason: "tool_deferred"` and a
`deferred_tool_use` object containing the tool call's `id`, `name`, and original
`input`. Resuming with `claude -p --resume <session-id>` replays that same tool
call through `PreToolUse`; returning `"allow"` with a complete `updatedInput`
lets the tool execute and the original turn continue. The session remains
subject to Claude's configured transcript-retention sweep. [Claude Code hooks:
defer a tool call for later](https://code.claude.com/docs/en/hooks#defer-a-tool-call-for-later)

Deferral works only when the turn contains one tool call; in a parallel batch
Claude ignores `defer` and uses the normal permission path. If the deferred
tool is unavailable on resume, Claude exits before the hook runs with
`stop_reason: "tool_deferred_unavailable"`, `is_error: true`, and the original
correlation. These outcomes must be classified rather than retried. [Claude
Code hooks: defer a tool call for later](https://code.claude.com/docs/en/hooks#defer-a-tool-call-for-later)

MCP tool matcher names use the `mcp__<server>__<tool>` form. `PreToolUse` input
includes `tool_name`, `tool_input`, and `tool_use_id`; `updatedInput` replaces
the entire input object. Hctl can therefore match only
`mcp__managed__channel.request_input`, retain a canonical semantic-request
digest, and reject any changed tool identity, call ID, or input on resume.
[Claude Code hooks reference](https://code.claude.com/docs/en/hooks#pretooluse)

Current common hook input exposes `agent_id` and `agent_type` for
subagent-originated tool events; `agent_id` is present only for a subagent.
The hctl hook can deny a subagent call before it becomes deferred or reaches
durable state, while a root call may park. [Claude Code hooks: common input
fields](https://code.claude.com/docs/en/hooks#common-input-fields)

Claude supports a `--settings` path or JSON value for one invocation, and hook
configuration accepts command handlers with an executable plus arguments.
Hctl can generate an owned settings fragment that is not loaded by ordinary
interactive Claude sessions and pass it only to its headless channel process.
[Claude Code settings](https://code.claude.com/docs/en/settings), [Claude Code
hooks configuration](https://code.claude.com/docs/en/hooks#configuration)

## Implementation consequence

The documented protocol is consistent with issue #22. Hctl adds durable state
before render, exact input digests, root-only decisions, manager capacity,
ephemeral answer brokering, child-environment filtering, and conservative
uncertainty after process execution begins. Discord rendering and callback
intake remain separate from this harness strategy.
