# ADR 0030: Use metadata-first process-isolated integration packages

- Status: accepted
- Extends: [ADR 0001](0001-use-native-harnesses.md),
  [ADR 0020](0020-map-plugin-mcp-through-native-harness-configuration.md), and
  [ADR 0027](0027-stage-agent-filesystems-for-downstream-oci-builds.md)

## Plain-English summary

Third-party integrations that are not portable authored source will be exact,
operator-installed packages containing external executables. Hctl validates a
small manifest without executing the package, then gives each recognized
capability to its own narrow consumer. The first capability is a native stdio
MCP server. Its process belongs to Claude Code or Codex after hctl writes native
configuration; it does not become an hctl-managed tool.

## Decision

Define one metadata-first package envelope with schema version 1. A package has
an exact semantic version, stable id, human-readable name and description,
license, source and revision provenance, and a half-open hctl compatibility
range (`minimum <= version < before`). The SHA-256 of the exact bounded
manifest bytes is its immutable manifest identity. Reformatting or changing any
field therefore creates a different identity even when the package id and
version are unchanged.

The manifest declares one or more platform artifacts. Each artifact has a
stable id, an exact supported OS and architecture, a bounded `binary`,
`tar.gz`, or `zip` format, size and lowercase SHA-256, and the expected
package-relative executable path, size, and SHA-256 after preparation. Its
source is a closed union:

- `package` names one normalized package-relative payload; or
- `https` names one HTTPS URL without embedded credentials, query, or fragment.

Both forms remain pinned by size and checksum. Metadata validation reads no
artifact. It does not resolve a symlink, fetch a URL, inspect an archive, load a
library, or start a process. Installation and content verification are the
separate work of #76.

Decoded package values retain their validated manifest privately and return
defensive metadata copies. Capability selection therefore always derives from
the exact bytes named by the retained manifest SHA-256; mutating a caller's
copy cannot create selection evidence with a stale identity.

Capabilities are closed, tagged schemas with a stable capability id, type, and
integer version. Schema 1 recognizes only `native-mcp` version 1. An unknown
type or version rejects the manifest with a typed unsupported-capability error;
it never invokes package code to discover behavior. Later capability schemas,
including `channel-adapter`, may share the envelope without adding fields to
`native-mcp` or introducing one generic runtime interface.

Package installation state is operator-owned and separate from the manifest.
Its closed schema version 1 records the package id and version, exact manifest
SHA-256, explicit `operator` trust, package-level enabled flag, verified
artifact and executable SHA-256 identities, and the exact id/type/version of
every declared capability. Validation binds all fields back to the immutable
manifest. It contains no executable path, credential material, or runtime
value. #76 persists that state and implements its commands; it does not invent
the state contract. Portable source may request a known package capability,
but it cannot choose a source or installed version, install or enable a
package, grant machine trust, or carry a credential.

### Native MCP capability version 1

A `native-mcp` declaration contains:

- one stable native server name with collision policy fixed to `reject`;
- the exact artifact ids forming its selective runtime and staging closure;
- one package-relative executable that must match every referenced artifact's
  executable identity;
- bounded literal arguments, package-relative working directory, and
  non-secret environment defaults;
- required ambient environment-variable names with safe descriptions, never
  values or references; and
- one or both native harness targets, each with `optional` or `required`
  startup and `native-project` trust ownership.

Literal arguments and environment defaults cannot contain environment
placeholders. A required ambient name cannot also have a default. The
manifest's capability/artifact references plus exact manifest, artifact, and
executable hashes provide the content-free evidence needed by apply and staging
to select one installed executable. The contract does not resolve or retain an
ambient value.

`native-project` means the selected native harness owns its project trust and
approval journey. Installing an exact package is the operator's authorization
for the selected external executable to run with its documented process
authority, but a package manifest cannot silently modify user, administrator,
or enterprise trust. Capability-specific delivery issues decide the exact
native configuration and unattended journey.

Once configured, Claude Code or Codex owns native MCP process startup,
lifecycle, authentication, approvals, discovery, calls, effects,
cancellation, results, and errors. Hctl does not proxy, supervise, authorize,
filter, confirm, retry, observe, or audit that traffic. Required ambient names
are diagnostic metadata, not a credential channel; resolved values must not
enter package state, generated files, staged filesystems, or retained evidence.

The resolved ambient value is nevertheless available to the native
harness-launched server and may also be visible to the harness,
model-accessible shell or execution tools, and other inherited native
processes. `native-mcp` does not claim to hide it. Required-environment
descriptions use a closed 1-512 character prose alphabet: ASCII letters,
spaces, commas, periods, semicolons, parentheses, apostrophes, and hyphens,
beginning with a letter. Variable markers, underscores, URI/reference
punctuation, equals signs, and token-like machine syntax are therefore invalid.
No text grammar can reliably recognize an arbitrary secret disguised as prose;
package authors remain prohibited from placing values or references there.

### Dependency direction

Core package lookup and capability consumers depend on these validated data
contracts. A consumer asks for one versioned capability and receives exact
immutable selection evidence. Vendor packages depend inward on the manifest
schema or their later capability protocol and run as separate executables.
They cannot contribute Go interfaces, import themselves into hctl, register an
in-process lifecycle, or make core switch on a vendor name.

The common envelope owns only identity, provenance, compatibility, artifacts,
capability tags, enablement, and selective closure. MCP configuration, channel
transport, credentials, providers, and other runtime behavior remain separate
capability domains.

## Context

Vendored Agent Plugins under authored `plugins/` are portable project source.
Their existing skills and native MCP declarations remain useful, but their
download, installation, and update model was deliberately outside ADRs 0019
and 0020. Machine-installed integration executables need different trust,
provenance, reuse, and staging ownership and must not be confused with that
authored-source model.

The official GitHub MCP server and Discord SDK currently illustrate the
dependency problem. Importing each vendor implementation into hctl's root Go
module couples releases and grows the trusted in-process dependency graph. A
universal plugin runtime would replace that coupling with an unbounded dynamic
authority surface. A metadata envelope plus narrow process contracts preserves
ordinary binary performance and lets operators add exact integrations without
rebuilding hctl.

## Consequences

- The credentialless native-MCP fixture and official `github-mcp-server`
  executable metadata validate and select through the same vendor-neutral
  code path.
- A future `channel-adapter` fixture is recognized as an unsupported tagged
  capability and rejected without reading or executing its artifact.
- Root hctl dependencies remain independent of package SDKs.
- #76 may implement local and pinned-HTTPS installation, shared cache, offline
  apply, enablement, and selective staging without learning MCP runtime
  semantics.
- Existing vendored Agent Plugins and their native MCP generation remain
  unchanged.
- Registry search, git/npm/Go installers, package scripts, automatic updates,
  signatures, remote HTTP MCP, credentials and OAuth, arbitrary hooks,
  in-process plugins, and channel runtime details remain deferred.
