# Domain docs

This repository uses a single-context domain layout.

## Authority and wayfinding

Read the existing product documentation before introducing or interpreting
domain guidance:

1. `docs/vision.md` defines product direction and boundaries.
2. `docs/product-spec.md` defines the current product contract.
3. `docs/glossary.md` defines established domain terms.
4. Relevant records under `docs/adr/` define accepted architecture decisions.
5. `docs/workbench/status.md` records current implementation evidence and gaps.

Read `CONTEXT.md` when it exists. It supplements the sources above with a
concise domain model and shared vocabulary; it does not supersede them. If a
proposed term or decision conflicts with established guidance, flag the
conflict explicitly rather than silently overriding either source.

Missing domain documentation is not itself an error. Create or extend it only
when design work establishes terminology or decisions worth preserving.
