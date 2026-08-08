# Third-party dependency evidence

The official Discord adapter is a separately built Go module. Its reproducible
dependency graph is locked by `go.mod` and `go.sum`; it is not part of hctl's
root module graph.

The production artifact directly uses:

- `github.com/bwmarrin/discordgo` v0.29.0 — BSD-3-Clause;
- `github.com/gofrs/flock` v0.13.0 — BSD-3-Clause;
- `github.com/pelletier/go-toml/v2` v2.2.4 — MIT;
- `github.com/zalando/go-keyring` v0.2.6 — MIT;
- `golang.org/x/term` v0.36.0 — BSD-3-Clause; and
- the dependency-free `hctl/channeladapter` protocol module from the same hctl
  source revision.

Transitive module identities and checksums remain in `go.sum`. Distribution
must retain the upstream license notices required by those modules.
