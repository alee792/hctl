# ADR 0002: Use Go for the local CLI

- Status: accepted

## Decision

Implement the initial local CLI in Go using the standard library unless a
concrete requirement proves it insufficient.

## Context

The product needs deterministic filesystem compilation, safe file writes,
local process supervision, JSON protocols, and a small MCP server. It does not
need its own model loop. The Go prototype produced a roughly 2.7 MiB
standard-library-only executable, while the Bun prototype produced a roughly
60.5 MiB executable for a similar functional slice.

## Consequence

Prefer a single portable executable and explicit process protocols. Revisit
the language only if implementation evidence shows that Go materially impedes
the product contract.

