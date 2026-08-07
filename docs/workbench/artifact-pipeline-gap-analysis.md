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
| Binary construction | One shared script now builds deterministic `darwin-arm64` and `linux-amd64` binaries; CI rebuilds, compares, smoke-tests, checksums, and retains them. The exact-tag release script reuses it for the supported Darwin archive. | The binary still needs explicit version identity before it can be a release or image input. |
| Staged filesystem | `hctl stage` produces a deterministic, selective runtime tree and manifest. | It has unit and fake-process coverage but no real Linux image acceptance. |
| Image definition | ADR 0027 fixes the harness-image and staged paths. | There is no Dockerfile, pinned base digest, OCI metadata contract, or non-root image configuration. |
| Harness inputs | Tests exercise Codex 0.144.1 and Claude Code 2.1.221 behavior. | Production sources, checksums, update policy, and canonical self-contained layouts are not selected. |
| Authored-tool runtimes | Staging knows the canonical Deno, Python/uv, and compiled Go closures. | The source image does not yet install pinned Linux runtimes at those paths. Python must have the exact `/opt/hctl/runtimes/python` base prefix. |
| Compatible final base | The manifest reports OS, architecture, and detected libc. | The clean base digest, required packages/shared libraries, certificates, shell, `id`, and UID/GID 65532 are not fixed or tested. |
| Credential-free acceptance | Unit tests use fake harnesses and do not call a model. | CI needs real harness `--version`, apply, stage, entrypoint, managed MCP, runtime-selection, ownership, and credential-absence checks. |
| Supply chain | The local release archive gets a SHA-256 manifest. | Harness and runtime inputs need digest verification; published images need immutable digests, SBOMs, provenance, and retained version metadata. |
| Publication | No release or package workflow exists. | GHCR naming, tag policy, permissions, protected release environment, and failure/rollback behavior remain to be implemented. |

Container acceptance cannot depend on every developer having a matching Linux
daemon. Hosted Linux CI is therefore the required acceptance environment, not
merely a mirror of an optional local container proof.

## Decisions required before image construction

1. Select one exact glibc Linux base by digest and use it for both harness
   source images unless a demonstrated harness constraint requires otherwise.
   The compatible downstream Dockerfile should begin from that same digest and
   install the documented generic OS facilities; no separately published
   runtime image is needed.
2. Add one checked-in input manifest containing the base digest plus hctl,
   harness, Deno, Python, uv, and Go versions and upstream checksums. Dockerfiles,
   labels, CI cache keys, and acceptance should read that single source.
3. Give the hctl binary an exact build version derived from the release tag.
   `project.GeneratorVersion` is currently a source constant, and the CLI has no
   release-version command, so OCI labels alone cannot prove that the binary
   matches the tag.
4. Choose self-contained harness layouts beneath `/opt/hctl/harness`. Codex has
   an Apache-2.0 standalone Linux artifact. Claude Code is proprietary, its npm
   package declares `SEE LICENSE IN README.md`, and its repository says use is
   subject to Anthropic's Commercial Terms. A Claude image must not be
   published without explicit redistribution authority.
5. Decide whether an unpushed, ephemeral Claude image may be built in CI while
   publication is blocked. If that permission is also unclear, CI should test
   the shared image contract with a fake Claude harness until authorization is
   obtained.

## Implementation slices

1. **CI and binary foundation.** Run the existing repository checks on Linux,
   build deterministic `linux-amd64` and `darwin-arm64` binaries through one
   script, smoke-test the Linux binary, and retain checksums as short-lived CI
   artifacts. This slice does not publish a release.
2. **Pinned image inputs and version identity.** Add the input manifest, exact
   hctl version injection and inspection, compatible-base contract, and
   checksum-verifying fetch helpers.
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
