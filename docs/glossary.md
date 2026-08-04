# Glossary

hctl follows Eve's filesystem-forward vocabulary when the concepts match.
hctl-specific terms exist only for behavior introduced by its native-harness
bootstrap and managed boundary.

| Term | Meaning |
| --- | --- |
| Agent project | The authored filesystem source of truth for one agent. |
| Project configuration | Optional settings that the conventional filesystem layout cannot express. The MVP does not require a configuration file. |
| Instructions | Always-on authored guidance projected into the native harness. |
| Skill | Reusable instructions and supporting files loaded when relevant. A skill is not itself a callable tool. |
| Tool | A model-callable capability with a declared input and output contract. |
| Tool implementation | Authored code behind a managed tool. Ad hoc scripts written during a session are ordinary workspace files, not declared tools. |
| Channel | A place where external input reaches an agent, such as Slack, an API, or local stdin. |
| Connection | Configured access to an external service, commonly through MCP or HTTP. Credential brokering may implement a connection without exposing secrets to the session. |
| Sandbox | An execution boundary that restricts code access to host resources. Process validation and timeouts alone are not a sandbox. |
| Subagent | A specialized agent delegated work by another agent. |
| Schedule | A recurring trigger for headless agent work. |
| Harness | The native agent product hctl prepares and extends, initially Claude Code or Codex. The harness owns the model loop and interactive UX. |
| Projection | Disposable, hctl-owned files generated for one native harness from the agent project. |
| Runtime manifest | The immutable compiled record of validated sources, fingerprints, target harness, and managed capabilities. |
| Managed capability | A capability whose requests cross an hctl-owned validation, policy, execution, and audit boundary. |
| Native capability | A harness capability that remains available but is not governed or observed by hctl. |
| Gateway | The optional headless boundary that connects input to a resumable native-harness session. |
| Session | One resumable interaction context owned by the native harness and mapped by the gateway when used headlessly. |
| Proposal | A recorded candidate improvement to instructions, a skill, a tool, or other agent feedback. A proposal is inert until a human accepts it into the authored project. |
| Credential broker | An internal boundary that uses a credential for an authorized capability without exposing the credential value to the agent session. |
