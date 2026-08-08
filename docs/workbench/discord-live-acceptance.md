# Discord live acceptance

> Historical note: this credentialed pass predates the final external-adapter
> dependency cutover. It remains evidence for the preserved product behavior,
> not live proof of the separately installed `hctl-discord` executable. The
> current external process path is covered by credential-free deterministic
> automation; another live pass requires separate authorization and a temporary
> least-privilege bot credential.

## External-adapter live procedure

Do not start this procedure until a maintainer explicitly authorizes the live
Discord effects and supplies a temporary bot credential through a trusted
terminal or runtime environment. Use one disposable bot application, one test
guild channel, one authorized user, a unique adapter profile name, an
authenticated supported harness, and a disposable Git repository. Grant only
the Discord permissions and Message Content intent required by the documented
single-user guild/DM journey. Do not paste the token into an issue, command
argument, source file, configuration file, transcript, or retained log.

1. Record the hctl commit/version, platform, harness version, authorization
   date, and the non-secret adapter package version and manifest/artifact
   digests. Build or obtain the matching `hctl-discord` package, then follow
   the exact install and verify commands in the
   [Discord example](../../examples/discord/README.md#local-setup).
2. Create a unique profile such as `external-live-YYYYMMDD`, then run the
   example's `hctl channel setup discord ... --profile PROFILE` through a
   trusted terminal. Confirm that setup names the expected bot without
   retaining its application, user, guild, or channel IDs. Run status with the
   same profile and retain only its bounded non-secret result.
3. Create a disposable Git workspace with one committed seed file. Start
   hctl in the foreground:

   ```sh
   hctl run examples/discord --workspace WORKSPACE \
     --harness codex --profile PROFILE --idle-timeout 30s
   ```

   Confirm that the installed external adapter connects; do not infer success
   merely from package installation.
4. From both the authorized guild channel and the authorized user's DM, send
   separate read-only messages and confirm replies remain on their originating
   surfaces. Ask each conversation to create a differently named marker with a
   different value, confirm the bounded write-access promotion occurs, and
   confirm each marker exists only in its own managed worktree.
5. Exercise two distinct interactive cases: complete one supported confirmation
   or choice through its native component, then request a date/time or freeform
   choice that degrades to declared text fallback and answer the marked fallback
   message. Do not treat an ordinary text reply to a native component as fallback
   evidence.
6. After a completed turn, wait more than 30 seconds and use `/status` to confirm
   the resident harness retired while the external adapter remained connected.
   Send a contextual follow-up on the same surface and confirm the native session
   resumes without exposing or recording its identifier. Also exercise `/new`
   while idle. Record only pass/fail and redacted lifecycle facts; do not retain
   callback payloads, Discord IDs, prompts, paths, native session IDs, or raw
   harness/adapter output.
7. Stop hctl cleanly, restart it with the same disposable workspace and profile,
   and have both surfaces read their own marker. Confirm assignments survive,
   no duplicate Discord delivery occurs, and redacted status exposes no path,
   Discord identity, credential, or native session identity. Stop cleanly
   again.
8. Run `hctl channel remove discord examples/discord --profile PROFILE` and
   confirm the adapter reports removal. Revoke or delete the temporary bot
   token in the Discord Developer Portal even if local removal succeeded, unset
   any ambient `HCTL_DISCORD_TOKEN`, and remove the disposable repository and
   package-build directory. Remove or disable the shared installed package only
   in an isolated test installation or after verifying it has no unrelated
   consumers.

Append a new dated result below with only the recorded versions/digests, the
scenario pass/fail matrix, bounded redacted diagnostics, shutdown/removal
outcomes, and cleanup/revocation confirmation. A failed or partial cleanup is a
failure and must be stated plainly. Never retain the token, Discord IDs, raw
protocol frames, raw model output, configuration paths, workspace paths, or
screenshots containing those values.

## Historical pre-cutover pass

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
