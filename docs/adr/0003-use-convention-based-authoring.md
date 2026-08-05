# ADR 0003: Use convention-based authoring

- Status: accepted

## Decision

Make the authored directory layout the project API. Derive the agent name from
the directory, load descriptive `instructions.md`, and discover optional
Agent Skills directories at `skills/NAME/SKILL.md`, `tools/`, and immediate
`subagents/` without a registry or required configuration file. Copy supported
regular skill resources into each harness's project skill location. Keep
harness-specific skill metadata beside `SKILL.md` only when the target has an
honest native representation. Warn and omit explicitly classified presentation
or discovery metadata when safe; fail rather than silently losing behavioral or
unknown semantics.

## Context

Authors may be nontechnical but can work with directories and common AI
concepts such as instructions, skills, and tools. Repeating the filesystem
inventory in configuration adds an internal concept and creates drift. Eve's
filesystem-forward conventions provide the clearer precedent.

## Consequence

New authored concepts should use an obvious conventional directory before
adding configuration. Configuration is reserved for settings the layout cannot
express. Harness-specific filenames remain generated setup details. The hard
cut from provisional `skills/*.md` avoids carrying a compatibility layer before
release; existing experimental agents must move each file to
`skills/NAME/SKILL.md`.
