# ADR 0009: Use a local secretless operation broker

- Status: accepted

## Decision

Before hctl ships a secret-bearing model-invocable managed tool or connection, add one
hctl-owned, local **secretless operation broker** boundary. The broker accepts
an authorized operation and an opaque credential reference, obtains the value
from a later-selected backend only when the operation runs, uses the value on
the managed side of the boundary, and returns only a schema-validated,
secret-free result. Its contract declares no credential or authorization input
fields and never returns a secret to the tool host, harness, MCP client, or
model.

This is an execution boundary, not a vault and not a new authoring surface.
HCTL-006 selects no storage backend, command, connection-file syntax, or
credential enrollment flow. A later, separately approved implementation must
select and validate a backend behind this contract. Native platform stores are
plausible local backends (macOS Keychain, the freedesktop.org Secret Service,
and Windows Credential Locker); they solve protected storage and retrieval,
not secretless execution. A managed service or identity-derived short-lived
credential may later be appropriate, but is also behind the same boundary.

### Minimum future contract

- **Reference and lookup.** A reference is an opaque, bounded identifier, not
  a value, URI with embedded credentials, command, environment-variable name,
  or filesystem path. Hctl validates it against the selected managed operation
  and resolves it only inside the broker at invocation time. The agent source
  may name no value; whether and how an author eventually selects a reference
  is deferred rather than invented in this spike.
- **Scope and authorization.** The broker binds a reference to a concrete
  managed tool/connection, operation, target/service identity, and allowed
  action. It independently authorizes every invocation after hctl's normal
  managed-tool approval. A harness- or client-presented bearer token is never
  forwarded upstream; an upstream credential is acquired for the upstream
  resource and least privilege. Human consent and step-up authorization,
  where a backend requires them, occur outside model-visible protocol data.
- **Ownership and IPC.** Hctl starts and owns one broker for the lifetime of
  its managed MCP server process, gives it a private local IPC endpoint, and
  passes a sensitive, session-scoped local authorization capability distinct
  from the upstream credential, scoped to that one managed MCP server
  instance. This capability is not an ordinary tool input and must stay out of
  model-visible I/O, generated harness configuration, logs, and audit. Hctl
  delivers it only to its managed MCP server/broker pair; it is not inherited
  by the harness or an authored language host. Endpoint permissions and peer
  identity checks limit the endpoint and capability to the hctl-owned managed
  host; no TCP listener,
  shared workspace path, inherited secret environment, or shell lookup is part
  of the contract. The broker is not placed in generated harness configuration.
- **Authenticated operation.** The broker, not the authored tool or language
  host, performs the authenticated request or proxies one supported protocol
  to an allowlisted upstream. Its operation-specific input schema is bounded,
  typed, and declares no credential or authorization fields; the managed MCP
  boundary rejects unknown fields fail closed. Callers must not submit secrets.
  Structural validation cannot reliably recognize a secret smuggled into an
  allowed string: once a model submits it, it has already crossed model-visible
  MCP and may be exposed to the native harness. Bounds and types reduce this
  risk but do not provide secret detection or redaction. It keeps the upstream
  credential in memory only as long as needed, does not put it in command
  arguments, environment variables, files, stdout/stderr, or normal IPC
  payloads, and does not permit arbitrary destination, headers, method, query,
  or body construction around it.
- **Results, errors, and audit.** Broker replies contain a bounded outcome,
  safe request ID, and declared non-sensitive result fields only. They never
  contain credential values, authorization headers, raw upstream bodies, or
  backend error text. Diagnostics and durable audit contain only reference
  pseudonym/identifier, operation, target label, authorization decision,
  lifecycle, and classified failure; they never contain model input/output,
  arguments, response bodies, or secret material.
- **Lifecycle and failure.** The broker and its session-scoped local
  authorization capability are created after apply validation for one managed
  MCP server process, rotated with that process, and removed when it exits.
  Private IPC material is removed on normal shutdown and created with
  owner-only permissions. Missing, locked,
  ambiguous, expired, unauthorized, unavailable, malformed, or rotation-race
  references fail closed with a stable secret-free category. No automatic
  fallback, retry of a non-idempotent authenticated effect, caching across the
  declared lifetime, or value persistence is promised. Backend prompts must
  be explicitly mediated by a local human-facing flow or fail closed; they are
  never relayed through the model.

### Evaluated patterns

| Pattern | Storage benefit | Why it does not meet this boundary alone |
| --- | --- | --- |
| Environment or file injection | Can obtain a value from a store | The injected child can read, log, copy, or return it. Vault Agent and `op run` deliberately provision values into a child environment; file templates have the same exposure. This violates the required harness/host environment and model-I/O boundary. |
| Credential-helper stdout | Lets a client use an OS-backed helper | Docker's `get` helper writes the username and secret to stdout. A tool host receiving that output becomes a secret-bearing process, so this may only be a backend adapter private to the broker. |
| Direct SDK/CLI retrieval in authored tool | Can use a native store directly | It gives agent-authored, model-invocable code the value and couples source to a backend. It cannot enforce target/action scope or safe response handling. |
| Local proxy/broker | Keeps value at a dedicated managed boundary | Selected smallest future boundary. It permits local platform stores or remote identity systems while binding each authorized operation to an allowlisted target and returning only safe results. |

Secretless execution is distinct from secure storage: a secure store protects a
value at rest and releases it to an authorized client; a secretless broker
instead consumes it for a constrained operation without disclosing it to the
agent session. Secretless's sidecar illustration follows this latter shape:
the application receives a connection and the sidecar holds the credential.

### Enforceable boundary and explicit limits

Hctl can enforce this contract for code and requests that cross its managed
tool boundary: it can avoid emitting values into authored or generated files,
launch the broker without secret-bearing environment, restrict its IPC,
validate operations and references, reject unknown input fields, keep its
instance capability out of its own replies/logs/audit, and fail closed. It
cannot reliably detect or redact a secret submitted in an allowed model-visible
string field; caller guidance and structural schemas reduce that exposure but
cannot undo it.

It cannot protect against a malicious authored tool, native harness tool,
plugin, shell command, or unrelated process running as the same OS user; those
actors remain outside hctl's additive boundary and may access resources the OS
permits. It does not provide an OS sandbox, govern native harness effects,
guarantee that a backend or upstream never logs, or make arbitrary protocols
safe through redaction. A later implementation must state each backend's trust
and prompting model and preserve these limits.

## Context

The product already promises that secret-bearing managed tools require a
broker and that secrets stay out of authored source, generated harness files,
the harness environment, model-visible I/O, diagnostics, and audit. The first
supported runtime is local and the managed MCP server is stdio, so a boundary
per managed MCP server process is smaller than operating a general vault,
network proxy, or cross-platform credential configuration now.

The cited storage APIs expose values to their clients: Secret Service models
items, sessions, and prompts; Credential Locker retrieves a password through
`PasswordCredential`; and Docker helpers return a secret on stdout. They are
useful backend candidates, not the selected execution architecture. MCP's
authorization specification requires audience validation and forbids token
passthrough; its security guidance also identifies proxy confused-deputy risk.
Those requirements support binding the broker to its own upstream identity and
constrained operations rather than forwarding model-side authority.

## Consequences

This ADR does not govern credentials consumed exclusively inside a built-in
channel transport and never exposed as a model-invocable operation; that trusted
boundary is specified separately by ADR 0012. When a concrete secret-bearing
managed tool is prioritized,
its proposal must choose the backend and local authorization UX, specify the
operation-specific allowlist and result schema, test only fake credentials and
fake backends, and prove that generated files, environments, protocol traffic,
diagnostics, and audit records remain value-free.

## Research sources

- [Apple Keychain Services](https://developer.apple.com/documentation/security/keychain-services)
- [freedesktop.org Secret Service API](https://specifications.freedesktop.org/secret-service/latest/)
- [Windows Credential Locker](https://learn.microsoft.com/en-us/windows/apps/develop/security/credential-locker)
- [Docker credential stores and helper protocol](https://docs.docker.com/reference/cli/docker/login/)
- [Secretless local sidecar pattern](https://docs.secretless.io/Latest/en/Content/Get%20Started/using-conjur-ent.htm)
- [Vault Agent process-supervisor environment injection](https://developer.hashicorp.com/vault/docs/agent-and-proxy/agent/process-supervisor)
- [1Password CLI secret injection and direct retrieval](https://developer.1password.com/docs/cli/secrets-scripts)
- [MCP authorization specification (2025-11-25)](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization)
- [MCP security best practices](https://modelcontextprotocol.io/docs/tutorials/security/security_best_practices)
