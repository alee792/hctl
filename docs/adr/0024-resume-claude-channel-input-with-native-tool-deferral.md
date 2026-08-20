# ADR 0024: Resume Claude channel input with native tool deferral

- Status: accepted
- Integrated by: [ADR 0025](0025-render-discord-input-with-bounded-native-components.md)
- Acceptance: credential-free stitched coverage passed; credentialed Claude was
  unavailable on 2026-08-07; see
  [interactive-input acceptance](../workbench/interactive-input-acceptance.md)
- Upstream protocol: [Claude Code hooks — defer a tool call for
  later](https://code.claude.com/docs/en/hooks#defer-a-tool-call-for-later),
  verified 2026-08-06

## Decision

Claude channel-root sessions implement `native_deferred_tool` with Claude
Code's headless `PreToolUse` deferral protocol. Hctl generates an owned channel
settings file containing one exact matcher and loads it explicitly for headless
Claude processes:
`^mcp__managed__channel\.request_input$`. The command hook validates tool identity
and semantic input and denies every event with Claude's documented subagent
`agent_id`. It returns `defer` only for an eligible root call.
The owner broker records that decision only after its response write succeeds;
the stream adapter must consume the exact tool-ID and input-digest receipt
before it can mint a proven root event.

The stream adapter accepts `tool_deferred` only with one bounded
`deferred_tool_use` whose ID, managed tool name, and strictly decoded semantic
request agree. It carries the native tool-use ID as the durable continuation
key through a proven root harness event. The dispatcher commits the pending
lifecycle before acknowledging that event; only then may the process close and
the channel renderer run. A managed call observed in a parallel tool batch is
classified as unsupported.

After a normalized answer is durable, the coordinator commits resume intent
before calling a manager-owned Claude continuation. That adapter uses the
existing capacity coordinator, workspace resolution, execution policy,
retained session ID, and exact native tool-use ID. It opens
`claude -p --resume` without a new user prompt. A private owner-only Unix
broker retains the original request digest and complete updated input outside
the Claude environment. The environment contains only its short-lived socket
path. The replayed hook requires the same tool ID, name, and digest before the
broker returns `allow`; the resumed MCP call must present canonically equivalent
hook-produced JSON before the broker returns the normalized answer.
Canonicalization is bounded, rejects duplicate keys and multiple values, and
does not weaken the exact tool ID, request digest, or normalized-answer checks.
Successful continuation additionally requires one successfully delivered
`allow` hook response and one successfully delivered exact MCP answer. Broker
state distinguishes an attempted response from a completed socket write. A
missing exchange fails deterministically; a disconnect after an allow or answer
attempt is classified as uncertain and cannot be retried.

The answer and resume envelope are never placed in a process environment or
written to source, generated settings, apply state, command arguments,
diagnostics, or audit. The broker socket path is filtered from downstream child
environments and removed when the one process closes. Native harness effects
remain outside the managed boundary.

Continuation deltas enter the controller's bounded per-turn buffer, but the
terminal event is emitted only after the coordinator has durably completed the
parked origin; only then can the controller publish the outcome. Missing
tools, changed correlation, cancellation, expiry, and known pre-execution
failures are deterministic. Once the resumed process may have performed work,
an unclassified failure is resume-uncertain and is never retried. Restart
reconstructs a not-yet-attempted resume from durable session, request, answer,
and continuation key; a committed resume intent recovers as uncertain.
Failure to deliver either continuation deltas or the post-commit terminal event
crosses the same runtime-fatal boundary as ordinary dispatcher event delivery:
the first failed delta callback immediately stops admission, shuts down shared
capacity, and cancels the active turn rather than waiting for the driver to
return, because outcomes can no longer be accounted for safely.

The hook fragment is passed only by hctl's headless Claude adapter. MCP
advertises the tool only when the manager has installed a trusted
per-conversation bridge with a compatible responder. ADR 0025 specifies the
separate Discord rendering and answer-intake behavior.

## Consequences

- Hctl resumes the same Claude tool call without holding a process while a
  human decides.
- Root provenance uses a documented Claude hook field instead of model prose.
- Claude's one-tool-call limit is a product limit for this continuation mode.
- Changed or unavailable sessions fail visibly; hctl does not replace the call
  with a new turn.
- The Codex continuation-turn strategy is unaffected.
