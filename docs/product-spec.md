# Product specification

- Status: experimental product contract
- Working CLI name: `hctl`; product naming is deferred
- Initial runtime: local Go executable
- Initial harnesses: Claude Code and Codex CLI

## Initial installation

The initial supported platform is `darwin-arm64`. The exact `vX.Y.Z` Git tag is
the authoritative release version and names
`hctl_X.Y.Z_darwin_arm64.tar.gz`, which contains one `hctl` executable at its
archive root. The accompanying `hctl_X.Y.Z_SHA256SUMS` manifest supplies its
SHA-256 checksum. A user downloads and verifies those exact files, extracts the
executable to a stable location on `PATH`, then runs `hctl apply` with an agent
source and workspace. The generated MCP configuration records the resolved
absolute executable path: moving the binary requires reapplying the workspace;
replacing it at the same path leaves that reference valid, but the supported
upgrade journey reruns `apply` to refresh any runtime cache.

`go install` is not a supported first-release user journey. It requires a Go
toolchain and source/module resolution rather than consuming the released,
checked artifact. `hctl package` is not introduced: portable agent source and
native lockfiles remain inputs to `apply`, while generated tool hosts and
dependency environments remain disposable workspace-local caches. Another
machine installs its needed native runtimes and reruns `apply`; it does not
reuse a copied `.hctl/cache/` directory.

That remains the local installation contract. ADR 0027 separately defines a
canonical staged filesystem for downstream OCI builds; it does not make raw
workspace caches relocatable or introduce a general `hctl package` command.

## User and job

The primary user is an agent author who understands basic files and directories
and common AI concepts such as instructions, skills, and tools. They should not
need to understand registration, manifests, or harness configuration. They
define one filesystem-authored agent project, apply it to a chosen workspace,
prove it interactively in Claude Code or Codex, and may operate the same setup
headlessly through channels.

## Product principles

1. The agent project is legible, versionable, portable source and is not
   coupled to the repository that stores it.
2. Common behavior is portable; harness-specific differences are explicit.
3. Compilation and validation happen before harness files are written or a
   turn dispatcher starts.
4. Generated native files are disposable and visibly tool-owned.
5. Native harness tools remain available and explicitly unmanaged.
6. Policy applies only at managed-tool and durable-state boundaries.
7. Interactive users remain in the native harness interface.
8. Unsupported harness behavior is reported without rewriting valid authored
   source or pretending that hctl enforces it.
9. Conventional files register behavior without a second inventory.
10. Author-facing language stays concrete; runtime terminology remains internal.

## Authored project

Authoring is filesystem-forward. Where the concepts match, hctl uses Eve's
conventional vocabulary: instructions, tools, skills, channels, connections,
sandbox, subagents, and schedules. Only the subset named below is implemented
in the MVP.

The authoring API is convention-driven. An MVP project is:

```text
my-agent/
  instructions.md
  skills/
    research/
      SKILL.md
      references/
        sources.md
  plugins/
    review-pack/
      plugin.json
      skills/
        review/
          SKILL.md
  tools/
    get_weather.ts
    lookup_policy.py
    hash_text/
      tool.go
  subagents/
    researcher/
      instructions.md
  connections/
    github.md
  channels/
    discord.md
  schedules/
    billing/
      sweep.md
  harnesses/
    claude/
      .claude/
        settings.json
    codex/
      .codex/
        rules/
          default.rules
```

The directory name supplies the agent name, normalized to lowercase words with
hyphens. `instructions.md` is required and contains YAML frontmatter with one
plain `description` plus a non-empty Markdown body. Generated always-on
instructions contain the body, not the frontmatter.

The `skills/` directory is optional. Each visible immediate directory is one
skill and contains a required `SKILL.md`; its frontmatter `name` must match the
directory name. A skill follows the open Agent Skills format and may include
regular-file resources such as `scripts/`, `references/`, `assets/`, and other
nested directories. Adding or removing a skill directory updates the compiled
project without separate registration. Hctl keeps the existing eight-skill
limit.

The optional `plugins/` directory vendors Agent Plugins v1 dependencies. Each
visible immediate real directory is one plugin with a required bounded
`plugin.json` targeting the exact canonical v1.0.0 schema identifier. The
directory may contain at most 32 entries, and each plugin `skills/` location at
most 128 entries before the merged eight-skill limit applies. Hctl
validates that schema locally without fetching it. Manifest violations reject
only that plugin; unsupported top-level fields, non-object `extensions` values,
and every unsupported extension namespace are ignored with warnings. Namespace
values are not validated. Hctl imports Agent Skills only from immediate real
directories beneath the plugin's fixed `skills/` location. A missing `plugins/`
directory, a missing plugin `skills/` directory, and empty component locations
are normal.

Root `skills/` load first. Plugin and component directories load in lexical
order. The first skill name wins; later collisions are skipped with a warning
and are not renamed. Invalid plugin skills are skipped independently while
valid sibling components continue. The merged skill set retains the existing
eight-skill aggregate limit and resource bounds. Symlinks are never followed.
Accepted plugin manifests and consumed skill resources participate in the
source fingerprint and generate through the same native Claude and Codex skill
paths as root skills.

See [ADR 0019](adr/0019-import-vendored-agent-plugin-skills.md) for the initial
vendored-skills decision.

An accepted plugin may also contain a bounded, regular UTF-8 `mcp.json` with
the exact canonical Agent Plugins v1.0.0 MCP schema identifier. Missing
`mcp.json` is normal. A malformed top-level MCP document disables only that
component; an invalid server disables only itself. Hctl supports `stdio` and
`streamable-http`; valid SSE declarations warn and are skipped. Plugin
directories and server names are processed lexically. The first exact server
name wins, `managed` is reserved for hctl, and collisions are skipped without
renaming.

A stdio command is one bare executable name or a plugin-relative `./` path.
Plugin-relative commands and plugin-root working directories must remain within
the real plugin tree without symlinks. The optional working directory is rooted
at `./`, `${PLUGIN_ROOT}`, or `${PLUGIN_DATA}` and defaults to the plugin root.
Hctl expands the exact `${PLUGIN_ROOT}` and `${PLUGIN_DATA}` placeholders once
in arguments, environment values, and working directories, then supplies both
variables in the server environment. A plugin may not override them. Before
native setup is written, hctl creates a private persistent data directory at a
deterministic agent-and-plugin-specific path beneath workspace
`.hctl/plugin-data/`; hctl normalizes and verifies that directory as owner-only
and does not delete plugin data when configuration is removed.

Remote URLs are absolute HTTP(S), contain no user information or fragment, and
use HTTPS unless their host is `localhost` or a loopback IP literal. Header
names and values must be valid HTTP and may not collide case-insensitively.
Headers are literal package-visible values: hctl performs no expansion and they
must not contain secrets.

Accepted plugin servers are emitted as native project MCP configuration. Claude
Code receives `.mcp.json`; Codex receives `.codex/config.toml`, where plugin
servers remain optional and use prompt approval. The harness owns startup,
approval, transport, authentication, retries, and runtime behavior. Hctl does
not proxy, supervise, authorize, observe, or audit plugin MCP calls. Accepted
server values and any consumed plugin-relative command bytes and executable
intent participate in the source fingerprint. See [ADR 0020](adr/0020-map-plugin-mcp-through-native-harness-configuration.md).

Codex preserves unsupported placeholder-like text literally. Claude project
MCP configuration performs its own environment expansion in commands,
arguments, environment values, URLs, and headers. To prevent accidental ambient-secret
substitution and preserve the portable literal-value contract, hctl skips a
Claude plugin server containing such text after portable expansion and emits a
warning.

`name` and `description` are required. Names contain 1-64 lowercase ASCII
letters, digits, and single hyphens, without a leading or trailing hyphen.
Descriptions contain 1-1024 characters. The portable optional frontmatter is
string `license`, 1-500 character `compatibility`, string-to-string `metadata`,
and experimental space-separated string `allowed-tools`. Documentary fields
are preserved without claiming that a harness operationalizes them.
Harness-specific behavior is honored only when the selected harness documents
an exact representation. Recognized vendor fields and files remain intact when
applied elsewhere, with a precise warning that they may have no effect. Hctl
does not translate, strip, or enforce them. In particular, it does not pretend
that Codex honors `allowed-tools` or a Claude skill model selection. An
OpenAI-host `agents/openai.yaml` file is copied byte-for-byte to either target;
Claude apply warns because Claude does not document the file.

Apply copies supported skill resources byte-for-byte into the selected
harness's project skill directory and preserves executable intent in its
ownership and source fingerprints. The reserved `agents/openai.yaml` resource
is copied unchanged to either target and warns for Claude. All authored skill
entries must be bounded regular files and real directories with valid UTF-8
relative paths. Symlinks are rejected even when a native harness supports them,
so the portable source boundary remains deterministic and cannot escape the
agent project.

Immediate directories under `subagents/` define native harness subagents. Each
contains only an `instructions.md` file with the same description-and-body
contract plus optional string `effort`. Effort accepts exactly `low`, `medium`,
or `high`; apply emits it as Claude agent `effort` or Codex custom-agent
`model_reasoning_effort`. The field is omitted from native output when absent.
Hctl validates and requests effort, while the selected harness, model, account,
and policy determine whether it is honored. Root `instructions.md` remains
description-only. The MVP allows one level and at most eight subagents. A subagent
inherits the selected parent's generated instructions, skills, managed MCP
tools, native tools, and permissions through native harness behavior. Child
skills, tools, dependency files, and nested subagents are rejected rather than
silently ignored. Subagent and tool names may not collide. Portable subagent
names use hyphens; generated Codex agent identifiers use underscores because
that harness requires them.

The optional `harnesses/` directory carries intentionally nonportable native
project files. `harnesses/claude/` may contain a literal `.claude/` tree and
`harnesses/codex/` may contain a literal `.codex/` tree. Apply reads only the
selected harness tree and mirrors its files at the same workspace-relative
paths. This supports native surfaces such as Claude's documented project
`.claude/settings.json` and Codex's documented project
`.codex/rules/*.rules` files without inventing an hctl schema. See the
[Claude settings documentation](https://code.claude.com/docs/en/settings) and
[Codex rules documentation](https://developers.openai.com/codex/rules).

Harness-specific files are bounded regular files beneath real directories;
paths must be normalized UTF-8 and symlinks are rejected. Contents are copied
byte-for-byte, executable intent is preserved, and the selected files join the
source fingerprint and apply ownership record. Hctl does not parse, merge, or
validate native semantics, and does not promise that a particular harness
version honors a copied file. Authors must not place credentials in these
files; hctl does not claim reliable secret detection.

Hctl-owned native destinations remain reserved. Claude authors cannot replace
`.claude/skills/` or `.claude/agents/`; Codex authors cannot replace
`.codex/config.toml` or `.codex/agents/`. Portable instructions, skills,
subagents, and managed MCP setup continue to use their existing conventions.
Case-folded aliases of these paths are also rejected before mutation so agent
source remains safe to apply to common case-insensitive workspaces.

The optional `connections/github.md` file contains a 1-1024 character UTF-8
Markdown description. Its conventional path registers a connection named
`github`; there is no connection manifest or name field. It exposes exactly
`github__get-repository`, `github__list-issues`, and `github__get-issue` through
the existing managed MCP server for both harnesses. The description is included
in each tool's model-visible description. Any other entry under `connections/`
fails before workspace mutation.

This first connection is anonymous, public, read-only GitHub REST access.
Repository inputs are bounded `owner` and `repo` strings. Issue listing accepts
optional `state` (`open`, `closed`, or `all`) and `limit` from 1-20, defaulting
to `open` and 10; GitHub's issues endpoint may include pull requests. Single
issue lookup requires a positive issue number. Hctl sends fixed GET requests
to `https://api.github.com` with GitHub's JSON accept and current
`2026-03-10` API-version headers,
no authorization header, a five-second client timeout, no redirects, no retry,
and a one-MiB response limit. Returned repository and issue fields are selected
and bounded rather than forwarding raw upstream bodies; a `truncated` field
reports when returned text or labels were shortened. Errors are stable
categories for invalid input, missing resources, rate limits, authorization,
service availability, timeouts, oversized or invalid responses, and other
request failures; they do not include upstream bodies or arbitrary diagnostics.
Apply performs no network request.

Private repository access, credentials, writes, a generic OpenAPI engine,
dynamic MCP proxying, approval UX, and credential-broker code are deferred. Any
secret-bearing extension must first satisfy
[ADR 0009](adr/0009-use-a-local-secretless-operation-broker.md).

The optional `channels/discord.md` file contains strict `mode: ambient`
frontmatter and a 1-1024 character UTF-8 Markdown participation policy. Its
conventional path registers the built-in `discord` channel; any other entry
under `channels/` fails before workspace mutation. The file contains no runtime
identity, authorization ID, profile, or credential. It joins the source
fingerprint, and apply adds its policy plus the exact `HCTL_NO_REPLY` control
result to generated native instructions.

`hctl channel setup discord` enrolls an existing bot. Local non-secret profiles
live in owner-only TOML beneath the OS user configuration directory; tokens live
in the OS credential store. Deployment selects an owner-only mounted config and
injects `HCTL_DISCORD_TOKEN`. Every run validates the token's application and bot
IDs against the selected profile before opening an outbound Gateway connection,
and removes the token from every child-process environment.

`hctl run` auto-applies missing or stale setup, then serves the authorized user
in one guild channel and DM. Each surface has independent durable dispatcher
state. A transport-neutral channel controller owns surface registration,
pending-turn correlation, complete-response buffering and control-result
handling, typing readiness, terminal classification, status/reset delegation,
and dispatcher lifecycle. The Discord adapter retains Gateway/REST integration,
authorization, native event filtering, reply references, rendering, mentions,
commands, and delivery semantics. Transport-owned reply targets remain
process-local and vendor payloads never enter dispatcher or durable state. One
deterministic managed-session lifecycle owns each surface's queue,
native session mapping, and resident harness process, while a shared state
owner serializes durable updates across surfaces. Other users, channels, bots,
and webhooks are ignored. Output is buffered until completion, exact
`HCTL_NO_REPLY` is suppressed, and visible replies use bounded 2,000-character
chunks with mentions disabled. `/new` resets an idle surface and `/status`
returns its redacted lifecycle and queue state. After 15 idle minutes by
default, the lifecycle closes the resident harness but retains the durable
native session mapping; `--idle-timeout` configures a positive interval up to
24 hours, and the next eligible message resumes that native conversation.
Active work is never hibernated, and queued work is never discarded. Under
resident saturation, a process with a durable backlog may close between turns
to let an older nonresident waiter run, then resume its own queue later.
Explicit `--input jsonl` selects the existing headless stream instead; one-shot
schedules retain their fresh-session semantics.

The channel runtime defaults to at most four resident harness processes and two
simultaneously active turns. `--max-resident-sessions` and
`--max-active-turns` provide bounded overrides. Accepted input remains durably
queued while it waits for active capacity, and turn grants advance in request
order across conversations so one busy surface cannot repeatedly jump ahead of
another. At resident pressure, the least-recently-idle eligible process
hibernates before a replacement opens; if all residents have backlogs, fair
rotation happens only between turns and preserves every queued input. Duplicate
delivery consumes neither a new queue entry nor capacity. `/status` includes
only aggregate active, resident, limit, and queued counts.

Channel-native human input uses a **transport-neutral interactive request**,
not unrestricted generative UI. The versioned semantic union contains exactly
`confirm`, `choose_one`, `choose_many`, `text`, `date_time`, and a modest
`form`. Every request has a bounded prompt, an optional bounded text fallback,
a relative expiry of 60 seconds through seven days, and an explicit allowed or
forbidden cancellation policy. Its fields and choices use stable semantic IDs;
choice labels, descriptions, values, selection cardinality, freeform input,
text lengths, and date/time representation are explicit and bounded. A form
has at most eight fields, each choice has at most 25 options, the whole request
has at most 64 options and 32 KiB of encoded JSON, and answers have at most
16 KiB of encoded JSON. Prompts are at most 2 KiB, fallbacks 4 KiB, labels 100
bytes, descriptions 300 bytes, option values 256 bytes, and text answers 4,000
Unicode code points. Semantic IDs contain 1-64 lowercase ASCII letters,
digits, underscores, or hyphens, begin with a letter, and are unique within
their request scope. Date input is canonical `YYYY-MM-DD`, time input is
24-hour `HH:MM`, and combined date/time input is RFC 3339 with an explicit
offset and is normalized to UTC.

Answers refer only to the request's stable field and option IDs. Trusted hctl
code independently validates them, orders fields and choices by the original
request, normalizes text line endings and date/time representations, and
rejects missing, duplicate, unknown, out-of-range, or inapplicable values.
Adapters advertise supported request kinds and concrete limits before a
lifecycle waits for input. An unsupported request deterministically uses its
specified text fallback or fails clearly when no fallback exists. Hctl assigns
interaction and callback IDs, ownership, authorization, expiry timestamps, and
continuation metadata; none are model-authored fields. The contract has no raw
vendor payloads, layout nesting, URLs, executable code, credential references,
or A2UI surface schema. A future renderer may adapt this semantic contract
without changing what the model is allowed to request.

Capabilities distinguish supported top-level request kinds from the bounded
field kinds that a native form may contain. Discord's first renderer supports
confirmations, non-freeform choices, text, and text-only forms of at most five
fields. It uses buttons or string selects for choices and an Answer button
followed by a modal for text input. Date/time, freeform choices, and mixed forms
degrade. The authored fallback is only introductory text: hctl appends and
parses one transport-neutral grammar using exact confirmations, one-based
choice ordinals, whole text replies, canonical date/time values, keyed form
lines, and exact allowed cancellation. A freeform choice is exactly
`other=TEXT`; choose-many may combine option ordinals and one freeform value as
`1,2;other=TEXT`. The freeform value counts as one selection, including an
explicit empty `other=` when the field permits zero-length text. A fallback
reply must correlate to the current bot request and enters answer acceptance
rather than ordinary input. Invalid correlated fallback replies remain pending
and receive one bounded, mention-disabled format correction. A successful
native cancellation receives an explicit cancellation acknowledgement.

The renderer command is deliberately narrower than the controller's pending
interaction snapshot. Expiry, continuation mode, and lifecycle phase support
recovery and authorization but are not exposed through the renderer seam.

Discord component and modal identifiers are bounded opaque digest handles plus
trusted positional slots. Callback decoding verifies the selected application,
authorized human, exact surface and durable owner, current pending request,
action, and request shape before mapping slots back to semantic IDs. The final
normalized answer commits before Discord acknowledgement, and continuation is
scheduled only after the acknowledgement attempt. Raw Discord payloads remain
inside the adapter. REST errors after a render attempt are ambiguous and are
not retried.

Each dispatch conversation may persist at most one nonterminal interactive
request in the same owner-only conversation record as its triggering input,
queue, native session mapping, and worktree assignment. The shared conversation
store remains the sole writer; renderers, harness continuations, MCP children,
and vendor adapters cannot save independent snapshots. The externally meaningful
lifecycle is `requested`, `rendered`, `answered`, `resuming`, then `completed`,
with explicit `cancelled` and `expired` terminal outcomes and bounded terminal
tombstones for duplicate classification.

Rendering and continuation each use a commit-before-side-effect intent. The
renderer atomically claims delivery so concurrent attempts cannot both send; a
crash before that claim remains safe for a first attempt. A crash or ambiguous
result after delivery intent becomes delivery-uncertain and is not automatically
redelivered; a subsequently valid answer may prove delivery. A crash or
ambiguous result after resume intent becomes resume-uncertain and is not
automatically resumed. Explicit recovery may adjudicate it as completed or
failed without invoking the harness again. Answers are normalized and committed
exactly once
before acknowledgement or continuation. Identical duplicates are idempotent;
conflicting, late, expired, cancelled, unauthorized, and cross-surface answers
are rejected. Acceptance requires the store-bound agent and conversation,
authorized principal and surface owner, interaction ID, original request, and
current pending record to agree; a callback or interaction ID alone is not
authority.

A terminal interaction commit also records whether a queued successor must be
woken. The next durable input consumes that wake intent when it becomes active;
runtime startup drains any intent left by a crash after completion,
cancellation, or expiry but before in-memory notification.

Waiting is parking rather than blocking: no live model turn, tool callback,
channel request, resident harness process, or active-turn grant remains held.
The pending request blocks later queued inputs for its conversation while other
conversations continue through shared capacity. A later ordinary message on
that surface is not silently discarded or added behind the parked origin: it
receives one bounded, mention-disabled busy response referencing that message.
Reset rejects a nonterminal request, worktree reconciliation treats it as busy,
and resume uncertainty also
prevents automatic worktree retirement. Status and audit expose only
`waiting_for_input` plus existing bounded aggregate queue and capacity state,
never prompts, answers, identifiers, continuation keys, paths, configuration,
or credentials.

The managed `channel.request_input` contract now exists behind a runtime
capability gate. It accepts only the semantic request above and emits a typed
harness event; the dispatcher then injects the active input, pseudonymous
owner, tool-call correlation, continuation mode, and runtime target before
calling the durable coordinator on its serialized state path. The dispatcher
recomputes native-versus-fallback resolution from trusted responder
capabilities and commits `requested` before acknowledging the harness bridge.
MCP children, renderers, and channel adapters do not write dispatch state.

Real advertisement requires a harness-owned root bridge, an available harness
continuation strategy, and responder support for native rendering or the
request's declared text fallback. A shared inherited MCP server cannot be
enabled merely by configuration or a process-wide root flag. Claude's exact
`PreToolUse` hook denies events containing its documented subagent `agent_id`;
an eligible root deferral becomes a structured harness event carrying opaque
proof produced by the harness-owned constructor. Zero or caller-assembled
events fail before persistence.
Schedules, explicit JSONL, ordinary native
sessions, unavailable responders, and unproven subagent calls do not receive
the capability. Claude and Codex independently prove root ancestry before
persistence; true subagent tool-list isolation is deferred. Generated Claude and Codex
channel instructions describe when to ask and forbid fabricated callback IDs
or vendor markup, but those instructions are not the enforcement boundary.
The selected harness strategy returns a bounded, content-free tool disposition
after the durable commit, leaving deferred-tool versus continuation-turn
semantics to ADRs 0024 and 0023. MCP does not manufacture that result. Audit
correlation uses only the MCP request identity and tool name, not semantic
request bytes. The resumed Claude tool response necessarily returns its
normalized answer to the original model turn; diagnostics and audit never
contain prompts, options, answers, fallback text, or vendor payloads.

The two durable continuation modes are intentionally different. A
**native deferred-tool continuation** later resumes the same logical tool call
using a harness-native continuation identity. A **continuation turn** later
opens another turn in the same native session with the normalized answer and
request context. Neither is a blocking request.

Claude implements `native_deferred_tool` with its documented headless
`PreToolUse` deferral protocol. Hctl commits the request, closes the process,
and retains no capacity while awaiting an answer. After answer acceptance the
generic manager restart scheduler claims the continuation exactly once,
resumes the persisted Claude session without a new user prompt, and replays the
exact tool identity and semantic input through a short-lived owner-only broker.
The initial deferred result is trusted only after consuming an exact,
single-use hook receipt recorded after successful broker delivery. A resumed
turn succeeds only after the broker atomically confirms one delivered allow
decision and one delivered exact MCP answer; attempted but disconnected broker
responses are uncertain rather than complete.
Only after lifecycle completion is durable does the manager publish the
terminal turn event. A known unavailable or lost retained session fails
deterministically; an ambiguous resumed process is never retried.

Codex implements `continuation_turn` through app-server's documented
experimental dynamic-tool and thread APIs. Only a channel-managed root with a
dispatcher handler and compatible responder registers the `channel` namespace
and `request_input` function. The adapter requires exact active root thread and
turn provenance before the dispatcher sees the semantic request. After the
durable park it returns only `continuation_turn`, lets the bounded turn end,
closes app-server, and retains no process or active-turn grant while waiting.
After answer acceptance it resumes the stored thread and starts a new turn with
a bounded controller-owned `hctl.channel_input_answer` JSON envelope containing
the exact request correlation and normalized answer. This is not
`turn/steer`, a live native input waiter, MCP elicitation, or same-tool-call
resume. Resume ambiguity follows the durable no-retry rule. Production
advertisement is enabled only for a channel-managed Discord root whose selected
adapter advertises a compatible native renderer or deterministic text fallback
codec.

New and resumed channel-managed sessions run read-only in the shared workspace:
Claude uses native plan permission mode, Codex uses a read-only sandbox with
approvals disabled, and the managed MCP boundary does not start or expose
authored tool hosts under that policy. Safe built-in and anonymous GitHub read
operations remain available. Generated channel instructions define the exact
`HCTL_REQUEST_WRITE_ACCESS` result for requests that genuinely require a
workspace change. Like `HCTL_NO_REPLY`, it is interpreted only after the whole
trimmed response exactly matches and is never delivered as ordinary output.
This policy does not change apply, explicit JSONL, schedules, or interactive
native-harness use.

When a read-only turn returns that exact write-access result, hctl creates a
conversation-specific branch-backed Git worktree in a private sibling
directory, applies the selected agent there, resumes the same native session
with workspace-write access, and submits one internal continuation of the
original request. The control result and continuation are never Discord
messages. The owner-only dispatch state records only the validated worktree
root and branch. Later turns and restarts reuse that assignment unless the
startup retirement rules below prove it disposable; other conversations remain
read-only in the shared checkout. A non-Git workspace,
identity mismatch, unsafe assignment, or modified generated file fails without
changing the shared checkout or ambiguously retrying the turn. `/new` starts a
fresh native session while retaining an existing isolated workspace.

Writable conversations remain independent when they overlap: each keeps its
own branch, worktree, durable queue, native session, and response surface while
sharing the runtime-wide admission limits. A harness, worktree-resolution, or
turn-deadline failure retires only that conversation's current worker and
preserves the other conversations' state and execution. Failure to deliver
dispatcher events is runtime-wide and still stops admission because the
channel can no longer account for outcomes safely.

At startup, hctl validates every durable worktree assignment before admitting
turns. Active, queued, uncertain, dirty, untracked, unmerged, missing, moved,
foreign, and otherwise unverifiable assignments are preserved with local
operator diagnostics. Only an inactive worktree with verified generated setup,
no non-generated changes, and a branch already reachable from the base
checkout's `HEAD` is retired automatically. Durable retirement intent precedes
narrow cleanup of the exact worktree and branch; partial failure retains the
assignment for idempotent retry and blocks only that conversation. Discord
status remains path- and identifier-free.

The optional root `schedules/` directory contains nested Markdown task files.
The bounded, valid UTF-8 path beneath `schedules/`, without `.md`, is the
schedule name. At most 32 schedules are discovered. Each file is bounded UTF-8
Markdown whose strict YAML frontmatter
contains exactly one string field named `cron`. The value is at most 256
printable ASCII characters and must parse as a standard five-field expression.
The non-empty body is the task prompt; hctl removes only one optional blank line
after the frontmatter delimiter and otherwise preserves its Markdown bytes.
Apply validates these files and includes their original bytes in the source
fingerprint, but starts no clock, harness process, network request, or external
registration.

```sh
hctl schedule trigger AGENT NAME --workspace WORKSPACE \
  --harness claude --input-id OCCURRENCE_ID \
  --turn-timeout 90s --timeout 2m
```

One-shot dispatch requires the selected setup to be current and the operator to
supply a stable dispatch input ID. A conversation derived from the schedule name
keeps bounded durable deduplication outcomes, while every accepted input opens
a fresh native-harness task session without a resume ID. Terminal task state
clears the stored native session ID; active work recovered after restart keeps
the turn dispatcher's existing uncertain semantics and is never silently retried.
Completed duplicate input returns the prior status without opening a harness.

`--turn-timeout` configures a positive task-turn deadline up to 30 minutes and
defaults to 90 seconds. It begins only after the native process opens and the
occurrence is durably active. It is independent of the existing positive,
bounded `--timeout`, which continues to cover verification and the complete
command lifetime. Turn-deadline expiry aborts only that native process,
durably records the occurrence as `uncertain`, clears its fresh-session
continuation, persists the bounded `deadline_exceeded` reason separately from
the lifecycle status, and returns a clear command error. The lifecycle line
includes that reason. Repeating the input ID returns the retained uncertain
outcome and reason without opening a process; generic restart uncertainty has
no deadline reason, while a distinct later occurrence opens a fresh session.
If the outer command context ends first, existing uncertain restart recovery
remains authoritative.

The command writes one bounded lifecycle line containing the schedule, input
ID, status, duplicate flag, and runtime IDs when available. It never emits
model text. A non-completed outcome returns a command error after the status is
reported. This task mode performs no channel delivery, registration, daemon
installation, missed-run replay, overlap handling, credential use, or live
model call during credential-free tests. TypeScript schedule handlers,
subagent schedules, and Eve's hosted auth and delivery runtime are unsupported.

`hctl schedule run AGENT --harness <claude|codex>` is the explicit foreground
clock. It requires current generated setup, loads schedules once, verifies the
harness once, and performs no auto-apply or hot reload. Standard five-field
cron expressions are evaluated in UTC. The first occurrence is strictly after
startup; each wake admits only a matching occurrence in its current UTC minute,
without downtime or clock-jump backfill. Repeated and backward wakeups do not
duplicate an admitted scheduled minute.

One shared task runtime owns the durable store and bounds concurrent fresh
task sessions with `--max-active-turns` (default 2, maximum 64). Queued capacity
counts as in flight, so the same schedule cannot overlap. Stable occurrence IDs
are the full SHA-256 of the exact UTF-8 schedule name and canonical scheduled
UTC minute. A local lock excludes another clock for the same canonical
workspace, agent identity, and harness. SIGINT or SIGTERM stops admission and
drains admitted work through completion or its `--turn-timeout`. Lifecycle
output is bounded and contains no prompt, model text, path, or raw harness
error. See [ADR 0026](adr/0026-run-schedules-from-a-foreground-utc-clock.md).

Visible `tools/*.ts` and `tools/*.py` files each declare one tool. A visible
`tools/NAME/tool.go` directory declares one Go tool. Filenames supply tool
names, with underscores exposed as hyphens. TypeScript definitions export a
default object containing `description`, strict Zod `inputSchema` and
`outputSchema`, and `execute`. Python modules export `description`, Pydantic
`Input` and `Output` models, and `execute`. Go packages export `Description`,
`Input`, `Output`, and `Execute`. The runnable mixed-language fixture is the
canonical syntax example while the product remains experimental.

Authored source entries must be bounded regular files and real directories
without symlink traversal. Contract and code files must be UTF-8; arbitrary
skill resources may be binary. There is no authored hctl manifest, registry,
or duplicated tool inventory. TypeScript uses root `deno.json` and `deno.lock`;
Python uses `pyproject.toml` and `uv.lock`; Go uses `go.mod` and an optional
`go.sum`. These native files describe dependencies without registering tools.
Compilation produces a deterministic apply record and source fingerprint. The
bounded `echo` managed tool remains an hctl-provided default; it is not author
configuration.

## Apply and handoff

```sh
hctl apply AGENT --workspace WORKSPACE --harness claude
hctl apply AGENT --workspace WORKSPACE --harness codex
```

`apply` validates the authored project, target harness executable, tool
definitions, locked dependencies, and protocol readiness. It invokes Deno,
`uv`, or Go only when that language is present, then materializes owned native
files in the selected workspace so the user can change into that directory and
start the selected harness normally. `--workspace` defaults to the agent
project directory, making a standalone agent the simplest case. Applying an
agent stored elsewhere is explicit:

```sh
hctl apply ~/agents/reviewer --workspace ~/Code/example --harness claude
cd ~/Code/example && claude
```

The agent project supplies instructions, skills, tools, subagents, vendored
plugins, harness-specific files, and native dependency files. The workspace
supplies harness-visible working files and is
the working directory for the harness and authored tools. Generated harness
files, apply records, plugin data, dispatch state, and runtime caches belong to
the workspace. Source discovery and dependency preparation remain rooted in
the agent project.

Claude receives `CLAUDE.md`, `.mcp.json`, `.claude/skills/`, and
`.claude/agents/`. Codex receives `AGENTS.md`, `.codex/config.toml`,
`.agents/skills/`, and `.codex/agents/`. Generated MCP configuration uses the
resolved `hctl` executable, agent-source, and workspace paths. Supported
vendored plugin servers join these native MCP files without joining hctl's
managed tool boundary. Hctl generates skills only at this project scope; it
does not modify user, administrator, enterprise, or plugin skill locations.

Codex project configuration remains subject to Codex's native repository-trust
flow. Apply does not edit the user's global Codex configuration or silently
trust a project on their behalf.

Apply refuses to overwrite hand-authored native files or any hctl-owned file
that was modified after the previous apply. Removing or changing a
harness-specific source file uses the same modified-file protection and stale
cleanup as generated portable setup. Reapplying identical source is
deterministic.

## Harness contract

Each harness integration declares and verifies:

- its executable and compatible version signal;
- native generated-file surfaces;
- managed tool exposure;
- new-session and resume behavior;
- structured input, output, and terminal events; and
- any interruption or steering behavior that is not portable.

Claude Code uses bidirectional stream JSON. A second message received during an
active turn is queued for the next turn. Codex uses its local App Server JSONL
protocol. Active-turn steering and interruption are Codex-specific and are not
part of the portable MVP promise.

## Headless run

```sh
hctl run AGENT --harness claude
hctl run AGENT --harness codex
```

The `run` command sends bounded JSONL input through the turn dispatcher. Each
input contains a caller-provided `input_id` and `text`. The dispatcher durably
accepts and queues input while a turn is active, processes one FIFO turn per
conversation, emits ordered JSONL events,
and maps the external conversation to a resumable harness session.

A repeated input ID is deduplicated within its conversation. After a restart,
an input that was active but lacks a proven terminal result becomes uncertain;
it is not silently retried.

Dispatch state is stored owner-only at `.hctl/dispatch.json`. For migration
compatibility, if that file is absent, hctl validates an existing owner-only
`.hctl/gateway.json`, installs the validated bytes atomically at the new path,
and removes the old regular file. When both paths exist, the dispatch path is
authoritative.
Dispatch state schema 4 retains compatibility with versions 1 through 3;
existing state without outcome reasons remains valid and upgrades on its next
write. A retained task deadline may add an optional bounded reason keyed only
to its corresponding uncertain outcome; it does not introduce another
lifecycle status or execution ledger.

The local stdin adapter and transport-neutral channel controller share the turn
dispatcher's typed submission and event seam. Built-in vendor adapters use the
controller's normalized inbound-message and semantic-outcome interface; this is
not a public plugin ABI or a rich component schema. The JSONL input adapter
remains the reference for durable state and event semantics. Other vendor
channels, generic webhooks, OAuth, proactive delivery, and public listener
management remain outside the MVP.

## Managed tool boundary

The MVP exposes one bounded, read-only `echo` tool, the optional anonymous
GitHub connection's three read-only tools, and conventionally authored
TypeScript, Python, and Go tools through one stdio MCP server in both harnesses.
Inputs and outputs are schema-validated. Audit output contains a safe request
identifier, tool name, and lifecycle outcome, never tool arguments or output.

One long-lived process per authored language serves inspection and calls for
the MCP session. Tool calls are serialized in the current MVP. A call that
exceeds its deadline terminates that language host and fails clearly; graceful
per-call cancellation and automatic host restart are not claimed.

The managed boundary is additive. It does not disable, authorize, observe, or
retry harness-native tools. Secret-bearing tools require the local secretless
operation broker selected by [ADR 0009](adr/0009-use-a-local-secretless-operation-broker.md)
before they ship. The broker resolves an opaque reference only at an authorized
managed invocation and consumes the value for a constrained upstream operation;
it declares no credential or authorization input fields and never returns the
value to a tool host, harness, MCP client, or model.
No backend, credential enrollment flow, connection syntax, or unused broker
code is scaffolded in the MVP.
Codex treats the generated managed server as required and delegates its tool
approval to hctl, avoiding a second harness approval prompt after hctl records
authorization where Codex user and administrator policy permits. This setting
does not affect native or unrelated MCP tools.

## Authored tool lifecycle

Tool source and native lockfiles join the validated source fingerprint. Apply
checks TypeScript with `deno check --frozen`, prepares Python with
`uv sync --locked`, and compiles a generated Go host with native Go module
tooling. Generic TypeScript and Python hosts, their local runtime environments,
and generated Go build output live under the workspace's disposable
`.hctl/cache/tools/`; no normalized tool manifest is written.
The cache records the exact Deno and `uv` executables used during apply so a
harness can start the managed server without inheriting the same shell `PATH`.

The generated MCP command identifies its harness. At startup hctl verifies the
matching workspace apply record, selected agent identity, and source
fingerprint before loading the cached hosts. Authors write typed functions and
do not implement MCP protocol code.

## Staged agent filesystems

Publish one Codex hctl harness image and one Claude hctl harness image. Each
contains the matching hctl release, one pinned native harness, and all supported
authored-tool build and execution inputs. Users may copy in an agent, run
`apply`, and ship that larger derived image directly. That is a supported
journey, not an accidental intermediate image.

For users who want a smaller image, `hctl stage` prepares one
complete runnable filesystem tree at canonical final paths. An ordinary
two-stage OCI build copies that tree onto the harness image's documented
compatible base. Hctl owns preparation and verification of the filesystem; it
does not construct OCI manifests or layers, contact registries, publish, sign,
deploy, or operate images.

```dockerfile
FROM ghcr.io/alee792/hctl/codex:VERSION AS build
COPY . /agent
RUN hctl stage /agent --harness codex --output /out

FROM DOCUMENTED_COMPATIBLE_BASE
COPY --from=build /out/opt/ /opt/
COPY --from=build --chown=65532:65532 /out/workspace/ /workspace/
COPY --from=build --chown=65532:65532 /out/home/hctl/ /home/hctl/
USER 65532:65532
ENTRYPOINT ["/opt/hctl/bin/agent-entrypoint"]
```

The staged tree contains hctl, the selected harness, immutable agent source,
generated workspace setup and apply record, an empty writable harness home,
an entrypoint, and an artifact manifest. It carries only the union of execution
requirements discovered from the agent's tools: Deno for TypeScript, Python
and uv for Python, and compiled Go hosts plus required shared libraries for Go.
Tool-free agents carry none of those runtimes. Build-only compilers, unused
runtimes, module and download caches, and temporary inspection output are
excluded.

The artifact manifest records the generator and harness versions, agent and
source identity, target OS, architecture and ABI, compatible base, required
runtimes, canonical paths, and hashes, modes, and intended ownership for staged
files. Generated configuration and executable receipts name final paths rather
than build-stage paths. It also records directory modes and intended ownership.
The compatible-base contract fixes required native facilities, UID/GID 65532,
writable `/workspace` and `/home/hctl`, and ABI; a glibc payload
is not portable to Alpine without a corresponding musl-compatible harness
image.

Staging requires a new output directory outside the selected source and
workspace. It re-reads every fingerprinted source, rejects symlinks and
collisions, prepares and protocol-inspects tools in a temporary sibling, strips
build-only Deno, Python, and Go state, verifies that preparation did not mutate
authored source, and publishes with one rename only after the manifest is
complete. Repeating the operation with identical source and pinned inputs
produces the same file contents and manifest. The entrypoint verifies the exact
runtime identity, harness setup, and source fingerprint before a turn and
refuses to run as an identity other than UID/GID 65532.

The Python interpreter must already be installed at the canonical
`/opt/hctl/runtimes/python` prefix by the pinned harness image. Staging rejects
an arbitrary system interpreter instead of copying a binary whose loader,
standard library, or virtual environment still binds it to its old prefix.

Credentials, native harness login state, user trust decisions, channel runtime
profiles, dispatch state, sessions, logs, registry credentials, signing
material, and deployment configuration remain outside the staged tree. The
selected harness continues to own model calls, native tools, approvals, and
sandbox behavior. Publishing a harness image also requires current permission
to redistribute that harness; ADR 0027 grants no such permission.

## Deferred direction: proposals

Scripts created ad hoc by the agent remain ordinary harness-native workspace
activity unless a human promotes them into `tools/` and reapplies the project.

Generated project instructions may encourage the harness to submit reusable
discoveries through a future managed proposal tool. Instructions can influence
this behavior but cannot enforce it or observe native filesystem writes.

A proposal is a local, inert record of a candidate improvement to one existing
instruction, skill, or managed-tool source file. It does not modify active
authored source, generated harness setup, or a running harness. Proposal files
belong to the producing workspace at `.hctl/proposals/ID/`, not to the agent
source that they name. `proposal.md` explains the suggestion and records its
target, selected source and run provenance, and the target's SHA-256 content
hash; `change.diff` is a bounded unified diff. `review.md` is added only after
a human accepts or rejects it. After publication, `proposal.md` and
`change.diff` are immutable evidence; `review.md` is the separate later human
decision record. There is no manifest or proposal registry.

A proposal can target `instructions.md`, a UTF-8 text file in an existing
skill, or an existing managed-tool source file. Binary skill resources are
outside this unified-diff flow. A proposal cannot add, remove, move, or rename
files, change a dependency file, or escape the agent source. A changed or
missing target is stale and must never be applied or rebased automatically.
The reviewer either manually makes a current change in the agent source and
reapplies it, or rejects the proposal. Both accepted and rejected records are
retained until a human removes them.

Proposals must not contain credentials, secrets, raw tool outputs, or
conversation transcripts. A future capture tool must tell callers this rule and
bound the content it accepts. It must not claim that it reliably detects or
removes secrets; owner-readable storage and human review do not make prohibited
content safe to record.

A future managed proposal tool may create this workspace-local record after
validating its bounded target, base content, and provenance. It must remain
additive: it cannot apply a diff, execute proposed code, reapply, delete a
proposal, or control native filesystem activity. Proposal capture, source
mutation, and review UX remain outside the MVP and are not scaffolded. See
[ADR 0008](adr/0008-keep-agent-proposals-workspace-local-and-inert.md).

## Failure and safety behavior

- Missing, stale, ambiguous, or edited harness setups fail closed.
- Input, output, queue, process lifetime, state size, and protocol lines are
  bounded.
- Durable state is owner-readable only and written atomically.
- Process failure is distinct from a completed or failed model turn.
- An uncertain external effect is never described as exactly-once or retried
  without a target idempotency contract.
- Diagnostics do not expose credentials, private prompts, or raw process
  output.
- A future secretless broker validates a reference, managed operation, target,
  and authorization on every call; uses private local IPC, a sensitive
  session-scoped authorization capability for one managed MCP server instance,
  and an upstream credential of its own; and returns/audits only bounded
  secret-free data. The capability is delivered only to hctl's managed
  MCP-server/broker pair, stays out of ordinary tool inputs, model-visible I/O,
  generated configuration, logs, and audit, and is rotated/removed with the
  managed MCP server process.
  Its typed operation schema declares no credential/authentication fields and
  rejects unknown fields; it cannot reliably detect a secret smuggled into an
  allowed string after the model has submitted it. It does not protect against
  native harness capabilities or any other process running as the same OS user.

## MVP acceptance

The MVP is complete when credential-free tests prove:

1. One authored project compiles deterministically for both harnesses.
2. Apply produces native, discoverable harness files and refuses conflicts.
3. Both generated harness setups expose the same managed MCP tool surface.
4. Both headless drivers start and resume sessions against fake harnesses.
5. Input arriving during an active turn is durably accepted and processed
   later in FIFO order.
6. Caller-provided input IDs are deduplicated.
7. Restart recovery marks unproven active work uncertain.
8. Managed audit output remains content-free.
9. A mixed TypeScript, Python, and Go project is prepared once per apply,
   exposed identically by both generated MCP configurations, and reuses one
   host process per language across calls.
10. One agent project can be applied outside its source directory; generated
    files and execution use the selected workspace while dependencies and tool
    definitions remain rooted in agent source.
11. Immediate subagents are generated in each harness's native format, inherit
    the parent setup without duplicated child tools or skills, and map optional
    `low`, `medium`, or `high` effort to the exact native field.
12. Agent Skills directories and their regular-file resources round-trip into
    both native project skill locations, including executable intent, while
    recognized unsupported vendor metadata remains intact and produces a
    path-, field-, and harness-specific warning.
13. Harness-specific regular files round-trip only into their selected native
    project directory, join stale-source detection, and use the same collision,
    ownership, modified-file, and cleanup protections as generated setup.
14. An authorized Discord Gateway message is durably dispatched for both
    harnesses, irrelevant output can resolve to `HCTL_NO_REPLY`, visible output
    is delivered through bounded replies, and the bot token is absent from
    source, generated files, state, logs, and child environments.
15. Nested Markdown schedules validate and fingerprint identically for both
    harnesses, and a one-shot trigger deduplicates stable occurrence IDs while
    opening a fresh native session for each accepted occurrence and discarding
    model text. Its independent turn deadline aborts a stalled native process,
    durably retains uncertainty for duplicate retries, and permits a later
    occurrence to open a fresh session.
16. An exact Discord write-access result promotes only that conversation into
    a validated branch-backed Git worktree, resumes the same Claude or Codex
    session under workspace-write policy, and continues the original request
    once without exposing internal control text.
17. Runtime-wide resident-session and active-turn limits keep accepted work
    durable under saturation, hibernate eligible idle capacity, and advance
    turns fairly across conversations without a model scheduler.
18. Concurrent guild and DM mutations use distinct worktrees and native
    sessions for both Claude and Codex, survive independent hibernation and
    restart, deliver out-of-order results only to their originating surfaces,
    and contain ordinary harness or worktree failures to one conversation.
19. Startup reconciliation preserves every worktree that is busy, uncertain,
    dirty, unmerged, or unverifiable and retires only exact clean merged
    assignments through restart-safe, idempotent cleanup.
20. A transport-neutral interactive request survives restart in its owning
    dispatch conversation, accepts one authorized normalized answer exactly
    once, parks without consuming harness or active-turn capacity, and preserves
    ambiguous delivery or continuation as uncertain without automatic replay.
21. A foreground UTC schedule clock admits only current, non-overlapping
    occurrences through one bounded durable task runtime, drains admitted work
    on shutdown, and never backfills missed minutes or emits model output.

## Explicit non-goals

- A model loop, context manager, or cross-harness chat UI
- Channels other than conversational Discord Gateway, generic webhooks, and
  proactive vendor delivery
- Claude Agent SDK or hosted OpenAI agent runtimes
- Background or distributed schedule clocks, workflows, independently
  configured nested subagents, or deployment orchestration
- Building OCI manifests or layers, publishing or signing images, deployment
  orchestration, or hosted image operation
- Governance claims over native harness tools
- Hosted secret managers and model-visible secret-bearing managed operations
- Automatic or unreviewed promotion of agent-authored improvements
