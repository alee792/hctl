# ADR 0027: Stage agent filesystems for downstream OCI builds

- Status: accepted
- Implementation: `hctl stage` and unpushed Codex image acceptance are
  implemented; publication and the Claude image remain deferred

## Decision

Add an OCI-neutral staging boundary for deployable agents. Hctl will prepare a
complete runnable filesystem tree that an existing build system can copy onto
a documented compatible base image. Hctl will not construct OCI manifests or
layers, contact a registry, publish, sign, deploy, or operate the resulting
image.

Publish one hctl image for Codex and one for Claude. Each contains the matching
hctl release, one pinned native harness, and all supported authored-tool build
and execution inputs. A user may add and apply an agent in that image and ship
the resulting larger image directly. A staging command additionally
lets the same image act as a disposable build stage by carrying only the
selected agent's required execution closure into a clean compatible image.

The direct journey is valid:

```dockerfile
FROM ghcr.io/alee792/hctl/codex:VERSION
COPY . /agent
RUN /opt/hctl/bin/hctl apply /agent --workspace /workspace --harness codex
ENTRYPOINT ["/opt/hctl/bin/hctl", "run", "/agent", "--workspace", "/workspace", "--harness", "codex"]
```

The documented two-stage optimization is:

```dockerfile
FROM ghcr.io/alee792/hctl/codex:VERSION AS build
COPY . /agent
RUN hctl stage /agent --harness codex --output /out/agent

FROM DOCUMENTED_COMPATIBLE_BASE
COPY --from=build /out/agent/opt/ /opt/
COPY --from=build --chown=65532:65532 /out/agent/workspace/ /workspace/
COPY --from=build --chown=65532:65532 /out/agent/home/hctl/ /home/hctl/
USER 65532:65532
ENTRYPOINT ["/opt/hctl/bin/agent-entrypoint"]
```

The source image creates `/out` as a writable directory owned by UID/GID
65532. Staging runs as that identity and atomically creates a new child such as
`/out/agent`; the output directory itself must not already exist.

The Claude journeys change only the source image and harness argument. The
two-stage form is recommended when image size, build-tool exposure, or offline
startup matters; it is not required for correctness. `hctl stage` is the
intended command name, not an authored manifest or an hctl-owned image builder.
The Claude image and publication remain separate implementation work until
their acceptance contracts and release gates are completed.

### Staged layout

One staged tree contains these fixed runtime locations:

```text
/opt/hctl/artifact.json
/opt/hctl/bin/hctl
/opt/hctl/bin/agent-entrypoint
/opt/hctl/harness/...
/opt/hctl/runtimes/...
/opt/hctl/agents/AGENT_NAME/...
/workspace/...
/home/hctl/...
```

`AGENT_NAME` is the normalized authored project name. Keeping it in the staged
source path preserves the existing directory-derived identity rather than
renaming every staged project `agent`. Generated MCP configuration and runtime
receipts refer only to these final paths, never build-stage source or output
paths. The entrypoint runs the staged hctl, agent source, workspace, and
harness selection without a shell wrapper supplied by the downstream build.

The staged source and workspace remain separate as selected by ADR 0005.
Portable source is immutable image content. The workspace contains generated
harness setup, the apply record, and prepared tool execution artifacts, and is
the harness working directory. A deployment must preserve those staged files
and make the runtime-owned portions of the workspace writable; mounting an
empty volume over the whole workspace would hide required setup and is not a
supported composition.

`artifact.json` is generated bookkeeping, not author configuration. It records
its schema version, hctl generator version, selected harness and version,
agent name and identity, source fingerprint, target OS and architecture, libc
or equivalent ABI, compatible base identifier, required runtime set, final
paths, and the hashes, modes, and intended runtime ownership of every other
staged file plus the modes and intended ownership of staged directories.
Identical agent source and pinned image inputs must produce
identical staged file contents and metadata. Downstream image metadata,
timestamps, compression, and image digest remain the downstream builder's
responsibility.

### Runtime selection

Staging carries the union of execution requirements discovered from authored
tools:

- A tool-free agent carries no Deno, Python, uv, or Go runtime.
- A TypeScript agent carries the pinned Deno executable, generated host, and
  locked dependency cache needed for cached-only execution.
- A Python agent carries the pinned Python and uv execution closure, generated
  host, and locked prepared environment.
- A Go agent carries the compiled host and any required runtime shared-library
  closure, but not the Go compiler, module cache, or generated build module.
- A mixed-language agent carries only the union of those requirements.

Build-only compilers, unused language runtimes, download caches, package
manager caches, and temporary inspection output are discarded. Staging targets
the source image's own OS and architecture; cross-compilation is not promised.
Each published harness image declares one exact compatible final-base contract.
A glibc-built payload cannot be copied onto Alpine merely because both images
use Linux; a separate harness image built for a musl ABI would be required.

The compatible-base contract also names required shared libraries, certificate
and shell facilities, the non-root runtime UID and GID, and a writable harness
home. The staged entrypoint requires UID/GID 65532 and uses `/home/hctl`; the
workspace and that home must be copied with matching ownership. The downstream
base may provide additional native utilities, but it must
not silently run the staged entrypoint as root or omit facilities the pinned
harness requires.

### Runtime and credential boundary

The staged tree includes generated harness files, the apply record, prepared
tool hosts and execution dependencies, and a content-free artifact manifest.
It excludes all runtime-created or operator-owned state:

- model-provider credentials and native harness login state;
- user-level harness configuration and trust decisions;
- Discord tokens, Discord runtime profiles, and hctl user configuration;
- dispatch conversations, native harness sessions, logs, and audit output; and
- registry credentials, signing material, and deployment configuration.

Those values are injected, mounted, or established through the selected
harness and channel's documented runtime mechanisms. Staging must not contact
a model, validate live credentials, or silently create a native user trust
decision. Durable runtime state remains outside the immutable staged artifact
and must be placed on storage appropriate to the deployment. The staging
implementation must state which workspace and harness-home paths require that
storage before it can claim restart-safe operation.

Authored source is copied into the artifact and remains prohibited from
containing credentials. Hctl does not claim that staging can reliably detect a
secret hidden in an otherwise allowed authored string or binary resource.
"Excludes" means staging never reads credentials from environment variables,
native stores, user homes, or deployment inputs and never introduces them into
the artifact.

The selected native harness still owns model calls, native tools, approvals,
and sandbox behavior. Packaging the processes together does not expand hctl's
policy boundary or make native harness effects managed.

This decision does not itself grant permission to redistribute a native
harness. Publishing either image requires a current review of that harness's
license and distribution terms. If redistribution is not permitted, that
image remains unpublished until the vendor authorizes it or supplies an
equivalent distributable artifact; hctl must not replace the pinned harness
with an unverified runtime download merely to preserve the example journey.

## Context

ADR 0007 intentionally chose a single `darwin-arm64` release archive for the
first local installation and rejected speculative copying of workspace caches.
That remains the supported local installation journey. A concrete distribution
need now exists: authors want normal OCI build systems to consume an
hctl-prepared agent without carrying every compiler and unused runtime into
the final image.

Raw `.hctl/cache/` directories are not a sufficient artifact. Current apply
records and executable receipts bind source, workspace, hctl, Deno, and uv to
absolute paths, Python environments may encode their installation location,
and compiled Go hosts are platform-specific. Staging therefore re-prepares and
verifies the agent inside the target image, installs the result at canonical
final paths, and publishes a manifest for the complete closure. It does not
declare an arbitrary local cache relocatable.

The alternatives were to require every deployment to ship the large
all-runtimes image, publish a matrix of harness and language-combination images,
or make hctl itself an OCI builder. The first remains a supported convenience
but cannot produce a smaller closure, the second grows combinatorially, and the
third duplicates mature build, registry, and supply-chain systems. Optional
selective staging keeps hctl responsible for the compilation facts it already
knows while leaving image construction to existing tools.

## Consequence

ADR 0005's source/workspace separation now defines the two canonical staged
locations. ADR 0007 remains authoritative for first local installation, but
its deferral of relocatable packaging and agent images is superseded for this
bounded staging contract. There is still no `hctl package` command and no
permission to copy arbitrary local runtime caches between machines.

The staging implementation must separate physical build paths from embedded
runtime paths, produce and verify the artifact manifest, select only discovered
runtime closures, fail before publishing partial output, and prove a
credential-free launch from the documented compatible clean base. Harness
image publication is a separate, explicitly authorized operation.

Downstream users own their final base contents, extra native tools, image
metadata, SBOM and provenance generation, signing, registry publication,
runtime credentials, writable and durable mounts, network policy, and
deployment platform. Hctl cannot claim reproducibility or safety for changes
made after the staged tree leaves its boundary.
