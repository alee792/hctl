# Vision

hctl makes an agent something you can read. An agent is a folder of
plain-language files — instructions you review like a document, skills you
compose by dropping in a directory — validated, versioned, and shared like any
other source. `hctl apply` compiles that folder into a generated integration
for the capable native harness you already trust, Claude Code or Codex,
through thin vendor adapters and without replacing their model loops or
interfaces.

The author we serve understands files, directories, and common AI concepts —
instructions, skills, tools — and should never need to learn manifests,
registration, or harness configuration. Today that person's agent lives as
vendor-specific configuration scattered through a workspace: hard to read,
hard to review, and bound to one harness. hctl exists so the agent itself is
the document — one legible folder that outlives any single vendor's format.

Natural-language authorship is dependable because the toolchain is strict:
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

The author is not always a person. An improvement loop — an agent or an
optimizer revising an agent's own files — is a second consumer of the same
artifact, and it needs exactly what the human author needs: a legible diff,
validation before anything runs, and a reproducible runtime. An optional
agent manifest pins what the folder alone cannot express — harness version,
model, hctl version, installed-package identities — so observations made
outside hctl can be joined back to the exact configuration that produced
them. Hctl is that loop's substrate, never the loop: it proves a revision is
well-formed, not that it is an improvement, and it collects no transcripts,
evaluations, or scores.

We bet that agent definitions converge on open, file-based formats such as
Agent Skills and Agent Plugins, and hctl is the toolchain for that world:
discovery, validation, composition, apply-and-drift discipline, and vendor
adapters kept thin. Acquired components are reviewed, explicit, and
inspectable. Hctl is not a marketplace, an automatic updater, a model runtime,
or another chat UI.

Each harness already reads its own native formats, so this product must
answer why a folder needs a compiler. The answer is the crossing: one
portable source of truth applied to any supported harness, proven valid
before it touches a workspace, and kept honest afterward by drift detection.
A harness vendor optimizes its own format; nobody else owns the crossing
between them. The bet fails — and we would rather learn it early — if
authors accept a per-vendor source of truth, if the open formats collapse
back into vendor-owned ones, or if self-improving systems settle on closed,
lab-internal configuration instead of open files.

The measure of the vision is the first five minutes and the last mile: a new
author goes from an empty directory to a working agent inside their harness
in five minutes, and the same folder later runs headless, scheduled, or
staged without edits.

## Boundary

The selected native harness owns intelligence: model calls, context
management, planning, native tools, approvals, interactive UX, and unmanaged
MCP runtime behavior. Hctl owns the portable agent-project contract,
dependency validation, generated harness integration, and tools routed through
its managed boundary.

Interactive authors work directly in Claude Code or Codex after hctl prepares
the generated harness integration. Headless operators may place the turn
dispatcher between an input source and a local harness process. The turn
dispatcher does not become another chat UI or model loop. The conversational
channel runtime — long-lived surfaces such as Discord managing several
independent conversation lifecycles over that dispatcher — is a coherent
second product built on this core, not part of it; it remains deterministic
runtime coordination rather than an agent orchestrator.

Acquiring or configuring a third-party component does not make it managed.
Harness-native tools and MCP servers remain valid but unmanaged unless they
deliberately cross an hctl-owned managed boundary. Hctl never claims that it
can enforce instructions, inspect every native effect, or make model behavior
safe from outside the harness.
