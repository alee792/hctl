# Tool authoring workbench

- Status: working design notes; not an accepted ADR
- Scope: convention-based TypeScript, Python, and Go tools exposed through MCP

This document preserves current intent and the questions that still require a
prototype. It should be updated as the design is tested so chat history is not
the source of truth.

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

Language-native dependency files and lockfiles may also be present. Their exact
placement and the Go package layout remain spike questions; the nested Go
example is only one candidate. The stable part is that file discovery replaces
hctl registration. Removing a tool file removes the tool on the next apply.

Each language contract must yield the same runtime facts:

- tool name, derived from the relative path unless a future convention says
  otherwise;
- human-readable description;
- bounded input schema;
- bounded output schema;
- callable implementation; and
- execution context carrying cancellation, deadline, and safe request identity.

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

## Proposed runtime shape

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

## Candidate language shapes

These examples are design probes, not committed APIs.

TypeScript can follow Eve closely with `defineTool`, Zod or another Standard
Schema implementation, and an async `execute` function:

```ts
export default defineTool({
  description: "Return weather for a city.",
  input: z.object({ city: z.string().min(1) }),
  output: z.object({ condition: z.string() }),
  async execute({ city }, context) {
    return { condition: await lookup(city, context.signal) };
  },
});
```

Python can reuse Roster's proven Pydantic contract, potentially with a smaller
decorator surface:

```py
@tool
async def lookup_weather(
    args: WeatherInput,
    context: ToolContext,
) -> WeatherOutput:
    ...
```

Go cannot import source dynamically. The likely shape is a typed exported
value or function that hctl discovers at build time, then links into a
generated project-local host:

```go
var Tool = hctl.Tool[WeatherInput, WeatherOutput]{
    Description: "Return weather for a city.",
    Run: lookupWeather,
}
```

Go is the part that most needs a spike. The design should prefer ordinary Go
packages and generated build glue over runtime plugins, which are
platform-constrained and difficult to distribute safely.

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
3. Start each required adapter in inspection mode. It loads module definitions
   and reports schemas without invoking a tool function.
4. Prepare locked dependencies with native tooling, then validate schemas,
   duplicate names, and the source fingerprint across all languages.
5. Compile and cache a Go host only when Go tools are present.
6. Generate the native harness files and their apply record.
7. Configure the harness to start `hctl mcp serve AGENT` when the session needs
   its tool catalog.
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

hctl keeps only generated ownership and fingerprint state under `.hctl/` so it
can detect a stale or hand-edited harness setup. That state is implementation
output, never an authored tool inventory.

## Spike questions

1. What is the smallest exact source contract for each language while keeping
   input and output validation equally strong?
2. Can TypeScript use Eve-compatible `defineTool` and Standard Schema directly,
   or should hctl own a narrower package?
3. Should Python require Pydantic models, infer from annotated parameters, or
   support both while compiling to one strict schema?
4. What Go directory/package convention lets multiple tools and local imports
   compile without an hctl registry file?
5. Which native dependency files and commands are used for each language, and
   how are locked/offline failures reported?
6. Does one host per language provide sufficient crash isolation, or is an
   optional stronger isolation mode needed later?
7. How are logs, stdout, cancellation, timeouts, and oversized results kept
   from corrupting the MCP stream?
8. Which artifacts should `apply` cache, and which must `package` rebuild for
   portability?

## First spike result

The executable proof in `spikes/polyglot-tools/` answers enough of the process
question to proceed without accepting the candidate source APIs yet.

| Concern | Evidence from the spike |
| --- | --- |
| TypeScript | A direct `tools/*.ts` default export using Zod can report JSON Schema and validate both sides of an async call. `deno check --frozen` verifies the host and tool without executing the handler. |
| Python | A direct `tools/*.py` module with `description`, Pydantic `Input` and `Output` models, and `execute` can be loaded by a generic host. `uv sync --locked` and `uv run --locked` provide the prepared environment. |
| Go | One package directory per tool works with `Description`, `Input`, `Output`, and `Execute`. Minimal generated registration glue imports each package and builds a cached host from a disposable Go module. |
| Go schemas | Go has no single Zod-equivalent incumbent. The spike keeps schema-generation and validation libraries in the generated host module; authored tools use ordinary structs and JSON Schema tags. This choice needs API review before promotion. |
| Runtime | One JSONL process per language served inspection and repeated calls without restarting. Stdout stayed protocol-only and stderr carried process diagnostics. |
| Validation | The combined catalog rejected cross-language duplicates. The hosts rejected invalid definitions, inputs, and outputs. The supervisor detected process loss and terminated a host whose call exceeded its deadline. |
| Generated state | Deno and Python keep their native lockfiles. Go host source, module metadata, sums, and binary are reproducible cache output keyed by tool source and host-template content. No normalized tool inventory is written. |

The deadline proof currently terminates the whole language host. Graceful
per-call cancellation, restart policy, concurrent calls, log routing, and
bounded output still require production design. The spike also stops one layer
short of hctl's MCP server and fake harnesses; promotion must prove that literal
end-to-end path before authored tools are called shipped behavior.

## MVP proof

The completed process spike proves discovery, schemas, persistent calls, and
the listed failure classes short of graceful cancellation. The next proof is
to expose the same catalog through hctl's MCP server, call it from fake Claude
and Codex harnesses, and define bounded output, concurrency, cancellation, and
restart behavior without expanding into channels or proposals.
