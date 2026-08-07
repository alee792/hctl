---
type: Repository Guide
title: hctl OpenWiki quickstart
description: Starting point for the hctl evidence wiki, covering portable agent setup, managed runtime dispatch, and Discord interactive input.
tags: [hctl, quickstart, go, agents]
openwiki:
  roles: [repository, workflow]
  source_paths: [cmd/hctl/main.go, internal/cli/cli.go, README.md]
  validation_commands: [go test ./...]
---

# hctl OpenWiki quickstart

`hctl` is an experimental local Go tool that compiles a filesystem-authored agent project into Claude Code or Codex setup. It leaves model calls, planning, native tools, approvals, and interactive UX to the selected harness. Its managed boundary covers generated setup, managed MCP tools, durable dispatch, and—when running a configured Discord channel—conversation coordination.

This wiki is source- and test-backed change navigation, not a replacement for the product contract. Start with the repository documents for intent: `docs/vision.md`, `docs/product-spec.md`, `docs/glossary.md`, and the applicable `docs/adr/` record. The two canonical technical pages are [runtime architecture](runtime-architecture.md) and [interactive input](interactive-input.md).

## Map of the wiki

- [Runtime architecture](runtime-architecture.md) explains authored project loading, CLI/runtime composition, durable dispatch, channel control, and the harness boundary.
- [Interactive input](interactive-input.md) is the canonical home for the managed `channel.request_input` contract, durable lifecycle, continuation paths, and Discord components.

## Task routing

| Change area or user intent | Relevant wiki page | Exact source entry points | Important symbols or types | Focused tests | Minimal validation command |
| --- | --- | --- | --- | --- | --- |
| CLI command, apply/run wiring, or harness selection | [Runtime architecture](runtime-architecture.md) | `cmd/hctl/main.go`; `internal/cli/cli.go` | `main`, `cli.Run`, `runApply`, `runAgent`, `newDriver` | `internal/cli/cli_test.go` | `go test ./internal/cli` |
| Portable agent layout, generated setup, or managed MCP configuration | [Runtime architecture](runtime-architecture.md) | `internal/project/project.go`; `internal/setup/setup.go`; `internal/mcp/server.go` | `project.Load`, `setup.Apply`, `mcp.Serve` | `internal/project/project_test.go`; `internal/setup/setup_test.go`; `internal/mcp/server_test.go` | `go test ./internal/project ./internal/setup ./internal/mcp` |
| Conversation scheduling, recovery, capacity, or worktree interaction | [Runtime architecture](runtime-architecture.md) | `internal/dispatch/manager.go`; `internal/dispatch/dispatch.go`; `internal/dispatch/state_store.go` | `dispatch.Manager`, `runSubmissions`, `conversationStore` | `internal/dispatch/manager_test.go`; `internal/dispatch/interaction_store_test.go` | `go test ./internal/dispatch` |
| Add or change interactive request/answer semantics or fallback grammar | [Interactive input](interactive-input.md) | `internal/interaction/interaction.go`; `internal/interaction/schema.go`; `internal/interaction/text.go` | `Request`, `Answer`, `ValidateRequest`, `NormalizeAnswer`, `Resolve`, `ParseTextAnswer` | `internal/interaction/interaction_test.go`; `internal/interaction/schema_test.go` | `go test ./internal/interaction` |
| Change parking, durable interaction state, expiry, callback acceptance, or recovery | [Interactive input](interactive-input.md) | `internal/interaction/lifecycle.go`; `internal/interaction/coordinator.go`; `internal/dispatch/request_input.go` | `Lifecycle`, `Coordinator`, `CoordinatorRequestInputHandler` | `internal/interaction/lifecycle_test.go`; `internal/interaction/coordinator_test.go`; `internal/dispatch/request_input_test.go`; `internal/dispatch/interaction_store_test.go` | `go test ./internal/interaction ./internal/dispatch` |
| Change Codex or Claude answer continuation | [Interactive input](interactive-input.md) | `internal/harness/codex/continuation.go`; `internal/harness/claude/continuation.go`; `internal/harness/claude/deferred.go` | `ContinueTurn`, `ResumeDeferredTool`, `BuildDeferredUpdatedInput`, `RunDeferredHook` | `internal/harness/codex/codex_test.go`; `internal/harness/claude/deferred_test.go`; `internal/harness/claude/claude_test.go` | `go test ./internal/harness/codex ./internal/harness/claude` |
| Change Discord interactive rendering, callbacks, or restart recovery | [Interactive input](interactive-input.md) | `internal/channel/discord/interactions.go`; `internal/channel/discord/discord.go`; `internal/channel/controller/controller.go` | `Runtime.Render`, `handleComponent`, `Capabilities`, `Controller.AcceptInteraction` | `internal/channel/discord/discord_test.go`; `internal/channel/discord/concurrent_acceptance_test.go`; `internal/channel/controller/controller_test.go` | `go test ./internal/channel/discord ./internal/channel/controller` |

## Validation scope

Use the focused commands above first; they use credential-free fakes and do not perform live model calls. `./scripts/check.sh` is a conditional repository-wide check: it requires `./scripts/bootstrap-tools.sh`, then runs formatting/import checks, `go test ./...`, `go vet`, `golangci-lint`, and `govulncheck`. Run it when a change crosses packages broadly or before an integration/release handoff—not for an isolated unit change.

## Backlog

No evidence-backed documentation gaps are deferred.
