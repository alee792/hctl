ARG SOURCE_IMAGE
ARG BASE_IMAGE

FROM ${SOURCE_IMAGE} AS build
COPY --chown=65532:65532 agent/ /agent/
RUN hctl stage /agent --harness codex --command /opt/hctl/harness/bin/codex --output /out/agent >/dev/null

FROM ${BASE_IMAGE}
ENV HOME=/home/hctl \
    PATH=/opt/hctl/bin:/opt/hctl/harness/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN set -eu; \
    groupadd --gid 65532 hctl; \
    useradd --uid 65532 --gid 65532 --home-dir /home/hctl --shell /bin/sh --no-create-home --no-log-init hctl; \
    mkdir -p /home/hctl/.codex /workspace; \
    chown -R 65532:65532 /home/hctl /workspace
COPY --from=build /out/agent/opt/ /opt/
COPY --from=build --chown=65532:65532 /out/agent/workspace/ /workspace/
COPY --from=build --chown=65532:65532 /out/agent/home/hctl/ /home/hctl/
USER 65532:65532
WORKDIR /workspace
ENTRYPOINT ["/opt/hctl/bin/agent-entrypoint"]
