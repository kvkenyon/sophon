# Sophon

Sophon is a local control plane for autonomous software engineering.

Sophon was formerly named Parallel Intellect.

A human interacts with one persistent commander agent; Sophon sits underneath and makes multi-agent work reliable by owning missions, tasks, dependencies, worker identity and lifecycle, Treehouse leases, task attempts, state transitions, blockers and decisions, completion evidence, validation results, delivery records, crash recovery, and durable history.

> Reasoning may be probabilistic. Execution state may not be.

The full product and engineering specification lives at [docs/sophon-v1-spec.md](docs/sophon-v1-spec.md).

## Stack

- Control plane: Go + SQLite (`sophon` CLI, `sophond` daemon)
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
sophon mission create --project /path/to/project --title "Mission" --objective "Goal"
sophon task create MSN_ID --title "Task" --objective "Change" --acceptance "Criterion"
sophon task start TSK_ID --herdr-session NAME
sophon worker complete TSK_ID --attempt 1 --head-sha SHA --result ~/.sophon/tasks/TSK_ID/1/result.json
```

`task start` acquires a Treehouse lease, writes the generated brief, and launches Codex through Herdr. Completion reaches `ready` only after current-attempt, live-lease, Git ancestry/head/cleanliness, and strict result-schema verification.

## Operator front door

Open the current mission snapshot and enter its conversational commander in one step:

```bash
sophon home --db /path/to/sophon.db
```

When more than one mission exists, add `--mission MSN_ID`. If the mission has no commander yet, `home` offers to start Codex; use `--yes` to accept that default without a prompt. Governed learning candidates can be reviewed with `sophon knowledge list`, then advanced explicitly with `knowledge promote`, `knowledge reject`, or `knowledge supersede`.

Runtime prompt sets are compiled into the binaries, so they work from any current directory. During development, set `SOPHON_PROMPT_DIR` to a checkout's `prompts/` directory to use live-edited commander, worker, and skill prompt files instead.

## Upgrading from Parallel Intellect

Sophon stores new local state in `~/.sophon`. If that directory is absent but `~/.parallel-intellect` exists, Sophon continues using the legacy database, task artifacts, skills, daemon pidfile, log, and health file and prints a migration notice. Move everything in one step when the daemon is stopped:

```bash
mv ~/.parallel-intellect ~/.sophon
```

`SOPHON_PROMPT_DIR` replaces `PINTELLECT_PROMPT_DIR`; the old environment variable remains a deprecated fallback when the new variable is unset.

## Reliability testing

The default test suite remains hermetic:

```bash
go test ./...
go build ./...
```

Milestone 11's process-kill matrix is opt-in and reports an exactly-once result or explicit recoverable state for every crash boundary:

```bash
SOPHON_RUN_CRASH_INJECTION=1 ./test/crash-injection.sh
```

To include the real Herdr stop/re-provision/husk-resume proof, use the guarded named lab path:

```bash
SOPHON_RUN_CRASH_INJECTION=1 SOPHON_CRASH_HERDR_LAB=1 ./test/crash-injection.sh
```
