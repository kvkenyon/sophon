---
name: bootstrap-diagnostics
description: Agent-only handling procedure for actionable sophon doctor findings; detect first, obtain current operator consent, then install or repair only through supported mechanisms.
user-invocable: false
metadata:
  internal: true
---

<!-- Provenance: adapted from prompts/upstream/firstmate/skills/bootstrap-diagnostics/SKILL.md. -->

# Bootstrap diagnostics

Use this skill whenever `sophon doctor` reports an actionable dependency, authentication, configuration, runtime, repository, lease, or delivery diagnostic.
Informational and passing checks require no action.

## Governing rule

Handle a finding before dispatching work that depends on it.
Detect first, explain the consequence, obtain current operator consent where installation or external change is required, then use only the supported owner path.
Never silently install, authenticate, weaken a requirement, select around malformed configuration, or restart shared infrastructure.

Run `sophon doctor` to verify the current condition after an authorized repair.
Do not treat an earlier prose report as proof that the dependency is now healthy.

## Finding classes

- **Missing automatically installable dependency** - explain its purpose and the exact proposed installation, then wait for operator consent.
  `TODO(spec-gap)`: V1 defines detection through `sophon doctor` but no installation API; until one exists, do not invent an install command.
- **Manual installation or authentication** - explain why it is required and give the operator the supported instructions supplied by the diagnostic.
  Wait for the operator to complete the interactive action, then rerun `sophon doctor`.
- **Invalid runtime or adapter** - block dependent dispatch until the configuration names a verified supported adapter.
  Do not silently fall back when the requested runtime is part of accepted intent.
- **Malformed configuration or profile** - preserve the actionable error and require correction.
  Do not ignore the invalid entry or guess a replacement.
- **Missing credential or permission** - create a structured blocker or Signal as appropriate and request only the minimum operator action needed.
  Never expose secret values.
- **Repository or worktree conflict** - preserve dirty files, commits, leases, and attempt identity.
  Do not force, stash, discard, release by path, or duplicate the task worktree.
- **Backend or external dependency unavailable** - distinguish a bounded nonblocking skip from a required unsafe state.
  Block only the dependent action and retain evidence for retry.
- **Migration or reconciliation incomplete** - keep affected operations unavailable until the owning control-plane path independently verifies a safe result.
  Never execute quarantined or ambiguous artifacts.

Report operator-facing diagnostics as outcome, consequence, required action, and evidence rather than internal labels.
