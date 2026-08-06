# Glossary

hctl follows Eve's filesystem-forward vocabulary when the concepts match. Its
public language names the thing an author works with: tool, skill, channel, or
connection. `Capability` came from Roster's internal umbrella for those
different concepts; hctl should not use it as a synonym for `tool` in authored
files, CLI guidance, or product documentation.

| Term | Meaning |
| --- | --- |
| Agent project | The portable authored filesystem source for one agent: instructions, skills, tools, subagents, harness-specific files, and native dependency files. It is not coupled to the repository that stores it or the workspace where it is used. |
| Agent source | The selected agent-project directory from which hctl discovers and validates authored files. |
| Workspace | The independently selected directory where the harness operates. Generated harness files, hctl state, caches, and authored tool processes belong here. It defaults to the agent source for a standalone agent. |
| Instructions | Always-on authored guidance applied to the native harness. |
| Skill | An open Agent Skills directory containing `SKILL.md` and optional supporting files loaded when relevant. A skill is not itself a callable tool. |
| Skill resource | A regular file beneath a skill directory, commonly under `scripts/`, `references/`, or `assets/`. Hctl copies resources into native project skill directories without interpreting their content. |
| Tool | A function the model can call through a declared, schema-validated input and output contract. |
| Tool file | Source under `tools/` that exports one tool using a supported language contract. Its path registers it by convention; there is no separate hctl manifest. |
| Tool host | An hctl-owned, long-lived language process that loads tool files and exposes them through MCP. Authors write functions, not host protocol code. |
| Dependency lockfile | A language-native file that pins code dependencies. It is allowed beside authored files but does not register tools with hctl. |
| Channel | A place where external input reaches an agent. The first vendor form is `channels/discord.md`: signed Discord HTTP Interactions normalize one authorized command into the existing durable gateway and return bounded output with its short-lived response token. |
| Connection | Filesystem-authored access to an external service through managed tools. The first concrete form is a bounded description at `connections/github.md`, which registers anonymous public GitHub access; a future secret-bearing connection must use the secretless operation broker without exposing credential values to the session. |
| Sandbox | An execution boundary that restricts code access to host resources. Process validation and timeouts alone are not a sandbox. |
| Subagent | A specialized native harness agent delegated work by a parent. In the MVP it supplies only instructions and inherits the parent's setup; it is not an independently applied agent project. |
| Schedule | A root-agent Markdown task under `schedules/` whose path is its name, whose frontmatter contains a cron cadence, and whose body is the prompt. Apply validates it without starting a clock; one-shot dispatch opens a fresh native-harness session and discards model text. |
| Harness | The native agent product hctl prepares and extends, initially Claude Code or Codex. The harness owns the model loop and interactive UX. |
| Harness-specific file | A nonportable native project file authored beneath `harnesses/claude/.claude/` or `harnesses/codex/.codex/` and copied literally only for that selected harness. Hctl owns the workspace copy but does not interpret or enforce its contents. |
| Apply | Validate an agent project and prepare the native harness files and local tool runtime needed to use it in a chosen workspace. |
| Harness setup | The generated instructions, skills, and MCP configuration that make an agent project usable in one native harness. Individual files are called generated harness files. |
| Apply record | Generated hctl bookkeeping used to detect a stale or edited harness setup. It is not authored configuration or a tool inventory. |
| MCP | The protocol boundary through which a harness discovers and invokes tools. Local tool authors do not need to implement it. |
| Managed tool | A tool whose requests cross an hctl-owned validation, policy, execution, and audit boundary. |
| Native tool | A harness-provided tool that remains available but is not governed or observed by hctl. |
| Gateway | The optional headless boundary that connects input to a resumable native-harness session. |
| Session | One resumable interaction context owned by the native harness and mapped by the gateway when used headlessly. |
| Proposal | A workspace-local, human-readable suggestion to change one existing UTF-8 instruction, skill, or managed-tool source file. Its immutable record holds provenance, the target's base content hash, and a diff; a later review record accepts or rejects it. It must not contain credentials, secrets, raw tool output, or conversation transcripts. |
| Secretless operation broker | A future hctl-owned local process that resolves an opaque credential reference only for an authorized managed operation, uses the value itself against a constrained upstream target, and returns only safe results. It is an execution boundary, not a credential store, vault, or protection from peer processes or native harness capabilities running as the same OS user. |
| Opaque credential reference | A bounded non-secret identifier that selects a credential inside the secretless operation broker. It is not a credential value, filesystem path, environment-variable name, command, or URI containing credentials. Its eventual author-facing syntax is deferred. |
| Agent image | A possible future deployable package containing a harness, an agent project, and hctl. It composes the same source/workspace contract rather than redefining the agent project as a runtime. |

Configuration may be added later only for settings a directory layout cannot
express. It must not duplicate the filesystem inventory.
