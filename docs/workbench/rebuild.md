# Rebuild charter

- Status: accepted direction, 2026-08-19; the rebuild repository is not yet
  created
- Decision: this repository is a prototype. The core product will be rebuilt
  fresh in a new repository against the distilled
  [product specification](../product-spec.md), rather than refactored in
  place. The conversational channel runtime stays here as a working second
  product ([channel-spec.md](../channel-spec.md)).

## Why rebuild rather than refactor

The 2026-08 restructure audit found the differentiated core is a small
fraction of the code, with the heaviest engineering spent on the channel
tail. The durable assets are documents — the north star, the product shape,
the acceptance intent, and the product-level decision records — all of which
transfer. Rebuilding to the distilled shape is cheaper than extracting the
core from underneath the machinery, and it removes the prototype's
accumulated context from every future session.

## What transfers to the new repository

Founding documents, copied at creation and owned there afterward:

- [North Star & Tenets](../north-star.md)
- [Vision](../vision.md)
- [Product specification](../product-spec.md) — the rebuild target; its
  acceptance section is the completion contract
- The product-shape decision records: ADRs 0001, 0003, 0005–0007, 0010,
  0013, 0019, 0020, 0026, 0027, 0029, 0031, 0034, 0036 (renumbered or
  re-recorded as the new repository's initial accepted decisions)
- [Skill compatibility](skill-compatibility.md) — the dated vendor matrix
- The maintainer agent definition, including the `north-star-review` and
  `direction-audit` skills

## What stays behind

- All implementation: the prototype remains the read-only reference oracle.
  When rebuilt behavior is in doubt, consult the prototype's tests and
  acceptance records; port intent, never code.
- The channel runtime and its domain (ADRs 0014–0018, 0021–0025, 0028, 0032,
  0033; [channel-spec.md](../channel-spec.md)). It keeps working here until
  the rebuilt core proves out, then gets its own decision.
- Implementation-specific records without a surviving product rule (ADR 0002,
  0004's host mechanics, 0008's directory convention) — the rebuild
  re-decides them on its own evidence.
- The acceptance evidence records under `docs/workbench/`, as the archive of
  what the prototype proved.

## Target architecture

The rebuilt core keeps the seams the prototype validated, and nothing else
is prescribed:

- One driver seam per harness (the only genuinely polymorphic boundary);
  harness-specific protocols stay inside their harness package.
- One managed MCP boundary; native harness tools stay unmanaged.
- Validation and bounding before any workspace mutation; generated files
  visibly owned; atomic owner-only durable state.
- External integrations depend inward through wire contracts and run as
  separate processes; no vendor SDK in core.
- Packages named by concrete responsibility; no core, common, util, or
  services layers.

## Gates and sequencing

1. **Shape review (human).** The distilled product spec and this charter are
   reviewed; corrections land here.
2. **Name (human).** The rebuild repository carries the real product name.
   This is the naming deadline the restructure asked for: no name, no
   repository.
3. **Bootstrap.** New repository with the founding documents, check
   tooling, and the maintainer agent; no prototype code.
4. **Build to acceptance.** The spec's acceptance list, in journey order:
   apply and the five-minute journey first, then tools, connections,
   headless dispatch, schedules, staging. Each slice validated
   credential-free, per the north star's tenets.
5. **Cutover decision.** When the acceptance list is green, decide the
   channel product's future (port, rewrite, or retire) and this
   repository's archival.

Within this repository, the rebuild supersedes the restructure plan's D5
module split, D6 documentation rewrite, and D7 test rebalance — that effort
now belongs to the new repository. The seam audit remains valid evidence
about where the channel boundary lies.
