# Official GitHub MCP server package

This curated package pins GitHub's official `github/github-mcp-server` release
`v1.8.0` for `darwin-arm64` and `linux-amd64`. Install it from this checkout in
one explicit operator-trust step:

```sh
./integrations/github-mcp-server/install.sh
```

Set `HCTL_EXECUTABLE=/absolute/path/to/hctl` only when `hctl` is not on `PATH`.
The installer selects the host platform, downloads the reviewed official
archive, verifies its URL redirect origin, size, SHA-256, exact archive layout,
and extracted executable identity, then supplies a local package source to
`hctl integration install --trust operator`. It does not install Go, copy a
binary onto `PATH`, or expose hctl's cache layout.

`sources.tsv` is the upstream delivery lock. `integration.json` is the exact
package manifest consumed by hctl. GitHub's stable release URLs redirect to
signed URLs on `release-assets.githubusercontent.com`; the materializer follows
that one HTTPS redirect and then verifies the complete pinned identity. This
happens outside hctl. The generic package store intentionally continues to
reject redirects, and later apply, verification, and staging remain offline.

The package declares only the required ambient variable name
`GITHUB_PERSONAL_ACCESS_TOKEN`. It never resolves or stores a value. The native
harness and official server may access the ambient value, as documented in
[ADR 0031](../../docs/adr/0031-use-the-official-github-server-as-native-unmanaged-mcp.md).
Follow the [native GitHub MCP journey](../../docs/github-native-mcp.md) for
offline apply, runtime injection, native trust, package lifecycle, and
troubleshooting.

For an image or air-gapped distribution, materialize a reviewed platform
package without installing it:

```sh
./scripts/materialize-integration-package.sh \
  --package ./integrations/github-mcp-server \
  --platform linux-amd64 \
  --output ./github-mcp-server-package
```

Distribute that directory through an operator-controlled channel and install
it with the same generic hctl command. Removing the installed package retires
its metadata from future closures but deliberately retains shared immutable
cache bytes.
