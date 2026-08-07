# Artifact pipeline gap analysis

- Updated: 2026-08-07
- Scope: issue #48, published Codex and Claude hctl harness images

## Recommended build graph

CI is the authoritative place to prove release inputs and Linux container
behavior. Local scripts remain the implementation of each build step so a
developer and CI execute the same commands.

Every pull request and push to `main` should follow this non-publishing graph:

```text
source revision
  -> repository checks
  -> deterministic target binaries
  -> unpushed Codex and Claude harness images
  -> direct-image and staged-image acceptance
  -> short-lived CI evidence
```

An exact `vX.Y.Z` tag should rerun that graph from clean tagged source, then
publish only the already-proven artifact kinds:

```text
exact tag
  -> release archives and checksums
  -> permitted harness images in GHCR
  -> immutable digests, SBOMs, and provenance
```

Pull-request workflows must not receive package-write, attestation, or
deployment authority. Publication belongs in a separate tag-triggered workflow
with minimal permissions.

## Current capability and gaps

| Area | Current evidence | Gap before image publication |
| --- | --- | --- |
| Repository checks | `scripts/check.sh` runs formatting, tests, vet, lint, and vulnerability checks with repository-pinned tools; CI now invokes that same script on Linux. | Container acceptance and tag publication must remain downstream of this gate. |
| Binary construction | One shared script builds deterministic `darwin-arm64` and `linux-amd64` binaries with a required exact version; CI rebuilds, compares, inspects `hctl --version`, checksums, and retains them. The exact-tag release script injects its clean tag version into the supported Darwin archive. | Image builds must consume the checked Linux binary rather than rebuild it through an unrelated path. |
| Staged filesystem | `hctl stage` produces a deterministic, selective runtime tree and manifest. | It has unit and fake-process coverage but no real Linux image acceptance. |
| Image definition | ADR 0027 fixes the harness-image and staged paths. `images/inputs.json` pins the Linux/amd64 Ubuntu 24.04 platform manifest and the UID, GID, home, shell, identity, certificate, and ABI contract. | There is no Dockerfile, OCI metadata, or executable acceptance of that contract. |
| Harness inputs | One validated manifest pins official standalone Codex 0.144.1 and Claude Code 2.1.221 artifacts by URL, byte size, and SHA-256. A bounded fetch helper rejects unapproved sources, oversized content, and checksum drift. | Canonical extraction layouts remain to be proven. Claude distribution stays blocked pending explicit permission. |
| Authored-tool runtimes | Staging knows the canonical Deno, Python/uv, and compiled Go closures. | The source image does not yet install pinned Linux runtimes at those paths. Python must have the exact `/opt/hctl/runtimes/python` base prefix. |
| Compatible final base | The staged manifest reports OS, architecture, and detected libc; the image-input manifest fixes the clean base platform digest and generic runtime facilities. | A clean-base image must prove all harness and selected-runtime shared-library requirements and UID/GID 65532 behavior. |
| Credential-free acceptance | Unit tests use fake harnesses and do not call a model. | CI needs real harness `--version`, apply, stage, entrypoint, managed MCP, runtime-selection, ownership, and credential-absence checks. |
| Supply chain | The local release archive gets a SHA-256 manifest. Harness and runtime inputs are checksum-pinned and fetched through one verifier. | Published images need immutable digests, SBOMs, provenance, and retained version metadata. |
| Publication | No release or package workflow exists. | GHCR naming, tag policy, permissions, protected release environment, and failure/rollback behavior remain to be implemented. |

Container acceptance cannot depend on every developer having a matching Linux
daemon. Hosted Linux CI is therefore the required acceptance environment, not
merely a mirror of an optional local container proof.

## Resolved construction inputs

1. Both source images use the exact Linux/amd64 manifest of Ubuntu 24.04. This
   is an hctl-owned thin base choice, not a layer on a vendor development
   environment. The compatible downstream image begins from the same platform
   digest; no separately published runtime image is needed.
2. `images/inputs.json` is the machine-readable source for that base and for
   Deno, Python, uv, Go, Codex, and Claude versions, URLs, sizes, checksums,
   formats, licenses, and publication gates. CI validates it without fetching
   roughly 500 MB of inputs on every pull request. Image construction uses the
   checksum-verifying fetch command.
3. The artifact builder requires an exact semantic version, `hctl --version`
   exposes it, generated state carries it through `project.GeneratorVersion`,
   and exact-tag archives inject the tag version.
4. OpenAI publishes `codex-universal` as a broad reference environment, but it
   does not contain the Codex CLI and brings unrelated runtimes. Anthropic
   publishes a devcontainer feature and explicitly describes its reference
   container as an example rather than a maintained base. The thin hctl image
   therefore consumes each vendor's pinned standalone artifact directly.
5. Codex's Apache-2.0 artifact is open for the planned image slice. Claude Code
   publication remains `blocked-pending-permission` under the project gate.
   Whether an unpushed Claude image may be built in CI remains an explicit
   authorization question; downloadability alone does not answer it.

## Implementation slices

1. **CI and binary foundation (complete).** Run the existing repository checks on Linux,
   build deterministic `linux-amd64` and `darwin-arm64` binaries through one
   script, smoke-test the Linux binary, and retain checksums as short-lived CI
   artifacts. This slice does not publish a release.
2. **Pinned image inputs and version identity (partially complete).** The input
   manifest, exact hctl version injection and inspection, generic base
   facilities, and checksum-verifying fetch helper are implemented. The Codex
   vertical slice must measure and add the required shared-library closure
   before the compatible-base contract is complete.
3. **Codex vertical slice.** Build an unpushed Codex image and prove direct and
   two-stage tool-free journeys on `linux/amd64` without credentials or model
   calls.
4. **Runtime matrix.** Add Deno-only, Python-only, Go-only, and mixed fixtures;
   prove both successful execution and absence of unused runtimes/build inputs.
5. **Claude vertical slice.** Build and test the equivalent image only within
   the authorization established for the proprietary harness.
6. **Tag publication.** Publish the existing archive and authorized GHCR images
   from exact clean tags, retain immutable digests, and generate SBOM and
   provenance evidence. Keep signing, deployment, and downstream agent images
   outside hctl's boundary.
