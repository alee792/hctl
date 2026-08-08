# Vision

Define an agent project as files and use it with the capable native harness you
already trust. Compile portable instructions, skills, and managed tools into
generated Claude Code and Codex integrations without replacing their model
loops or interfaces. For headless use, add a session-aware turn dispatcher that
connects external input and governs only what crosses its managed boundary.

An agent project is portable source, not a repository-bound runtime. Apply it
to any chosen workspace: the agent files define behavior, while the workspace
defines where the harness operates. A pinned hctl harness image may apply an
agent and ship as-is, or optionally stage only that agent's runtime closure for
a smaller downstream image, without changing the source/workspace separation.
Existing OCI build systems own image construction, publication, and deployment.

## Boundary

The selected native harness owns intelligence: model calls, context management,
planning, native tools, approvals, and interactive UX. The tool owns the
portable agent-project contract, generated harness integration, and the tools
routed through its managed boundary.

Interactive users work directly in Claude Code or Codex after hctl prepares
the generated harness integration. Headless users may place the turn dispatcher
between an input source and a local harness process. The turn dispatcher does
not become another chat UI or model loop. A long-lived channel runtime may
manage several independent conversation lifecycles over that dispatcher, but it
remains deterministic runtime coordination rather than an agent orchestrator.

Harness-native tools remain valid but unmanaged. The tool never claims that it
can enforce instructions, inspect every native effect, or make model behavior
safe from outside the harness.
