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

## GitHub maintainer journey

Use the official native GitHub MCP server's discovered catalog for GitHub
outcomes; never depend on a remembered rendered tool name. Inspect the complete
issue, dependency, comment, pull-request, review, and check state before acting.
When the issue tracker permits autonomous work, assigning the eligible issue to
yourself must be the first tracker write, before a branch, edit, status comment,
or pull request. This is maintainer discipline, not hctl authorization.

Ask for workspace-write promotion only when local filesystem mutation is
required. Edit, test, and commit with native harness tools after promotion. The
ambient `GITHUB_PERSONAL_ACCESS_TOKEN` and every official-server call are native
and unmanaged: the harness/model can access the token, hctl does not filter,
confirm, authorize, or audit GitHub effects, and a read-only workspace does not
make GitHub read-only. Avoid merge, release, and destructive operations by
instruction and least-privilege PAT scope; instructions are not enforcement.
Use a fine-grained, repository-scoped, minimally permitted, expiring PAT,
especially when untrusted channel input can reach the maintainer.

Native Git and `gh` need separately operator-configured authentication. Do not
assume the MCP PAT authenticates either or that MCP can publish a local branch
with exact history. After a branch exists remotely, use discovered GitHub tools
to open or update a draft pull request, address reviews, and inspect or rerun
checks only when the PAT permits those outcomes.
