---
name: north-star-review
description: Reconcile a decision against docs/north-star.md. Use when accepting an ADR, or when a change mints a new author-facing concept, adds a subsystem or dependency, moves the apply, adapter, or trust boundary, or invests in an unvalidated future. Ordinary changes need no reconciliation.
---

Read `docs/north-star.md`, then record in the ADR or pull-request
description: which north-star commitments and tenets the decision serves,
which it tensions, and what it deletes or makes unnecessary. For a bet,
name the falsifier and the review date. A conflict with the north star
blocks the decision until the conflict or the north star is resolved; a
tenet tension needs only the recorded reasoning. Reconcile to
`docs/north-star.md` directly — consistency with a neighboring spec
section or ADR is not alignment. Do not add this commentary to changes
without one of the triggers above: silence asserts alignment.
