# Vision

hctl makes an agent something you can read. An agent is a folder of
plain-language files — instructions you review like a document, skills you
compose by dropping in a directory — validated, versioned, and shared like any
other source. `hctl apply` compiles that folder into a generated integration
for the capable native harness you already trust, Claude Code or Codex,
through thin vendor adapters and without replacing their model loops or
interfaces.

Natural-language authorship is trustworthy because the toolchain is strict:
as much plain language as possible, as little schema as necessary, and
everything validated before it touches a workspace.

There is one author and one capability ladder, not an author/developer split.
An author starts by writing instructions and composing existing Agent Skills.
Further up the ladder, a TypeScript, Python, or Go source file under `tools/`
declares one schema-validated function; hctl-owned language hosts expose those
functions to the selected harness through one managed MCP server, and nobody
writes protocol code. The author may write that file directly or ask their
harness to draft it. Either way the trust boundary stays with the author:
validation proves the contract, not the behavior. An authored tool is the
author's code — no different from any other code they adopt — and accepting
one into `tools/` is a deliberate, reviewable act. Hctl can supply skills and
managed tools that help review; it does not sandbox authored behavior or claim
to make it safe.

Operating an agent is a distinct role on the same artifact: credentials,
integration packages, schedules, channels, and staged filesystems for
deployment, each behind its own explicit guardrails. The operator journey is
where portability is proven — the same folder that runs interactively applies
unchanged to a headless dispatcher, a schedule clock, or a pinned harness
image, with existing OCI build systems owning image construction, publication,
and deployment.

We bet that agent definitions converge on open, file-based formats such as
Agent Skills and Agent Plugins, and hctl is the toolchain for that world:
discovery, validation, composition, apply-and-drift discipline, and vendor
adapters kept thin. Acquired components are reviewed, explicit, and
inspectable. Hctl is not a marketplace, an automatic updater, a model runtime,
or another chat UI.

## Boundary

The selected native harness owns intelligence: model calls, context
management, planning, native tools, approvals, interactive UX, and unmanaged
MCP runtime behavior. Hctl owns the portable agent-project contract,
dependency validation, generated harness integration, and tools routed through
its managed boundary.

Interactive authors work directly in Claude Code or Codex after hctl prepares
the generated harness integration. Headless operators may place the turn
dispatcher between an input source and a local harness process. The turn
dispatcher does not become another chat UI or model loop. A long-lived channel
runtime may manage several independent conversation lifecycles over that
dispatcher, but it remains deterministic runtime coordination rather than an
agent orchestrator.

Acquiring or configuring a third-party component does not make it managed.
Harness-native tools and MCP servers remain valid but unmanaged unless they
deliberately cross an hctl-owned managed boundary. Hctl never claims that it
can enforce instructions, inspect every native effect, or make model behavior
safe from outside the harness.
