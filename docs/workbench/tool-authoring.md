# Tool authoring workbench

- Status: implemented MVP plus open follow-up questions; see ADR 0004
- Scope: convention-based TypeScript, Python, and Go tools exposed through MCP

This document preserves current intent, implementation evidence, and open
questions so chat history is not the source of truth.

## Settled requirements

1. Authors work with ordinary directories and files. There is no authored hctl
   manifest, registry, or second list of tools.
2. A source file placed under `tools/` declares a tool through a small,
   language-native function contract. Its path supplies its default name.
3. TypeScript, Python, and Go are first-class tool-authoring languages. Authors
   should not need to write MCP protocol code in any of them.
4. hctl validates and adapts authored tools into MCP when the project is
   applied or packaged. Claude Code and Codex consume the resulting MCP tools.
5. Tool processes are long-lived for a session. hctl must not start a new
   language runtime for every tool call.
6. Inputs and outputs cross a common, schema-validated boundary even though
   each language uses its idiomatic type system and validation library.
7. Language-native dependency metadata and lockfiles are allowed. They manage
   code dependencies; they do not register tools with hctl.
8. Public language uses concrete terms such as `tool`, `skill`, and `channel`.
   `Capability` is not the author-facing name for a tool.
9. Ad hoc scripts created by a running harness remain native workspace
   activity. They become hctl tools only when a human adds them to the authored
   `tools/` directory and reapplies the project.
10. Authored tools are trusted project code for the local MVP. hctl provides
    process and protocol safety but does not claim malicious-code containment.
11. `apply` prepares locked dependencies by invoking native Deno, Python, and
    Go tooling. hctl does not define another dependency manifest or installer.
12. Tool definitions and dependency files are loaded from portable agent
    source. Tool processes run in the independently selected workspace, and
    their disposable runtime environments live under that workspace's
    `.hctl/cache/tools/`.

The implementation now uses `tool`, `generated harness files`, `harness setup`,
and `apply record` for these concrete concepts. `Capability` remains appropriate
only where it genuinely represents more than tools; `projection` is retained
only in migration code that recognizes an older apply-record location.

## Authoring model

A mixed-language project should be able to look approximately like this:

```text
my-agent/
  instructions.md
  skills/
    research.md
  tools/
    get_weather.ts
    lookup_policy.py
    hash_text/
      tool.go
```

Language-native dependency files and lockfiles sit at the project root. The Go
directory shown is the current convention: one package directory per tool with
a required `tool.go`. File discovery replaces hctl registration. Removing a
tool file removes the tool on the next apply.

Each language contract must yield the same runtime facts:

- tool name, derived from the relative path unless a future convention says
  otherwise;
- human-readable description;
- bounded input schema;
- bounded output schema;
- callable implementation; and
- execution context carrying a safe request identity. The current deadline
  boundary terminates the language host rather than cancelling one call
  gracefully.

Language-specific metadata should live beside the function in code, not in a
separate project manifest.

## Precedent

Eve's TypeScript model is the primary filesystem precedent. A file such as
`agent/tools/get_weather.ts` default-exports `defineTool({ description,
inputSchema, execute })`; the filename supplies the tool's identity. The author
writes a typed function, not an MCP server.

Roster already proved the analogous Python path. Each `tools/*.py` module
exports one `ToolDefinition` named `tool`, backed by an exactly typed async
handler with Pydantic input and output models. Its filesystem loader imports,
validates, and registers the definition without invoking the handler.

These precedents support one product model with language-specific adapters;
they do not require the three source APIs to have identical syntax.

## Runtime shape

```text
TypeScript tool functions --\
Python tool functions -------> hctl-owned language hosts -> hctl MCP boundary -> harness
Go tool functions -----------/
```

An hctl language host discovers and loads the functions for one language,
translates their contracts into MCP tool descriptions, validates calls and
results, and dispatches to the correct function. The hosts remain alive for
the harness session. The Go control plane supervises them and exposes one
coherent tool surface to the harness.

MCP is therefore the interoperability boundary, not the authoring API. An
author may still provide a complete external MCP server as a connection, but
that is a different path from writing a local tool function.

## Current language shapes

These structural contracts are implemented for the experimental MVP. A future
helper package may reduce repetition without changing filesystem discovery.

TypeScript default-exports an object using Zod schemas:

```ts
export default {
  description: "Return weather for a city.",
  inputSchema: z.object({ city: z.string().min(1) }).strict(),
  outputSchema: z.object({ condition: z.string() }).strict(),
  async execute({ city }) {
    return { condition: await lookup(city) };
  },
};
```

Python exports a description, Pydantic models, and a sync or async function:

```py
description = "Return weather for a city."

class Input(BaseModel):
    city: str

class Output(BaseModel):
    condition: str

async def execute(args: Input, context: dict[str, object]) -> Output:
    ...
```

Go exports ordinary types and a function that generated registration glue
links into the cached host:

```go
const Description = "Return weather for a city."

type Input struct {
    City string `json:"city" jsonschema:"minLength=1"`
}

type Output struct {
    Condition string `json:"condition"`
}

func Execute(ctx context.Context, input Input) (Output, error) {
    ...
}
```

Go host-only dependencies generate and validate JSON Schema; authored Go
packages do not import hctl.

## Apply and package behavior

### Generate only what the target requires

The default is to read authored source directly and build the tool catalog in
memory. hctl should not compile every project into a second declarative runtime
format.

| Output | Target behavior | Why it exists |
| --- | --- | --- |
| Native harness files | Generated by `apply` | Claude Code and Codex require their own instruction, skill, and MCP configuration files. |
| Apply record | Generated by `apply` | Records the source fingerprint and hashes of hctl-owned files so stale or hand-edited harness setups fail safely. |
| TypeScript tool wrapper | Not generated per tool | A generic TypeScript host loads the exported definitions directly. |
| Python tool wrapper | Not generated per tool | A generic Python host imports the authored definitions directly. |
| Go tool host | Compiled and cached by source fingerprint | Go cannot dynamically import authored source, so hctl must generate minimal build glue and compile it. |
| Normalized tool manifest | Not persisted | The language hosts report their tool schemas at inspection and startup; hctl combines them in memory. |
| Dependency environment/cache | Owned by native Deno, Python, or Go tooling | `apply` prepares and verifies the locked environment rather than inventing another package manager. |

Generated build glue, compiled Go hosts, and extracted host support files belong
in an hctl cache, not among authored project files. They must be safe to delete
and reproduce. A future `package` command may intentionally collect relocatable
runtime artifacts; `apply` should not package by accident.

The MVP now writes only `.hctl/apply/<harness>.json`. Apply migrates an intact
legacy `.hctl/projections/<harness>.json` record and removes its duplicated
`.hctl/manifests/<harness>.json` file. This keeps backward compatibility out of
the current authoring model.

### Literal lifecycle

The intended lifecycle is:

1. Discover visible source files under `tools/` by convention.
2. Select the language adapter from the file extension.
3. Prepare locked dependencies with native tooling and compile a cached Go host
   only when Go tools are present.
4. Start each required adapter in inspection mode. It loads module definitions
   and reports schemas without invoking a tool function.
5. Validate schemas, duplicate names, and the source fingerprint across all
   languages.
6. Generate the native harness files and their apply record.
7. Configure the harness to start
   `hctl mcp serve AGENT --harness TARGET` when the session needs its tool
   catalog.
8. Verify the source fingerprint, start the required long-lived language hosts,
   combine their catalogs in memory, and dispatch calls until the session ends.

Inspection necessarily imports or evaluates module-level TypeScript and Python
code even though it does not call tool functions. Static parsing cannot safely
recover arbitrary runtime schemas. Inspection must therefore run in a bounded
subprocess. This is a reliability boundary for trusted project code, not an OS
sandbox or malicious-code-containment claim.

`apply` necessarily prepares the local tool runtime because the generated
harness setup must be immediately usable. A future `package` command may
produce a relocatable artifact from the same conventions and adapters.

hctl keeps generated ownership, fingerprint state, and disposable tool-host
cache output under `.hctl/`. That state is implementation output, never an
authored tool inventory.

## Remaining questions

1. Should a helper package wrap the structural TypeScript, Python, or Go
   contracts after real author feedback?
2. How should richer local imports and nested supporting files affect the
   source fingerprint without capturing an unrelated application tree?
3. Does one host per language provide sufficient crash isolation, or is an
   optional stronger isolation mode needed later?
4. What graceful cancellation, restart, concurrency, and log-routing behavior
   is worth adding beyond the current bounded serial process contract?
5. Which artifacts must a future `package` command rebuild for
   portability?

## First spike result

The executable proof in `spikes/polyglot-tools/` now runs through production
apply, generated Claude and Codex MCP configurations, and production hosts.

| Concern | Evidence from the spike |
| --- | --- |
| TypeScript | A direct `tools/*.ts` default export using Zod can report JSON Schema and validate both sides of an async call. `deno check --frozen` verifies the host and tool without executing the handler. |
| Python | A direct `tools/*.py` module with `description`, Pydantic `Input` and `Output` models, and `execute` can be loaded by a generic host. `uv sync --locked` and `uv run --locked` provide the prepared environment. |
| Go | One package directory per tool works with `Description`, `Input`, `Output`, and `Execute`. Minimal generated registration glue imports each package and builds a cached host from a disposable Go module. |
| Go schemas | Go has no single Zod-equivalent incumbent. Schema-generation and validation libraries stay in the generated host module; authored tools use ordinary structs and JSON Schema tags. |
| Runtime | One JSONL process per language served inspection and repeated calls without restarting. Stdout stayed protocol-only, and process exits surfaced bounded diagnostics without forwarding raw stderr to the model. |
| Validation | The combined catalog rejected cross-language duplicates. The hosts rejected invalid definitions, inputs, and outputs. The supervisor detected process loss and terminated a host whose call exceeded its deadline. |
| Generated state | Deno and Python keep their native lockfiles. Go host source, module metadata, sums, and binary are reproducible cache output keyed by tool source and host-template content. No normalized tool inventory is written. |

The deadline proof terminates the whole language host. Graceful per-call
cancellation, restart policy, concurrent calls, and richer log routing remain
deliberate limits rather than shipped claims.

## MVP proof

The completed proof covers discovery, native locked preparation, apply for both
harnesses, generated MCP configuration, schemas, persistent calls, audit
redaction, and definition, duplicate, input, output, process, and timeout
failures. It intentionally does not expand into channels or proposals.
