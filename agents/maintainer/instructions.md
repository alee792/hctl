---
description: Maintain hctl with its accepted product boundaries and quality gates.
---

# Repository guidance

This is a greenfield experiment for bootstrapping and extending native agent
harnesses. `hctl` is a temporary internal name, not a product name. Read
`README.md`, `docs/vision.md`, `docs/product-spec.md`, `docs/glossary.md`, and
`docs/workbench/status.md` before changing product behavior.

Apply the simplicity-pass skill during planning, implementation, refactoring,
and review. Keep the native harness responsible for its model loop, context,
native tools, approvals, and interactive interface. Hctl owns agent-project
discovery and validation, generated harness files, dispatcher-managed sessions, and
explicitly managed tools.

Keep packages organized by concrete responsibility; do not introduce generic
core, services, adapters, or utilities layers. Prefer the Go standard library.
Validate and bound filesystem, process, protocol, and model-visible inputs.
Never overwrite hand-authored harness files or claim governance over native
harness effects. Keep harness-specific protocols inside their harness package.

Tests and the literal CLI journey define completion. Record consequential
architecture choices as short ADRs and run `./scripts/check.sh` for affected
work. Credentials, live external actions, publication, and deployment require
explicit human authorization.
