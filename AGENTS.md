# Project agent memory

This file is the project's committed home for project-intrinsic agent knowledge: build, test, release, architecture, and sharp-edge notes that should travel with the code.

- `docs/filesystem-protocol.md` is the authoritative design: layout, rules (atomic publication, one mutator, worker write surface, attempt fencing, derived status, external boundaries, authority), and the CLI surface. There is no daemon, database, or managed runtime.
- `internal/store` owns record schemas, atomic publication, the `state/.lock` mkdir lock, and read-time state derivation (`Derive`); wake lines under `state/` are notifications only and must never be read for truth.
- `internal/flow` is the command core behind every `sophon` subcommand; `cmd/sophon/main.go` is a thin flag layer over it. Attempt fencing lives in `flow.Spawn` (retry fences the old lease by exact identity, then bumps `current_attempt`); stale-attempt results are refused in `flow.VerifyComplete`.
- Runtime prompts are embedded by `prompts/embed.go` (`commander`, `workers`, `skills` sets); `SOPHON_PROMPT_DIR` selects a checkout's `prompts/` tree for live development edits. `prompts.Compose` renders the commander prompt; `internal/flow/brief.go` renders worker briefs from the workers set plus the generated task section.
- Worker completion accepts only the attempt-scoped `result.json` via `sophon worker complete` after strict-schema validation and a live worktree HEAD pin (`internal/flow/complete.go`); delivery requires `--confirmed` and publishes typed intent before the external effect (`internal/flow/deliver.go`). After durable publication the CLI best-effort wakes the attached commander (`flow.NotifyCommander`); failures are stderr diagnostics, never task failures.
- Spawn resolves the data home once (`datahome.AbsDir`) and propagates it as a `SOPHON_DATA_HOME` launch-environment assignment on every runtime command (`internal/herdr` `initialCommand`/`resumeCommand`) plus a pinned assignment in the brief's completion command. Workers must never rely on inherited environment to find the store.
- `sophon commander attach` records the live commander's exact Herdr session/workspace/tab/pane in `state/commander.json` — volatile liveness/presentation routing only, never truth (`flow.AttachCommander`, `internal/flow/commander.go`). Spawn groups workers as tabs into a same-session registered workspace (`herdr.StartRequest.ParentWorkspace`, isolated-workspace fallback on `workspace_not_found`); terminal worker evidence retires the exact worker tab (`flow.RetireWorker`, called by the verify-complete/validate CLI after success, idempotent, no cleanup receipt by design).
- Herdr commands stay behind `internal/herdr.Adapter`; pane ID is the runtime placement. Real-Herdr tests are gated behind `SOPHON_HERDR_LAB=1` with `HERDR_LAB_HELPER`; the ONLY herdr isolation path in tests is the lab helper plus a trailing `--session` argument. Herdr injects `HERDR_SESSION`/`HERDR_WORKSPACE_ID`/`HERDR_TAB_ID`/`HERDR_PANE_ID` into panes; `commander attach` reads them as ambient defaults.
- The installed Treehouse lease JSON supplies path and lease identity but not Git metadata; `internal/treehouse` derives branch/base SHA through `internal/git`, and all lease returns must retain both conditional identity guards (lease id + holder).
- Run `go test ./...` and `go build ./...` before delivery.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
