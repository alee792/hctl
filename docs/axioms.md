# Axioms

- Status: decision constitution
- Updated: 2026-08-19
- Purpose: the small fixed set of tests that every product and architecture
  decision either follows or explicitly reconciles against. This document
  exists so that many independent sessions — human or agent — converge on the
  same product without sharing chat history, and so that local rigor cannot
  compound into global drift.

## The methodology

1. **Follow or reconcile.** Every ADR and every non-trivial PR names the
   axioms it serves and any axiom it tensions, with the reconciliation.
   Silence asserts full alignment; a reviewer who finds an unstated tension
   has a blocking finding. Consistency with an existing spec section or ADR
   is not alignment — decisions reconcile to the axioms, not to their
   neighbors.
2. **Everything below this document is prototype.** The vision is the
   positioning, the product spec is the current contract, ADRs are accepted
   decisions, and workbench documents are working state. Each is kept while
   it survives reconciliation upward and is corrected or deleted when it
   does not. No lower document earns permanence by being detailed.
3. **Axioms are expensive to change.** Amendment requires a dedicated ADR
   naming the evidence that changed, and never lands in the same change that
   benefits from the amendment. Everything else should change easily; this
   document should change rarely.
4. **The inversion audit is scheduled, not a crisis.** At a fixed cadence —
   quarterly, or sooner when a review requests it — one zero-based pass
   asks: does effort match the differentiated core, are the thinnest-tested
   surfaces the trust boundaries, do the docs serve the author or the
   process, and what should be deleted? Findings become the next charter.
   The August 2026 restructure is the template, run on schedule instead of
   after the drift.
5. **Simplicity is tracked, not admired.** The quickstart's length, the
   product spec's size, and the glossary's term count are costs. The
   glossary is a budget, not an index: a change that mints a new
   author-facing concept spends it and says so.

## The axioms

### A1 — Legibility is the product

An agent is a document a person reads, reviews, and diffs. Every feature
must leave the folder more legible, not more powerful at legibility's
expense. *Test: can the author explain their agent by reading its files,
and understand a diff to it without running hctl?* Forbidden: a second
inventory, registration state, or any surface the author must mentally
model but cannot read.

### A2 — Own the crossing, never the intelligence

The selected harness owns model loops, context, native tools, approvals,
and interactive UX. Hctl compiles one portable source of truth into native
integration, proves it valid before it touches a workspace, and detects
drift afterward. *Test: if the harness shipped this natively next release,
would hctl's version have been the wrong thing to build?* Forbidden:
proxying, supervising, or re-implementing harness behavior.

### A3 — One ladder, no cliffs

Every capability is a rung reachable from plain language, and the author
never changes persona to climb. *Test: name the rung directly beneath the
new capability and the one thing the author must newly learn; if that thing
is a new mental model rather than a new file, redesign.*

### A4 — Prove contracts, never behavior

Everything validates before a workspace mutates, and trust stays with the
author: hctl never claims enforcement, inspection, or safety it cannot
deliver, and never silently rewrites authored source. *Test: does any
message, document, or API imply hctl made something safe?*

### A5 — Subtract before you add

Cost is what an author or contributor must now know, not lines of code.
Every slice states what it deletes, simplifies, or makes unnecessary; a
pure addition must say so and justify the budget it spends. *Test: what
does this change let us remove or never build?*

### A6 — The five minutes and the last mile are the measure

A new author goes from an empty directory to a working agent inside their
harness in five minutes, and the same folder later runs headless,
scheduled, or staged without edits. *Test: does this change make the
quickstart shorter or longer, and does it fork the folder?* A decision that
lengthens the first or breaks the second pays for itself in the same
review or does not land.

### A7 — Bets are named, bounded, and falsifiable

Work serving a future that is not yet validated is recorded as a bet with a
falsifier and a review date, and receives prototype-grade investment —
spikes, disposable by construction — until the bet is validated. *Test:
which axiom or which observed author need does this serve? If the honest
answer is a hope, label the hope.* Production-grade machinery for a
speculative tail is the failure this axiom exists to prevent.

## Relation to existing documents

The product principles in [product-spec.md](product-spec.md) are derived,
operational consequences of these axioms at the contract level; where they
conflict, the axioms win and the spec is corrected. The [vision](vision.md)
carries the positioning these axioms defend. Existing ADRs and workbench
documents are prototype evidence under rule 2 and are reconciled as they
are next touched, not retroactively rewritten.
