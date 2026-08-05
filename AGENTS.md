# Repository guidance

## Product status

This is a greenfield experiment for bootstrapping and extending native agent
harnesses. `hctl` is a temporary internal name, not a product name. Read
`README.md`, `docs/vision.md`, `docs/product-spec.md`, `docs/glossary.md`, and
`docs/workbench/status.md` before changing product behavior.

## Engineering rules

- Use `.agents/skills/ponytail/SKILL.md` at full intensity for planning,
  implementation, refactoring, and review.
- Keep the native harness responsible for its model loop, context, native
  tools, approvals, and interactive interface.
- Keep `hctl` responsible only for authored project discovery and validation,
  generated harness files, gateway sessions, and explicitly managed tools.
- Keep packages organized by concrete responsibility. Do not introduce generic
  `core`, `services`, `adapters`, or `utils` layers.
- Use the Go standard library until a concrete requirement proves it
  insufficient.
- Validate and bound filesystem, process, protocol, and model-visible inputs.
- Never overwrite hand-authored harness files or claim governance over native
  harness effects.
- Keep harness-specific protocol types inside their harness package.
- Treat tests and the literal CLI journey as the definition of completion.
- Record consequential architecture changes as short ADRs.

## Quality gates

Run `./scripts/check.sh` for affected work. It covers formatting, imports,
tests, vet, lint, and vulnerability analysis with the pinned repository-local
tools. Keep credentialed model calls, package publication, deployment, and
external integrations explicitly authorized.
