# Repository guidance

Treat the repository's existing product documents as authoritative rather than
replacing them with guidance here:

1. `docs/north-star.md` states what the product holds constant — the north
   star and tenets that decisions and every other document reconcile to.
2. `docs/vision.md` defines the product direction and boundary.
3. `docs/product-spec.md` defines the current product contract.
4. `docs/glossary.md` defines the project's vocabulary.
5. Relevant records under `docs/adr/` define accepted architecture decisions.
6. `docs/workbench/status.md` records current implementation evidence and gaps.

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

<!-- OPENWIKI:START -->

## OpenWiki

This repository has a generated `openwiki/` evidence index. It is optional just-in-time context, not required startup reading.

- Treat source code and tests as authoritative. A brief's unknowns and review items are verification gaps, not automatic requirements.
- Prefer the narrowest quiet validation that proves the changed behavior. Preserve complete failure output.

The scheduled OpenWiki GitHub Actions workflow refreshes the repository wiki. Do not hand-edit generated OpenWiki pages unless explicitly asked; prefer updating source code/docs and letting OpenWiki regenerate.

<!-- OPENWIKI:END -->
