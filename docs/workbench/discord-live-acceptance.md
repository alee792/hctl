# Discord live acceptance

> Historical note: this credentialed pass predates the final external-adapter
> dependency cutover. It remains evidence for the preserved product behavior,
> not live proof of the separately installed `hctl-discord` executable. The
> current external process path is covered by credential-free deterministic
> automation; another live pass requires separate authorization and a temporary
> least-privilege bot credential.

- Date: 2026-08-06
- Harness: Codex
- Channel surfaces: one authorized guild channel and the authorized user's DM
- Credentials: existing user-controlled bot enrollment; no token, Discord ID,
  configuration path, or credential value was recorded

## Scenario

The current `hctl` binary ran the Discord example agent against a fresh
temporary Git repository. An initial attempt against an existing checkout was
abandoned after apply correctly refused to overwrite user-owned generated
paths. The disposable repository allowed the runtime and cleanup behavior to
be exercised without changing that checkout.

The authorized user asked the guild conversation and DM conversation to create
different untracked marker files through write-access promotion. Hctl created
two distinct managed worktrees, and each marker contained the requested value
only in its own conversation worktree.

The runtime then stopped cleanly and restarted against the same repository and
durable dispatch state. Startup reported that both managed worktrees were
preserved because they contained dirty or untracked work. It did not create a
replacement worktree for either conversation.

After restart, each Discord surface read its own marker successfully:

- the guild conversation returned `guild-pass`;
- the DM conversation returned `dm-pass`.

The two responses stayed on their originating surfaces. The DM `/status`
response reported the agent, harness, surface, queue, active-turn, and resident
session state without exposing a filesystem path, branch, Discord ID, native
session ID, configuration value, or credential. The runtime then stopped
cleanly again.

## Result

The live pass confirms the automated startup matrix with an enrolled bot:
guild and DM conversations retain independent writable assignments across a
process restart, dirty work is preserved in place, resumed sessions continue
on the correct surface, and channel-visible status remains redacted.
