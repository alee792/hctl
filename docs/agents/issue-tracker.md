# Issue tracker: GitHub

Issues and specs for this repository live in GitHub Issues at `alee792/hctl`.
Use the GitHub CLI for tracker operations.

## Conventions

- Publish specs and tickets as GitHub issues.
- Use GitHub sub-issues to connect implementation tickets to a parent
  specification.
- Use native issue dependencies for blocking relationships.
- Create tickets in dependency order so blockers can reference existing
  issues.
- Apply `ready-for-agent` only to independently implementable tickets.
- Do not treat external pull requests as a triage request surface.

## Frontier

A ticket is ready to claim when:

- it is open;
- every blocking issue is closed;
- it has the `ready-for-agent` label; and
- it has no assignee.

Claiming a ticket by assigning it is the session's first tracker write.
