# Skill compatibility

- Status: accepted implementation contract
- Last verified: 2026-08-05

## Outcome

Use the open Agent Skills directory format as hctl's portable skill source:

```text
skills/
  code-review/
    SKILL.md
    scripts/
    references/
    assets/
    agents/
      openai.yaml
```

This is a hard cut from the provisional `skills/*.md` convention. There is no
dual-format loader or authored hctl manifest. The standard defines portable
packaging; a harness extension is honored only when the selected harness
documents that exact behavior. Hctl warns and omits only differences explicitly
classified as presentation or discovery metadata whose loss is safe.

## Verified references

- The [Agent Skills specification](https://agentskills.io/specification)
  defines `SKILL.md`, its portable frontmatter, and arbitrary bundled files.
- The [Claude Code skills documentation](https://code.claude.com/docs/en/skills)
  documents project skills under `.claude/skills/` and Claude-specific
  frontmatter.
- The [OpenAI skill documentation](https://developers.openai.com/codex/skills)
  documents Codex repository skills under `.agents/skills/` and optional
  `agents/openai.yaml` metadata.

These vendor surfaces change independently and must be reverified before adding
or translating another extension.

## Compatibility matrix

| Authored surface | Classification | Claude Code | Codex | hctl behavior |
| --- | --- | --- | --- | --- |
| `name` | Portable, required | Native | Native | Validate against the parent directory and preserve. |
| `description` | Portable, required | Native discovery metadata | Native discovery metadata | Validate and preserve. |
| Markdown body | Portable instructions | Native instructions | Native instructions | Preserve; do not interpret prompt behavior. |
| `license` | Portable documentary field | Preserved | Preserved | Preserve without operational claims. |
| `compatibility` | Portable documentary field | Preserved for the model | Preserved for the model | Preserve; hctl does not install or enforce the stated environment. |
| `metadata` string map | Portable documentary extension point | Preserved, normally inert | Preserved, normally inert | Preserve; namespaced keys do not create hctl behavior. |
| `scripts/`, `references/`, `assets/` | Portable resource conventions | Native resources | Native resources | Copy regular files byte-for-byte. |
| Other nested files and directories | Portable resources | Available to the skill | Available to the skill | Copy regular files byte-for-byte; do not require a fixed resource inventory. The reserved `agents/openai.yaml` exception is below. |
| `allowed-tools` | Experimental standard field; operational in Claude CLI | Temporarily pre-approves listed tools for the invoking turn; it does not restrict other tools | No documented enforcement | Preserve for Claude. Fail Codex apply rather than imply approval or restriction. Hctl does not enforce it. |

Hctl enforces the standard name rules: 1-64 lowercase ASCII letters, digits,
and single hyphens, with no leading or trailing hyphen, and exact agreement with
the parent directory. `description` is limited to 1-1024 characters;
`compatibility`, when present, is limited to 1-500 characters; `metadata` maps
strings to strings; and `allowed-tools` is a space-separated string.

The portable standard does not define model choice, reasoning effort,
invocation policy, routing, hooks, or a tool-deny policy. Putting such a value
under `metadata` preserves text only; it does not turn the value into behavior.

## Claude Code extensions

Claude Code currently documents these additions to `SKILL.md` frontmatter:

| Field | Effect in Claude Code | Other targets |
| --- | --- | --- |
| `when_to_use` | Adds discovery context. | Warn and omit for Codex. |
| `argument-hint` | Adds command-completion UI text. | Warn and omit for Codex. |
| `arguments` | Defines named positional substitutions. | Fail when unsupported. |
| `disable-model-invocation`, `user-invocable` | Controls model or user invocation. | Fail when unsupported. |
| `allowed-tools`, `disallowed-tools` | Grants approval or removes tools for the invoking turn. | Fail when unsupported; never reinterpret either as an hctl policy. |
| `model`, `effort` | Selects Claude model or effort for the invoking turn. | Fail when unsupported; never describe a recommendation as enforcement. |
| `context`, `agent`, `background` | Routes execution through a Claude subagent. | Fail when unsupported. |
| `hooks` | Runs Claude lifecycle hooks while the skill is active. | Fail when unsupported. |
| `paths` | Limits Claude's automatic activation by file path. | Fail when unsupported. |
| `shell` | Selects the shell for Claude dynamic context commands. | Fail when unsupported. |

Hctl passes recognized Claude extensions through for a Claude project apply.
For Codex, it removes `when_to_use` and `argument-hint` from generated
frontmatter with one warning per field. Other documented Claude extensions fail
because losing them can change approval, invocation, routing, model selection,
execution, or activation behavior. Hctl does not translate them into
speculative equivalents. Dynamic content and argument placeholders inside the
Markdown body remain harness-interpreted content, so portability claims stop at
preserving the file.

## OpenAI host extension

OpenAI documents optional `agents/openai.yaml` inside the skill directory. Hctl
carries it in the Codex project layout:

| Surface | Documented OpenAI meaning | Other targets |
| --- | --- | --- |
| `interface.display_name`, `short_description`, `icon_small`, `icon_large`, `brand_color` | ChatGPT desktop presentation metadata. | Warn and omit the vendor file for Claude. |
| `interface.default_prompt` | ChatGPT desktop default prompt text. | Fail a Claude project apply because prompt behavior would be lost. |
| `policy.allow_implicit_invocation` | Enables or disables implicit OpenAI-host invocation; explicit invocation remains available. | Fail a Claude project apply. |
| `dependencies.tools` | Declares supported MCP tool dependencies and connection metadata. | Fail a Claude project apply; copying a declaration is not dependency provisioning. |

Codex documents `name` and `description` as the fields used to decide when to
load `SKILL.md`. It does not document skill-level `allowed-tools`, model, or
effort enforcement. Hctl therefore makes no such claim. `agents/openai.yaml`
remains an OpenAI-host surface, not an hctl configuration file. Hctl treats it
as Codex-targeted source and emits the whole file byte-for-byte for Codex. For
Claude, a file containing only the five recognized presentation fields above is
omitted with one warning per field. `default_prompt`, `policy`, `dependencies`,
unknown fields, malformed YAML, and mixed safe/behavioral documents fail. Hctl
does not guess that an unknown field is harmless.

## Generation and filesystem decisions

1. Generate only project-scoped native skills: `.claude/skills/NAME/` for
   Claude and `.agents/skills/NAME/` for Codex. Never modify personal, user,
   administrator, enterprise, system, or plugin locations.
2. Copy every bounded regular resource file byte-for-byte when the target
   supports it. Preserve its path relative to the skill root, including
   arbitrary directories beyond the conventional `scripts/`, `references/`,
   and `assets/` names. Treat `agents/openai.yaml` as the reserved exception:
   copy it for Codex; warn and omit a recognized presentation-only document for
   Claude; fail other contents.
3. Preserve executable intent for resource files and include that intent in
   source fingerprints and generated-file ownership checks. A mode-only change
   to an executable resource is a source change.
4. Reject symlinked skill directories, `SKILL.md` files, resources, and nested
   directories. Codex can follow symlinked skill folders, but hctl's portable
   source boundary does not.
5. Require valid UTF-8 relative resource paths so JSON ownership records can
   represent every generated file exactly.
6. Keep the existing eight-skill limit and bound resource counts, individual
   files, and aggregate skill content. Reject entries before reading outside
   those bounds.
7. Parse frontmatter as YAML. Do not extend the former line parser to
   approximate nested `metadata`, lists, booleans, or vendor documents.
8. Diagnose an unsupported field by authored path, skill, field, selected
   harness, and action. Collect safe-loss warnings before writing generated
   files. Unknown frontmatter outside the standard `metadata` extension point
   is unsupported, not silently discarded.

## Diagnostic rule

Preserve supported fields. Warn and omit a field only when it is explicitly
classified as presentation or discovery metadata and losing it cannot change
authorization, execution, routing, invocation control, dependencies, or model
selection. Fail apply for those material behaviors, malformed vendor metadata,
and unknown fields. Ordinary standard `license`, `compatibility`, and
`metadata` remain preserved but inert.

Warnings are emitted on stderr and identify the authored path, field, selected
harness, and omission. Errors identify the same context and stop before any
generated file is written. Compatibility classifications live in code and
tests as well as this dated matrix; vendor changes require re-verification of
the linked documentation rather than runtime guessing.

This rule prevents two especially misleading claims:

- `allowed-tools` never means that hctl restricts native harness tools. In
  Claude it is a native temporary pre-approval; in Codex it is unsupported.
- A model or effort value is never a cross-harness recommendation or hctl
  enforcement. Claude may honor its documented extension; Codex has no
  equivalent skill field.

## Acceptance evidence

The cutover is complete when credential-free checks prove:

1. A standard-only skill with referenced and arbitrary nested resources
   appears in both native project skill directories with identical bytes.
2. Executable intent survives apply and changes the source fingerprint.
3. Symlinked files or directories and bounded-resource violations fail before
   setup is written.
4. Supported Claude frontmatter survives Claude apply. Claude presentation
   fields warn and are absent from Codex output, while operational fields fail
   before setup is written.
5. Codex `agents/openai.yaml` survives Codex apply byte-for-byte. A recognized
   presentation-only document warns and is omitted for Claude; behavioral or
   unknown contents fail.
6. Flat `skills/NAME.md` source is rejected, and all first-party examples use
   `skills/NAME/SKILL.md`.
