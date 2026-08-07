ARG BASE_IMAGE
FROM ${BASE_IMAGE}

ARG HCTL_VERSION
ARG HCTL_SHA256
ARG SOURCE_REVISION
ARG BASE_REFERENCE
ARG BASE_DIGEST
ARG INPUTS_SHA256
ARG CODEX_VERSION
ARG CODEX_SHA256
ARG DENO_VERSION
ARG DENO_SHA256
ARG PYTHON_VERSION
ARG PYTHON_SHA256
ARG UV_VERSION
ARG UV_SHA256
ARG GO_VERSION
ARG GO_SHA256

LABEL org.opencontainers.image.title="hctl Codex harness" \
      org.opencontainers.image.description="Pinned Codex harness and hctl authored-tool preparation inputs" \
      org.opencontainers.image.source="https://github.com/alee792/hctl" \
      org.opencontainers.image.revision="${SOURCE_REVISION}" \
      org.opencontainers.image.version="${HCTL_VERSION}" \
      org.opencontainers.image.base.name="${BASE_REFERENCE}" \
      org.opencontainers.image.base.digest="${BASE_DIGEST}" \
      io.hctl.binary.sha256="${HCTL_SHA256}" \
      io.hctl.inputs.sha256="${INPUTS_SHA256}" \
      io.hctl.base.reference="${BASE_REFERENCE}" \
      io.hctl.base.digest="${BASE_DIGEST}" \
      io.hctl.harness.name="codex" \
      io.hctl.harness.version="${CODEX_VERSION}" \
      io.hctl.harness.sha256="${CODEX_SHA256}" \
      io.hctl.runtime.deno.version="${DENO_VERSION}" \
      io.hctl.runtime.deno.sha256="${DENO_SHA256}" \
      io.hctl.runtime.python.version="${PYTHON_VERSION}" \
      io.hctl.runtime.python.sha256="${PYTHON_SHA256}" \
      io.hctl.runtime.uv.version="${UV_VERSION}" \
      io.hctl.runtime.uv.sha256="${UV_SHA256}" \
      io.hctl.runtime.go.version="${GO_VERSION}" \
      io.hctl.runtime.go.sha256="${GO_SHA256}" \
      io.hctl.compatible-base="linux-amd64-glibc"

ENV HOME=/home/hctl \
    PATH=/opt/hctl/bin:/opt/hctl/harness/bin:/opt/hctl/runtimes/deno/bin:/opt/hctl/runtimes/python/bin:/opt/hctl/runtimes/uv/bin:/opt/hctl/runtimes/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt \
    DENO_NO_UPDATE_CHECK=1 \
    GOTOOLCHAIN=local \
    UV_PYTHON=/opt/hctl/runtimes/python/bin/python3 \
    UV_PYTHON_DOWNLOADS=never

COPY --chown=0:0 rootfs/ /

RUN set -eu; \
    groupadd --gid 65532 hctl; \
    useradd --uid 65532 --gid 65532 --home-dir /home/hctl --shell /bin/sh --no-create-home --no-log-init hctl; \
    mkdir -p /home/hctl/.codex /workspace /out; \
    chown -R 65532:65532 /home/hctl /workspace /out; \
    test -x /opt/hctl/bin/hctl; \
    test -x /opt/hctl/harness/bin/codex; \
    test -s /etc/ssl/certs/ca-certificates.crt

USER 65532:65532
WORKDIR /workspace
ENTRYPOINT ["/opt/hctl/bin/hctl"]
CMD ["--help"]
