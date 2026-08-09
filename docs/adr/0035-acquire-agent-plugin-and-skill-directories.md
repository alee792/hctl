# ADR 0035: Acquire Agent Plugin and Skill directories into portable source

- Status: accepted
- Date: 2026-08-09
- Extends: [ADR 0003](0003-use-convention-based-authoring.md),
  [ADR 0019](0019-import-vendored-agent-plugin-skills.md), and
  [ADR 0029](0029-bound-authored-projects-with-aggregate-budgets.md)

## Plain-English summary

An author may explicitly add a complete Agent Plugin or Agent Skill from a
local directory, an HTTPS Git repository, or a digest-pinned HTTPS archive.
Hctl validates the selected component, asks the author to trust its complete
code-bearing tree, copies that tree into the conventional `plugins/` or
`skills/` location, and records exact provenance and content identity in one
committed `hctl-dependencies.json` lock. Apply reads only those local authored
bytes; it never fetches, updates, or resolves a moving dependency reference.

## Decision

Acquisition is a consumer convenience around the existing filesystem contract,
not a package format or second component inventory. A successfully acquired
Plugin is still one unmodified publisher-authored Agent Plugins v1 directory
beneath `plugins/`. A successfully acquired Skill is still one complete Agent
Skills directory beneath `skills/`. Existing project loading, validation,
precedence, source fingerprinting, native generation, and runtime ownership
remain authoritative after acquisition.

Every dependency command receives one exact positional `AGENT` root whose
required `instructions.md` proves the destination project. Commands never
search ancestors, infer an `agents/` directory, select a workspace or harness,
or implicitly apply. The two component command families use the same forms:

```text
hctl plugin add AGENT --from-dir DIR [--subdir DIR] [--yes]
hctl plugin add AGENT --from-git HTTPS_URL --ref REF [--subdir DIR] [--yes]
hctl plugin add AGENT --from-archive HTTPS_URL --sha256 SHA256 [--subdir DIR] [--yes]
hctl plugin status AGENT [NAME]
hctl plugin update AGENT NAME [SOURCE SELECTOR] [--yes]
hctl plugin remove AGENT NAME [--force] [--yes]

hctl skill add AGENT --from-dir DIR [--subdir DIR] [--yes]
hctl skill add AGENT --from-git HTTPS_URL --ref REF [--subdir DIR] [--yes]
hctl skill add AGENT --from-archive HTTPS_URL --sha256 SHA256 [--subdir DIR] [--yes]
hctl skill status AGENT [NAME]
hctl skill update AGENT NAME [SOURCE SELECTOR] [--yes]
hctl skill remove AGENT NAME [--force] [--yes]
```

`SOURCE SELECTOR` is either absent or one complete `--from-dir`, `--from-git`
plus `--ref`, or `--from-archive` plus `--sha256` form accepted by `add`.
When absent, update reuses the recorded selector. Supplying a selector replaces
the recorded source, but the component kind, validated name, and destination
must remain unchanged. Changing identity requires remove followed by add. There
is no background or apply-time update.

### Component root, identity, and destination

Every selector resolves one exact component root. `--subdir` is optional only
when that root is the selected source root; it is otherwise required and is a
normalized slash-separated relative path. Hctl never recursively searches a
repository or archive for `plugin.json` or `SKILL.md`.

Source URL strings contain at most 2,048 UTF-8 bytes. Git refs contain 1-256
UTF-8 bytes and cannot begin with `-`. Local source operands contain at most
4,096 UTF-8 bytes. A subdirectory or materialized relative path contains at
most 1,024 UTF-8 bytes, 64 nonempty components, and 255 UTF-8 bytes per
component. NUL, backslash, empty, `.`, and (inside a selected tree) `..`
components fail. The local source locator stored relative to the agent root is
the sole exception that may contain leading `..` components. Names retain
their existing 64-character Plugin or Skill bounds. Git commit ids are exactly
40 or 64 lowercase hexadecimal characters; archive and tree SHA-256 values are
exactly 64 lowercase hexadecimal characters.

A Plugin root contains one regular UTF-8 `plugin.json`. The existing complete
Agent Plugins v1 validator supplies the Plugin name. Acquisition publishes it
at `plugins/<plugin-name>/`; the manifest bytes and every accepted package file
are preserved. The publisher's name becomes the deterministic acquired storage
name even though manually copied Plugins may continue using another local
storage name.

A Skill root contains one regular UTF-8 `SKILL.md`. Its existing validator
supplies the Skill name and requires that name to equal the selected root
directory's basename. Acquisition publishes it at `skills/<skill-name>/` and
preserves the complete accepted resource tree.

Add fails without replacement when the destination already exists as any file
type. It also preflights the component names that ordinary project loading
would accept: an acquired Plugin cannot introduce a supported Skill or MCP
server collision, and an acquired root Skill cannot collide with an existing
root or accepted Plugin Skill. Acquisition does not weaken ordinary warning
and first-wins behavior for manually copied Plugin components.

The command `NAME` namespace is always the immediate conventional storage
directory. For an acquired Plugin that storage name equals the manifest name;
for a manual Plugin it may differ, so status selects `plugins/NAME/` and reports
the validated publisher manifest name separately. Two manual storage
directories may therefore report the same publisher name without making
selection ambiguous; existing component collision behavior still governs what
they contribute. A Skill's required name/path equality means its storage and
publisher names are already identical.

### Source forms and immutable resolution

The first delivery accepts exactly three source forms:

1. `--from-dir` reads an existing local real directory. Hctl copies its current
   complete bytes; the resulting tree identity is the immutable evidence. The
   committed lock records the source base as a normalized slash path relative
   to the exact agent root plus the optional subdirectory. This source locator
   may escape the agent root because it is read only during explicit
   acquisition; the selected component is still copied into the root. A fresh
   clone does not need that path to apply, and update may replace an unavailable
   local selector explicitly.
2. `--from-git` accepts an HTTPS repository URL with a nonempty host and no
   user information, query, or fragment, plus one nonempty bounded ref. Hctl
   resolves that ref only during add or update, peels it to one exact commit,
   records both the requested ref and full commit id, and materializes only the
   selected tree at that commit. It does not perform a working-tree checkout,
   run hooks or filters, initialize submodules, or fetch Git LFS content.
   Symlink entries and gitlinks in the selected tree fail.
3. `--from-archive` accepts an HTTPS URL with the same URL restrictions, one
   lowercase SHA-256 supplied by the author, and a ZIP or gzip-compressed TAR
   payload. Hctl follows no redirect. It verifies the bounded response against
   the digest before extraction and then selects the exact optional
   subdirectory. Links, devices, duplicate normalized paths, and paths that are
   absolute or escape their root fail.

Git may use the operator's existing HTTPS credential helper for a private
repository. Hctl accepts no credential flag, credential-bearing URL, token
environment reference, or interactive Git prompt. The Git child receives no
model input, materializes through object data rather than a checkout, and its
raw standard output, standard error, helper output, and remote response are not
retained or repeated in diagnostics. Redirects are disabled. This is
operator-owned source access, not an hctl credential store or secretless
broker.

A moving Git ref is not a lock identity. Only the resolved commit plus the
materialized tree identity pins the acquired dependency. An archive URL is not
a lock identity either; its required SHA-256 plus tree identity pins it. Local
source is pinned solely by the copied tree identity. A fresh clone needs no
original source or network access to apply the committed acquired bytes.

### Committed provenance lock

The optional root regular UTF-8 file `hctl-dependencies.json` is a closed,
version-1 client-owned provenance lock of at most 1 MiB. It is not required to
author a project, does not register components, and contains only dependencies
acquired by hctl. Manual `plugins/` and `skills/` directories remain valid
without an entry.

Its exact JSON shape is:

```json
{
  "schema_version": 1,
  "dependencies": [
    {
      "kind": "plugin",
      "name": "review-pack",
      "destination": "plugins/review-pack",
      "source": {
        "type": "git",
        "url": "https://example.com/agent-components.git",
        "ref": "v1.2.3",
        "commit": "0123456789abcdef0123456789abcdef01234567",
        "subdirectory": "plugins/review-pack"
      },
      "marker_sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
      "tree_sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
      "file_count": 4,
      "byte_count": 8192
    }
  ]
}
```

The root contains exactly integer `schema_version` and array `dependencies`.
Every entry contains exactly string `kind`, `name`, `destination`, object
`source`, string `marker_sha256`, string `tree_sha256`, nonnegative integer
`file_count`, and nonnegative integer `byte_count`. `kind` is `plugin` or
`skill`. Name and destination must equal the component-derived conventional
values.

`source` is one closed object selected by string `type`:

- `local` contains exactly `type: "local"`, string `path`, and optional string
  `subdirectory`;
- `git` contains exactly `type: "git"`, string `url`, string `ref`, string
  `commit`, and optional string `subdirectory`; and
- `archive` contains exactly `type: "archive"`, string `url`, string `sha256`,
  string `format` equal to `zip` or `tar.gz`, and optional string
  `subdirectory`.

An absent subdirectory means the source root and is the only canonical
representation; empty or `.` values fail. Entries sort by `kind`, then `name`,
as raw UTF-8 bytes. The writer emits the fields in the order shown, uses two
ASCII spaces per indentation level, does not HTML-escape strings, and appends
one LF. The parser accepts insignificant JSON whitespace and any object-key
order but rejects duplicate keys, unknown fields, noncanonical dependency
array order, and multiple JSON values. Exact accepted bytes, rather than a
reserialized form, join the project fingerprint.

Unknown versions or kinds, duplicate names or destinations, unsafe values, and
identities or counts that do not match the installed source fail before
workspace mutation. Lock entries are provenance for paths that already exist
by convention; project loading never discovers a component from the lock
alone.

Tree identity uses SHA-256 over one exact byte stream. Write ASCII
`hctl-dependency-tree-v1`, one NUL byte, and the total entry count as an
unsigned 32-bit big-endian integer. Sort entries by their normalized relative
UTF-8 path bytes without Unicode normalization. For each entry write, in order:
the path byte length as unsigned 32-bit big-endian, the exact path bytes, one
type byte (`0x00` directory or `0x01` regular file), the normalized mode as
unsigned 32-bit big-endian, the content length as unsigned 64-bit big-endian,
and the exact content bytes. Directory mode is octal 0755 with zero content
length. A regular file is octal 0755 when any publisher execute bit is set and
0644 otherwise. Empty directories therefore remain visible, while owner,
group, timestamps, archive order, and host-specific permission bits do not
change identity. The root itself is not an entry.

The externally produced root `skills-lock.json` in this repository remains an
opaque ordinary file. Hctl does not read, adopt, rewrite, merge, or delete its
schema. A Skill already placed by that or another tool makes the conventional
destination exist, so hctl add refuses to create a second owner. If another
tool later changes an hctl-acquired tree, the hctl lock reports ordinary drift.

### Trust, bounds, and publication

Remote acquisition is code installation. Local acquisition receives the same
warning because a Skill may contain scripts and a Plugin may contain executable
MCP resources. Before mutation, an interactive command displays a bounded
summary containing kind, validated name, sanitized source, requested and
resolved immutable identities when applicable, destination, tree hash, file
and byte counts, executable-file count, and supported Plugin component counts.
The complete summary is at most 64 KiB. It lists at most 128 executable paths
in lexical order and then one count-only truncation line; every displayed path
uses the path bounds above. It then requires an affirmative terminal
confirmation. `--yes` is the exact noninteractive equivalent. A noninteractive
command without `--yes` fails. Neither form grants integration-package trust
or native MCP approval.

Materialization accepts only directories and regular files with valid bounded
relative UTF-8 paths. It rejects symlinks, hard links, junctions, reparse
points, sockets, devices, named pipes, Git metadata, case-folding path
collisions, and special permission bits. Publisher file bytes are preserved;
portable modes retain only executable intent. Marker files still use their
existing UTF-8 and semantic validators, while other resources may be binary.

Case-collision detection preserves each exact path but derives one comparison
key by applying Unicode 15.0.0 canonical caseless matching to every complete
slash-separated path: normalize to NFD, apply full default case folding, then
normalize to NFC. Two distinct source paths with the same resulting UTF-8 key
fail before materialization. Slash is a separator rather than a foldable path
character. Tree identity continues to hash exact, unnormalized path bytes.

One selected dependency tree contains at most 8,192 entries, 8,192 regular
files, 64 MiB per regular file, and 256 MiB total. A remote archive is at most
128 MiB before expansion. Acquired trees in one project contain at most 16,384
entries and 512 MiB total. The existing narrower Skill, Plugin, Plugin Skill,
MCP, generated-file, and aggregate project limits still apply; passing the
acquisition envelope never bypasses component validation. These constants are
not author configuration.

One exclusive operation lock keyed by the canonical real agent root serializes
add, update, remove, status, apply, stage, and every other hctl project load
that interprets acquired provenance. The lock lives in owner-only OS-user
state, not authored source. On entry, every reader first recovers any bounded
transaction journal. A mutator holds the lock through prospective validation
and publication. A reader holds it until all dependency lock and tree bytes
needed by its immutable project snapshot or staged copy have been captured;
later workspace generation may proceed after release from that snapshot.

Hctl stages a complete candidate on the destination filesystem, validates the
prospective project and next lock before publication, and uses atomic renames
plus the recovery journal so interruption resolves to either the complete old
tree/lock or complete new tree/lock before any reader interprets them. The
journal is a closed owner-written regular file of at most 64 KiB and names only
the one operation, destination, old/new tree identities, and bounded temporary
paths; it contains no source URL, Git output, file content, or credential.
Temporary trees never become authored components. Validation, download,
cancellation, collision, and confirmation failure leave active source and
provenance unchanged.

### Status, apply, update, and removal

Status performs no network request and executes no acquired file. With no name
it lists both acquired and manually present components lexically; with a name
it selects one exact conventional storage directory as defined above. Plugin
output includes storage name and validated manifest name separately. Acquired
state is `clean`, `drifted`, or `missing`; manual state is `untracked`. It
reports bounded source and identity metadata but never file contents, remote
output, credentials, or helper output. Malformed lock or selected source
returns nonzero.

When the lock exists, ordinary project loading verifies every acquired
destination and tree identity before consuming Plugins or Skills. Drift,
missing source, an unsafe path, or malformed provenance fails apply and stage
before workspace mutation. Untracked manual components continue through their
existing loaders. Apply never repairs drift, contacts a source, resolves a Git
ref, downloads an archive, or changes the lock.

Update requires a clean current tree. It resolves the stored or explicitly
replaced source, shows and confirms the same trust summary, requires the same
kind/name/destination, and atomically replaces both complete tree and lock only
after full validation. An unchanged identity is a successful no-op. Update
never merges local edits or runs publisher code.

Remove deletes one exact acquired destination and its lock entry. It requires
confirmation or `--yes`. A clean tree is removable normally. Drift, missing
source, or an unsafe destination refuses ordinary removal; `--force --yes` is
the explicit destructive override for a drifted or missing tracked tree, but
still refuses an unsafe path and never follows a link. Remove does not delete
Plugin data, integration packages, external source, another dependency, or an
empty conventional parent directory, and does not reapply workspaces.

## Evidence contract

Credential-free fixtures acquire the same complete Plugin or Skill from a
local directory, a direct HTTPS Git repository at a moving ref resolved to an
exact commit, and direct digest-pinned ZIP and TAR.GZ archives. A Plugin fixture
contains one Skill, one executable relative stdio MCP server, binary data, and
an empty directory. A Skill fixture contains `SKILL.md`, nested text, binary,
and executable resources. Exact marker and resource bytes must match across
all sources and apply identically to Claude and Codex in an independently
selected workspace.

Tests cover exact-root commands, destination derivation, required subdirectory,
Git ref pinning, archive digest and format, no redirect, no Git checkout side
effects, helper-output suppression, trust confirmation, noninteractive failure,
path and file-type rejection, count and byte bounds, deterministic tree hashes,
closed and canonical lock parsing, `skills-lock.json` noninterference,
component collisions, concurrent operations, interruption recovery, offline
status/apply, drift and missing states, update rollback, destructive removal,
source fingerprinting, and manual untracked compatibility. No test needs a
credential or executes acquired code during acquisition or status.

## Consequences

- Agent source remains sufficient to apply on another machine; acquisition is
  explicit source mutation, not an apply-time package manager.
- Plugin and Skill formats stay publisher-owned. Hctl adds provenance around
  complete directories without inserting metadata into them.
- Git branches and tags are convenient tracking inputs but never retained as
  the immutable result.
- Private HTTPS Git can reuse operator-owned Git authentication without adding
  a portable credential field or hctl secret store.
- Acquired directories are intentionally immutable while tracked. Authors may
  continue maintaining ordinary manual directories when they want editable
  local source instead of acquisition provenance.
- Marketplace search, background updates, dependency scripts, signatures,
  registries, provider installers, submodules, Git LFS, and credential-bearing
  archives remain outside this decision.

## Sources

- [Agent Plugins specification v1.0.0](https://agent-plugins.org/specification)
- [Agent Skills specification](https://agentskills.io/specification)
- [Product specification](../product-spec.md)
