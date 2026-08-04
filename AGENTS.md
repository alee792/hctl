# Repository guidance

## Product status

This is a greenfield experiment for bootstrapping and extending native agent
harnesses. `hctl` is a temporary internal name, not a product name. Read
`README.md`, `docs/vision.md`, and `docs/product-spec.md` before changing product
behavior.

## Engineering rules

- Use `.agents/skills/ponytail/SKILL.md` at full intensity for planning,
  implementation, refactoring, and review.
- Keep the native harness responsible for its model loop, context, native
  tools, approvals, and interactive interface.
- Keep `hctl` responsible only for authored project compilation, projections,
  gateway sessions, and explicitly managed capabilities.
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

Run `go test ./...` and `go vet ./...` for affected work. Keep credentialed
model calls, package publication, deployment, and external integrations
explicitly authorized.

