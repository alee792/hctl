ARG SOURCE_IMAGE
FROM ${SOURCE_IMAGE}

COPY --chown=65532:65532 examples/minimal/ /agent/
RUN hctl apply /agent --workspace /workspace --harness codex --command /opt/hctl/harness/bin/codex >/dev/null

ENTRYPOINT ["/opt/hctl/bin/hctl", "run", "/agent", "--workspace", "/workspace", "--harness", "codex", "--command", "/opt/hctl/harness/bin/codex"]
