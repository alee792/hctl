# North Star & Tenets

- Status: steering kernel
- Updated: 2026-08-19
- Purpose: the smallest set of durable commitments that lets independent
  sessions — human or agent — converge on the same product without shared
  chat history. Tiered by revisability, so rigidity is confined to the few
  statements that define the product while everything else stays cheap to
  revise or discard.

## Tiers and how they bind

| Tier | Binds how | Changes how |
| --- | --- | --- |
| North star (invariants and the measure) | A conflicting decision does not land | Dedicated ADR naming the evidence that changed; never in the change that benefits from the amendment |
| Tenets | Settle debates; a decision that tensions one records its reasoning | Ordinary PR that states the better tenet — all tenets are held "unless you know better ones" |
| Fitness functions | Measured; growth without stated payment is a review finding | Tuned freely with a one-line rationale |
| Case law (spec detail, ADRs, workbench) | Evidence, not authority | Promoted, corrected, or deleted freely; git history is the archive |

Reconciliation runs upward: consistency with a neighboring spec section or
ADR is not alignment. A decision reconciles to this document, and the lower
document is corrected when they disagree.

Alignment is cheap by default: silence asserts it, and an ordinary change
carries no citation, kernel paragraph, or ceremony. Explicit reconciliation
is owed only when a tripwire fires — the change mints a new author-facing
concept, adds a subsystem, module, or dependency, grows a fitness-function
measure, moves the crossing boundary, or invests in an unvalidated future.
Everything else proceeds without kernel prose. The kernel is a lens for
reviewers and a tiebreaker for authors, not a form to fill.

## North star

The identity. If any of these stops being true, the product is no longer
hctl:

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
scheduled, or staged without edits. Every slice can be asked which side of
this measure it improves.

## Tenets

Ranked: when two tenets conflict, the earlier one wins unless the decision
records why not. Replacing a tenet with a better one is a welcome
contribution, not a violation.

1. **Subtract before you add.** Cost is what an author or contributor must
   now know, not lines of code. Every slice names what it deletes,
   simplifies, or makes unnecessary; a pure addition says so and pays
   explicitly.
2. **One ladder, no cliffs.** Every capability is a rung reachable from
   plain language; climbing never requires a new persona, only a new file.
3. **As little schema as necessary — and as little machinery as either.**
   Plain language over schema, schema over machinery, convention over
   registration.
4. **Bets get appetites, not architecture.** Work for an unvalidated future
   is a spike with a falsifier and a review date, disposable by
   construction. Production-grade machinery for a speculative tail is the
   recorded failure this tenet exists to prevent.
5. **Explicit beats implicit at boundaries.** Apply, acquisition, trust,
   and credentials stay deliberate acts even where implicit would be
   smoother.

The tensions are deliberate: tenet 5 pulls against the five-minute measure,
and tenet 1 pulls against completeness. A decision inside such a tension is
resolved by recorded reasoning where the decision lives, not by citation.

## Fitness functions

Measured proxies for simplicity and quality — tuned, not worshiped:

- The README quickstart's step count does not grow.
- The glossary is a budget, not an index: a change minting a new
  author-facing concept says so.
- `docs/product-spec.md` trends down; the D5 docs split is the active
  mechanism.
- The thinnest-tested code in the repository is never a trust boundary.

Growth in any of these without stated payment is a review finding. The
first mechanical-enforcement candidate is a small ratchet check (recorded
budgets a PR must consciously raise); adopt it only if manual review proves
insufficient — tenet 3 applies to enforcement machinery too.

## When each document is read, and what enforces it

A document steers only if it is in context at decision time, so
consultation is designed rather than hoped for:

| Document | Read when | Enforced by |
| --- | --- | --- |
| This kernel | Every session start (first in AGENTS.md); before authoring an ADR or slice; during every review | The review's kernel axis below |
| [vision.md](vision.md) | When scope or positioning is in question | Reconciles to the north star |
| [product-spec.md](product-spec.md) | Just in time, for the contract area being touched | Shrinks through distillation |
| ADRs and workbench documents | Just in time, only when touching their area | Prunable case law |

Enforcement points, in order of leverage:

1. **Review carries a kernel axis — on the reviewer, not the author.** The
   repository's independent review practice adds one lens: did a tripwire
   fire without its reconciliation? An unstated north-star conflict blocks;
   an unstated tenet tension is an ordinary finding; a change with no
   tripwire owes nothing and gets no kernel commentary.
2. **ADRs open with a kernel section** naming what they serve and tension —
   ADRs are already the deliberate, ceremonial path, so the cost lands
   where deliberation already happens. An ADR that cites only neighboring
   ADRs is incomplete.
3. **The inversion audit is scheduled, not a crisis.** Quarterly, one
   zero-based pass asks: does effort match the differentiated core, are the
   thinnest-tested surfaces the trust boundaries, do the docs serve the
   author or the process, and what should be deleted? Findings become the
   next charter. The August 2026 restructure is the template, run on
   schedule instead of after the drift.

## Case law and the distillation follow-up

Existing ADRs, spec detail, and workbench documents are demoted to case law
now: evidence of past decisions, consulted just in time, never required
reading. A separate distillation pass will walk them once — promoting any
still-load-bearing rule upward, keeping active contracts, and deleting the
rest, with git history as the archive. Until that pass runs, an overfit
document loses any conflict with this kernel by rule rather than by edit.
