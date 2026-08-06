# ADR 0013: Run schedules as fresh gateway tasks

- Status: accepted

## Plain-English summary

An agent author can add a Markdown schedule whose path is its name, whose
frontmatter contains one cron string, and whose body is the task prompt. Apply
validates and fingerprints that source without starting a clock. An operator
can trigger one occurrence through the durable gateway, but every accepted
occurrence starts a fresh native-harness session so recurring work does not
silently inherit old model context. The supplied input ID makes retries
deduplicatable. Automatic clocks, deployment registration, output delivery,
and per-turn deadline policy remain separate work.

## Decision

Root-agent schedules use Eve's Markdown convention at
`schedules/NESTED/NAME.md`. The relative path without `.md` is the schedule
name. Frontmatter contains exactly one string field named `cron`; it must be a
bounded, canonical five-field printable-ASCII value. The non-empty Markdown
body is the prompt. Apply discovers at most 32 schedules, validates their real
paths and bounded contents, and includes their original bytes in the source
fingerprint. It starts no harness process, clock, or external registration.

`hctl schedule trigger AGENT NAME --input-id ID` submits one prompt through the
existing typed gateway. A stable gateway conversation derived from the
schedule name retains bounded outcome history for deduplication, but task mode
opens the native harness without a resume ID for every accepted input. It
clears the stored native session ID after a terminal result. A crash can still
retain the active session ID long enough for the existing gateway recovery to
classify the occurrence as uncertain; it is never silently retried.

The command reports only the schedule name, input ID, lifecycle status,
duplicate flag, and available native runtime IDs. It discards model text.
Completed duplicates return the prior status without opening a harness. Any
non-completed terminal status produces a nonzero command result after its
status line is written.

The `cron` value is structurally validated in this slice but not evaluated.
HCTL-015 owns calendar semantics when it adds a foreground clock, including UTC
evaluation and overlap behavior. HCTL-014 separately owns durable per-turn
deadline behavior; the command currently retains the gateway's bounded
whole-process timeout.

## Context

Eve treats a Markdown schedule as task mode: the file carries one five-field
cron value and a prompt, each occurrence gets its own session, and model output
is discarded. Hctl does not own a hosted runtime, so the first useful slice is
portable source plus explicit one-shot dispatch. Reusing the durable gateway
preserves its existing input validation, acceptance, deduplication, terminal
outcomes, and uncertain restart recovery without inventing a second scheduler
state store.

One durable conversation per occurrence would also create unbounded
conversation records. A stable conversation per schedule plus fresh native
sessions keeps deduplication bounded by the gateway's existing recent-outcome
limit while preserving task isolation.

## Consequences

- Schedules are root-only because subagents still accept `instructions.md`
  only.
- Nested names use the same lowercase letter, number, and hyphen convention as
  other portable names.
- Changing a schedule invalidates the applied setup through the source
  fingerprint, but it does not require native-session migration.
- One-shot dispatch is local and explicit. It performs no channel delivery,
  network registration, missed-run replay, daemon installation, or schedule
  overlap management.
- TypeScript schedule handlers and Eve's channel/authenticated hosted runtime
  remain unsupported.
- The current gateway keeps only bounded recent outcomes, so deduplication is
  not an unbounded execution ledger.

## Sources

- [Eve schedules](https://github.com/vercel/eve/blob/84c3dfc1ff91e075444eee7c6d8e2ef55b2aaebe/docs/schedules.mdx)
- [Product specification](../product-spec.md)
- [ADR 0001](0001-use-native-harnesses.md)
