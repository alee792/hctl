# ADR 0011: Start GitHub connections anonymously

- Status: superseded
- Superseded by: [ADR 0031](0031-use-the-official-github-server-as-native-unmanaged-mcp.md)

## Plain-English summary

The first connection proves the authoring and managed-runtime journey without
also inventing credential storage, OAuth, an OpenAPI engine, or an MCP proxy.
An author adds a plain description at `connections/github.md`; hctl then offers
three fixed public GitHub read operations through its existing MCP server. It
cannot access private repositories or write anything.

## Decision

Follow Eve's `connections/`, path-derived identity, model-facing description,
and `<connection>__<tool>` conventions for one concrete GitHub slice. Support
only `connections/github.md` and expose `github__get-repository`,
`github__list-issues`, and `github__get-issue` identically to Claude Code and
Codex.

Use Go's standard HTTP client for fixed anonymous GET requests to GitHub's REST
API. Reject redirects, bound time and response bytes, validate exact tool
inputs, select bounded output fields, classify failures without upstream body
text, and never retry. Discovery and apply perform no network request.

Do not add a generic connection interface, TypeScript connection definitions,
OpenAPI loading, remote MCP proxying, credentials, writes, approval UX, or the
broker selected by [ADR 0009](0009-use-a-local-secretless-operation-broker.md).

## Consequence

This connection works only for public data and is subject to GitHub's anonymous
rate limits. Its source joins the normal project fingerprint, while invocations
use the same content-free managed-tool audit as existing tools. Authenticated or
private GitHub access is separate work that must implement ADR 0009 before it
ships.

## Context

Eve's current connection contract derives identity from files under
`connections/`, keeps connection credentials out of model context, and
qualifies tools by connection name. Eve can supply a general hosted OpenAPI and
authentication runtime; hctl currently extends native local harnesses and does
not own an equivalent runtime. The bounded anonymous slice preserves the useful
authoring precedent without claiming that broader machinery.

- [Eve connections overview](https://github.com/vercel/eve/blob/84c3dfc1ff91e075444eee7c6d8e2ef55b2aaebe/docs/connections/overview.mdx)
- [GitHub REST API versioning](https://docs.github.com/en/rest/about-the-rest-api/api-versions)
- [GitHub REST rate limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api)
