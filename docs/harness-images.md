# Harness images

Hctl's harness images are ordinary OCI build inputs. Each image contains one
native harness, hctl, and the pinned Deno, Python/uv, and Go inputs that `apply`
or `stage` may need. They do not contain an agent, provider credentials, login
state, or conversation state.

The Codex image definition is built and exercised without publishing on every
pull request. An exact `vX.Y.Z` hctl tag reruns that proof and publishes
`ghcr.io/alee792/hctl/codex:vX.Y.Z`. No moving `latest`, major, or minor tag is
published. The Claude image remains separate work and must not be published
without permission under Anthropic's terms.

## Direct image

Shipping the all-runtimes image is supported. Pin an exact hctl release tag,
copy the agent as the image's non-root user, and apply it during the build:

```dockerfile
ARG HCTL_VERSION
FROM ghcr.io/alee792/hctl/codex:${HCTL_VERSION}

COPY --chown=65532:65532 . /agent
RUN hctl apply /agent --workspace /workspace --harness codex \
    --command /opt/hctl/harness/bin/codex

ENTRYPOINT ["/opt/hctl/bin/hctl", "run", "/agent", "--workspace", "/workspace", "--harness", "codex", "--command", "/opt/hctl/harness/bin/codex"]
```

This is the shortest journey and retains the build toolchains. Use the
two-stage form only when the smaller execution closure matters.

## Selective two-stage image

The clean final base is the same pinned Linux/amd64 Ubuntu platform manifest as
the source image. The generic CA bundle is copied from the pinned build image;
the raw Ubuntu layer does not supply one. The final image creates the required
numeric identity and carries only the files selected by `hctl stage`. The
source image provides `/out` as a non-root writable parent; the stage output
itself, `/out/agent`, must not exist before the command runs:

```dockerfile
ARG HCTL_VERSION
ARG UBUNTU_AMD64_DIGEST=sha256:019e8eb29a85e74d64925745884f2ec79aa27e3feab36353d24656f4d6b89467

FROM ghcr.io/alee792/hctl/codex:${HCTL_VERSION} AS build
COPY --chown=65532:65532 . /agent
RUN hctl stage /agent --harness codex \
    --command /opt/hctl/harness/bin/codex --output /out/agent

FROM docker.io/library/ubuntu:24.04@${UBUNTU_AMD64_DIGEST}
ENV HOME=/home/hctl \
    PATH=/opt/hctl/bin:/opt/hctl/harness/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN groupadd --gid 65532 hctl \
    && useradd --uid 65532 --gid 65532 --home-dir /home/hctl \
       --shell /bin/sh --no-create-home --no-log-init hctl \
    && mkdir -p /home/hctl/.codex /workspace \
    && chown -R 65532:65532 /home/hctl /workspace
COPY --from=build /out/agent/opt/ /opt/
COPY --from=build --chown=65532:65532 /out/agent/workspace/ /workspace/
COPY --from=build --chown=65532:65532 /out/agent/home/hctl/ /home/hctl/
USER 65532:65532
WORKDIR /workspace
ENTRYPOINT ["/opt/hctl/bin/agent-entrypoint"]
```

Do not substitute Alpine: the published input and staged payload target
Linux/amd64 with glibc. Keep `/workspace` and `/home/hctl` writable. Add any
extra native utilities the agent expects in the downstream Dockerfile; they are
not part of the selective hctl payload.

## Runtime authentication

Image builds and staging are credential-free. Authenticate only at runtime on
a trusted system. For API-key automation, OpenAI recommends piping
`OPENAI_API_KEY` to `codex login --with-api-key`; Codex then caches credentials
under `$CODEX_HOME` or `~/.codex`. Treat that cache like a password. For
example, initialize a private named volume in a short-lived process:

```sh
docker volume create my-agent-codex-home
printenv OPENAI_API_KEY | docker run --rm -i \
  --mount type=volume,src=my-agent-codex-home,dst=/home/hctl/.codex \
  --entrypoint /opt/hctl/harness/bin/codex my-agent:VERSION \
  login --with-api-key

docker run --rm \
  --mount type=volume,src=my-agent-codex-home,dst=/home/hctl/.codex \
  my-agent:VERSION
```

An existing `~/.codex/auth.json` may instead be mounted or copied through the
deployment platform's secret mechanism. Never add it to the Dockerfile, build
context, staged tree, or registry layer. OpenAI's current
[Codex authentication guidance](https://learn.chatgpt.com/docs/auth.md)
documents API-key login, headless login, cache locations, and credential-store
options.

An agent whose generic `connections/github.md` selects
`github-mcp-server`/`github` separately needs runtime-only
`GITHUB_PERSONAL_ACCESS_TOKEN` injection. Pass the variable when the container
starts; never place it in an image `ARG`, `ENV`, build secret, source tree, or
staged filesystem. The harness/model may read it and hctl does not govern its
GitHub effects. Follow the [native GitHub MCP journey](github-native-mcp.md).
The Linux/amd64 image check builds both GitHub-bearing direct/staged images and
a GitHub-free counterpart with a conspicuous runtime-only fake marker; it never
starts the official server or uses a live credential.

An agent with `channels/discord.md` separately requires the exact installed
`hctl-discord` package before direct `apply` or `stage`. Direct images retain
the operator-installed shared package store. Selective staging copies only the
adapter's verified current-platform artifact plus one non-secret descriptor
bound to the agent id, source fingerprint, package manifest, capability, and
executable hash. The generated staged entrypoint selects only that adjacent
descriptor; a normal direct hctl invocation ignores an arbitrary descriptor
environment path. Agents without the Discord channel stage no adapter artifact
or descriptor.

Inject `HCTL_DISCORD_TOKEN` only when the container starts. Never place it in
an image `ARG`, `ENV`, build secret, source tree, package metadata, or staged
filesystem. Hctl passes the opaque value only to the exact adapter and scrubs
both the value and its internal staged-descriptor locator from harness, MCP,
tool-host, and unrelated child environments. Mount adapter profile state,
channel durable state, the workspace, and harness home on storage appropriate
to the deployment. Process separation is dependency and ownership isolation,
not an OS sandbox or protection from a malicious same-user process.

## Release identity and verification

The release workflow publishes the already-checked Codex source image, not a
second independently constructed image. Its GitHub release contains:

- `hctl_X.Y.Z_darwin_arm64.tar.gz`;
- `hctl_X.Y.Z_SHA256SUMS`;
- `hctl_X.Y.Z_codex_linux_amd64_DIGEST`; and
- `hctl_X.Y.Z_codex_linux_amd64.spdx.json`.

Use the digest file when an immutable deployment input matters:

```sh
image=$(cat hctl_X.Y.Z_codex_linux_amd64_DIGEST)
docker pull "$image"
```

The exact image digest receives both GitHub build-provenance and SPDX SBOM
attestations. With a current GitHub CLI and an authenticated GHCR session:

```sh
gh attestation verify \
  oci://ghcr.io/alee792/hctl/codex:vX.Y.Z \
  --repo alee792/hctl
```

The tag workflow has a read-only build job and a downstream `release`
environment with release, package, and attestation authority. Repository
owners must protect that environment, restrict `v*` tag creation with a
repository ruleset, and make the linked GHCR package public before treating the
image as generally available. The workflow refuses to replace an existing
exact image tag. If publication stops after the image push but before the
GitHub release is created, do not move or recreate the Git tag; inspect the
pushed digest and remove the incomplete package version before a controlled
rerun.
