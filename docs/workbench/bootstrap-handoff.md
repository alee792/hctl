# Bootstrap handoff

The prompt below is given verbatim to the session that bootstraps the
Tenon repository. It is self-contained; this prototype repository is its
only external dependency.

---

You are bootstrapping **Tenon**, a new product repository. Tenon compiles
filesystem-authored AI agent projects ("agent source") into native
configuration for coding-agent harnesses — Claude Code and Codex — and is
the reproducibility substrate for agent improvement loops. It was
prototyped as `hctl` in **alee792/hctl**, now a frozen, read-only
reference implementation. The product shape was distilled, independently
reviewed, and merged there; both human gates (shape review, name) are
closed. Your job is the bootstrap phase of the prototype's
`docs/workbench/rebuild.md` — read that charter first; it governs.

Ground rules, non-negotiable:

- **Port intent, never code.** No file from the prototype's `internal/`,
  `cmd/`, or module trees is ever copied. Consult the prototype only when
  a specified behavior is in doubt; it is an oracle, not a source.
- **The north star governs.** After copying it, every decision reconciles
  to `docs/north-star.md`. Silence asserts alignment; explicit
  reconciliation is owed only on its tripwires. Every stated behavior in
  the product specification binds — the acceptance list is the proof
  skeleton, not the whole contract.
- **Out of scope, permanently unless re-decided by ADR:** evaluations,
  scoring, transcript retention, selection among revisions, lineage
  tracking, a marketplace, network acquisition of components. The channel
  product (Discord runtime) stays in the prototype.

Bootstrap steps:

1. Attach alee792/hctl read-only. Copy from its `main`:
   - `docs/north-star.md`, `docs/vision.md`, `docs/product-spec.md` —
     already Tenon-named; copy verbatim into `docs/`.
   - `docs/github-native-mcp.md` — copy, then rename `hctl` → `tenon` in
     commands and prose; drop prototype-only issue/evidence references.
   - `docs/workbench/skill-compatibility.md` — copy to the same relative
     path; it is a dated vendor matrix and must be reverified before any
     new vendor extension is honored.
2. Re-record the prototype's ADRs 0001, 0003, 0005–0007, 0009, 0010,
   0013, 0019, 0020, 0026, 0027, 0029–0031, 0034, 0036, and 0037 as
   Tenon's initial accepted decisions: renumber sequentially from 0001,
   rename `hctl` → `tenon`, keep each decision and its rationale, and
   drop prototype-transient content (issue numbers, PR references,
   workbench links, acceptance-evidence pointers, superseded-ADR
   scaffolding). Then re-point every ADR link inside the copied
   specification and north star to the new numbers.
3. Copy `agents/maintainer/` and adapt: `north-star-review` and
   `direction-audit` copy verbatim (their doc paths hold); the
   instructions file's reading order becomes north star → vision →
   product specification → glossary → working status, with `tenon`
   replacing `hctl` throughout and prototype-only sections (Discord,
   GitHub-tracker journeys) dropped until those features exist.
4. Derive `docs/glossary.md` from the transferred specification: durable
   author-facing terms only (agent project, agent source, agent manifest,
   improvement loop, workspace, harness, skill, plugin, connection,
   schedule). Channel vocabulary stays behind. The glossary is a budget,
   not an index.
5. Write fresh: `AGENTS.md` (authority order with the north star first,
   mirroring the prototype's), `docs/workbench/status.md` (implemented:
   nothing; gaps: the acceptance list), and a `README.md` built around
   the five-minute journey with `tenon` commands, acknowledging both
   authors per the north star's balance rule.
6. Tooling: choose the implementation language by your own recorded ADR —
   the prototype's Go choice is explicitly a re-decide, not an
   inheritance — and set up the check script and CI gate before the first
   feature commit. Machine-readable diagnostics with stable identifiers
   are a founding requirement, not a retrofit.
7. Build to the specification's acceptance list in journey order: apply
   and the five-minute journey first, then authored tools, connections,
   headless dispatch, schedules, staging. Each slice lands
   credential-free tested (fake harness processes, no live model calls),
   with the smallest complete change that preserves required safety and
   evidence.

The measure you are building toward, verbatim from the north star: empty
directory to a working agent inside the author's harness in five minutes;
the same folder later runs headless, scheduled, or staged without edits;
and a revision applies, runs, and attributes to its exact configuration
without human hands.
