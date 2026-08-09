# Sophon behavioral contracts

Sophon's mechanics are specified in `docs/filesystem-protocol.md`. This
document states the behavioral contracts of the two agent roles as they are
implemented in `prompts/`. The prompt files are authoritative; this is the
map and the short version.

## Commander

Authoritative source: `prompts/commander/AGENTS.md`, rendered by
`sophon prompt commander` (`prompts.Compose`).

The commander is an ordinary, unmanaged, disposable agent session — no daemon,
no managed runtime, no resume machinery. Its contract:

- **Sole point of contact.** The operator talks only to the commander. All
  execution is delegated to workers; the commander never edits, commits,
  pushes, or otherwise mutates a project itself.
- **Judgment within accepted intent.** The commander owns ambiguous routine
  judgment inside the accepted mission objective and escalates genuine
  operator choices — contract expansions, destructive or security-sensitive
  actions, credentials — in one concise, evidence-first message.
- **Durable-state reconstruction.** The commander reconstructs work from the
  durable records under the data home plus live observation through
  `sophon status`, never from conversation memory. A restart is a non-event:
  session start is `sophon commander attach` (registering the volatile
  wake/placement address so completions can wake the session and new workers
  can join its Herdr workspace as tabs), then `sophon status`, reconcile
  attention items (ready tasks awaiting verification, lost or unknown-pane
  tasks), then report.
- **Outcomes, not mechanics.** The commander reports what changed, what it
  means, and what is needed next. Wake lines in `~/.sophon/state/`, the
  volatile `state/commander.json` registration, and worker notification prose
  are notifications, never state, and are never relayed as current truth.
- **Bounded authority.** The commander may create missions and tasks, spawn,
  verify-complete, validate, send, and release autonomously. Every delivery
  effect requires explicit operator confirmation, enforced mechanically by
  `sophon deliver --confirmed`. The commander never merges.
- **Attempt fencing.** Verification, validation, delivery, and release act
  only on the current attempt. `sophon spawn --retry` fences the old attempt's
  lease first; a stale attempt's result is refused loudly and mutates nothing.
- **Routine worker cleanup.** Successful terminal worker evidence — a verified
  outcome, plus a passing validation when one is configured — retires the
  exact finished worker pane automatically. This is quiet presentation
  cleanup: branch, lease, and records remain until operator-confirmed
  delivery and explicit release, and a cleanup failure is a bounded
  diagnostic, never a verification failure. Worker tab grouping inside the
  commander's Herdr workspace is likewise presentation only.

Conditional procedures live in the materialized skills under `prompts/skills/`
(recap, status, operator-authority, decision-lifecycle, diagnostic-reasoning,
coding-guidelines, worker-recovery, agent-adapters); the commander prompt
carries the load triggers.

## Worker

Authoritative sources: `prompts/workers/common.md` and
`prompts/workers/implementation.md`, concatenated into every generated brief
by `internal/flow/brief.go`.

A worker is an isolated agent session that executes exactly one task attempt
in its own leased Treehouse worktree. Its contract:

- **Bounded written instructions.** The generated brief is the complete
  authority: mission, task, attempt, worktree, branch, base SHA, validation,
  delivery mode, permissions, and completion steps. The worker does exactly
  what it says and never expands scope.
- **Isolated workspace.** All project changes happen inside the assigned
  attempt worktree and nowhere else. The worker never touches leases,
  worktrees, or any shared state — mission or task records, other attempts,
  outcomes, delivery — and never pushes, opens a PR, delivers, or merges.
- **Structured result only.** The worker's sole write outside the worktree is
  the version 1 result JSON (`version`, `status`, `summary`, `verification`,
  `changed_files`, `risks`), published exactly once through the brief's exact
  completion command — `sophon worker complete <task-id> --attempt <n>
  --head-sha $(git rev-parse HEAD) --result <path>` prefixed with the
  `SOPHON_DATA_HOME` assignment that pins the assigned store. Completion prose
  is never completion.
- **Concrete escalation only.** A worker escalates only genuine decisions and
  blockers it cannot resolve within the brief, by stopping, preserving work,
  and reporting the evidence — never by addressing the operator, and never by
  answering its own decision blocker or fabricating a result.

## Why this holds without a daemon

Every guarantee above is enforced by records and commands, not by supervision
liveness. Workers cannot publish outside their own attempt directory; the
strict result schema and head pin are validated at publication; verification
re-proves lease identity and Git descent; delivery refuses without
`--confirmed`. With no commander session alive, nothing advances and nothing
is lost — completed work waits on disk and surfaces as `ready` to the next
session.
