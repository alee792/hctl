# Glossary

hctl follows Eve's filesystem-forward vocabulary when the concepts match. Its
public language names the thing an author works with: tool, skill, channel, or
connection. `Capability` came from Roster's internal umbrella for those
different concepts; hctl should not use it as a synonym for `tool` in authored
files, CLI guidance, or product documentation.

| Term | Meaning |
| --- | --- |
| Agent project | The authored filesystem source of truth for one agent. |
| Instructions | Always-on authored guidance projected into the native harness. |
| Skill | Reusable instructions and supporting files loaded when relevant. A skill is not itself a callable tool. |
| Tool | A function the model can call through a declared, schema-validated input and output contract. |
| Tool file | Source under `tools/` that exports one tool using a supported language contract. Its path registers it by convention; there is no separate hctl manifest. |
| Tool host | An hctl-owned, long-lived language process that loads tool files and exposes them through MCP. Authors write functions, not host protocol code. |
| Dependency lockfile | A language-native file that pins code dependencies. It is allowed beside authored files but does not register tools with hctl. |
| Channel | A place where external input reaches an agent, such as Slack, an API, or local stdin. |
| Connection | Configured access to an external service, commonly through MCP or HTTP. Credential brokering may implement a connection without exposing secrets to the session. |
| Sandbox | An execution boundary that restricts code access to host resources. Process validation and timeouts alone are not a sandbox. |
| Subagent | A specialized agent delegated work by another agent. |
| Schedule | A recurring trigger for headless agent work. |
| Harness | The native agent product hctl prepares and extends, initially Claude Code or Codex. The harness owns the model loop and interactive UX. |
| Apply | Validate an agent project and prepare the native harness files and local tool runtime needed to use it. |
| Harness setup | The generated instructions, skills, and MCP configuration that make an agent project usable in one native harness. Individual files are called generated harness files. |
| Apply record | Generated hctl bookkeeping used to detect a stale or edited harness setup. It is not authored configuration or a tool inventory. |
| MCP | The protocol boundary through which a harness discovers and invokes tools. Local tool authors do not need to implement it. |
| Managed tool | A tool whose requests cross an hctl-owned validation, policy, execution, and audit boundary. |
| Native tool | A harness-provided tool that remains available but is not governed or observed by hctl. |
| Gateway | The optional headless boundary that connects input to a resumable native-harness session. |
| Session | One resumable interaction context owned by the native harness and mapped by the gateway when used headlessly. |
| Proposal | A recorded candidate improvement to instructions, a skill, a tool, or other agent feedback. A proposal is inert until a human accepts it into the authored project. |
| Credential broker | An internal boundary that uses a credential for an authorized tool or connection without exposing the credential value to the agent session. |

Configuration may be added later only for settings a directory layout cannot
express. It must not duplicate the filesystem inventory.
