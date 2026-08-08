# Interactive input acceptance

- Date: 2026-08-07
- Automated scope: credential-free Discord adapter, controller, dispatcher,
  durable store, and real Claude/Codex process adapters against fake children
- Credentialed scope: Codex core lifecycle passed; Claude was unavailable
  because the installed CLI had no authenticated account

## Automated evidence

The current automation crosses the installed external-adapter process boundary.
`TestExternalAdapterInteractionsControlsRecoveryAndFailures` drives semantic
interaction render, exact receipt, authorized answer, cancellation, status and
reset, delivered-interaction restoration, ambiguity, child failure, and
recovery through the process host. `TestInteractionAcknowledgesDiscordOnlyAfterDurableEventAck`
and the adjacent adapter regressions prove vendor acknowledgement follows
durable event acceptance, hostile or stale callback state fails closed, and
ambiguous effects are not replayed. Claude and Codex external-host conversation
tests separately prove that normalized Discord input reaches both native
harness drivers while the adapter-only token stays out of their environments
and retained diagnostics.

Controller, dispatcher, and interaction-store tests remain the authority for
durable parking, busy `/new`, exact continuation, generation revalidation,
capacity release, and restart transitions. The adapter-host recovery test binds
those generic guarantees to external interaction restore-before-replay. The
separate adapter owns only callback authorization, rendering, transport
acknowledgement, and vendor state; it cannot write the durable interaction
lifecycle directly.

The model-facing request schema is a per-kind discriminated union whose
required properties match runtime validation. In particular, `choose_one`
advertises a nonempty options list and exact one-of-one cardinality rather than
the formerly underconstrained shared field shape. Schema conformance tests
cover every top-level and form-field kind. A live-like Codex process test sends
the previously schema-plausible underconstrained choice call, proves it cannot
create durable input work, and receives the bounded safe `invalid` class rather
than the misleading session/provenance `unavailable` class.

`TestDiscordSemanticFixturesNormalizeAcrossNativeAndFallback` compares
normalized answers across the shared conformance fixtures. Confirm, single
choice, multiple choice, and text exercise native controls where supported;
every kind exercises deterministic fallback. Date/time, freeform choice, and
the mixed form degrade to text as specified. A separate text-only form proves
the modal and keyed-text paths normalize identically.

The existing fault tests remain the higher-value verification seam for cases
that should not be recreated by timing real processes:

| Contract | Evidence |
| --- | --- |
| Duplicate, conflicting, late, expired, and cancelled answers | `internal/interaction/coordinator_test.go` |
| Unauthorized, cross-surface, malformed, and stale callbacks | `discordadapter/adapter_test.go` and `discordadapter/review_regression_test.go` |
| Restart before render, after answer, around resume intent, and no retry after ambiguity | Coordinator, manager, adapter-host recovery, and Discord-adapter replay tests |
| Queue saturation, fair admission, idle hibernation, shutdown, and parked-slot release | Capacity, manager, and stitched process tests |
| Bounds, disabled mentions, acknowledgement ordering, and ambiguous delivery | Channel-adapter protocol, adapter-host, Discord-adapter, and controller tests |
| Environment, diagnostics, status, and audit redaction | Secure-environment, CLI, MCP, controller, adapter-host, and Discord-adapter tests |

## Credentialed manual pass

The pass used the enrolled user-controlled Discord bot, Codex CLI 0.144.1
authenticated through ChatGPT, and a disposable Git workspace. Claude Code
2.1.221 was installed but reported `loggedIn: false` with no authentication
method, so no credentialed Claude claim is made. No tokens, configuration
contents, Discord IDs, callback handles, native session IDs, raw state,
screenshots, or raw harness logs are retained in the repository.

The live Codex result was:

| Check | Result |
| --- | --- |
| Enrolled profile and Keychain credential validation | Pass |
| Native confirmation render and acknowledgement | Pass |
| Waiting status with one pending request and zero active/resident capacity | Pass |
| Second ordinary message and `/new` bounded busy responses | Pass |
| Same-workspace restart with no duplicate control | Pass |
| Same-thread continuation and exactly one final Discord response | Pass |
| Native `choose_one` render, acknowledgement, and resumed result | Pass |
| Post-completion hibernation with zero active/resident capacity | Pass |
| Second concurrent application runtime rejection | Pass |
| Graceful shutdown | Pass |

The first live choice attempt exposed an underconstrained model-facing schema
and a misleading `unavailable` error class. After aligning the per-kind schema
with runtime validation and separating `invalid` from provenance/session
failure, the credentialed retry rendered Staging/Production controls and
resumed with Staging exactly once. Native multiple-choice, modal, fallback,
cancellation, hostile callback, and precise crash-window variants were not
replayed manually; the deterministic automated evidence above remains the
acceptance authority for those cases.

Manual result: **Codex passed; Claude unavailable (not authenticated)**.
