# Parallel Intellect

Parallel Intellect is a local control plane for autonomous software engineering.

A human interacts with one persistent captain agent; Parallel Intellect sits underneath and makes multi-agent work reliable by owning missions, tasks, dependencies, worker identity and lifecycle, Treehouse leases, task attempts, state transitions, blockers and decisions, completion evidence, validation results, delivery records, crash recovery, and durable history.

> Reasoning may be probabilistic. Execution state may not be.

The full product and engineering specification lives at [docs/parallel-intellect-v1-spec.md](docs/parallel-intellect-v1-spec.md).

## Stack

- Control plane: Go + SQLite (`pintellect` CLI, `pintellectd` daemon)
- Captain: Prime Agent (first-class), Pi / Claude Code / Codex as alternatives
- Workers: Pi, Claude Code, Codex
- Terminal runtime: Herdr
- Worktree runtime: Treehouse
- Validation / delivery: no-mistakes
- Deployment: local-first, single machine

## Repository layout

See spec section 60. Implementation follows the milestone order in spec section 61.
