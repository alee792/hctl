# Repository guidance

Treat the repository's existing product documents as authoritative rather than
replacing them with guidance here:

1. `docs/vision.md` defines the product direction and boundary.
2. `docs/product-spec.md` defines the current product contract.
3. `docs/glossary.md` defines the project's vocabulary.
4. Relevant records under `docs/adr/` define accepted architecture decisions.
5. `docs/workbench/status.md` records current implementation evidence and gaps.

`CONTEXT.md`, when present, supplements these sources with domain knowledge. It
does not override them. If guidance conflicts, surface the conflict instead of
silently allowing a newer or more local document to shadow an established
decision.

## Agent skills

### Issue tracker

Issues and specs are tracked in GitHub Issues for `alee792/hctl`. See
`docs/agents/issue-tracker.md`.

### Triage labels

Triage uses the default five-state label vocabulary. See
`docs/agents/triage-labels.md`.

### Domain docs

This repository uses a single-context domain layout. See
`docs/agents/domain.md`.
