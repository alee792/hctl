# ADR 0008: Keep agent proposals workspace-local and inert

- Status: accepted

## Decision

Keep a proposal in the workspace that produced it, not in the portable agent
source it suggests changing. Use the visible convention
`.hctl/proposals/ID/`, where `ID` begins with a sortable creation time and a
short human-chosen name. The directory contains:

```text
.hctl/proposals/20260805T153000Z-clearer-reviewer/
  proposal.md
  change.diff
  review.md              # written only after a human decides
```

`proposal.md` is the human-readable record. It names one existing source file
relative to the selected agent source, its SHA-256 content hash at proposal
time, the selected agent-source fingerprint, workspace, harness, session or
run identifier when available, creation time, short reason, and any known
limits. `change.diff` is a bounded unified diff from that exact file content.
It is a suggestion, not an executable instruction. After publication,
`proposal.md` and `change.diff` are immutable evidence for that suggestion;
`review.md` is a separate, later decision record.

One proposal changes one existing UTF-8 text file only. Its target is one of
`instructions.md`; a text file in an existing `skills/NAME/` directory; or an
existing managed-tool source file under `tools/`. Binary skill resources are
outside this diff-based flow. A proposal cannot add, delete, rename, or move
files, change a dependency file, or target a path outside the agent source. A
multi-file idea is several independently reviewed proposals.

The source file must still have the recorded hash before a proposal can be
accepted as current. A different or missing source file makes the proposal
stale; it is never merged or rebased automatically. A person may write a new
proposal against the new content, or use the old one only as reference.

Review is an explicit human action. The reviewer reads the reason and diff,
checks the target and base hash, and either manually makes the change in agent
source and reapplies that source, or rejects it. They then add `review.md` with
the decision, time, reviewer name or role, and a short reason. An accepted
record says that the change was manually made; it does not itself alter source
or a running harness. Rejection and staleness do not delete a proposal.

Proposal directories are durable, owner-readable workspace state. They are
retained until a human removes them. A future writer creates a new directory
with restrictive permissions, writes bounded files, and publishes it with an
atomic rename. Invalid targets, unavailable source, an unreadable apply
record, an existing ID, unsafe paths, or write failure return a clear failure
and leave no usable partial proposal. Neither failure nor success changes
agent source, generated harness files, a running session, or tool code.

Proposals must not contain credentials, secrets, raw tool outputs, or
conversation transcripts. A future writer must tell callers this rule and
bound the content it accepts, but cannot claim to reliably detect or remove
secrets. Owner-readable storage and human review reduce exposure; they do not
make prohibited content safe to record.

A future managed proposal tool may only add this record: it reads the selected
apply context and the named existing source file, validates the target and
size, records provenance and the base hash, and writes the proposal directory.
It must not apply a diff, execute proposed tool code, reapply a project,
delete a proposal, or observe or control native filesystem activity. The tool
is additive to native harness behavior and remains human-in-the-loop. Its
caller guidance must prohibit sensitive content, and its size bounds do not
imply reliable sensitive-content detection.

## Context

An applied agent has two independent locations. The agent source is portable,
may be shared or read-only, and is the active authored truth. The workspace is
where the running harness, apply record, and other hctl durable state belong.
Writing a run's suggestion to agent source would mutate active behavior and
would also make a reusable source depend on one workspace's activity.

Authors need a convention they can inspect with ordinary file tools. A
proposal manifest or centralized registry would duplicate the proposal
inventory and add a technical concept without helping review. An exact base
hash prevents a familiar-looking patch from silently landing on changed
instructions, a changed skill, or changed tool code.

## Consequence

The proposal spike records a filesystem contract only. It introduces no
proposal command, managed proposal tool, source mutation, patch application,
or review interface. A later implementation must preserve this layout and
inert behavior, or replace this ADR through a new evidence-backed decision.
