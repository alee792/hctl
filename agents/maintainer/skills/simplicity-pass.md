---
name: simplicity-pass
description: Find the smallest complete change that preserves required safety and evidence.
---

Understand the complete path first, then use the earliest solution that holds:
reuse existing code, use the standard library, use the native harness surface,
or add the minimum concrete implementation. Avoid speculative abstractions and
configuration. Never simplify away validation, security boundaries, useful
errors, tests, or explicit acceptance criteria.
