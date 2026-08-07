# Sophon

Sophon is a local control plane for coordinating autonomous coding agents. A human operator talks to one persistent commander agent; the commander decomposes work and dispatches specialist workers running Pi, Claude Code, or Codex. Sophon owns the durable execution graph underneath.

> Reasoning may be probabilistic. Execution state may not be.

## The cast

- **Operator** — the human who sets objectives and retains final authority.
- **Commander** — the persistent agent the operator talks to.
- **Worker** — a specialist agent assigned to one task.
- **Mission** — the objective being pursued.
- **Task** — one schedulable unit of work within a mission.
- **Signal** — a durable decision that needs the operator.
- **Lease** — Treehouse worktree ownership for a task attempt.

## Quickstart

Build and install the local CLI and daemon:

```bash
go install ./cmd/sophon ./cmd/sophond
sophon daemon start
```

Then enter the repository you want to work on and describe the work in plain language:

```bash
cd /path/to/your/repo
sophon home
```

`sophon home` opens a workspace in Herdr with your persistent commander. Use `sophon status`, `sophon mission list`, and `sophon daemon status` to inspect the work, missions, and local service.

## Architecture

```text
operator → commander → sophon → herdr → treehouse → delivery
                       │
                 state machine
                 SQLite + events
```

Sophon decides and records execution state; Herdr runs agents, Treehouse provides isolated worktrees, and delivery moves verified work forward.

## What it guarantees

- Structured evidence, not terminal prose, completes a task.
- Lease fencing prevents stale attempts from acting on current worktrees.
- Idempotent delivery prevents duplicate releases or pull requests.
- Durable state and recovery logic let work survive crashes and restarts.
- Operator authority is explicit: unresolved decisions become signals, not silent agent choices.

Named for the all-seeing sophons in *The Three-Body Problem*.
