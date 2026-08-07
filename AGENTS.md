# Project agent memory

This file is the project's committed home for project-intrinsic agent knowledge: build, test, release, architecture, and sharp-edge notes that should travel with the code.

- The vendored FirstMate behavioral baseline is under `prompts/upstream/firstmate/`; its pinned provenance is `VERSION`, and `docs/rule-classification.md` is the authoritative V1 adaptation map.
- Use `commander` for the coordinator-agent role in code, schema, CLI, events, and logs; `operator` remains the human role and `prime` remains a runtime name.
- Task lifecycle policy is authoritative in `internal/task/state_machine.go`; SQLite writes must go through the command-idempotent CAS operations in `internal/db` so projection changes and events commit atomically.
- The installed Treehouse lease JSON supplies path and lease identity but not Git metadata; `internal/treehouse` derives branch/base SHA through `internal/git` and all returns must retain both conditional lease guards.
- The one-Codex slice is owned by `internal/worker`; Herdr commands stay behind `internal/herdr.Adapter`, pane ID is the current runtime placement, and completion accepts only the attempt-scoped `~/.parallel-intellect/tasks/<task>/<attempt>/result.json` after live lease and Git verification.
- Worker-session lifecycle policy is separate from task lifecycle in `internal/workersession`; idle never means done. Herdr liveness comes from agent registration, restart husks are replaced create-before-close by resuming the persisted Codex session in a new pane, and structurally missing panes reconcile to `lost` plus task `needs_attention`.
- Task recovery is implemented by `internal/db` and `internal/worker/cancel.go`: retry fences the old attempt before creating the next queued attempt; cancellation is terminal before best-effort conditional lease return and worker-tab cleanup.
- Delivery policy and external boundaries live in `internal/delivery`; every attempt is pinned to its recorded branch/head SHA, PR recovery matches repository + branch + SHA, and branch-only delivery retains its lease until the explicit M2 release path runs.
- Commander runtime policy lives in `internal/commander`; sessions reuse the M5 Herdr profiles, persist mission and native resume identity, and advance their durable event-wake cursor only after Herdr accepts the structured mission message.
- Add schema changes as numbered, forward-only files in `migrations/`; run `go test ./...` and `go build ./...` before delivery.

## Maintaining this file

Keep this file for knowledge useful to almost every future agent session in this project.
Do not repeat what the codebase already shows; point to the authoritative file or command instead.
Prefer rewriting or pruning existing entries over appending new ones.
When updating this file, preserve this bar for all agents and keep entries concise.
