# North Star & Tenets

What this project holds constant. Decisions follow these commitments or
record why not. Every other document — the product spec, ADRs, workbench
notes — describes decisions already made: useful evidence, freely corrected
or deleted, and overruled by this file wherever they disagree.

## North star

If any of these stops being true, the product is no longer hctl:

1. **An agent is a legible document.** A folder of plain-language files a
   person can read, review, and diff — never a second inventory or a
   surface the author must mentally model but cannot read.
2. **The harness owns intelligence; hctl owns the crossing.** Hctl compiles
   one portable source of truth into native integration, proves it valid
   before it touches a workspace, and detects drift afterward. It never
   absorbs model loops, context, approvals, interactive UX, or runtime
   supervision.
3. **Nothing mutates a workspace unvalidated, and trust stays with the
   author.** Hctl proves contracts, never behavior, and never claims
   enforcement or safety it cannot deliver.

**The measure.** Empty directory to a working agent inside the author's
harness in five minutes, and the same folder later runs headless,
scheduled, or staged without edits.

## Tenets

Ranked: when two conflict, the earlier wins unless the decision records why
not. Hold them unless you know better ones — replacing a tenet with a
better one is a contribution, not a violation.

1. **Subtract before you add.** Cost is what an author or contributor must
   now know, not lines of code. Prefer the change that deletes, simplifies,
   or makes something unnecessary.
2. **One ladder, no cliffs.** Every capability is a rung reachable from
   plain language; climbing never requires a new persona, only a new file.
3. **As little schema as necessary — and as little machinery as either.**
   Plain language over schema, schema over machinery, convention over
   registration.
4. **Bets get appetites, not architecture.** Work for an unvalidated future
   is a spike with a falsifier and a review date, disposable by
   construction — never production-grade machinery.
5. **Explicit beats implicit at boundaries.** Apply, acquisition, trust,
   and credentials stay deliberate acts even where implicit would be
   smoother.

The tensions are deliberate — tenet 5 pulls against the five-minute
measure, tenet 1 against completeness — and are resolved by reasoning
recorded where the decision lives.

## Changing this file

The north star changes only through a dedicated ADR naming the evidence
that changed, never in the same change that benefits from the amendment.
Tenets change by ordinary PR. Most work owes this file nothing: alignment
is assumed unless a change mints a new author-facing concept, adds a
subsystem or dependency, moves a boundary named above, or bets on an
unvalidated future — the maintainer's `north-star-review` skill covers
those, and its `direction-audit` skill periodically checks the whole
repository against this file.
