# ADR 0003: Use convention-based authoring

- Status: accepted

## Decision

Make the authored directory layout the project API. Derive the agent name from
the directory, load descriptive `instructions.md`, and discover optional
`skills/*.md`, `tools/`, and immediate `subagents/` without a registry or
required configuration file.

## Context

Authors may be nontechnical but can work with directories and common AI
concepts such as instructions, skills, and tools. Repeating the filesystem
inventory in configuration adds an internal concept and creates drift. Eve's
filesystem-forward conventions provide the clearer precedent.

## Consequence

New authored concepts should use an obvious conventional directory before
adding configuration. Configuration is reserved for settings the layout cannot
express. Harness-specific filenames remain generated setup details.
