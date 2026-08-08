# Sophon

Sophon is single-operator local orchestration for autonomous coding agents. A human operator talks to one **commander** — an ordinary, disposable agent session; the commander decomposes work into tasks and dispatches **workers**, each an isolated agent session executing one attempt in its own Treehouse worktree over a Herdr pane.

There is no daemon, database, or event stream. Sophon's only state model is a filesystem protocol: durable typed records under `~/.sophon/`, with task status **derived at read time** from those records plus live Herdr, Treehouse, Git, and forge observation. Every `sophon` command is one short-lived invocation.

> Reasoning may be probabilistic. Execution state may not be.

## The cast

- **Operator** — the human who sets objectives and retains final authority.
- **Commander** — the agent session the operator talks to; plans, delegates, verifies, and reports.
- **Worker** — an isolated agent session that executes one task attempt.
- **Mission** — the durable objective: project path plus intent.
- **Task** — one substantive unit of work; attempts are fenced incarnations of it.
- **Lease** — Treehouse worktree ownership for one task attempt.

## Install

```bash
go build -o sophon ./cmd/sophon
```

External tools (`herdr`, `treehouse`, `git`, `gh-axi`) are resolved from PATH by default; every command that needs one accepts a flag to override it.

## Quickstart

Start a commander session from the rendered prompt (for example, paste the output of `sophon prompt commander` into a fresh agent session), or drive the CLI directly:

```bash
# 1. Create a mission: durable intent for one project.
sophon mission create --project /path/to/repo --title "Rate limiting" \
  --objective "Add per-client rate limiting to the API with regression tests"

# 2. Create a task: one substantive outcome, its delivery mode and validation.
sophon task create --mission <mission-id> --title "Token-bucket limiter" \
  --delivery pr --validate "go test ./..."

# 3. Spawn attempt 1: lease, branch, generated brief, worker pane.
sophon spawn <task-id>

# 4. Watch derived state. queued → active → ready → verified → delivered.
sophon status

# 5. When the task is ready, prove the attempt and run validation.
sophon verify-complete <task-id>
sophon validate <task-id>

# 6. Deliver only with explicit operator confirmation.
sophon deliver <task-id> --confirmed

# 7. Return the lease when the delivered branch no longer needs it.
sophon release <task-id>
```

Other commands: `sophon spawn <task-id> --retry` (fence the current attempt and spawn the next), `sophon send <task-id> <message>` (steer a live worker), `sophon mission list`, `sophon version`.

Workers finish by publishing their structured result:

```bash
sophon worker complete <task-id> --attempt <n> --head-sha "$(git rev-parse HEAD)" --result <path>
```

## What it guarantees

- **Derived state, no daemon.** Nothing runs in the background; nothing automatically recovers, restarts, or fails over. With no commander session alive, nothing advances and nothing is lost — completed work waits on disk and surfaces as `ready` to the next session.
- **Structured evidence, not prose.** A strict result schema, pinned to the live worktree HEAD, is the only completion path; worker prose and notification lines are never state.
- **Attempt fencing.** Retry fences the old attempt's lease by exact identity before the next attempt exists; stale results are refused loudly.
- **Operator authority at the boundary.** Verification and validation are autonomous; every delivery effect requires `--confirmed`. There is no merge path.
- **Crash-safe external effects.** Delivery and release write typed intent before the effect and a receipt after; re-running converges to the same result.

## Documentation

- `docs/filesystem-protocol.md` — the authoritative design: layout, rules, and CLI surface.
- `docs/behavioral-contracts.md` — the commander and worker behavioral contracts implemented in `prompts/`.

Named for the all-seeing sophons in *The Three-Body Problem*.
