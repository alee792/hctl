# 0036: Remove the acquisition engine

- Status: accepted
- Date: 2026-08-18
- Supersedes: ADR 0035, Plugin and Skill directory acquisition (deleted; in git history)

## Context

ADR 0035 added `hctl plugin|skill add/status/update/remove`: a
component-neutral engine that fetched complete Agent Plugin and Agent Skill
directories from exact local, HTTPS Git, and digest-pinned archive sources,
recorded provenance in `hctl-dependencies.json`, enforced offline drift checks
on every project load, and protected mutations with a write-ahead journal,
cross-process locks, and prospective full-project validation. The machinery was
roughly 4,000 lines across the engine, project hooks, and CLI — for behavior an
author can perform with `cp` or `git`: copying a reviewed directory into
conventional source. The 2026-08 restructure refocuses hctl on the
author/apply/tools core.

## Decision

Remove `internal/acquisition`, the project prospective-validation hooks, the
`hctl plugin` and `hctl skill` commands, and all `hctl-dependencies.json`
support. Manual vendoring is unchanged and remains the only journey: any
complete directory copied beneath `plugins/` or `skills/` is discovered by
convention exactly as before. Review, version pinning, and provenance belong
to the author's own version control, where they already live for every other
authored file.

## Consequences

- `hctl plugin ...` and `hctl skill ...` no longer exist; ADR 0035 is
  superseded.
- A leftover `hctl-dependencies.json` is inert: hctl neither reads, validates,
  nor deletes it, like any other unrecognized root file.
- Project loading no longer takes a per-root operation lock or verifies
  acquired-tree identities; the source fingerprint continues to cover every
  discovered conventional file byte-for-byte, so drift in vendored directories
  still changes the fingerprint and triggers ordinary stale-setup handling.
- The external `skills-lock.json` convention remains opaque to hctl.
