# ADR 0007: Use a release archive for first installation

- Status: accepted

## Decision

The first supported platform is `darwin-arm64`, the one platform exercised by
the existing clean-install proof. The exact `vX.Y.Z` Git tag is the
authoritative release version. It produces
`hctl_X.Y.Z_darwin_arm64.tar.gz`, containing one `hctl` executable at the
archive root, and `hctl_X.Y.Z_SHA256SUMS`, containing that archive's SHA-256
checksum. A user verifies the exact release, extracts the executable to a
stable location on `PATH`, and applies portable agent source to a workspace.

Generated MCP configuration records the resolved absolute path to that
executable. Moving the binary requires `hctl apply` again. Replacing the binary
at the same path leaves the reference valid, but the supported upgrade journey
reruns `apply` to refresh any runtime cache. No `hctl package` command is
introduced in this slice.

## Context

The clean-install proof builds an isolated binary, copies an agent project and
workspace outside the checkout, and proves that the generated MCP server starts
from the installed executable. The runtime reads agent source directly and
keeps generated host files, native dependency environments, executable
receipts, and compiled Go hosts in a fingerprinted workspace-local
`.hctl/cache/tools/` directory. They are disposable and must be rebuilt by
`apply` on another machine.

`go install` would require users to supply a Go toolchain and resolve source or
a module version, rather than consuming the checked released artifact. It does
not improve the first cross-platform journey. A relocatable package would need
to define how it bundles source, lockfiles, native runtimes, and caches, but
the current source-plus-apply contract has no demonstrated need for that extra
surface.

## Consequence

HCTL-002 must accept only an exact `vX.Y.Z` Git tag as its version source,
produce the named `darwin-arm64` archive and checksum manifest without
publishing them, document the installation commands, and make the
credential-free proof extract and use the archive. It must not introduce an
agent-image or deployment system, copy `.hctl/cache/` between machines, add a
`hctl package` command, or claim another platform. A future relocatable package
or platform matrix needs a separate, evidence-backed decision.
