# Parallel Intellect

Parallel Intellect is a local control plane for autonomous software engineering.

A human interacts with one persistent commander agent; Parallel Intellect sits underneath and makes multi-agent work reliable by owning missions, tasks, dependencies, worker identity and lifecycle, Treehouse leases, task attempts, state transitions, blockers and decisions, completion evidence, validation results, delivery records, crash recovery, and durable history.

> Reasoning may be probabilistic. Execution state may not be.

The full product and engineering specification lives at [docs/parallel-intellect-v1-spec.md](docs/parallel-intellect-v1-spec.md).

## Stack

- Control plane: Go + SQLite (`pintellect` CLI, `pintellectd` daemon)
- Commander runtimes: Pi, Claude Code, Codex
- Workers: Pi, Claude Code, Codex
- Terminal runtime: Herdr
- Worktree runtime: Treehouse
- Validation / delivery: no-mistakes
- Deployment: local-first, single machine

## Repository layout

See spec section 60. Implementation follows the milestone order in spec section 61.

## Milestone 3 vertical slice

The first executable path is CLI-driven:

```bash
pintellect mission create --project /path/to/project --title "Mission" --objective "Goal"
pintellect task create MSN_ID --title "Task" --objective "Change" --acceptance "Criterion"
pintellect task start TSK_ID --herdr-session NAME
pintellect worker complete TSK_ID --attempt 1 --head-sha SHA --result ~/.parallel-intellect/tasks/TSK_ID/1/result.json
```

`task start` acquires a Treehouse lease, writes the generated brief, and launches Codex through Herdr. Completion reaches `ready` only after current-attempt, live-lease, Git ancestry/head/cleanliness, and strict result-schema verification.

## Operator front door

Open the current mission snapshot and enter its conversational commander in one step:

```bash
pintellect home --db /path/to/pintellect.db
```

When more than one mission exists, add `--mission MSN_ID`. If the mission has no commander yet, `home` offers to start Codex; use `--yes` to accept that default without a prompt. Governed learning candidates can be reviewed with `pintellect knowledge list`, then advanced explicitly with `knowledge promote`, `knowledge reject`, or `knowledge supersede`.

## Reliability testing

The default test suite remains hermetic:

```bash
go test ./...
go build ./...
```

Milestone 11's process-kill matrix is opt-in and reports an exactly-once result or explicit recoverable state for every crash boundary:

```bash
PI_RUN_CRASH_INJECTION=1 ./test/crash-injection.sh
```

To include the real Herdr stop/re-provision/husk-resume proof, use the guarded named lab path:

```bash
PI_RUN_CRASH_INJECTION=1 PI_CRASH_HERDR_LAB=1 ./test/crash-injection.sh
```
